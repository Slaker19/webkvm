package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/models"
)

// Task history and per-task logs live next to the existing in-memory job
// tracker (isoJobs): terminal jobs are appended to
// <dataDir>/jobs-history.json (capped, deduped by job ID) so finished work
// survives a WebKVM restart, and every job accumulates a bounded log line
// ring for the "download task log" endpoint. The history file is set once
// at startup via SetJobsHistoryDir; unit tests that skip it keep
// memory-only behavior.

const (
	jobLogCap      = 200 // last N log lines kept per job
	jobHistoryCap  = 200 // last N terminal jobs kept on disk
	jobHistoryTTL  = 7 * 24 * time.Hour
	jobHistoryName = "jobs-history.json"
)

var (
	histMu   sync.Mutex
	histPath string // "" disables persistence (tests)
	histJobs []models.DownloadJob
)

// SetJobsHistoryDir points the terminal-job history at dataDir. Called
// once at startup; calling it with "" (unit tests) clears the in-memory
// view and disables persistence.
func SetJobsHistoryDir(dir string) {
	histMu.Lock()
	defer histMu.Unlock()
	histJobs = nil
	if dir == "" {
		histPath = ""
		return
	}
	histPath = filepath.Join(dir, jobHistoryName)
	data, err := os.ReadFile(histPath)
	if err == nil {
		var recs []models.DownloadJob
		if json.Unmarshal(data, &recs) == nil {
			histJobs = recs
		}
	}
}

func appendJobLog(j *models.DownloadJob, text string) {
	if text == "" {
		return
	}
	j.Logs = append(j.Logs, models.JobLogLine{Ts: time.Now().Unix(), Msg: text})
	if len(j.Logs) > jobLogCap {
		j.Logs = j.Logs[len(j.Logs)-jobLogCap:]
	}
}

// addJobLog appends a progress line to a running job's log ring. Used by
// background loops (e.g. evacuation) that publish progress outside the
// central updateJob helpers.
func addJobLog(jobID, text string) {
	if jobID == "" || text == "" {
		return
	}
	jobsMu.Lock()
	defer jobsMu.Unlock()
	if j, ok := isoJobs[jobID]; ok {
		appendJobLog(j, text)
		j.UpdatedAt = time.Now().Unix()
	}
}

// recordJobHistory stores a snapshot of a job that just reached a terminal
// state, replacing any earlier snapshot of the same job (retries rewrite
// their own entry).
func recordJobHistory(j models.DownloadJob) {
	histMu.Lock()
	defer histMu.Unlock()
	if histPath == "" {
		return
	}
	out := make([]models.DownloadJob, 0, len(histJobs)+1)
	replaced := false
	for _, prev := range histJobs {
		if prev.ID == j.ID {
			out = append(out, j)
			replaced = true
		} else {
			out = append(out, prev)
		}
	}
	if !replaced {
		out = append(out, j)
	}
	if len(out) > jobHistoryCap {
		out = out[len(out)-jobHistoryCap:]
	}
	cutoff := time.Now().Add(-jobHistoryTTL).Unix()
	kept := out[:0]
	for _, prev := range out {
		if prev.UpdatedAt >= cutoff {
			kept = append(kept, prev)
		}
	}
	histJobs = kept
	data, err := json.Marshal(histJobs)
	if err != nil {
		return
	}
	tmp := histPath + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, histPath)
	}
}

func jobHistorySnapshot() []models.DownloadJob {
	histMu.Lock()
	defer histMu.Unlock()
	return append([]models.DownloadJob(nil), histJobs...)
}

// GetJobHistory returns the persisted terminal jobs, newest first,
// owner-filtered like ListJobs. GET /api/jobs/history?limit=N
func (h *Handler) GetJobHistory(w http.ResponseWriter, r *http.Request) {
	all := jobHistorySnapshot()
	out := make([]models.DownloadJob, 0, len(all))
	for _, j := range all {
		if j.Owner != "" && !h.isJobOwnerOrAdmin(r, j.Owner) {
			continue
		}
		out = append(out, j)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].UpdatedAt > out[k].UpdatedAt })
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n := parseLimit(v); n > 0 && n <= jobHistoryCap {
			limit = n
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	jsonResp(w, http.StatusOK, map[string]any{"jobs": out})
}

func parseLimit(v string) int {
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 1_000_000 {
			return 0
		}
	}
	return n
}

// GetJobLog serves a job's accumulated log lines as plain text, so an
// operator can pull a task trace without the UI. Owner or admin only.
// GET /api/jobs/{id}/log
func (h *Handler) GetJobLog(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok := getJob(id)
	if !ok {
		// Fall back to the persisted history: a restarted server no
		// longer has the job in memory.
		for _, j := range jobHistorySnapshot() {
			if j.ID == id {
				job, ok = j, true
				break
			}
		}
	}
	if !ok {
		jsonErr(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Owner != "" && !h.isJobOwnerOrAdmin(r, job.Owner) {
		jsonErr(w, http.StatusNotFound, "job not found")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, line := range job.Logs {
		fmt.Fprintf(w, "%d %s\n", line.Ts, line.Msg)
	}
}

// GetJobStats summarizes job activity for the dashboard: live in-memory
// state plus the 24h window from the persisted history.
// GET /api/jobs/stats
func (h *Handler) GetJobStats(w http.ResponseWriter, r *http.Request) {
	running, done24, failed24 := 0, 0, 0
	cutoff := time.Now().Add(-24 * time.Hour).Unix()
	jobsMu.RLock()
	for _, j := range isoJobs {
		switch j.Status {
		case "running", "queued":
			running++
		case "done", "completed":
			if !isEvacuateJob(*j) && j.UpdatedAt >= cutoff {
				done24++
			}
		case "error":
			if !isEvacuateJob(*j) && j.UpdatedAt >= cutoff {
				failed24++
			}
		}
	}
	jobsMu.RUnlock()
	seen := map[string]bool{}
	for _, j := range isoJobs {
		seen[j.ID] = true
	}
	for _, j := range jobHistorySnapshot() {
		if seen[j.ID] {
			continue
		}
		if j.Owner != "" && !h.isJobOwnerOrAdmin(r, j.Owner) {
			continue
		}
		switch j.Status {
		case "done", "completed":
			if !isEvacuateJob(j) && j.UpdatedAt >= cutoff {
				done24++
			}
		case "error":
			if !isEvacuateJob(j) && j.UpdatedAt >= cutoff {
				failed24++
			}
		}
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"running":     running,
		"done_24h":    done24,
		"failed_24h":  failed24,
		"evacuations": evacuateCount24(cutoff),
	})
}

// isEvacuateJob reports whether a job is a node evacuation, which is
// counted separately by the stats endpoint.
func isEvacuateJob(j models.DownloadJob) bool {
	return len(j.Name) > 9 && j.Name[:9] == "evacuate:"
}

// evacuateCount24 counts evacuation jobs (name prefix "evacuate:") in the
// 24h window across memory and history.
func evacuateCount24(cutoff int64) int {
	count := func(j models.DownloadJob) bool {
		return isEvacuateJob(j) && j.UpdatedAt >= cutoff &&
			(j.Status == "done" || j.Status == "completed" || j.Status == "error")
	}
	seen := map[string]bool{}
	n := 0
	jobsMu.RLock()
	for _, j := range isoJobs {
		seen[j.ID] = true
		if count(*j) {
			n++
		}
	}
	jobsMu.RUnlock()
	for _, j := range jobHistorySnapshot() {
		if !seen[j.ID] && count(j) {
			n++
		}
	}
	return n
}
