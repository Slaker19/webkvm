package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"webkvm/internal/models"
)

func setHistoryDir(t *testing.T, dir string) {
	t.Helper()
	SetJobsHistoryDir(dir)
	t.Cleanup(func() { SetJobsHistoryDir("") })
}

// TestJobLogRingCapsLines: the per-job log ring keeps only the last
// jobLogCap lines.
func TestJobLogRingCapsLines(t *testing.T) {
	j := &models.DownloadJob{ID: "job-1"}
	for i := 0; i < jobLogCap+50; i++ {
		appendJobLog(j, fmt.Sprintf("step %d", i))
	}
	if len(j.Logs) != jobLogCap {
		t.Fatalf("logs = %d, want %d", len(j.Logs), jobLogCap)
	}
	if j.Logs[jobLogCap-1].Msg != fmt.Sprintf("step %d", jobLogCap+49) {
		t.Fatalf("last line = %s", j.Logs[jobLogCap-1].Msg)
	}
}

// TestJobHistoryPersistAndReload: terminal jobs are persisted to
// jobs-history.json, deduped by ID, and survive a reload.
func TestJobHistoryPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	setHistoryDir(t, dir)
	recordJobHistory(models.DownloadJob{ID: "j1", Status: "done", Owner: "alice", UpdatedAt: time.Now().Unix()})
	recordJobHistory(models.DownloadJob{ID: "j2", Status: "error", Owner: "bob", UpdatedAt: time.Now().Unix()})
	recordJobHistory(models.DownloadJob{ID: "j1", Status: "error", Owner: "alice", UpdatedAt: time.Now().Unix()})

	data, err := os.ReadFile(filepath.Join(dir, "jobs-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recs []models.DownloadJob
	if err := json.Unmarshal(data, &recs); err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("history entries = %d, want 2 (j1 deduped)", len(recs))
	}
	// Reload through SetJobsHistoryDir (fresh process equivalent).
	SetJobsHistoryDir(dir)
	got := jobHistorySnapshot()
	if len(got) != 2 {
		t.Fatalf("reloaded = %d, want 2", len(got))
	}
}

// TestJobHistoryCapsAndTTL: the file keeps at most jobHistoryCap records
// and drops entries older than the TTL window.
func TestJobHistoryCapsAndTTL(t *testing.T) {
	dir := t.TempDir()
	setHistoryDir(t, dir)
	now := time.Now().Unix()
	// One ancient record, evicted by the cap before the TTL could.
	recordJobHistory(models.DownloadJob{ID: "old", Status: "done", UpdatedAt: now - int64(8*24*time.Hour/time.Second)})
	for i := 0; i < jobHistoryCap+30; i++ {
		recordJobHistory(models.DownloadJob{
			ID: fmt.Sprintf("j%03d", i), Status: "done", UpdatedAt: now,
		})
	}
	snap := jobHistorySnapshot()
	if len(snap) != jobHistoryCap {
		t.Fatalf("history = %d, want %d", len(snap), jobHistoryCap)
	}
	for _, j := range snap {
		if j.ID == "old" {
			t.Fatal("expired record survived the cap")
		}
		if j.ID == fmt.Sprintf("j%03d", 0) {
			t.Fatal("oldest record should have been dropped by the cap")
		}
	}
}

// TestGetJobHistoryOwnerFilter: non-admin callers only see their own
// terminal jobs.
func TestGetJobHistoryOwnerFilter(t *testing.T) {
	dir := t.TempDir()
	setHistoryDir(t, dir)
	now := time.Now().Unix()
	recordJobHistory(models.DownloadJob{ID: "a", Status: "done", Owner: "alice", UpdatedAt: now})
	recordJobHistory(models.DownloadJob{ID: "b", Status: "done", Owner: "bob", UpdatedAt: now + 1})

	h := &Handler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/history", nil)
	req.Header.Set("X-User", "alice")
	req.Header.Set("X-Role", "user")
	h.GetJobHistory(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var out struct{ Jobs []models.DownloadJob }
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Jobs) != 1 || out.Jobs[0].ID != "a" {
		t.Fatalf("alice sees %+v", out.Jobs)
	}

	// Admin sees both, newest first.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/jobs/history", nil)
	req.Header.Set("X-User", "root")
	req.Header.Set("X-Role", "admin")
	h.GetJobHistory(rec, req)
	out.Jobs = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Jobs) != 2 || out.Jobs[0].ID != "b" {
		t.Fatalf("admin sees %+v", out.Jobs)
	}
}

