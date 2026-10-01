package libvirt

import (
	"context"
	"regexp"
	"sync"
	"time"

	"webkvm/internal/events"
	"webkvm/internal/models"

	"libvirt.org/go/libvirt"
)

// ringBuffer is a fixed-size circular buffer of MetricsSample. When full,
// new samples overwrite the oldest. It is *not* safe for concurrent use;
// callers must hold the parent VM's lock.
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
	if !r.full {
		r.full = true
	}
	r.data[r.head] = s
	r.head = (r.head + 1) % cap(r.data)
}

func (r *ringBuffer) snapshot() []models.MetricsSample {
	if !r.full {
		// Return a copy so the caller can use it without racing.
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

// vmMetricsState holds the in-memory ring buffers and last-counter cache
// for one VM. lastCounters lets us compute per-second deltas for the
// cumulative disk/net counters.
type vmMetricsState struct {
	uuid string

	mu    sync.Mutex
	cpu   *ringBuffer
	ram   *ringBuffer
	diskR *ringBuffer
	diskW *ringBuffer
	netRx *ringBuffer
	netTx *ringBuffer

	// Last cumulative counters, used for delta-based metrics.
	lastDiskRdBytes int64
	lastDiskWrBytes int64
	lastNetRxBytes  int64
	lastNetTxBytes  int64
	lastCPUAbs      uint64 // nanoseconds of CPU consumed
	lastSampleTime  time.Time
	// lastIOSampleTime is the disk/net equivalent of lastSampleTime and
	// must stay separate from it. The CPU block updates lastSampleTime
	// to `now` before the I/O block runs, so the I/O rates used to be
	// divided by the handful of microseconds spent between the two
	// blocks instead of by the sampling interval — inflating every
	// throughput figure by 3-4 orders of magnitude.
	lastIOSampleTime time.Time
}

func newVMMetricsState(uuid string, capacity int) *vmMetricsState {
	return &vmMetricsState{
		uuid:  uuid,
		cpu:   newRingBuffer(capacity),
		ram:   newRingBuffer(capacity),
		diskR: newRingBuffer(capacity),
		diskW: newRingBuffer(capacity),
		netRx: newRingBuffer(capacity),
		netTx: newRingBuffer(capacity),
	}
}

// snapshot returns a fresh VMMetrics with the current ring contents.
// pushIORates turns the cumulative disk/net byte counters into
// per-second rates and appends them to the ring buffers.
//
// The elapsed time MUST come from lastIOSampleTime and not from
// lastSampleTime: the latter is the CPU block's clock, which
// collectOne already advanced to `now` before reaching here. Measuring
// against it meant dividing by the handful of microseconds spent
// between the two blocks instead of by the sampling interval, which
// inflated every throughput figure by three to four orders of
// magnitude — the charts, the SSE stream, the history store and the
// throughput alert rules all saw the same fiction.
//
// The first sample of a VM is skipped entirely: with no previous
// counters the delta would be the guest's lifetime byte count, which
// rendered as one enormous spike that rescaled the Y axis and flattened
// every real data point after it.
func (s *vmMetricsState) pushIORates(now time.Time, diskRd, diskWr, netRx, netTx int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	elapsed := 0.0
	if !s.lastIOSampleTime.IsZero() {
		elapsed = now.Sub(s.lastIOSampleTime).Seconds()
	}
	if elapsed > 0 {
		rate := func(cur, prev int64) float64 {
			// Counters reset when the guest reboots or a device is
			// hot-unplugged; a negative delta is not a rate.
			if v := float64(cur-prev) / elapsed; v > 0 {
				return v
			}
			return 0
		}
		if s.lastDiskRdBytes > 0 || diskRd > 0 {
			s.diskR.push(models.MetricsSample{T: now.Unix(), V: rate(diskRd, s.lastDiskRdBytes)})
			s.diskW.push(models.MetricsSample{T: now.Unix(), V: rate(diskWr, s.lastDiskWrBytes)})
		}
		if s.lastNetRxBytes > 0 || netRx > 0 {
			s.netRx.push(models.MetricsSample{T: now.Unix(), V: rate(netRx, s.lastNetRxBytes)})
			s.netTx.push(models.MetricsSample{T: now.Unix(), V: rate(netTx, s.lastNetTxBytes)})
		}
	}
	s.lastDiskRdBytes = diskRd
	s.lastDiskWrBytes = diskWr
	s.lastNetRxBytes = netRx
	s.lastNetTxBytes = netTx
	s.lastIOSampleTime = now
}

func (s *vmMetricsState) snapshot() models.VMMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().Unix()
	return models.VMMetrics{
		VMID:      s.uuid,
		SampledAt: now,
		CPU:       models.MetricsSeries{Kind: "cpu", Unit: "%", Window: 0, Points: s.cpu.snapshot()},
		RAM:       models.MetricsSeries{Kind: "ram", Unit: "%", Window: 0, Points: s.ram.snapshot()},
		DiskRead:  models.MetricsSeries{Kind: "disk_r", Unit: "B/s", Window: 0, Points: s.diskR.snapshot()},
		DiskWrite: models.MetricsSeries{Kind: "disk_w", Unit: "B/s", Window: 0, Points: s.diskW.snapshot()},
		NetRx:     models.MetricsSeries{Kind: "net_rx", Unit: "B/s", Window: 0, Points: s.netRx.snapshot()},
		NetTx:     models.MetricsSeries{Kind: "net_tx", Unit: "B/s", Window: 0, Points: s.netTx.snapshot()},
	}
}

// MetricsCollector holds per-VM ring buffers and runs a polling loop that
// samples CPU/RAM/Disk/Net stats and broadcasts them on the event hub.
type MetricsCollector struct {
	lv       *Connector
	hub      *events.Hub
	interval time.Duration
	capacity int

	mu  sync.Mutex
	vms map[string]*vmMetricsState
	// sink receives every sampled VM metrics set (V13-C-03/04) so the
	// history store and alert engine can consume them without coupling.
	sink SampleSink
}

// SampleSink is invoked with every sampled metric set (after the SSE
// broadcast). at is the sample time; m is the VM's full series.
type SampleSink func(vmID string, at time.Time, m models.VMMetrics)

// SetSink attaches the history/alert sink (V13-C-03/04).
func (m *MetricsCollector) SetSink(s SampleSink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sink = s
}

func NewMetricsCollector(lv *Connector, hub *events.Hub) *MetricsCollector {
	return &MetricsCollector{
		lv:       lv,
		hub:      hub,
		interval: 5 * time.Second,
		capacity: 720, // 1h @ 5s
		vms:      map[string]*vmMetricsState{},
	}
}

// Get returns the current metrics snapshot for a single VM.
func (m *MetricsCollector) Get(uuid string) (models.VMMetrics, error) {
	m.mu.Lock()
	st, ok := m.vms[uuid]
	m.mu.Unlock()
	if !ok {
		// Lazily create so the GET endpoint works even before the loop
		// has ever sampled a running VM.
		st = newVMMetricsState(uuid, m.capacity)
		m.mu.Lock()
		if _, exists := m.vms[uuid]; !exists {
			m.vms[uuid] = st
		}
		m.mu.Unlock()
	}
	return st.snapshot(), nil
}

// Run starts the polling loop. Returns when ctx is cancelled.
func (m *MetricsCollector) Run(ctx context.Context) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	// Take an initial sample immediately so the first UI request gets data.
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
	if err := m.lv.EnsureConnected(); err != nil {
		return
	}
	doms, err := m.lv.conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_RUNNING)
	if err != nil {
		return
	}
	now := time.Now()
	for i := range doms {
		uuid, _ := doms[i].GetUUIDString()
		state := m.upsertState(uuid)
		m.collectOne(&doms[i], uuid, state, now)
		doms[i].Free()
	}
}

