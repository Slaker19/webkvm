package nodes

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// After a control-plane failover nodes.json (shared DATA_DIR) still carries
// the previous host's CPUs/RAM for the local entry. Loading must never trust
// them: the local capacity comes from the host we are running on.
func TestLoadDropsPersistedLocalCapacity(t *testing.T) {
	dir := t.TempDir()
	stale := []map[string]any{{
		"id": "local", "name": "old-host", "uri": "qemu:///system", "type": NodeTypeLocal,
		"enabled": true, "cpus": 4, "ram_total": 4099915776, "ram_free": 1000,
	}}
	data, _ := json.Marshal(stale)
	if err := os.WriteFile(filepath.Join(dir, "nodes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := r.Get("local")
	if got.CPUs != 0 || got.RAMTotal != 0 || got.RAMFree != 0 {
		t.Fatalf("persisted local capacity trusted: %+v", got)
	}
}

func TestLocalCapacityReaderRefreshesAtStartupProbeAndSummary(t *testing.T) {
	r, err := New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	cpus, total := 2, int64(3_100_000_000)
	reads := 0
	r.SetLocalCapacityReader(func() (int, int64, int64, error) {
		reads++
		return cpus, total / 2, total, nil
	})
	// Startup: wiring the reader fills the local entry immediately.
	got, _ := r.Get("local")
	if got.CPUs != 2 || got.RAMTotal != total || got.RAMFree != total/2 || reads != 1 {
		t.Fatalf("after set: %+v reads=%d", got, reads)
	}
	// The host changes (failover / hotplug): the summary re-reads it.
	cpus, total = 8, 16_000_000_000
	_ = r.ClusterSummary()
	got, _ = r.Get("local")
	if got.CPUs != 8 || got.RAMTotal != total {
		t.Fatalf("after summary: %+v", got)
	}
	// The local probe uses the local reader, never the remote (SSH) prober.
	r.SetRemoteProber(func(string) (int, int64, int64, error) {
		t.Fatal("remote prober used for the local node")
		return 0, 0, 0, nil
	})
	cpus = 3
	if _, err := r.ProbeNode("local"); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get("local")
	if got.CPUs != 3 {
		t.Fatalf("after probe: %+v", got)
	}
}

func TestLocalCapacityReaderErrorKeepsValues(t *testing.T) {
	r, err := New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	fail := false
	r.SetLocalCapacityReader(func() (int, int64, int64, error) {
		if fail {
			return 0, 0, 0, errors.New("no /proc")
		}
		return 2, 1, 2, nil
	})
	fail = true
	r.RefreshLocalCapacity()
	if got, _ := r.Get("local"); got.CPUs != 2 {
		t.Fatalf("a failed read must keep the last good value: %+v", got)
	}
}

func TestParseMeminfo(t *testing.T) {
	const in = "MemTotal:        3014656 kB\nMemFree:          100000 kB\nMemAvailable:    2000000 kB\n"
	free, total, err := parseMeminfo(in)
	if err != nil || total != 3014656*1024 || free != 2000000*1024 {
		t.Fatalf("free=%d total=%d err=%v", free, total, err)
	}
	// Old kernels without MemAvailable fall back to MemFree.
	free, _, err = parseMeminfo("MemTotal: 10 kB\nMemFree: 4 kB\n")
	if err != nil || free != 4*1024 {
		t.Fatalf("fallback free=%d err=%v", free, err)
	}
	if _, _, err := parseMeminfo("garbage"); err == nil {
		t.Fatal("want error without MemTotal")
	}
}
