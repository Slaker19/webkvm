package incus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lxc/incus/v6/shared/api"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// TestInstanceToVM maps a running container to the neutral model.
func TestInstanceToVM(t *testing.T) {
	inst := &api.Instance{
		Name:   "web",
		Status: "Running",
		Type:   string(api.InstanceTypeContainer),
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"limits.cpu":     "2",
				"limits.memory":  "1GiB",
				"boot.autostart": "true",
			},
		},
	}
	vm := instanceToVM(inst)
	if vm.ID != "web" || vm.Name != "web" {
		t.Errorf("id/name = %q/%q", vm.ID, vm.Name)
	}
	if vm.Type != "container" || vm.Hypervisor != "incus" {
		t.Errorf("type/hypervisor = %q/%q", vm.Type, vm.Hypervisor)
	}
	if vm.State != models.VMStateRunning {
		t.Errorf("state = %q, want running", vm.State)
	}
	if vm.VCPUs != 2 {
		t.Errorf("vcpus = %d, want 2", vm.VCPUs)
	}
	if vm.RAMMB != 1024 {
		t.Errorf("ram_mb = %d, want 1024 (1GiB)", vm.RAMMB)
	}
	if !vm.Autostart {
		t.Error("autostart should be true")
	}
	if vm.ProvisionMethod != "" {
		t.Errorf("provision_method = %q, want empty without user-data", vm.ProvisionMethod)
	}
}

// A container created with native cloud-init carries user.user-data in
// its config -> the provisioning chip should read "cloud-init".
func TestInstanceToVM_ProvisionMethod(t *testing.T) {
	provisioned := instanceToVM(&api.Instance{
		Name:   "web2",
		Status: "Running",
		Type:   "container",
		InstancePut: api.InstancePut{
			Config: map[string]string{"user.user-data": "#cloud-config\n"},
		},
	})
	if provisioned.ProvisionMethod != "cloud-init" {
		t.Errorf("provision_method = %q, want cloud-init", provisioned.ProvisionMethod)
	}
	plain := instanceToVM(&api.Instance{Name: "plain", Status: "Stopped", Type: "container"})
	if plain.ProvisionMethod != "" {
		t.Errorf("provision_method = %q, want empty", plain.ProvisionMethod)
	}
}

// An LXD virtual-machine maps Type=vm; a stopped container -> shutoff.
func TestInstanceToVM_VMTypeAndStates(t *testing.T) {
	lxdVM := instanceToVM(&api.Instance{
		Name: "vm1", Status: "Running", Type: string(api.InstanceTypeVM),
	})
	if lxdVM.Type != "vm" {
		t.Errorf("lxd VM type = %q, want vm", lxdVM.Type)
	}
	stopped := instanceToVM(&api.Instance{Name: "c1", Status: "Stopped", Type: "container"})
	if stopped.State != models.VMStateShutoff {
		t.Errorf("stopped state = %q, want shutoff", stopped.State)
	}
	frozen := instanceToVM(&api.Instance{Name: "c2", Status: "Frozen", Type: "container"})
	if frozen.State != models.VMStatePaused {
		t.Errorf("frozen state = %q, want paused", frozen.State)
	}
}

