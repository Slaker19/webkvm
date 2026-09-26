package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"webkvm/internal/models"
)

// TestFlushAndWaitPersistsBeforeReturning covers the shutdown data loss.
//
// main used to `defer cancelEvents()`, so the context was cancelled as
// the process was already exiting. TimeSeriesStore.Run reacted by
// flushing, but nothing joined that goroutine: the write was still in
// flight when the process died, so up to a full DefaultFlushInterval of
// metrics for the whole fleet was lost on every restart and upgrade.
// FlushAndWait is the join point the shutdown path calls.
func TestFlushAndWaitPersistsBeforeReturning(t *testing.T) {
	dir := t.TempDir()
	s := NewTimeSeriesStore(dir)

	now := time.Now()
	s.Record("vm-a", now, models.VMMetrics{CPU: models.MetricsSeries{Kind: "cpu", Points: []models.MetricsSample{{T: now.Unix(), V: 42}}}})

	// Nothing on disk yet: the store only persists on Flush.
	entries, _ := os.ReadDir(filepath.Join(dir, "history"))
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && fi.Size() > 0 {
			t.Fatalf("fixture wrote %s before any flush", e.Name())
		}
	}

	s.FlushAndWait()

	// The contract: when FlushAndWait returns, the data is on disk.
	// A caller that returns immediately after it may exit the process.
	got, err := s.History("vm-a", 24*time.Hour)
	if err != nil {
		t.Fatalf("history unreadable after FlushAndWait: %v", err)
	}
	if len(got.CPU.Points) == 0 {
		t.Fatal("FlushAndWait returned but the series is not persisted")
	}
}
