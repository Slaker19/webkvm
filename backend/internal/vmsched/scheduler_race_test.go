package vmsched

import (
	"log/slog"
	"sync"
	"testing"
)

// TestSchedulerConcurrentRebuildAndStop must be run with -race.
//
// Rebuild REPLACES s.cron (it is called on every schedule edit from the
// API), while Start and Stop used to read the field with no lock at
// all. `go test -race` flagged vmsched.go:187 against :199 as a genuine
// data race: Stop could act on a cron that Rebuild was in the middle of
// discarding, so "stop the scheduler" silently left the live cron
// running and VMs kept being powered on and snapshotted on schedule.
func TestSchedulerConcurrentRebuildAndStop(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Set("vm1", Schedule{StartCron: "*/5 * * * *"}); err != nil {
		t.Fatal(err)
	}
	sc := NewScheduler(store, func(string, string) error { return nil }, nil, slog.Default())
	sc.Start()
	defer sc.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); sc.Rebuild() }()
		go func() { defer wg.Done(); sc.Stop() }()
	}
	wg.Wait()
}
