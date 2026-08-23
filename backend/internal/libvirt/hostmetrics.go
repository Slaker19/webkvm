package libvirt

import (
	"bufio"
	"context"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"webkvm/internal/events"
)

// HostMetricsSample is one 5-second sample of the host as a whole.
type HostMetricsSample struct {
	T         int64   `json:"t"`
	CPUUsage  float64 `json:"cpu_usage"`
	UsedRAM   int64   `json:"used_ram"`
	TotalRAM  int64   `json:"total_ram"`
	UsedDisk  int64   `json:"used_disk"`
	TotalDisk int64   `json:"total_disk"`
	NetRx     int64   `json:"net_rx"` // bytes/sec aggregate
	NetTx     int64   `json:"net_tx"` // bytes/sec aggregate
}

// HostMetricsSeries is the time-series history of host metrics.
type HostMetricsSeries struct {
	Kind   string              `json:"kind"`
	Window int                 `json:"window"`
	Points []HostMetricsSample `json:"points"`
}

// HostMetricsCollector samples the host every `interval` and keeps
// the last `capacity` samples in a ring buffer. Each new sample is
// also broadcast over the events hub as a `host.metrics` event so
// the frontend can stream it via SSE.
type HostMetricsCollector struct {
	mu       sync.RWMutex
	samples  []HostMetricsSample
	head     int
	full     bool
	capacity int
	interval time.Duration
	hub      *events.Hub

	// last /proc/net/dev totals for rate calculation
	lastNetRx int64
	lastNetTx int64
	lastTime  time.Time
}

// NewHostMetricsCollector creates the collector. Call Run() in a
// goroutine to start the sampling loop.
func NewHostMetricsCollector(hub *events.Hub) *HostMetricsCollector {
	return &HostMetricsCollector{
		samples:  make([]HostMetricsSample, 0, 720),
		capacity: 720, // 1 hour @ 5s
		interval: 5 * time.Second,
		hub:      hub,
		lastTime: time.Now(),
	}
}

func (c *HostMetricsCollector) Run(ctx context.Context) {
	t := time.NewTicker(c.interval)
	defer t.Stop()
	c.sample() // take first sample immediately
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.sampleAt(now)
		}
	}
}

func (c *HostMetricsCollector) sample() {
	c.sampleAt(time.Now())
}

func (c *HostMetricsCollector) sampleAt(now time.Time) {
	s := HostMetricsSample{T: now.Unix()}
	s.CPUUsage = readCPUUsage()
	if ram, total, used, ok := readRAM(); ok {
		s.TotalRAM = total
		s.UsedRAM = used
		_ = ram
	}
	if total, used, ok := readDisk(); ok {
		s.TotalDisk = total
		s.UsedDisk = used
	}
	if rx, tx, ok := readNetTotals(); ok {
		// Convert cumulative bytes to bytes/sec since last sample.
		dt := now.Sub(c.lastTime).Seconds()
		if dt > 0 && c.lastTime.Unix() > 0 {
			s.NetRx = int64(float64(rx-c.lastNetRx) / dt)
			s.NetTx = int64(float64(tx-c.lastNetTx) / dt)
			if s.NetRx < 0 {
				s.NetRx = 0
			}
			if s.NetTx < 0 {
				s.NetTx = 0
			}
		}
		c.lastNetRx = rx
		c.lastNetTx = tx
	}
	c.lastTime = now

	c.mu.Lock()
	if len(c.samples) < c.capacity {
		c.samples = append(c.samples, s)
	} else {
		c.samples[c.head] = s
	}
	c.head = (c.head + 1) % c.capacity
	if c.head == 0 {
		c.full = true
	}
	c.mu.Unlock()

	if c.hub != nil {
		c.hub.Broadcast(events.Event{
			Type: "host.metrics",
			Data: s,
		})
	}
}

// Series returns a snapshot of the samples in chronological order.
func (c *HostMetricsCollector) Series() HostMetricsSeries {
	c.mu.RLock()
	defer c.mu.RUnlock()
	points := make([]HostMetricsSample, len(c.samples))
	copy(points, c.samples)
	return HostMetricsSeries{
		Kind:   "host",
		Window: int(c.interval.Seconds()) * c.capacity,
		Points: points,
	}
}

// readCPUUsage returns the system-wide CPU usage in [0, 100] using
// the /proc/stat jiffies delta approach. First call returns 0.
func readCPUUsage() float64 {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		return 0
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0
	}
	var total, idle uint64
	for i, v := range fields[1:] {
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0
		}
		total += n
		if i == 3 || i == 4 {
			idle += n
		}
	}
	c := &globalHostCPUSampler
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastTotal == 0 {
		c.lastTotal = total
		c.lastIdle = idle
		return 0
	}
	dTotal := total - c.lastTotal
	dIdle := idle - c.lastIdle
	c.lastTotal = total
	c.lastIdle = idle
	if dTotal == 0 {
		return 0
	}
	pct := (1.0 - float64(dIdle)/float64(dTotal)) * 100.0
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return pct
}

var globalHostCPUSampler struct {
	mu        sync.Mutex
	lastTotal uint64
	lastIdle  uint64
}

// readRAM returns (free, total, used) in bytes. Free is approximate
// (MemAvailable from /proc/meminfo if present, else MemFree).
func readRAM() (free, total, used int64, ok bool) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, 0, 0, false
	}
	defer f.Close()
	var memTotal, memAvail int64
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		// All values in /proc/meminfo are in kB.
		val *= 1024
		switch fields[0] {
		case "MemTotal:":
			memTotal = val
		case "MemAvailable:", "MemFree:":
			if memAvail == 0 {
				memAvail = val
			}
		}
	}
	if memTotal == 0 {
		return 0, 0, 0, false
	}
	return memAvail, memTotal, memTotal - memAvail, true
}

// readDisk returns (total, used) in bytes for the filesystem
// mounted at `dataDir`. Caller passes the path; the collector
// uses a hard-coded default (set when New is called).
func readDisk() (total, used int64, ok bool) {
	dataDir := defaultDiskMount
	var st syscall.Statfs_t
	if err := syscall.Statfs(dataDir, &st); err != nil {
		return 0, 0, false
	}
	total = int64(st.Blocks) * int64(st.Bsize)
	used = int64(st.Blocks-st.Bfree) * int64(st.Bsize)
	return total, used, true
}

var defaultDiskMount = "/opt/webkvm"

// readNetTotals returns (rxBytes, txBytes) summed across all
// non-loopback, non-virtual interfaces in /proc/net/dev.
func readNetTotals() (rx, tx int64, ok bool) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	// Skip the two header lines.
	if !scanner.Scan() || !scanner.Scan() {
		return 0, 0, false
	}
	for scanner.Scan() {
		line := scanner.Text()
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		if iface == "lo" || strings.HasPrefix(iface, "vnet") || strings.HasPrefix(iface, "virbr") || strings.HasPrefix(iface, "br-") || strings.HasPrefix(iface, "docker") {
			continue
		}
		fields := strings.Fields(line[colon+1:])
		if len(fields) < 9 {
			continue
		}
		// /proc/net/dev: iface: rx_bytes ... tx_bytes ...
		rxb, _ := strconv.ParseInt(fields[0], 10, 64)
		txb, _ := strconv.ParseInt(fields[8], 10, 64)
		rx += rxb
		tx += txb
	}
	if rx == 0 && tx == 0 {
		return 0, 0, false
	}
	return rx, tx, true
}