func TestParseMemoryMB(t *testing.T) {
	cases := map[string]int64{
		"1GiB": 1024, "2GB": 2000, "512MiB": 512, "256MB": 256,
		"": 0, "bogus": 0, "1KiB": 1,
	}
	for in, want := range cases {
		if got := parseMemoryMB(in); got != want {
			t.Errorf("parseMemoryMB(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestParseIntConfig(t *testing.T) {
	if parseIntConfig("4") != 4 {
		t.Error("plain int should parse")
	}
	if parseIntConfig("2-4") != 0 || parseIntConfig("50%") != 0 || parseIntConfig("") != 0 {
		t.Error("ranges/percentages/empty should map to 0")
	}
}

// TestIncusBackendFailSafe: every not-yet-implemented operation returns
// the ErrNotImplemented sentinel (which the handlers surface as 501).
func TestIncusBackendFailSafe(t *testing.T) {
	b := &IncusBackend{} // no client needed for stubs
	ops := []func() error{
		func() error { _, err := b.ListSnapshots("x"); return err },
		func() error { _, err := b.ListStoragePools(); return err },
		func() error { return b.AttachDisk("x", models.AttachDiskRequest{}) },
		func() error { return b.ExportDomainOVA(context.Background(), "x", compute.OVAOptions{}, nil) },
		func() error { _, err := b.ListHostUSBDevices(); return err },
	}
	for i, op := range ops {
		if err := op(); !errors.Is(err, compute.ErrNotImplemented) {
			t.Errorf("op %d: err = %v, want compute.ErrNotImplemented", i, err)
		}
	}
	if c := b.Capabilities(); c.SupportsOVA || c.SupportsVNC || c.SupportsSnapshots {
		t.Errorf("LXD must not advertise OVA/VNC/snapshots, got %+v", c)
	}
	if c := b.Capabilities(); !c.SupportsSerialConsole {
		t.Error("LXD advertises the interactive serial console since Fase 2")
	}
}

// fakeLXDServer is a minimal LXD daemon over a unix socket: enough for
// ConnectIncusUnix (GET /1.0) and GetInstances (GET /1.0/instances).
func fakeLXDServer(t *testing.T, instances []api.Instance) string {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "unix.socket")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"api_extensions":["instances","snapshots","resources","networks"],"api_status":"stable","api_version":"1.0","auth":"trusted","public":false,"environment":{"server_version":"6.0.0-fake","certificate":""}}}`)
	})
	mux.HandleFunc("/1.0/instances", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		meta, _ := json.Marshal(instances)
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, meta)
	})
	go http.Serve(l, mux)
	t.Cleanup(func() { l.Close(); _ = os.Remove(sock) })
	return sock
}

// TestIncusBackendConnectAndList is the Fase 1 gate: connect to the LXD
// daemon over a unix socket (simulated) and list instances mapped to the
// neutral model — the exact path the backend follows in production.
func TestIncusBackendConnectAndList(t *testing.T) {
	instances := []api.Instance{
		{Name: "web", Status: "Running", Type: "container",
			InstancePut: api.InstancePut{Config: map[string]string{"limits.cpu": "2", "limits.memory": "1GiB"}}},
		{Name: "db", Status: "Stopped", Type: "container",
			InstancePut: api.InstancePut{Config: map[string]string{"limits.memory": "512MiB"}}},
	}
	sock := fakeLXDServer(t, instances)

	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect to LXD socket: %v", err)
	}
	if v, err := b.ServerInfo(); err != nil || v != "6.0.0-fake" {
		t.Fatalf("ServerInfo = %q, %v", v, err)
	}
	vms, err := b.ListDomains()
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(vms) != 2 {
		t.Fatalf("got %d instances, want 2: %+v", len(vms), vms)
	}
	if vms[0].Name != "web" || vms[0].State != models.VMStateRunning || vms[0].RAMMB != 1024 {
		t.Errorf("web mapping wrong: %+v", vms[0])
	}
	if vms[1].Name != "db" || vms[1].State != models.VMStateShutoff || vms[1].Hypervisor != "incus" {
		t.Errorf("db mapping wrong: %+v", vms[1])
	}
}

// TestNewIncusBackendMissingSocket: a missing socket returns an error so
// the caller can degrade to KVM-only (fail-safe, no crash).
func TestNewIncusBackendMissingSocket(t *testing.T) {
	_, err := NewIncusBackend(filepath.Join(t.TempDir(), "no-such.socket"))
	if err == nil {
		t.Fatal("expected connection error for a missing socket")
	}
	if strings.Contains(err.Error(), "panic") {
		t.Fatalf("should be a clean error, got %v", err)
	}
}

// The official client import must not leak into the neutral interface:
// compute.Backend is implemented by *IncusBackend (compile-time check).
var _ compute.Backend = (*IncusBackend)(nil)

func TestValidateProfiles(t *testing.T) {
	if err := validateProfiles([]string{"default"}); err != nil {
		t.Errorf("default profile should be valid: %v", err)
	}
	if err := validateProfiles([]string{"default", "gpu"}); err != nil {
		t.Errorf("multi profile should be valid: %v", err)
	}
	for _, bad := range []string{"", " has space", "tab\t", "new\nline"} {
		if err := validateProfiles([]string{bad}); err == nil {
			t.Errorf("profile %q should be rejected", bad)
		}
	}
}

func TestInstanceToVM_Advanced(t *testing.T) {
	inst := &api.Instance{
		Name:   "web",
		Status: "Running",
		Type:   "container",
		InstancePut: api.InstancePut{
			Config: map[string]string{
				"security.privileged": "true",
				"security.nesting":    "true",
				"boot.autostart":      "false",
			},
			Profiles: []string{"default", "gpu"},
		},
	}
	vm := instanceToVM(inst)
	if !vm.Privileged {
		t.Error("expected Privileged=true from security.privileged=true")
	}
	if !vm.Nesting {
		t.Error("expected Nesting=true from security.nesting=true")
	}
	if vm.Autostart {
		t.Error("expected Autostart=false from boot.autostart=false")
	}
	if len(vm.Profiles) != 2 || vm.Profiles[0] != "default" || vm.Profiles[1] != "gpu" {
		t.Errorf("Profiles = %v, want [default gpu]", vm.Profiles)
	}
}

// fakeInstanceServer serves a single container instance over a unix socket
// for UpdateDomain tests (GET /1.0 + GET /1.0/instances/<name>).
func fakeInstanceServer(t *testing.T, inst api.Instance) string {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "unix.socket")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"api_extensions":["instances"],"api_status":"stable","api_version":"1.0","auth":"trusted","public":false,"environment":{"server_version":"6.0.0-fake","certificate":""}}}`)
	})
	mux.HandleFunc("/1.0/instances/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		meta, _ := json.Marshal(inst)
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, meta)
	})
	go http.Serve(l, mux)
	t.Cleanup(func() { l.Close(); _ = os.Remove(sock) })
	return sock
}

