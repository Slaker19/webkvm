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

	mu sync.Mutex
	cpu    *ringBuffer
	ram    *ringBuffer
	diskR  *ringBuffer
	diskW  *ringBuffer
	netRx  *ringBuffer
	netTx  *ringBuffer

	// Last cumulative counters, used for delta-based metrics.
	lastDiskRdBytes  int64
	lastDiskWrBytes  int64
	lastNetRxBytes   int64
	lastNetTxBytes   int64
	lastCPUAbs       uint64 // nanoseconds of CPU consumed
	lastSampleTime   time.Time
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

	mu   sync.Mutex
	vms  map[string]*vmMetricsState
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
				// nanoseconds -> fraction of 1 CPU; multiply by 100 for %.
				// Assume 1 vCPU for normalization when vcpu count is unknown;
				// we'll refine when vcpus is exposed here.
				pct := (delta / 1e9) / elapsed * 100
				if pct < 0 {
					pct = 0
				}
				if pct > 100*128 {
					// libvirt timeouts / counter resets can produce huge jumps;
					// clamp to a sane upper bound (128 vCPUs at 100%).
					pct = 0
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
			switch {
			case total > 0 && (unused > 0 || available > 0):
				// Guest agent reporting: (balloon - available) is the
				// most accurate "used" number, since AVAILABLE
				// includes reclaimable cache.
				used := total - available
				if used < 0 {
					used = total - unused
				}
				pct = float64(used) / float64(total) * 100
			case rss > 0 && total > 0:
				// No agent: KVM-tracked RSS is the next-best signal.
				pct = float64(rss) / float64(total) * 100
			}
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

	st.mu.Lock()
	elapsed := time.Since(st.lastSampleTime).Seconds()
	if elapsed > 0 {
		if st.lastDiskRdBytes > 0 || totalDiskRd > 0 {
			dr := float64(int64(totalDiskRd)-int64(st.lastDiskRdBytes)) / elapsed
			dw := float64(int64(totalDiskWr)-int64(st.lastDiskWrBytes)) / elapsed
			if dr < 0 {
				dr = 0
			}
			if dw < 0 {
				dw = 0
			}
			st.diskR.push(models.MetricsSample{T: now.Unix(), V: dr})
			st.diskW.push(models.MetricsSample{T: now.Unix(), V: dw})
		}
		if st.lastNetRxBytes > 0 || totalNetRx > 0 {
			rx := float64(int64(totalNetRx)-int64(st.lastNetRxBytes)) / elapsed
			tx := float64(int64(totalNetTx)-int64(st.lastNetTxBytes)) / elapsed
			if rx < 0 {
				rx = 0
			}
			if tx < 0 {
				tx = 0
			}
			st.netRx.push(models.MetricsSample{T: now.Unix(), V: rx})
			st.netTx.push(models.MetricsSample{T: now.Unix(), V: tx})
		}
	}
	st.lastDiskRdBytes = totalDiskRd
	st.lastDiskWrBytes = totalDiskWr
	st.lastNetRxBytes = totalNetRx
	st.lastNetTxBytes = totalNetTx
	st.mu.Unlock()

	// Broadcast to SSE subscribers.
	if m.hub != nil {
		m.hub.Broadcast(events.Event{
			Type:      "vm.metrics",
			VmID:      uuid,
			Timestamp: now.Unix(),
			Data:      st.snapshot(),
		})
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