// upsertState gets-or-creates the per-VM state, cleaning up VMs that no
// longer exist after the call.
func (m *MetricsCollector) upsertState(uuid string) *vmMetricsState {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.vms[uuid]
	if !ok {
		st = newVMMetricsState(uuid, m.capacity)
		m.vms[uuid] = st
	}
	return st
}

func (m *MetricsCollector) collectOne(dom *libvirt.Domain, uuid string, st *vmMetricsState, now time.Time) {
	// --- CPU ---
	// nCpus=0 means "all vCPUs" in libvirt. We sum them up to get a single
	// "total CPU time" counter, then compute a delta-vs-elapsed percentage.
	if cpuStats, err := dom.GetCPUStats(-1, 0, 0); err == nil && len(cpuStats) > 0 {
		var abs uint64
		for _, s := range cpuStats {
			abs += s.CpuTime
		}
		st.mu.Lock()
		if !st.lastSampleTime.IsZero() {
			elapsed := now.Sub(st.lastSampleTime).Seconds()
			if elapsed > 0 {
				delta := float64(int64(abs) - int64(st.lastCPUAbs))
				// nanoseconds -> fraction of 1 CPU; divide by vCPUs for [0, 100]% range
				vcpus := 1
				if info, ierr := dom.GetInfo(); ierr == nil && info.NrVirtCpu > 0 {
					vcpus = int(info.NrVirtCpu)
				}
				pct := ((delta / 1e9) / elapsed * 100) / float64(vcpus)
				if pct < 0 {
					pct = 0
				}
				if pct > 100 {
					pct = 100
				}
				st.cpu.push(models.MetricsSample{T: now.Unix(), V: pct})
			}
		}
		st.lastCPUAbs = abs
		st.lastSampleTime = now
		st.mu.Unlock()
	}

	// --- RAM (real guest usage) ---
	// info.Memory / info.MaxMem is the configured-vs-allocated ratio,
	// which sits at 100% once the guest has been told it can have its
	// full max memory — meaningless as a usage chart. Use MemoryStats
	// to read RSS (resident set size) which the KVM hypervisor tracks
	// directly without needing the QEMU guest agent.
	//
	// If the guest agent IS available, prefer (balloon - available)
	// which is the most accurate measure of guest memory pressure.
	if info, err := dom.GetInfo(); err == nil && info.Memory > 0 {
		var pct float64
		if stats, mErr := dom.MemoryStats(8, 0); mErr == nil {
			var total, rss, unused, available uint64
			for _, s := range stats {
				switch libvirt.DomainMemoryStatTags(s.Tag) {
				case libvirt.DOMAIN_MEMORY_STAT_ACTUAL_BALLOON:
					total = s.Val
				case libvirt.DOMAIN_MEMORY_STAT_RSS:
					rss = s.Val
				case libvirt.DOMAIN_MEMORY_STAT_UNUSED:
					unused = s.Val
				case libvirt.DOMAIN_MEMORY_STAT_AVAILABLE:
					available = s.Val
				}
			}
			pct = memoryUsedPct(total, available, unused, rss)
		}
		// Last-resort fallback: configured-vs-allocated ratio.
		if pct == 0 && info.MaxMem > 0 {
			pct = float64(info.Memory) / float64(info.MaxMem) * 100
		}
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		st.mu.Lock()
		st.ram.push(models.MetricsSample{T: now.Unix(), V: pct})
		st.mu.Unlock()
	}

	// --- Disk + Net (per-target/per-iface stats) ---
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return
	}
	disks := extractDiskTargets(xmlDesc)
	ifaces := extractIfaceMACs(xmlDesc)

	var totalDiskRd, totalDiskWr int64
	for _, dev := range disks {
		if bs, err := dom.BlockStats(dev); err == nil {
			if bs.RdBytes > 0 {
				totalDiskRd += bs.RdBytes
			}
			if bs.WrBytes > 0 {
				totalDiskWr += bs.WrBytes
			}
		}
	}
	var totalNetRx, totalNetTx int64
	for _, mac := range ifaces {
		if is, err := dom.InterfaceStats(mac); err == nil {
			if is.RxBytes > 0 {
				totalNetRx += is.RxBytes
			}
			if is.TxBytes > 0 {
				totalNetTx += is.TxBytes
			}
		}
	}

	st.pushIORates(now, totalDiskRd, totalDiskWr, totalNetRx, totalNetTx)

	// Broadcast to SSE subscribers.
	if m.hub != nil {
		m.hub.Broadcast(events.Event{
			Type:      "vm.metrics",
			VmID:      uuid,
			Timestamp: now.Unix(),
			Data:      st.snapshot(),
		})
	}
	// Feed the history store / alert engine (V13-C-03/04).
	m.mu.Lock()
	sink := m.sink
	m.mu.Unlock()
	if sink != nil {
		sink(uuid, now, st.snapshot())
	}
}