func TestUpdateDomain_PrivilegedHotChangeLocked(t *testing.T) {
	inst := api.Instance{
		Name:       "web",
		Status:     "Running",
		StatusCode: api.Running,
		Type:       "container",
		InstancePut: api.InstancePut{
			Config:   map[string]string{"security.privileged": "false"},
			Profiles: []string{"default"},
		},
	}
	b, err := NewIncusBackend(fakeInstanceServer(t, inst))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	priv := true
	_, err = b.UpdateDomain("web", models.UpdateVMRequest{Privileged: &priv})
	if err == nil {
		t.Fatal("expected an error when toggling security.privileged on a running container")
	}
	if !strings.Contains(err.Error(), "stop it first") {
		t.Errorf("error should ask to stop the container, got: %v", err)
	}
}

// fakeInstanceStateServer serves a container plus its live state (with a LAN
// IPv4 on eth0) so GetDomain/ListDomains can surface the IP, fixing the
// "containers look isolated" UI issue.
func fakeInstanceStateServer(t *testing.T, inst api.Instance, ip string) string {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "unix.socket")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"api_extensions":["instances"],"api_status":"stable","api_version":"1.0","auth":"trusted","public":false,"environment":{"server_version":"6.0.0-fake","certificate":""}}}`)
	})
	mux.HandleFunc("/1.0/instances", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		meta, _ := json.Marshal([]api.Instance{inst})
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, meta)
	})
	mux.HandleFunc("/1.0/instances/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/state") {
			state := fmt.Sprintf(`{"type":"sync","status":"Success","status_code":200,"metadata":{"status":"Running","pid":1234,"network":{"eth0":{"addresses":[{"family":"inet","address":%q,"netmask":"24","scope":"global"}],"hwaddr":"00:16:3e:aa:bb:cc","mtu":1500,"state":"up","type":"broadcast"},"lo":{"addresses":[{"family":"inet","address":"127.0.0.1","netmask":"8","scope":"local"}],"mtu":65536,"state":"up","type":"loopback"}}}}`, ip)
			fmt.Fprint(w, state)
			return
		}
		meta, _ := json.Marshal(inst)
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, meta)
	})
	go http.Serve(l, mux)
	t.Cleanup(func() { l.Close(); _ = os.Remove(sock) })
	return sock
}

func TestIncusBackendSurfacesContainerIP(t *testing.T) {
	inst := api.Instance{
		Name:       "wk-lan",
		Status:     "Running",
		StatusCode: api.Running,
		Type:       "container",
		InstancePut: api.InstancePut{
			Config:   map[string]string{"limits.cpu": "2"},
			Profiles: []string{"default"},
		},
	}
	b, err := NewIncusBackend(fakeInstanceStateServer(t, inst, "192.168.1.121"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	vm, err := b.GetDomain("wk-lan")
	if err != nil {
		t.Fatalf("GetDomain: %v", err)
	}
	if vm.IP != "192.168.1.121" {
		t.Errorf("GetDomain IP = %q, want 192.168.1.121 (container must show its LAN IP, not look isolated)", vm.IP)
	}

	vms, err := b.ListDomains()
	if err != nil {
		t.Fatalf("ListDomains: %v", err)
	}
	if len(vms) != 1 || vms[0].IP != "192.168.1.121" {
		t.Errorf("ListDomains IP = %+v, want the container LAN IP surfaced", vms)
	}
}

func TestInstanceIPFallbackAnyInterface(t *testing.T) {
	if got := instanceIP(nil); got != "" {
		t.Errorf("nil state: got %q, want empty", got)
	}
	st := &api.InstanceState{
		Network: map[string]api.InstanceStateNetwork{
			"eth0": {Addresses: []api.InstanceStateNetworkAddress{
				{Family: "inet6", Address: "fd66::1"},
			}},
			"eth1": {Addresses: []api.InstanceStateNetworkAddress{
				{Family: "inet", Address: "10.0.0.7"},
			}},
		},
	}
	if got := instanceIP(st); got != "10.0.0.7" {
		t.Errorf("eth0 without IPv4 should fall back to eth1: got %q", got)
	}
}

func TestBridgeForNetwork(t *testing.T) {
	b := &IncusBackend{}

	// Empty network: uses the host's PHYSICAL bridge when present, else
	// fails with the fatal no-physical-bridge error (never virbr0/lxdbr0).
	if real := findAnyPhysicalBridge(); real != "" {
		if br, err := b.bridgeForNetwork(""); err != nil || br != real {
			t.Errorf("empty network: bridge=%q err=%v (want %q)", br, err, real)
		}
	} else if _, err := b.bridgeForNetwork(""); !errors.Is(err, compute.ErrNoPhysicalBridge) {
		t.Errorf("empty network without a physical bridge: err=%v, want ErrNoPhysicalBridge", err)
	}

	// Without a resolver, a logical network must NOT be silently guessed.
	if _, err := b.bridgeForNetwork("webkvm-bridge"); err == nil {
		t.Error("expected an error for a logical network when no resolver is wired")
	}

	// A resolver returning a VIRTUAL bridge (virbr0) — or the logical name
	// — must be rejected: only physical bridges are valid NIC parents.
	b.networkResolver = func(name string) (string, error) { return "virbr0", nil }
	if _, err := b.bridgeForNetwork("webkvm-bridge"); err == nil {
		t.Error("expected an error when the resolver returns a virtual bridge (virbr0)")
	}

	// Resolver error: propagates as ErrNoPhysicalBridge — NO fallback to
	// NAT/virtual bridges.
	b.networkResolver = func(name string) (string, error) { return "", fmt.Errorf("no such network") }
	if _, err := b.bridgeForNetwork("mynet"); !errors.Is(err, compute.ErrNoPhysicalBridge) {
		t.Errorf("resolver error should surface ErrNoPhysicalBridge, got: %v", err)
	}

	// A resolver returning a REAL physical host bridge is used as the
	// parent (logical → physical translation).
	if real := findAnyPhysicalBridge(); real != "" {
		b.networkResolver = func(name string) (string, error) { return real, nil }
		if br, err := b.bridgeForNetwork("webkvm-bridge"); err != nil || br != real {
			t.Errorf("resolved bridge: bridge=%q err=%v (want %q)", br, err, real)
		}
	}

	// A name that IS a physical Linux bridge on the host is used directly.
	if isPhysicalBridge("vmbr0") {
		if br, err := b.bridgeForNetwork("vmbr0"); err != nil || br != "vmbr0" {
			t.Errorf("direct bridge: bridge=%q err=%v", br, err)
		}
	}
	if isPhysicalBridge("virbr0") {
		t.Error("virbr0 must not be treated as a physical (shared) bridge")
	}
	if isPhysicalBridge("definitely-not-a-bridge") {
		t.Error("isPhysicalBridge should be false for a non-bridge name")
	}
}

// findAnyPhysicalBridge returns the name of the first PHYSICAL Linux bridge
// present on the host (vmbr0/br0 — excluding virbr*/lxdbr*/docker*/br-*),
// or "" if there is none (skips the resolved-bridge assertions).
func findAnyPhysicalBridge() string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if isPhysicalBridge(e.Name()) {
			return e.Name()
		}
	}
	return ""
}
