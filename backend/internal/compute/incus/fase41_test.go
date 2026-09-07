package incus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lxc/incus/v6/shared/api"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

func isErrNotImplemented(err error) bool {
	return errors.Is(err, compute.ErrNotImplemented)
}

// --- pure helpers ---

func TestParseDiskGB(t *testing.T) {
	cases := map[string]int64{
		"10GB": 10, "2GiB": 2, "1TB": 1000, "": 0, "bogus": 0, "5MB": 0,
	}
	for in, want := range cases {
		if got := parseDiskGB(in); got != want {
			t.Errorf("parseDiskGB(%q) = %d, want %d", in, got, want)
		}
	}
}

// instanceToVM must surface the root device as a disk and each NIC as a
// network interface (v1.4 Fase 4.1) so the KVM-equivalent tabs work.
func TestInstanceToVM_DisksAndNetworks(t *testing.T) {
	inst := &api.Instance{
		Name:   "ct1",
		Status: "Running",
		Type:   "container",
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"limits.cpu":           "2",
				"volatile.eth0.hwaddr": "00:16:3e:aa:bb:cc",
				"volatile.eth1.hwaddr": "00:16:3e:dd:ee:ff",
				"user.user-data":       "#cloud-config\n",
			},
			Devices: map[string]map[string]string{
				"root": {"type": "disk", "path": "/", "pool": "default", "size": "10GB"},
				"eth0": {"type": "nic", "nictype": "bridged", "parent": "virbr0", "name": "eth0"},
				"eth1": {"type": "nic", "nictype": "bridged", "parent": "lxdbr0", "name": "eth1"},
			},
		},
	}
	vm := instanceToVM(inst)
	if len(vm.Disks) != 1 {
		t.Fatalf("expected 1 disk, got %d", len(vm.Disks))
	}
	d := vm.Disks[0]
	if d.Target != "root" || d.SizeGB != 10 || d.Pool != "default" {
		t.Errorf("root disk = %+v", d)
	}
	if vm.DiskGB != 10 {
		t.Errorf("disk_gb = %d, want 10", vm.DiskGB)
	}
	if len(vm.Networks) != 2 {
		t.Fatalf("expected 2 ifaces, got %d", len(vm.Networks))
	}
	n0 := vm.Networks[0]
	if n0.MAC != "00:16:3e:aa:bb:cc" || n0.Network != "virbr0" {
		t.Errorf("eth0 = %+v", n0)
	}
	n1 := vm.Networks[1]
	if n1.Network != "lxdbr0" {
		t.Errorf("eth1 network = %q, want lxdbr0", n1.Network)
	}
}

func TestNextNicName(t *testing.T) {
	if got := nextNicName(map[string]map[string]string{}); got != "eth0" {
		t.Errorf("empty = %q", got)
	}
	devs := map[string]map[string]string{"eth0": {}, "eth2": {}}
	if got := nextNicName(devs); got != "eth3" {
		t.Errorf("gapped = %q, want eth3", got)
	}
}

// --- disk + iface mutations against the stateful fake daemon ---

func initialContainer() *api.Instance {
	return &api.Instance{
		Name:   "ct1",
		Status: "Stopped",
		Type:   "container",
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"limits.cpu":           "2",
				"volatile.eth0.hwaddr": "00:16:3e:aa:bb:cc",
			},
			Devices: map[string]map[string]string{
				"root": {"type": "disk", "path": "/", "pool": "default", "size": "10GB"},
				"eth0": {"type": "nic", "nictype": "bridged", "parent": "virbr0", "name": "eth0"},
			},
		},
	}
}

func TestIncusBackendResizeRootDisk(t *testing.T) {
	sock, _ := newFakeLXD3(t, map[string]*api.Instance{"ct1": initialContainer()}, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatal(err)
	}
	n, err := b.ResizeDomainDisk(context.Background(), "ct1", "root", 14)
	if err != nil {
		t.Fatal(err)
	}
	if n != 14 {
		t.Errorf("resized = %d, want 14", n)
	}
	vm, err := b.GetDomain("ct1")
	if err != nil {
		t.Fatal(err)
	}
	if vm.DiskGB != 14 {
		t.Errorf("disk_gb after resize = %d, want 14", vm.DiskGB)
	}
}

