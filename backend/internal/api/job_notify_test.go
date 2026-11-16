package api

import (
	"testing"

	"webkvm/internal/models"
)

func TestJobTerminalTransitionNotifiesOnce(t *testing.T) {
	var calls []string
	SetJobNotifier(func(owner, name, status, detail string) {
		calls = append(calls, owner+"/"+name+"/"+status)
	})
	defer SetJobNotifier(nil)

	job := &models.DownloadJob{ID: "notify-test", Name: "clone", Owner: "admin", Status: "running"}
	storeJob(job)
	updateJobFull(job.ID, 50, "running", "working", 0, 0, 0, 0)
	updateJobFull(job.ID, 100, "done", "finished", 0, 0, 0, 0)
	updateJobFull(job.ID, 100, "done", "finished", 0, 0, 0, 0)
	if len(calls) != 1 || calls[0] != "admin/clone/done" {
		t.Fatalf("calls = %v", calls)
	}
}
