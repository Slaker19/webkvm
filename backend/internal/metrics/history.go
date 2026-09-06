// Package metrics implements time-series metric history and alerting
// for VMs (V13-C-03 / V13-C-04).
//
// Design constraints (from the Sprint C directives):
//
//  1. Zero DB bloat: nothing is stored in the main store (JSON/SQLite).
//     Samples live in an in-memory bucketed ring per VM and are flushed
//     periodically (once a minute) to independent append-only JSONL
//     files under {dataDir}/metrics/. Bounded retention keeps the SSD
//     from filling: per-minute files hold ~24h, hourly rollups ~30 days.
//
//  2. Downsampling: fine resolution (per-minute averages) for the last
//     24 hours; hourly averages (rollup) for the 7/30 day views. The
//     collector feeds raw 5s samples; we aggregate, never store raw.
package metrics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"webkvm/internal/models"
)

// agg is a running aggregate of one metric over one time bucket.
type agg struct {
	Sum   float64 `json:"avg,omitempty"`
	Count int     `json:"count,omitempty"`
	Max   float64 `json:"max,omitempty"`
}

// avg returns the bucket average (0 when empty).
func (a agg) avg() float64 {
	if a.Count == 0 {
		return 0
	}
	return a.Sum / float64(a.Count)
}

// bucket holds one aggregated minute/hour for a VM across all series.
type bucket struct {
	CPU   agg `json:"cpu"`
	RAM   agg `json:"ram"`
	DiskR agg `json:"disk_r"`
	DiskW agg `json:"disk_w"`
	NetRx agg `json:"net_rx"`
	NetTx agg `json:"net_tx"`
}

// add folds one sample into the bucket.
func (b *bucket) add(kind string, v float64) {
	a := b.aggFor(kind)
	if a == nil {
		return
	}
	a.Sum += v
	a.Count++
	if v > a.Max {
		a.Max = v
	}
}

func (b *bucket) aggFor(kind string) *agg {
	switch kind {
	case "cpu":
		return &b.CPU
	case "ram":
		return &b.RAM
	case "disk_r":
		return &b.DiskR
	case "disk_w":
		return &b.DiskW
	case "net_rx":
		return &b.NetRx
	case "net_tx":
		return &b.NetTx
	}
	return nil
}

// pointsFor renders a bucket as one ModelsSample (its average).
func (b *bucket) pointsFor(kind string) (float64, bool) {
	a := b.aggFor(kind)
	if a == nil || a.Count == 0 {
		return 0, false
	}
	return a.avg(), true
}

// bucketLine is the on-disk shape of one bucket in a JSONL file.
type bucketLine struct {
	T int64 `json:"t"`
	bucket
}

const (
	// DefaultFlushInterval is how often completed buckets are flushed to
	// disk (60s).
	DefaultFlushInterval = time.Minute
	// MinuteRetention keeps per-minute resolution for 24h.
	MinuteRetention = 24 * time.Hour
	// HourRetention keeps hourly rollups for 30 days.
	HourRetention = 30 * 24 * time.Hour
	// MaxMinuteBuckets is a safety bound on the in-memory minute map.
	MaxMinuteBuckets = 1440 + 4
	// MaxHourBuckets is a safety bound on the in-memory hourly map.
	MaxHourBuckets = 30*24 + 4
)

// TimeSeriesStore aggregates per-VM samples into minute and hourly
// buckets, keeps a bounded in-memory window, and flushes completed
// buckets to append-only JSONL files.
//
// The in-memory maps ARE the query source (bounded to the retention
// windows: 24h of minutes + 30d of hours). The files are the durable
// copy for restart recovery. Flush appends every bucket not yet on
// disk (tracked per VM via lastFlushed) and prunes memory to the
// retention window, so the append is idempotent and memory stays
// bounded regardless of uptime.
type TimeSeriesStore struct {
	dir string

	mu              sync.Mutex
	minutes         map[string]map[int64]*bucket // vmID -> epochMinute -> bucket
	hours           map[string]map[int64]*bucket // vmID -> epochHour -> bucket
	lastFlushedMin  map[string]int64             // vmID -> last minute key written
	lastFlushedHour map[string]int64             // vmID -> last hour key written
}

// NewTimeSeriesStore creates the store under {dataDir}/metrics/.
func NewTimeSeriesStore(dataDir string) *TimeSeriesStore {
	dir := filepath.Join(dataDir, "metrics")
	return &TimeSeriesStore{
		dir:             dir,
		minutes:         map[string]map[int64]*bucket{},
		hours:           map[string]map[int64]*bucket{},
		lastFlushedMin:  map[string]int64{},
		lastFlushedHour: map[string]int64{},
	}
}

