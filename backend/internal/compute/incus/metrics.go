package incus

import (
	"context"
	"sync"
	"time"

	"webkvm/internal/events"
	"webkvm/internal/models"

	incus "github.com/lxc/incus/v6/client"
	"github.com/lxc/incus/v6/shared/api"
)

// ringBuffer is a fixed-size circular buffer of MetricsSample. When full,
// new samples overwrite the oldest. It is *not* safe for concurrent use;
// callers must hold the parent instance's lock. Mirrors the libvirt
// collector's ring (kept local so compute/lxd stays self-contained).
type ringBuffer struct {
	data []models.MetricsSample
	head int  // next write index
	full bool // true once we've wrapped around
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{data: make([]models.MetricsSample, 0, capacity)}
}

func (r *ringBuffer) push(s models.MetricsSample) {
	if len(r.data) < cap(r.data) {
		r.data = append(r.data, s)
		return
	}
	r.full = true
	r.data[r.head] = s
	r.head = (r.head + 1) % cap(r.data)
}

func (r *ringBuffer) snapshot() []models.MetricsSample {
	if !r.full {
		out := make([]models.MetricsSample, len(r.data))
		copy(out, r.data)
		return out
	}
	out := make([]models.MetricsSample, cap(r.data))
	for i := 0; i < cap(r.data); i++ {
		out[i] = r.data[(r.head+i)%cap(r.data)]
	}
	return out
}

// metricsState holds the in-memory ring buffers and last-counter cache
// for one container. last counters let us compute per-second deltas for
// CPU usage and the cumulative network byte counters.
type metricsState struct {
	name string

	mu    sync.Mutex
	cpu   *ringBuffer
	ram   *ringBuffer
	diskR *ringBuffer
	diskW *ringBuffer
	netRx *ringBuffer
	netTx *ringBuffer

	lastCPU     int64 // cumulative ns of CPU consumed
	lastNetRx   int64
	lastNetTx   int64
	lastSampleT time.Time
}

func newMetricsState(name string, capacity int) *metricsState {
	return &metricsState{
		name:  name,
		cpu:   newRingBuffer(capacity),
		ram:   newRingBuffer(capacity),
		diskR: newRingBuffer(capacity),
		diskW: newRingBuffer(capacity),
		netRx: newRingBuffer(capacity),
		netTx: newRingBuffer(capacity),
	}
}

func (s *metricsState) snapshot() models.VMMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	return models.VMMetrics{
		VMID:      s.name,
		SampledAt: now,
		CPU:       models.MetricsSeries{Kind: "cpu", Unit: "%", Window: 0, Points: s.cpu.snapshot()},
		RAM:       models.MetricsSeries{Kind: "ram", Unit: "%", Window: 0, Points: s.ram.snapshot()},
		DiskRead:  models.MetricsSeries{Kind: "disk_r", Unit: "B/s", Window: 0, Points: s.diskR.snapshot()},
		DiskWrite: models.MetricsSeries{Kind: "disk_w", Unit: "B/s", Window: 0, Points: s.diskW.snapshot()},
		NetRx:     models.MetricsSeries{Kind: "net_rx", Unit: "B/s", Window: 0, Points: s.netRx.snapshot()},
		NetTx:     models.MetricsSeries{Kind: "net_tx", Unit: "B/s", Window: 0, Points: s.netTx.snapshot()},
	}
}

// instanceStateFetcher abstracts the Incus client calls the collector
// makes so unit tests can inject a fake without a live daemon.
type instanceStateFetcher interface {
	ListInstances() ([]api.Instance, error)
	State(name string) (*api.InstanceState, error)
}

// clientAdapter adapts the Incus client to instanceStateFetcher to instanceStateFetcher.
type clientAdapter struct{ client incus.InstanceServer }

func (a clientAdapter) ListInstances() ([]api.Instance, error) {
	// Incus v6 client: GetInstances takes the instance type filter
	// (api.InstanceTypeAny = containers + virtual machines).
	return a.client.GetInstances(api.InstanceTypeAny)
}

func (a clientAdapter) State(name string) (*api.InstanceState, error) {
	s, _, err := a.client.GetInstanceState(name)
	return s, err
}

// MetricsCollector samples CPU/RAM/Net for running containers
// (v1.4 Fase 4.1) so the UI shows the same sparklines/charts as KVM. It
// broadcasts on the event hub and feeds the shared history/alert sink.
type MetricsCollector struct {
	fetch    instanceStateFetcher
	hub      *events.Hub
	interval time.Duration
	capacity int

	mu   sync.Mutex
	vms  map[string]*metricsState
	sink SampleSink
}

