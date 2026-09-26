package libvirt

import (
	"testing"
	"time"

	libvirt "libvirt.org/go/libvirt"
)

// An idle 4-vCPU guest that has accumulated 60 s of CPU since boot.
var libvirtDomainInfoIdle = libvirt.DomainInfo{NrVirtCpu: 4, CpuTime: 60 * 1e9}

// TestPushIORatesUsesSamplingInterval pins the disk/net rate maths.
//
// The elapsed time used to be `time.Since(st.lastSampleTime)`, but the
// CPU block of collectOne sets lastSampleTime = now before the I/O
// block runs. The divisor was therefore the microseconds spent between
// the two blocks, not the sampling interval, and every throughput
// number was inflated by three to four orders of magnitude: a guest
// writing 10 MB/s was charted at tens of GB/s, and any alert rule on
// disk or network throughput fired permanently.
func TestPushIORatesUsesSamplingInterval(t *testing.T) {
	st := newVMMetricsState("vm", 64)
	t0 := time.Unix(1_700_000_000, 0)

	// First sample only primes the counters.
	st.pushIORates(t0, 1_000_000, 2_000_000, 3_000_000, 4_000_000)

	// 5 s later, 50 MB read => exactly 10 MB/s.
	st.pushIORates(t0.Add(5*time.Second), 51_000_000, 2_000_000, 3_000_000, 4_000_000)

	pts := st.diskR.snapshot()
	if len(pts) != 1 {
		t.Fatalf("expected exactly 1 disk-read point, got %d", len(pts))
	}
	const want = 10_000_000.0
	if got := pts[0].V; got < want*0.99 || got > want*1.01 {
		t.Errorf("disk read rate = %.0f B/s, want ~%.0f B/s (off by %.1fx)",
			got, want, got/want)
	}
}

// TestPushIORatesSkipsFirstSample: with no previous counters the delta
// is the guest's cumulative lifetime byte count. Publishing it as a
// rate produced a single enormous spike as the first point of every
// chart, which rescaled the Y axis and flattened all the real data.
func TestPushIORatesSkipsFirstSample(t *testing.T) {
	st := newVMMetricsState("vm", 64)
	st.pushIORates(time.Unix(1_700_000_000, 0), 900_000_000_000, 0, 0, 0)

	if pts := st.diskR.snapshot(); len(pts) != 0 {
		t.Fatalf("first sample must publish no rate, got %v", pts)
	}
	if st.lastDiskRdBytes != 900_000_000_000 {
		t.Errorf("first sample must still prime the counter, got %d", st.lastDiskRdBytes)
	}
}

// TestPushIORatesIgnoresCounterReset: a guest reboot or a hot-unplugged
// device resets the cumulative counters, making the delta negative. A
// negative throughput is meaningless, so it must read as zero rather
// than propagate into the charts and the alert engine.
func TestPushIORatesIgnoresCounterReset(t *testing.T) {
	st := newVMMetricsState("vm", 64)
	t0 := time.Unix(1_700_000_000, 0)
	st.pushIORates(t0, 5_000_000_000, 0, 0, 0)
	st.pushIORates(t0.Add(5*time.Second), 1_000, 0, 0, 0)

	pts := st.diskR.snapshot()
	if len(pts) != 1 {
		t.Fatalf("expected 1 point, got %d", len(pts))
	}
	if pts[0].V != 0 {
		t.Errorf("counter reset produced rate %.0f, want 0", pts[0].V)
	}
}

// TestCalculateCPUUsageReportsNothing: info.CpuTime is the domain's
// CUMULATIVE CPU nanoseconds since boot. The old implementation divided
// it by a hardcoded 1 second and clamped to 100, so every VM that had
// ever burned NrVirtCpu seconds of CPU — i.e. any VM up for more than a
// few seconds — reported a constant "100.0% used" on its detail page.
//
// models.VM.CPUUsage is `omitempty` and the UI guards on `!= null`, so
// returning 0 omits the field and the page falls back to the real
// delta-based series from MetricsCollector.
func TestCalculateCPUUsageReportsNothing(t *testing.T) {
	// An idle 4-vCPU guest with 60 s of lifetime CPU: the old code
	// answered 100.00%.
	if got := calculateCPUUsage(nil, &libvirtDomainInfoIdle); got != 0 {
		t.Errorf("idle VM reported %.2f%% CPU, want 0 (field omitted)", got)
	}
}
