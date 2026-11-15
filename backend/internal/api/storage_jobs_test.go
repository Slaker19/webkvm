package api

import (
	"testing"
	"time"

	"webkvm/internal/models"
)

func TestUpdateJobErrorStoresStableCodeAndFinalProgress(t *testing.T) {
	job := &models.DownloadJob{ID: "job-test-progress", Status: "running"}
	storeJob(job)
	t.Cleanup(func() {
		jobsMu.Lock()
		delete(isoJobs, job.ID)
		jobsMu.Unlock()
	})

	updateJobErrorAtProgress(job.ID, "quota_exceeded_after_materialization", "quota exceeded", 99)

	got, ok := getJob(job.ID)
	if !ok || got.Status != "error" || got.ErrorCode != "quota_exceeded_after_materialization" || got.Error != "quota exceeded" || got.Progress != 99 {
		t.Fatalf("failed job = %+v, exists=%t", got, ok)
	}
	if got.UpdatedAt < time.Now().Add(-time.Minute).Unix() {
		t.Fatalf("UpdatedAt = %d, expected recent update", got.UpdatedAt)
	}
}