// SampleSink is invoked with every sampled metric set (same contract as
// the libvirt collector, so history + alerts share the sink).
type SampleSink func(vmID string, at time.Time, m models.VMMetrics)

// SetSink attaches the history/alert sink.
func (m *MetricsCollector) SetSink(s SampleSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink = s
}

// NewMetricsCollector builds a collector over the given instance fetcher.
func NewMetricsCollector(fetch instanceStateFetcher, hub *events.Hub) *MetricsCollector {
	return &MetricsCollector{
		fetch:    fetch,
		hub:      hub,
		interval: 5 * time.Second,
		capacity: 720, // 1h @ 5s
		vms:      map[string]*metricsState{},
	}
}

// Get returns the current metrics snapshot for one container.
func (m *MetricsCollector) Get(name string) (models.VMMetrics, error) {
	m.mu.Lock()
	st, ok := m.vms[name]
	m.mu.Unlock()
	if !ok {
		st = newMetricsState(name, m.capacity)
		m.mu.Lock()
		if _, exists := m.vms[name]; !exists {
			m.vms[name] = st
		}
		m.mu.Unlock()
	}
	return st.snapshot(), nil
}

// Run starts the polling loop. Returns when ctx is cancelled.
func (m *MetricsCollector) Run(ctx context.Context) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	m.sampleOnce()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.sampleOnce()
		}
	}
}

func (m *MetricsCollector) sampleOnce() {
	instances, err := m.fetch.ListInstances()
	if err != nil {
		return
	}
	now := time.Now()
	for i := range instances {
		inst := &instances[i]
		if inst.Status != "Running" {
			continue
		}
		st := m.upsertState(inst.Name)
		state, err := m.fetch.State(inst.Name)
		if err != nil {
			continue
		}
		m.collectOne(inst.Name, st, state, now)
	}
}

func (m *MetricsCollector) upsertState(name string) *metricsState {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.vms[name]
	if !ok {
		st = newMetricsState(name, m.capacity)
		m.vms[name] = st
	}
	return st
}

func (m *MetricsCollector) collectOne(name string, st *metricsState, state *api.InstanceState, now time.Time) {
	// A single elapsed window, captured before any update, feeds every
	// delta so CPU and Net rates are computed against the SAME previous
	// sample (updating lastSampleT early would zero the elapsed window).
	st.mu.Lock()
	elapsed := 0.0
	if !st.lastSampleT.IsZero() {
		elapsed = now.Sub(st.lastSampleT).Seconds()
	}

	// --- CPU: cumulative ns -> per-second delta % (mirrors libvirt). ---
	if state.CPU.Usage > 0 {
		if elapsed > 0 {
			delta := float64(state.CPU.Usage - st.lastCPU)
			pct := (delta / 1e9) / elapsed * 100
			if pct < 0 || pct > 100*128 {
				pct = 0
			}
			st.cpu.push(models.MetricsSample{T: now.Unix(), V: pct})
		}
		st.lastCPU = state.CPU.Usage
	}

	// --- RAM: current usage / limit (Total is the memory limit). ---
	if state.Memory.Usage > 0 && state.Memory.Total > 0 {
		pct := float64(state.Memory.Usage) / float64(state.Memory.Total) * 100
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		st.ram.push(models.MetricsSample{T: now.Unix(), V: pct})
	}

	// --- Net: cumulative byte counters -> per-second rates. ---
	var totalRx, totalTx int64
	for _, nic := range state.Network {
		totalRx += int64(nic.Counters.BytesReceived)
		totalTx += int64(nic.Counters.BytesSent)
	}
	if elapsed > 0 {
		rx := float64(totalRx-st.lastNetRx) / elapsed
		tx := float64(totalTx-st.lastNetTx) / elapsed
		if rx < 0 {
			rx = 0
		}
		if tx < 0 {
			tx = 0
		}
		st.netRx.push(models.MetricsSample{T: now.Unix(), V: rx})
		st.netTx.push(models.MetricsSample{T: now.Unix(), V: tx})
		// LXD state exposes no cumulative disk I/O counters (only
		// current usage), so disk rates stay 0.
		st.diskR.push(models.MetricsSample{T: now.Unix(), V: 0})
		st.diskW.push(models.MetricsSample{T: now.Unix(), V: 0})
	}
	st.lastNetRx = totalRx
	st.lastNetTx = totalTx
	st.lastSampleT = now
	st.mu.Unlock()

	if m.hub != nil {
		m.hub.Broadcast(events.Event{
			Type:      "vm.metrics",
			VmID:      name,
			Timestamp: now.Unix(),
			Data:      st.snapshot(),
		})
	}
	m.mu.Lock()
	sink := m.sink
	m.mu.Unlock()
	if sink != nil {
		sink(name, now, st.snapshot())
	}
}