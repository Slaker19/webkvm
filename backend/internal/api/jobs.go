package api

import (
	"fmt"
	"log/slog"
	"time"

	"webkvm/internal/models"
	"webkvm/internal/safego"
)

// submitJob registers an async job and runs fn in a guarded background
// goroutine. The handler returns immediately with the job ID (HTTP 202)
// instead of blocking for a long operation (VM clone, snapshot, ...);
// consumers poll GET /api/jobs/{id} until Status is a terminal state
// ("done" | "error"). Success payloads land in Result, failures in Error.
func submitJob(name string, fn func() (any, error)) *models.DownloadJob {
	job := &models.DownloadJob{
		ID:     fmt.Sprintf("vm_%d", time.Now().UnixNano()),
		Name:   name,
		Status: "running",
	}
	storeJob(job)
	go func() {
		defer safego.Recover("job:" + name)
		result, err := fn()
		if err != nil {
			slog.Warn("job_failed", "job", job.ID, "name", name, "err", err)
			updateJob(job.ID, 0, "error", err.Error())
			return
		}
		jobsMu.Lock()
		job.Status = "done"
		job.Result = result
		job.UpdatedAt = time.Now().Unix()
		jobsMu.Unlock()
	}()
	return job
}
