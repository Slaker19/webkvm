package api

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/models"
	"webkvm/internal/safego"
)

// ---- Job Tracking (thread-safe) ----
var (
	isoJobs = make(map[string]*models.DownloadJob)
	jobsMu  sync.RWMutex
)

// Job GC policy: finished jobs (completed/error) older than
// jobTTL are purged by a dedicated goroutine every jobSweepInterval.
// queued/running jobs are NEVER purged, no matter how old they get — an
// in-flight download keeps its entry until it reaches a terminal state.
const (
	jobTTL           = 24 * time.Hour
	jobSweepInterval = 5 * time.Minute
)

func storeJob(j *models.DownloadJob) {
	jobsMu.Lock()
	j.UpdatedAt = time.Now().Unix()
	isoJobs[j.ID] = j
	jobsMu.Unlock()
}

func getJob(id string) (models.DownloadJob, bool) {
	jobsMu.RLock()
	defer jobsMu.RUnlock()
	j, ok := isoJobs[id]
	if !ok {
		return models.DownloadJob{}, false
	}
	return *j, true
}

// updateJob records a job's progress. The last argument is the job's
// text for this step; it is filed as the error only when the status
// says the job failed, and as commentary otherwise. Call sites pass
// both kinds of string, and routing every one of them into Error left
// successful jobs looking broken to anything reading that field.
func updateJob(id string, progress float64, status string, text string) {
	updateJobFull(id, progress, status, text, 0, 0, 0, 0)
}

func updateJobFull(id string, progress float64, status string, text string, bytesDone, bytesTotal, speedBps, etaSeconds int64) {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	if j, ok := isoJobs[id]; ok {
		j.Progress = progress
		j.Status = status
		if status == "error" {
			j.Error = text
		} else {
			j.Message = text
			// A retry that succeeds must not keep displaying the
			// failure that preceded it.
			j.Error = ""
		}
		j.UpdatedAt = time.Now().Unix()
		if bytesDone > 0 || bytesTotal > 0 {
			j.BytesDone = bytesDone
			j.BytesTotal = bytesTotal
		}
		if speedBps > 0 {
			j.SpeedBps = speedBps
		}
		if etaSeconds >= 0 {
			j.ETASeconds = etaSeconds
		}
	}
}

// pruneExpiredJobs removes jobs that reached a terminal state
// (completed/error) more than ttl ago. queued/running jobs are never
// touched. Thread-safe. Returns the number of purged jobs.
func pruneExpiredJobs(now time.Time, ttl time.Duration) int {
	jobsMu.Lock()
	defer jobsMu.Unlock()
	cutoff := now.Add(-ttl).Unix()
	n := 0
	for id, j := range isoJobs {
		// Two terminal-status conventions coexist: download/appliance
		// jobs use "completed", while submitJob (clone/snapshot/deploy/
		// probe) uses "done". Purging only "completed"/"error" leaked
		// every "done" job forever — an unbounded map growth.
		terminal := j.Status == "completed" || j.Status == "done" || j.Status == "error"
		if terminal && j.UpdatedAt > 0 && j.UpdatedAt <= cutoff {
			delete(isoJobs, id)
			n++
		}
	}
	return n
}

// StartJobSweeper launches the background garbage collector for finished
// download/appliance jobs. It ticks every interval and purges only
// terminal jobs older than ttl; the loop stops when ctx is cancelled
// (server shutdown). log is optional and receives a single line per run
// that actually purged something.
func StartJobSweeper(ctx context.Context, interval, ttl time.Duration, log func(msg string, args ...any)) {
	go func() {
		defer safego.Recover("job_sweeper")
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n := pruneExpiredJobs(time.Now(), ttl)
				if n > 0 && log != nil {
					log("jobs_purged", "count", n, "ttl", ttl.String())
				}
			}
		}
	}()
}

// progressReportingWriter wraps dst and reports the cumulative number of
// bytes written via report(n). It lets long-running downloads surface live
// progress to the job tracker instead of jumping from 0% to 100%.
type progressReportingWriter struct {
	w       io.Writer
	total   int64
	written int64
	report  func(n int64)
}

func (p *progressReportingWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if n > 0 {
		p.report(p.written)
	}
	return n, err
}

func (h *Handler) GetDownloadJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	job, ok := getJob(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "job not found")
		return
	}
	if job.Owner != "" && !h.isJobOwnerOrAdmin(r, job.Owner) {
		jsonErr(w, http.StatusNotFound, "job not found")
		return
	}
	jsonResp(w, http.StatusOK, job)
}

func (h *Handler) ListJobs(w http.ResponseWriter, r *http.Request) {
	jobsMu.RLock()
	defer jobsMu.RUnlock()

	out := make([]models.DownloadJob, 0, len(isoJobs))
	for _, j := range isoJobs {
		if j.Owner != "" && !h.isJobOwnerOrAdmin(r, j.Owner) {
			continue
		}
		out = append(out, *j)
	}
	jsonResp(w, http.StatusOK, map[string]any{"jobs": out})
}

func (h *Handler) isJobOwnerOrAdmin(r *http.Request, owner string) bool {
	if r.Header.Get("X-Role") == models.RoleAdmin {
		return true
	}
	return r.Header.Get("X-User") == owner
}