// Record folds one raw sample set (every 5s per VM) into the current
// minute and hour buckets.
func (s *TimeSeriesStore) Record(vmID string, at time.Time, m models.VMMetrics) {
	min := at.Truncate(time.Minute).Unix()
	hour := at.Truncate(time.Hour).Unix()

	s.mu.Lock()
	defer s.mu.Unlock()

	mb := s.bucketAt(s.minutes, vmID, min)
	hb := s.bucketAt(s.hours, vmID, hour)
	for _, kind := range []string{"cpu", "ram", "disk_r", "disk_w", "net_rx", "net_tx"} {
		var v float64
		var ok bool
		switch kind {
		case "cpu":
			v, ok = lastOf(m.CPU)
		case "ram":
			v, ok = lastOf(m.RAM)
		case "disk_r":
			v, ok = lastOf(m.DiskRead)
		case "disk_w":
			v, ok = lastOf(m.DiskWrite)
		case "net_rx":
			v, ok = lastOf(m.NetRx)
		case "net_tx":
			v, ok = lastOf(m.NetTx)
		}
		if !ok {
			continue
		}
		mb.add(kind, v)
		hb.add(kind, v)
	}
}

// bucketAt gets-or-creates a bucket, enforcing the retention bounds.
func (s *TimeSeriesStore) bucketAt(m map[string]map[int64]*bucket, vmID string, key int64) *bucket {
	byKey, ok := m[vmID]
	if !ok {
		byKey = map[int64]*bucket{}
		m[vmID] = byKey
	}
	b, ok := byKey[key]
	if !ok {
		if len(byKey) >= MaxMinuteBuckets || len(byKey) >= MaxHourBuckets {
			// Retention window is far larger than the safety bound; if
			// we ever hit it, drop the oldest key.
			var oldest int64
			first := true
			for k := range byKey {
				if first || k < oldest {
					oldest = k
					first = false
				}
			}
			delete(byKey, oldest)
		}
		b = &bucket{}
		byKey[key] = b
	}
	return b
}

func lastOf(series models.MetricsSeries) (float64, bool) {
	if len(series.Points) == 0 {
		return 0, false
	}
	return series.Points[len(series.Points)-1].V, true
}

// Flush appends every completed, not-yet-written bucket to its
// append-only file, then prunes memory to the retention windows.
// Called on a timer and once at shutdown. Idempotent: buckets already
// written are tracked per VM and never re-appended.
func (s *TimeSeriesStore) Flush() error {
	now := time.Now().UTC()
	lastCompletedMin := now.Add(-time.Minute).Truncate(time.Minute).Unix()
	lastCompletedHour := now.Add(-time.Hour).Truncate(time.Hour).Unix()
	minCut := now.Add(-MinuteRetention).Unix()
	hourCut := now.Add(-HourRetention).Unix()

	if err := os.MkdirAll(filepath.Join(s.dir, "history"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "rollup"), 0o700); err != nil {
		return err
	}

	s.mu.Lock()
	var minFlush, hourFlush []string
	for vmID, byKey := range s.minutes {
		last := s.lastFlushedMin[vmID]
		var lines []string
		maxKey := last
		for key, b := range byKey {
			if key <= lastCompletedMin && key > last {
				lines = append(lines, encodeBucket(key, b))
				if key > maxKey {
					maxKey = key
				}
			}
		}
		if len(lines) > 0 {
			sort.Slice(lines, func(i, j int) bool { return lineTime(lines[i]) < lineTime(lines[j]) })
			minFlush = append(minFlush, vmID+"\n"+strings.Join(lines, "\n"))
			s.lastFlushedMin[vmID] = maxKey
		}
		// Prune memory to the retention window (the query source).
		for key := range byKey {
			if key < minCut {
				delete(byKey, key)
			}
		}
		if len(byKey) == 0 {
			delete(s.minutes, vmID)
		}
	}
	for vmID, byKey := range s.hours {
		last := s.lastFlushedHour[vmID]
		var lines []string
		maxKey := last
		for key, b := range byKey {
			if key <= lastCompletedHour && key > last {
				lines = append(lines, encodeBucket(key, b))
				if key > maxKey {
					maxKey = key
				}
			}
		}
		if len(lines) > 0 {
			sort.Slice(lines, func(i, j int) bool { return lineTime(lines[i]) < lineTime(lines[j]) })
			hourFlush = append(hourFlush, vmID+"\n"+strings.Join(lines, "\n"))
			s.lastFlushedHour[vmID] = maxKey
		}
		for key := range byKey {
			if key < hourCut {
				delete(byKey, key)
			}
		}
		if len(byKey) == 0 {
			delete(s.hours, vmID)
		}
	}
	s.mu.Unlock()

	for _, chunk := range minFlush {
		vmID, body := splitChunk(chunk)
		if err := appendLines(filepath.Join(s.dir, "history", vmID+".jsonl"), body); err != nil {
			return err
		}
	}
	for _, chunk := range hourFlush {
		vmID, body := splitChunk(chunk)
		if err := appendLines(filepath.Join(s.dir, "rollup", vmID+".jsonl"), body); err != nil {
			return err
		}
	}

	// Trim retention from disk (occasional rewrite, at most ~1x per day
	// per VM, only when there is actually something to drop).
	if err := s.trimFile(filepath.Join(s.dir, "history"), now.Add(-MinuteRetention).Add(-time.Hour)); err != nil {
		return err
	}
	return s.trimFile(filepath.Join(s.dir, "rollup"), now.Add(-HourRetention).Add(-24*time.Hour))
}

