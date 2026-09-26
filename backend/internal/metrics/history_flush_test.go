package metrics

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"webkvm/internal/models"
)

func oneSample(v float64) models.VMMetrics {
	return models.VMMetrics{CPU: models.MetricsSeries{Points: []models.MetricsSample{{V: v}}}}
}

// TestFlushRetriesAfterWriteFailure: the watermark used to advance
// before the append, so a failed write (disk full, EIO) still marked the
// buckets as persisted. The next Flush skipped them and the retention
// prune dropped them from memory — samples gone for good, with no
// retry possible. A failed write must leave the data flushable.
func TestFlushRetriesAfterWriteFailure(t *testing.T) {
	dir := t.TempDir()
	s := NewTimeSeriesStore(dir)
	hist := filepath.Join(dir, "metrics", "history")
	if err := os.MkdirAll(hist, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory where the .jsonl belongs makes the append fail.
	blocked := filepath.Join(hist, "vm-1.jsonl")
	if err := os.MkdirAll(blocked, 0o755); err != nil {
		t.Fatal(err)
	}

	s.Record("vm-1", time.Now().UTC().Add(-5*time.Minute), oneSample(42))
	if err := s.Flush(); err == nil {
		t.Fatal("Flush must report the write failure")
	}

	// Clear the obstacle: the sample must still be pending, not lost.
	if err := os.RemoveAll(blocked); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("second Flush: %v", err)
	}
	data, err := os.ReadFile(blocked)
	if err != nil || len(data) == 0 {
		t.Fatalf("sample was lost instead of retried: err=%v", err)
	}
}

// TestFlushIsolatesPerVMFailures: one unwritable file used to abort the
// loop mid-iteration, so every VM after it (and the entire hourly
// rollup pass) silently skipped its flush. Map order is random, so the
// victims changed on every tick.
func TestFlushIsolatesPerVMFailures(t *testing.T) {
	dir := t.TempDir()
	s := NewTimeSeriesStore(dir)
	hist := filepath.Join(dir, "metrics", "history")
	rollup := filepath.Join(dir, "metrics", "rollup")
	os.MkdirAll(hist, 0o755)
	os.MkdirAll(filepath.Join(hist, "vm-bad.jsonl"), 0o755)

	past := time.Now().UTC().Add(-2 * time.Hour)
	for _, id := range []string{"vm-bad", "vm-a", "vm-b", "vm-c"} {
		s.Record(id, past, oneSample(7))
	}
	if err := s.Flush(); err == nil {
		t.Fatal("Flush must still report the failing VM")
	}
	for _, id := range []string{"vm-a", "vm-b", "vm-c"} {
		if _, err := os.Stat(filepath.Join(hist, id+".jsonl")); err != nil {
			t.Errorf("%s was dragged down by vm-bad: %v", id, err)
		}
		// The hourly pass runs after the minute pass; it must not be
		// skipped either.
		if _, err := os.Stat(filepath.Join(rollup, id+".jsonl")); err != nil {
			t.Errorf("%s hourly rollup skipped: %v", id, err)
		}
	}
}

// TestMinuteBucketsHoldFullRetention: the bound was
// `>= MaxMinuteBuckets || >= MaxHourBuckets`, i.e. min(1444, 724) for
// both maps. History() reads memory, never the files, so the UI lost
// half of the 24h of per-minute resolution it advertises.
func TestMinuteBucketsHoldFullRetention(t *testing.T) {
	s := NewTimeSeriesStore(t.TempDir())
	base := time.Now().UTC().Add(-23 * time.Hour).Truncate(time.Minute)
	const samples = 1400
	for i := 0; i < samples; i++ {
		s.Record("vm-1", base.Add(time.Duration(i)*time.Minute), oneSample(1))
	}
	got, err := s.History("vm-1", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.CPU.Points) != samples {
		t.Errorf("History(24h) = %d points, want %d (minute map capped at the HOUR bound)",
			len(got.CPU.Points), samples)
	}
}

// TestMetricsFileNameRejectsTraversal: Record() takes whatever id the
// collector reports and it reached filepath.Join unchecked, so an id
// with "/" or ".." appended JSONL onto files outside the metrics dir.
func TestMetricsFileNameRejectsTraversal(t *testing.T) {
	for _, bad := range []string{
		"", ".", "..", "../../users.json", "a/b", `a\b`, "vm\n1", "vm\x00",
	} {
		if _, err := metricsFileName(bad); err == nil {
			t.Errorf("metricsFileName(%q) was accepted", bad)
		}
	}
	got, err := metricsFileName("vm-1")
	if err != nil || got != "vm-1.jsonl" {
		t.Errorf("metricsFileName(\"vm-1\") = %q, %v", got, err)
	}
}