// extractDiskTargets returns the disk target dev names (vda, sda, ...) from
// the domain XML. Used to feed BlockStats().
func extractDiskTargets(xml string) []string {
	re := regexp.MustCompile(`<disk[^>]*>[\s\S]*?<target\s+dev='([^']+)'[\s\S]*?</disk>`)
	matches := re.FindAllStringSubmatch(xml, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// extractIfaceMACs returns the MAC addresses of all <interface>s in the
// domain XML. InterfaceStats uses the MAC as the path argument.
func extractIfaceMACs(xml string) []string {
	re := regexp.MustCompile(`<mac\s+address='([^']+)'`)
	matches := re.FindAllStringSubmatch(xml, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// memoryUsedPct turns libvirt's memory stats into a 0-100 percentage.
//
// Extracted as a pure function because the arithmetic here had a bug that
// no amount of staring at the call site would surface: the previous code
// did `used := total - available` on two uint64 values and then checked
// `if used < 0`, which a uint64 can never be. When available exceeded
// total the subtraction wrapped to ~1.8e19 instead of going negative, the
// dead guard never fired, the (total - unused) fallback never ran, and the
// clamp turned the overflow into a permanent 100%.
//
// available > total is a normal transient, not corruption: ACTUAL_BALLOON
// is the memory currently granted to the balloon, while the guest agent
// reports AVAILABLE including reclaimable cache, so a deflating balloon
// leaves the two figures inconsistent for several seconds.
//
// Returns 0 when no stat is usable, so the caller can apply its own
// last-resort fallback rather than publish a made-up number.
func memoryUsedPct(total, available, unused, rss uint64) float64 {
	if total == 0 {
		return 0
	}
	// Compared before subtracting: with unsigned values the comparison is
	// the guard. Order matches the original intent — available first
	// (most accurate, excludes reclaimable cache), then unused.
	for _, free := range []uint64{available, unused} {
		if free > 0 && free <= total {
			return float64(total-free) / float64(total) * 100
		}
	}
	if rss > 0 {
		pct := float64(rss) / float64(total) * 100
		if pct > 100 {
			return 100
		}
		return pct
	}
	return 0
}
