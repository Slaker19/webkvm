package metrics

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"webkvm/internal/models"
)

func vmMetrics(cpu, ram, diskR, diskW, netRx, netTx float64) models.VMMetrics {
	return models.VMMetrics{
		VMID:      "vm1",
		SampledAt: time.Now().Unix(),
		CPU:       models.MetricsSeries{Kind: "cpu", Points: []models.MetricsSample{{T: 1, V: cpu}}},
		RAM:       models.MetricsSeries{Kind: "ram", Points: []models.MetricsSample{{T: 1, V: ram}}},
		DiskRead:  models.MetricsSeries{Kind: "disk_r", Points: []models.MetricsSample{{T: 1, V: diskR}}},
		DiskWrite: models.MetricsSeries{Kind: "disk_w", Points: []models.MetricsSample{{T: 1, V: diskW}}},
		NetRx:     models.MetricsSeries{Kind: "net_rx", Points: []models.MetricsSample{{T: 1, V: netRx}}},
		NetTx:     models.MetricsSeries{Kind: "net_tx", Points: []models.MetricsSample{{T: 1, V: netTx}}},
	}
}

// Recording samples in one minute aggregates them into a single
// per-minute bucket whose point is the average.
func TestHistory_MinuteBucketAverages(t *testing.T) {
	dir := t.TempDir()
	s := NewTimeSeriesStore(dir)
	at := time.Now().UTC()
	for i := 0; i < 4; i++ {
		s.Record("vm1", at, vmMetrics(float64(20+i), 50, 100, 200, 300, 400))
	}
	h, err := s.History("vm1", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.CPU.Points) != 1 {
		t.Fatalf("expected 1 minute bucket, got %d", len(h.CPU.Points))
	}
	// average of 20,21,22,23 = 21.5
	if v := h.CPU.Points[0].V; v < 21.4 || v > 21.6 {
		t.Errorf("cpu avg = %v, want ~21.5", v)
	}
	if v := h.RAM.Points[0].V; v != 50 {
		t.Errorf("ram avg = %v, want 50", v)
	}
	if v := h.DiskRead.Points[0].V; v != 100 {
		t.Errorf("disk_r avg = %v, want 100", v)
	}
}

// Different minutes produce separate points, ordered chronologically.
func TestHistory_MultipleMinutes(t *testing.T) {
	dir := t.TempDir()
	s := NewTimeSeriesStore(dir)
	base := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute)
	for i := 0; i < 3; i++ {
		s.Record("vm1", base.Add(time.Duration(i)*time.Minute), vmMetrics(float64(10*i), 0, 0, 0, 0, 0))
	}
	h, _ := s.History("vm1", time.Hour)
	if len(h.CPU.Points) != 3 {
		t.Fatalf("expected 3 points, got %d", len(h.CPU.Points))
	}
	if h.CPU.Points[0].V != 0 || h.CPU.Points[1].V != 10 || h.CPU.Points[2].V != 20 {
		t.Errorf("points out of order: %+v", h.CPU.Points)
	}
}

// Flush writes completed buckets to JSONL files; a second flush does not
// duplicate lines (idempotency via lastFlushed).
func TestFlush_AppendOnlyAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := NewTimeSeriesStore(dir)
	// A COMPLETED minute (in the past) plus an in-flight current one.
	past := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	s.Record("vm1", past, vmMetrics(42, 50, 100, 200, 300, 400))
	s.Record("vm1", time.Now().UTC(), vmMetrics(1, 2, 3, 4, 5, 6))

	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	lines := readLines(t, filepath.Join(dir, "metrics", "history", "vm1.jsonl"))
	if len(lines) != 1 {
		t.Fatalf("history file should have 1 line (completed minute only), got %d", len(lines))
	}
	if !strings.Contains(lines[0], `"cpu":{"avg":42`) {
		t.Errorf("line missing cpu avg 42: %s", lines[0])
	}

	// Second flush: nothing new appended (current minute still in flight).
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := len(readLines(t, filepath.Join(dir, "metrics", "history", "vm1.jsonl"))); got != 1 {
		t.Fatalf("second flush duplicated lines: %d", got)
	}

	// File mode 0600.
	if fi, err := os.Stat(filepath.Join(dir, "metrics", "history", "vm1.jsonl")); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode = %v, want 0600", fi.Mode().Perm())
	}
}

// Load rehydrates memory from disk and the next Flush does not re-append
// what was loaded (restart-safe).
func TestLoad_RestartNoDuplicates(t *testing.T) {
	dir := t.TempDir()
	s := NewTimeSeriesStore(dir)
	past := time.Now().UTC().Truncate(time.Minute).Add(-2 * time.Minute)
	s.Record("vm1", past, vmMetrics(42, 50, 100, 200, 300, 400))
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	// "Restart".
	s2 := NewTimeSeriesStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	h, _ := s2.History("vm1", time.Hour)
	if len(h.CPU.Points) != 1 || h.CPU.Points[0].V != 42 {
		t.Fatalf("history not rehydrated: %+v", h.CPU.Points)
	}
	if err := s2.Flush(); err != nil {
		t.Fatal(err)
	}
	if got := len(readLines(t, filepath.Join(dir, "metrics", "history", "vm1.jsonl"))); got != 1 {
		t.Fatalf("flush after load duplicated lines: %d", got)
	}
}

// Longer windows use the hourly rollup resolution.
func TestHistory_HourlyRollup(t *testing.T) {
	dir := t.TempDir()
	s := NewTimeSeriesStore(dir)
	// Two samples inside the SAME hour -> one hourly bucket, averaged.
	h1 := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	s.Record("vm1", h1, vmMetrics(40, 0, 0, 0, 0, 0))
	s.Record("vm1", h1.Add(30*time.Minute), vmMetrics(60, 0, 0, 0, 0, 0))
	// Different hour -> second bucket.
	s.Record("vm1", h1.Add(time.Hour), vmMetrics(10, 0, 0, 0, 0, 0))

	h, err := s.History("vm1", 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.CPU.Points) != 2 {
		t.Fatalf("expected 2 hourly buckets, got %d", len(h.CPU.Points))
	}
	if v := h.CPU.Points[0].V; v < 49.9 || v > 50.1 {
		t.Errorf("hourly avg = %v, want ~50", v)
	}
}

// An empty window returns no points.
func TestHistory_EmptyVM(t *testing.T) {
	dir := t.TempDir()
	s := NewTimeSeriesStore(dir)
	h, err := s.History("ghost", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.CPU.Points) != 0 {
		t.Fatalf("ghost VM should have no history, got %+v", h.CPU.Points)
	}
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if sc.Text() != "" {
			out = append(out, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
