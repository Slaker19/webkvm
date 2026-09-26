package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"webkvm/internal/models"
	"webkvm/internal/safego"
)

// jobOwner extracts the authenticated username the auth middleware put on
// the request. Kept as a helper so every job-creating handler records the
// owner the same way, and so a missing X-User is obvious at one place.
func jobOwner(r *http.Request) string {
	return r.Header.Get("X-User")
}

// submitJob registers an async job and runs fn in a guarded background
// goroutine. The handler returns immediately with the job ID (HTTP 202)
// instead of blocking for a long operation (VM clone, snapshot, ...);
// consumers poll GET /api/jobs/{id} until Status is a terminal state
// ("done" | "error"). Success payloads land in Result, failures in Error.
// owner is the username the job belongs to (see models.DownloadJob.Owner);
// pass jobOwner(r) at every call site so GetDownloadJob can keep one
// user's transfers out of another's reach.
func submitJob(owner, name string, fn func() (any, error)) *models.DownloadJob {
	job := &models.DownloadJob{
		ID:     fmt.Sprintf("vm_%d", time.Now().UnixNano()),
		Name:   name,
		Owner:  owner,
		Status: "running",
	}
	storeJob(job)
	initial := *job
	go func() {
		defer safego.Recover("job:" + name)
		result, err := fn()
		if err != nil {
			slog.Warn("job_failed", "job", job.ID, "name", name, "err", err)
			updateJob(job.ID, 0, "error", err.Error())
			return
		}
		jobsMu.Lock()
		if j, ok := isoJobs[job.ID]; ok {
			j.Status = "done"
			j.Result = result
			j.UpdatedAt = time.Now().Unix()
		}
		jobsMu.Unlock()
	}()
	return &initial
}

// submitJobWithPool is submitJob for work that writes into a storage
// pool and can report its own progress.
//
// The pool is recorded on the job so a polling client can say where the
// data is going, and fn receives a progress callback so a long copy
// shows movement instead of a bar frozen at 0 until it finishes.
//
// A negative pct means "stage changed, percentage unknown" — used by
// backends (Incus) that report a human-readable transfer string rather
// than a number, where overwriting a real percentage with 0 would make
// the bar jump backwards.
func submitJobWithPool(owner, name, pool string, fn func(progress func(pct float64, stage string)) (any, error)) *models.DownloadJob {
	job := &models.DownloadJob{
		ID:     fmt.Sprintf("job_%d", time.Now().UnixNano()),
		Name:   name,
		Owner:  owner,
		Status: "running",
		Pool:   pool,
	}
	storeJob(job)
	initial := *job
	go func() {
		defer safego.Recover("job:" + name)
		progress := func(pct float64, stage string) {
			jobsMu.Lock()
			if j, ok := isoJobs[job.ID]; ok {
				if pct >= 0 {
					j.Progress = pct
				}
				j.Stage = stage
				j.UpdatedAt = time.Now().Unix()
			}
			jobsMu.Unlock()
		}
		result, err := fn(progress)
		if err != nil {
			slog.Warn("job_failed", "job", job.ID, "name", name, "err", err)
			updateJob(job.ID, 0, "error", err.Error())
			return
		}
		jobsMu.Lock()
		if j, ok := isoJobs[job.ID]; ok {
			j.Status = "done"
			j.Stage = "move_done"
			j.Progress = 100
			j.Result = result
			j.UpdatedAt = time.Now().Unix()
		}
		jobsMu.Unlock()
	}()
	return &initial
}