// TestGetJobLogFromHistory: after the in-memory job is gone the log
// endpoint still serves the persisted copy.
func TestGetJobLogFromHistory(t *testing.T) {
	dir := t.TempDir()
	setHistoryDir(t, dir)
	now := time.Now().Unix()
	recordJobHistory(models.DownloadJob{
		ID: "gone", Status: "done", Owner: "alice", UpdatedAt: now,
		Logs: []models.JobLogLine{{Ts: now, Msg: "step one"}, {Ts: now, Msg: "step two"}},
	})

	h := &Handler{}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/jobs/gone/log", nil)
	req.Header.Set("X-User", "alice")
	req.Header.Set("X-Role", "user")
	h.GetJobLog(rec, withID(req, "gone"))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	body := rec.Body.String()
	if body != fmt.Sprintf("%d step one\n%d step two\n", now, now) {
		t.Fatalf("log body = %q", body)
	}

	// Another user gets a 404, not the log.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/jobs/gone/log", nil)
	req.Header.Set("X-User", "bob")
	req.Header.Set("X-Role", "user")
	h.GetJobLog(rec, withID(req, "gone"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("bob code = %d, want 404", rec.Code)
	}
}

// TestGetJobStatsCountsWindows: live running jobs count once, terminal
// ones fall into the 24h window, evacuations are counted by name prefix.
// Asserts on deltas: the shared isoJobs map carries jobs from other tests.
func TestGetJobStatsCountsWindows(t *testing.T) {
	dir := t.TempDir()
	setHistoryDir(t, dir)
	h := &Handler{}
	fetch := func() map[string]any {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/jobs/stats", nil)
		req.Header.Set("X-User", "a")
		req.Header.Set("X-Role", "admin")
		h.GetJobStats(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("code = %d", rec.Code)
		}
		var stats map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
			t.Fatal(err)
		}
		return stats
	}
	before := fetch()
	num := func(m map[string]any, k string) float64 { return m[k].(float64) }

	now := time.Now().Unix()
	storeJob(&models.DownloadJob{ID: "live", Status: "running", Owner: "a"})
	recordJobHistory(models.DownloadJob{ID: "h1", Status: "done", Owner: "a", UpdatedAt: now})
	recordJobHistory(models.DownloadJob{ID: "h2", Status: "error", Owner: "a", UpdatedAt: now - 3600})
	recordJobHistory(models.DownloadJob{ID: "h3", Status: "error", Owner: "a", UpdatedAt: now - 3*86400}) // outside window
	recordJobHistory(models.DownloadJob{ID: "ev1", Status: "done", Owner: "a", UpdatedAt: now, Name: "evacuate:node-x"})

	after := fetch()
	if got := num(after, "running") - num(before, "running"); got != 1 {
		t.Fatalf("running delta = %v", got)
	}
	if got := num(after, "done_24h") - num(before, "done_24h"); got != 1 {
		t.Fatalf("done_24h delta = %v (want 1: h1; ev1 counted separately)", got)
	}
	if got := num(after, "failed_24h") - num(before, "failed_24h"); got != 1 {
		t.Fatalf("failed_24h delta = %v", got)
	}
	if got := num(after, "evacuations") - num(before, "evacuations"); got != 1 {
		t.Fatalf("evacuations delta = %v", got)
	}
}

// TestEvacuationWritesJobLog: the evacuation loop appends per-VM lines to
// the job's log ring.
func TestEvacuationWritesJobLog(t *testing.T) {
	vms := []models.VM{{ID: "vm-ok", Name: "ok", State: models.VMStateRunning}}
	mig := &fakeMigrator{}
	h, src, dst := evacuateFixture(t, vms, mig)

	code, start := evacuateRequest(h, src, `{"target":"`+dst.Name+`"}`)
	if code != http.StatusAccepted {
		t.Fatalf("code = %d", code)
	}
	em := h.evacuationManager()
	waitFor(t, func() bool {
		rec, _ := em.get(src.ID)
		return rec.Status != "running"
	})
	jobID, _ := start["job"].(string)
	job, ok := getJob(jobID)
	if !ok {
		t.Fatal("job disappeared")
	}
	found := false
	for _, line := range job.Logs {
		if line.Msg == "migrated ok (vm-ok) to dst-node" {
			found = true
		}
	}
	if !found {
		t.Fatalf("log lines = %+v", job.Logs)
	}
}

// TestHistoryDisabledInUnitContext: without SetJobsHistoryDir the recorder
// is a no-op (memory-only behavior for tests that don't opt in).
func TestHistoryDisabledInUnitContext(t *testing.T) {
	SetJobsHistoryDir("")
	recordJobHistory(models.DownloadJob{ID: "nope", Status: "done", UpdatedAt: time.Now().Unix()})
	if got := jobHistorySnapshot(); len(got) != 0 {
		t.Fatalf("snapshot = %+v, want empty", got)
	}
}