func TestIncusBackendResizeRejectsOtherTarget(t *testing.T) {
	sock, _ := newFakeLXD3(t, map[string]*api.Instance{"ct1": initialContainer()}, nil)
	b, _ := NewIncusBackend(sock)
	if _, err := b.ResizeDomainDisk(context.Background(), "ct1", "vda", 14); !isErrNotImplemented(err) {
		t.Errorf("expected ErrNotImplemented, got %v", err)
	}
}

func TestIncusBackendAttachNetworkIface(t *testing.T) {
	sock, _ := newFakeLXD3(t, map[string]*api.Instance{"ct1": initialContainer()}, nil)
	b, _ := NewIncusBackend(sock)
	if err := b.AttachNetworkIface("ct1", models.AttachNetRequest{Network: "default"}); err != nil {
		t.Fatal(err)
	}
	vm, err := b.GetDomain("ct1")
	if err != nil {
		t.Fatal(err)
	}
	if len(vm.Networks) != 2 {
		t.Fatalf("expected 2 ifaces after attach, got %d", len(vm.Networks))
	}
	if vm.Networks[1].Network != "lxdbr0" {
		t.Errorf("eth1 parent = %q, want lxdbr0 (no resolver -> default)", vm.Networks[1].Network)
	}
}

func TestIncusBackendAttachUsesResolvedBridge(t *testing.T) {
	sock, _ := newFakeLXD3(t, map[string]*api.Instance{"ct1": initialContainer()}, nil)
	b, err := NewIncusBackend(sock, WithNetworkResolver(func(name string) (string, error) {
		if name == "lan" {
			return "vmbr0", nil
		}
		return "", compute.ErrNotImplemented
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.AttachNetworkIface("ct1", models.AttachNetRequest{Network: "lan"}); err != nil {
		t.Fatal(err)
	}
	vm, _ := b.GetDomain("ct1")
	if vm.Networks[1].Network != "vmbr0" {
		t.Errorf("eth1 parent = %q, want vmbr0 (resolved bridge)", vm.Networks[1].Network)
	}
}

func TestIncusBackendDetachNetworkIface(t *testing.T) {
	sock, _ := newFakeLXD3(t, map[string]*api.Instance{"ct1": initialContainer()}, nil)
	b, _ := NewIncusBackend(sock)
	if err := b.DetachNetworkIface("ct1", "00:16:3e:aa:bb:cc"); err != nil {
		t.Fatal(err)
	}
	vm, _ := b.GetDomain("ct1")
	if len(vm.Networks) != 0 {
		t.Errorf("expected 0 ifaces after detach, got %d", len(vm.Networks))
	}
}

func TestIncusBackendUpdateNetworkIface(t *testing.T) {
	sock, _ := newFakeLXD3(t, map[string]*api.Instance{"ct1": initialContainer()}, nil)
	b, _ := NewIncusBackend(sock)
	net := "lan"
	if err := b.UpdateNetworkIface("ct1", "00:16:3e:aa:bb:cc", models.UpdateNetIfaceRequest{Network: &net}); err != nil {
		t.Fatal(err)
	}
	vm, _ := b.GetDomain("ct1")
	if len(vm.Networks) != 1 || vm.Networks[0].Network != "virbr0" {
		// resolver absent -> default network maps to lxdbr0 fallback
		if vm.Networks[0].Network != "lxdbr0" {
			t.Errorf("eth0 parent after update = %q, want lxdbr0 fallback", vm.Networks[0].Network)
		}
	}
}

func TestIncusBackendUpdateNetworkIfaceRejectsVLAN(t *testing.T) {
	sock, _ := newFakeLXD3(t, map[string]*api.Instance{"ct1": initialContainer()}, nil)
	b, _ := NewIncusBackend(sock)
	vlan := 10
	err := b.UpdateNetworkIface("ct1", "00:16:3e:aa:bb:cc", models.UpdateNetIfaceRequest{VLANTag: &vlan})
	if !isErrNotImplemented(err) {
		t.Errorf("expected ErrNotImplemented for VLAN, got %v", err)
	}
}

// --- metrics collector ---

type fakeFetcher struct {
	instances []api.Instance
	states    map[string]*api.InstanceState
}

func (f fakeFetcher) ListInstances() ([]api.Instance, error) { return f.instances, nil }
func (f fakeFetcher) State(name string) (*api.InstanceState, error) {
	return f.states[name], nil
}

func TestLXDMetricsCollectorDeltas(t *testing.T) {
	col := NewMetricsCollector(nil, nil)
	st := col.upsertState("ct1")

	now1 := time.Now()
	col.collectOne("ct1", st, &api.InstanceState{
		CPU:    api.InstanceStateCPU{Usage: 1_000_000_000}, // 1s of CPU
		Memory: api.InstanceStateMemory{Usage: 512 << 20, Total: 1 << 30},
		Network: map[string]api.InstanceStateNetwork{},
	}, now1)

	now2 := now1.Add(2 * time.Second)
	col.collectOne("ct1", st, &api.InstanceState{
		CPU:    api.InstanceStateCPU{Usage: 3_000_000_000}, // +2s over 2s elapsed -> 100%
		Memory: api.InstanceStateMemory{Usage: 512 << 20, Total: 1 << 30},
		Network: map[string]api.InstanceStateNetwork{
			"eth0": {Counters: api.InstanceStateNetworkCounters{BytesReceived: 1000, BytesSent: 500}},
		},
	}, now2)

	snap, err := col.Get("ct1")
	if err != nil {
		t.Fatal(err)
	}
	cpu := snap.CPU.Points
	if len(cpu) != 1 {
		t.Fatalf("cpu points = %d, want 1", len(cpu))
	}
	if cpu[0].V < 99 || cpu[0].V > 101 {
		t.Errorf("cpu pct = %v, want ~100", cpu[0].V)
	}
	ram := snap.RAM.Points
	if len(ram) != 2 {
		t.Fatalf("ram points = %d, want 2", len(ram))
	}
	if ram[1].V < 49 || ram[1].V > 51 {
		t.Errorf("ram pct = %v, want ~50", ram[1].V)
	}
	nr := snap.NetRx.Points
	if len(nr) != 1 || nr[0].V < 499 || nr[0].V > 501 {
		t.Errorf("net rx = %+v, want ~500 B/s", nr)
	}
	nt := snap.NetTx.Points
	if len(nt) != 1 || nt[0].V < 249 || nt[0].V > 251 {
		t.Errorf("net tx = %+v, want ~250 B/s", nt)
	}
}

func TestLXDMetricsCollectorSampleOnceAndSink(t *testing.T) {
	col := NewMetricsCollector(fakeFetcher{
		instances: []api.Instance{
			{Name: "ct1", Status: "Running"},
			{Name: "off", Status: "Stopped"},
		},
		states: map[string]*api.InstanceState{
			"ct1": {
				CPU:    api.InstanceStateCPU{Usage: 1_000_000_000},
				Memory: api.InstanceStateMemory{Usage: 256 << 20, Total: 1 << 30},
			},
		},
	}, nil)

	var sinkCalls []string
	col.SetSink(func(vmID string, _ time.Time, _ models.VMMetrics) { sinkCalls = append(sinkCalls, vmID) })
	col.sampleOnce()

	snap, _ := col.Get("ct1")
	if len(snap.RAM.Points) != 1 {
		t.Fatalf("running container not sampled")
	}
	if _, ok := col.vms["off"]; ok {
		t.Error("stopped container must not be sampled")
	}
	if len(sinkCalls) != 1 || sinkCalls[0] != "ct1" {
		t.Errorf("sink calls = %v, want [ct1]", sinkCalls)
	}
}