func encodeBucket(key int64, b *bucket) string {
	data, _ := json.Marshal(bucketLine{T: key, bucket: *b})
	return string(data)
}

func lineTime(line string) int64 {
	var bl bucketLine
	if json.Unmarshal([]byte(line), &bl) == nil {
		return bl.T
	}
	return 0
}

func splitChunk(chunk string) (string, string) {
	i := strings.IndexByte(chunk, '\n')
	return chunk[:i], chunk[i+1:]
}

// trimFile rewrites a JSONL directory's files, dropping lines older than
// cutoff (only when there is anything to drop — no rewrite otherwise).
func (s *TimeSeriesStore) trimFile(dir string, cutoff time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	cut := cutoff.Unix()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		if err := trimLines(p, cut); err != nil {
			return err
		}
	}
	return nil
}

func trimLines(path string, cut int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var keep []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	oldestKept := true
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		if lineTime(line) < cut {
			oldestKept = false
			continue
		}
		keep = append(keep, line)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if oldestKept {
		return nil // nothing to drop, no rewrite
	}
	return writeLines(path, keep)
}

// Run is the background flusher. Returns when ctx is cancelled.
func (s *TimeSeriesStore) Run(ctx context.Context) {
	t := time.NewTicker(DefaultFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = s.Flush()
			return
		case <-t.C:
			if err := s.Flush(); err != nil {
				// Logged by the caller (the collector's goroutine has no
				// logger here); flushes are best-effort and retried next tick.
				fmt.Fprintf(os.Stderr, "metrics_flush_failed: %v\n", err)
			}
		}
	}
}

// History returns the downsampled series for a VM over a window.
// window <= 24h uses per-minute buckets; longer uses hourly rollups.
// Points are the bucket averages, in chronological order.
func (s *TimeSeriesStore) History(vmID string, window time.Duration) (models.VMMetrics, error) {
	res := "hour"
	if window <= MinuteRetention {
		res = "minute"
	}
	cut := time.Now().Add(-window).UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	src := s.hours
	if res == "minute" {
		src = s.minutes
	}
	byKey := src[vmID]
	keys := make([]int64, 0, len(byKey))
	for k := range byKey {
		if k >= cut.Unix() {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	out := models.VMMetrics{VMID: vmID, SampledAt: time.Now().Unix()}
	series := []struct {
		dst  *models.MetricsSeries
		kind string
		unit string
	}{
		{&out.CPU, "cpu", "%"},
		{&out.RAM, "ram", "%"},
		{&out.DiskRead, "disk_r", "B/s"},
		{&out.DiskWrite, "disk_w", "B/s"},
		{&out.NetRx, "net_rx", "B/s"},
		{&out.NetTx, "net_tx", "B/s"},
	}
	for _, sdef := range series {
		*sdef.dst = models.MetricsSeries{
			Kind: sdef.kind, Unit: sdef.unit, Window: int(window.Seconds()),
			Points: []models.MetricsSample{},
		}
		for _, k := range keys {
			if v, ok := byKey[k].pointsFor(sdef.kind); ok {
				sdef.dst.Points = append(sdef.dst.Points, models.MetricsSample{T: k, V: v})
			}
		}
	}
	return out, nil
}

// Load rehydrates the in-memory maps from disk so history survives a
// restart, and records the per-VM last-flushed key so the next Flush
// never re-appends what was just loaded (idempotency across restarts).
func (s *TimeSeriesStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cut := time.Now().Add(-MinuteRetention).Unix()
	if err := s.loadDir(filepath.Join(s.dir, "history"), s.minutes, s.lastFlushedMin, cut); err != nil {
		return err
	}
	cutHour := time.Now().Add(-HourRetention).Unix()
	return s.loadDir(filepath.Join(s.dir, "rollup"), s.hours, s.lastFlushedHour, cutHour)
}

func (s *TimeSeriesStore) loadDir(dir string, into map[string]map[int64]*bucket, lastFlushed map[string]int64, minT int64) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		vmID := strings.TrimSuffix(e.Name(), ".jsonl")
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var maxKey int64
		for sc.Scan() {
			line := sc.Text()
			if line == "" {
				continue
			}
			var bl bucketLine
			if err := json.Unmarshal([]byte(line), &bl); err != nil {
				continue
			}
			if bl.T < minT {
				continue
			}
			byKey := into[vmID]
			if byKey == nil {
				byKey = map[int64]*bucket{}
				into[vmID] = byKey
			}
			byKey[bl.T] = &bl.bucket
			if bl.T > maxKey {
				maxKey = bl.T
			}
		}
		ferr := sc.Err()
		f.Close()
		if ferr != nil {
			return ferr
		}
		lastFlushed[vmID] = maxKey
	}
	return nil
}

// appendLines appends lines to a JSONL file (creating it if missing).
func appendLines(path, body string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, werr := f.WriteString(body)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	return cerr
}

func writeLines(path string, lines []string) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	for _, l := range lines {
		if _, err := f.WriteString(l + "\n"); err != nil {
			f.Close()
			return err
		}
	}
	cerr := f.Close()
	if cerr != nil {
		return cerr
	}
	return os.Rename(tmp, path)
}
