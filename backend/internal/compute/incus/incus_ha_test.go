package incus

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/compute"
	"webkvm/internal/ha"
)

type haMock struct {
	mu        sync.Mutex
	projects  []string // "project" query value of every instance GET
	instances map[string]api.Instance
	pools     map[string]string // pool -> driver
	members   []api.ClusterMember
	// networks maps member -> network name -> network; a member missing from
	// the map answers 500 (unreachable), a name missing from it answers 404.
	networks map[string]map[string]api.Network
	profile  *api.Profile
}

func (m *haMock) start(t *testing.T) *IncusBackend {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "unix.socket")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	reply := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		meta, _ := json.Marshal(v)
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, meta)
	}
	notFound := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		fmt.Fprint(w, `{"type":"error","error":"not found","error_code":404}`)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"api_extensions":["instances","clustering","storage","projects","network"],"api_status":"stable","api_version":"1.0","auth":"trusted","public":false,"environment":{"server_version":"7.4.0-fake","server_clustered":true,"server_name":"a","certificate":""}}}`)
	})
	mux.HandleFunc("/1.0/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/1.0/")
		parts := strings.Split(p, "/")
		switch {
		case p == "cluster/members":
			reply(w, m.members)
		case parts[0] == "instances" && len(parts) == 2:
			m.mu.Lock()
			m.projects = append(m.projects, r.URL.Query().Get("project"))
			inst, ok := m.instances[parts[1]]
			m.mu.Unlock()
			if !ok {
				notFound(w)
				return
			}
			reply(w, inst)
		case parts[0] == "networks" && len(parts) == 2:
			nets, ok := m.networks[r.URL.Query().Get("target")]
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(500)
				fmt.Fprint(w, `{"type":"error","error":"member unreachable","error_code":500}`)
				return
			}
			n, ok := nets[parts[1]]
			if !ok {
				notFound(w)
				return
			}
			reply(w, n)
		case parts[0] == "profiles" && parts[1:] != nil && len(parts) == 2:
			if m.profile == nil || m.profile.Name != parts[1] {
				notFound(w)
				return
			}
			reply(w, m.profile)
		case parts[0] == "storage-pools" && len(parts) == 2:
			driver, ok := m.pools[parts[1]]
			if !ok {
				notFound(w)
				return
			}
			reply(w, api.StoragePool{Name: parts[1], Driver: driver})
		default:
			notFound(w)
		}
	})
	go func() { _ = http.Serve(l, mux) }()
	t.Cleanup(func() { _ = l.Close(); _ = os.Remove(sock) })
	client, err := incus.ConnectIncusUnix(sock, &incus.ConnectionArgs{})
	if err != nil {
		t.Fatal(err)
	}
	return newIncusBackend(client, "")
}

func haInstance() api.Instance {
	return api.Instance{
		Name: "web", Type: "container", Location: "a",
		ExpandedConfig: map[string]string{"boot.autostart": "true", "limits.cpu": "2"},
		ExpandedDevices: map[string]map[string]string{
			"root": {"type": "disk", "path": "/", "pool": "ha-shared"},
			"eth0": {"type": "nic", "network": "br0"},
			"gpu0": {"type": "gpu"},
			"usb1": {"type": "usb", "vendorid": "1234"},
			"data": {"type": "disk", "source": "/srv/data", "path": "/data"},
			"vol":  {"type": "disk", "source": "v1", "pool": "ha-shared", "path": "/vol"},
			"web":  {"type": "proxy", "listen": "tcp:0.0.0.0:80", "connect": "tcp:127.0.0.1:80"},
		},
	}
}

func TestHAInspectorReadsPoolDevicesConfigAndMembers(t *testing.T) {
	m := &haMock{
		instances: map[string]api.Instance{"web": haInstance()},
		pools:     map[string]string{"ha-shared": "linstor"},
		members:   twoMembers(), // a, b online; c offline
	}
	b := m.start(t)
	insp, members, err := b.HAInspector(t.Context(), "", "web")
	if err != nil {
		t.Fatal(err)
	}
	if got := insp.RootPoolDriver("web"); got != "linstor" {
		t.Fatalf("driver = %q", got)
	}
	if got := insp.Config("web"); got["boot.autostart"] != "true" || got["limits.cpu"] != "2" {
		t.Fatalf("config = %v", got)
	}
	types := map[string]ha.DeviceInfo{}
	for _, d := range insp.Devices("web") {
		types[d.Name] = d
	}
	for name, typ := range map[string]string{"gpu0": "gpu", "usb1": "usb", "web": "proxy", "root": "disk", "data": "disk"} {
		if types[name].Type != typ {
			t.Fatalf("device %s = %+v, want type %s (all: %v)", name, types[name], typ, types)
		}
	}
	if types["data"].Pool != "" || types["data"].Source != "/srv/data" || types["vol"].Pool != "ha-shared" {
		t.Fatalf("disk pool/source wrong: %+v %+v", types["data"], types["vol"])
	}
	if len(members) != 3 || !insp.MemberOnline("a") || insp.MemberOnline("c") || insp.MemberOnline("zzz") {
		t.Fatalf("members = %v online a=%v c=%v", members, insp.MemberOnline("a"), insp.MemberOnline("c"))
	}
	// Feeds ha.EvalIncus end to end: local devices, autostart and offline member.
	e := ha.EvalIncus(insp, ha.IncusInputs{Name: "web", Project: "default", Members: members})
	codes := map[string]bool{}
	for _, r := range e.Reasons {
		codes[r.Code] = true
	}
	for _, want := range []string{ha.ReasonLocalDevices, ha.ReasonBootAutostart, ha.ReasonMemberOffline} {
		if !codes[want] {
			t.Fatalf("missing reason %s in %+v", want, e.Reasons)
		}
	}
	if codes[ha.ReasonPoolNotShared] {
		t.Fatalf("linstor pool must count as shared: %+v", e.Reasons)
	}
}

func TestHAInspectorProjectAndNotFound(t *testing.T) {
	m := &haMock{
		instances: map[string]api.Instance{"web": haInstance()},
		pools:     map[string]string{"ha-shared": "ceph"},
		members:   twoMembers(),
	}
	b := m.start(t)
	if _, _, err := b.HAInspector(t.Context(), "team", "web"); err != nil {
		t.Fatal(err)
	}
	if len(m.projects) == 0 || m.projects[len(m.projects)-1] != "team" {
		t.Fatalf("project not forwarded: %v", m.projects)
	}
	_, _, err := b.HAInspector(t.Context(), "", "ghost")
	if !errors.Is(err, compute.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, _, err := (*IncusBackend)(nil).HAInspector(t.Context(), "", "x"); !errors.Is(err, compute.ErrNotImplemented) {
		t.Fatalf("nil backend: %v", err)
	}
}

func TestHAInspectorUnresolvablePoolIsNotShared(t *testing.T) {
	m := &haMock{instances: map[string]api.Instance{"web": haInstance()}, pools: map[string]string{}, members: twoMembers()}
	insp, _, err := m.start(t).HAInspector(t.Context(), "", "web")
	if err != nil {
		t.Fatal(err)
	}
	if insp.RootPoolDriver("web") != "" || insp.RootPoolDriver("other") != "" {
		t.Fatal("unresolved pool must give an empty driver")
	}
}

func threeMembers() []api.ClusterMember {
	return []api.ClusterMember{
		{ServerName: "a", Status: "Online"}, {ServerName: "b", Status: "Online"}, {ServerName: "c", Status: "Online"},
	}
}

func TestHAInspectorNICsAndNetworkMembers(t *testing.T) {
	inst := haInstance()
	inst.ExpandedDevices["eth0"] = map[string]string{"type": "nic", "nictype": "bridged", "parent": "vmbr0"}
	inst.ExpandedDevices["eth1"] = map[string]string{"type": "nic", "network": "lan"}
	m := &haMock{
		instances: map[string]api.Instance{"web": inst},
		pools:     map[string]string{"ha-shared": "linstor"},
		members:   threeMembers(),
		networks: map[string]map[string]api.Network{
			// vmbr0 is an unmanaged host bridge present on member a only (live case).
			"a": {"vmbr0": {Name: "vmbr0", Managed: false, Type: "bridge"}, "lan": {Name: "lan", Managed: true, Status: "Created"}},
			"b": {"lan": {Name: "lan", Managed: true, Status: "Created"}},
			// c is unreachable for network probes: unknown, never "missing".
		},
	}
	insp, members, err := m.start(t).HAInspector(t.Context(), "", "web")
	if err != nil {
		t.Fatal(err)
	}
	nics := insp.NICs("web")
	if len(nics) != 2 || nics[0].Name != "eth0" || nics[0].Parent != "vmbr0" || nics[1].Network != "lan" {
		t.Fatalf("nics = %+v", nics)
	}
	present, managed, err := insp.NetworkMembers("vmbr0")
	if err != nil || managed || !present["a"] || present["b"] {
		t.Fatalf("vmbr0 present=%v managed=%v err=%v", present, managed, err)
	}
	if _, known := present["c"]; known {
		t.Fatalf("unreachable member must stay unknown: %v", present)
	}
	present, managed, err = insp.NetworkMembers("lan")
	if err != nil || !managed || !present["a"] || !present["b"] {
		t.Fatalf("lan present=%v managed=%v err=%v", present, managed, err)
	}
	e := ha.EvalIncus(insp, ha.IncusInputs{Name: "web", Project: "default", Members: members})
	var got *ha.Reason
	for i := range e.Reasons {
		if e.Reasons[i].Code == ha.ReasonNICNetworkNotOnAllMembers {
			got = &e.Reasons[i]
		}
	}
	if got == nil || !strings.Contains(got.Detail, "eth0") || !strings.Contains(got.Detail, "vmbr0") || !strings.Contains(got.Detail, "b") || strings.Contains(got.Detail, "c") && !strings.Contains(got.Detail, "vmbr0") {
		t.Fatalf("reasons = %+v", e.Reasons)
	}
	if len(got.Commands) == 0 {
		t.Fatalf("no commands: %+v", got)
	}
	if _, _, err := insp.NetworkMembers("never-probed"); err == nil {
		t.Fatal("unprobed network must be an error")
	}
}

func TestManagedNetworkPendingCountsAsMissing(t *testing.T) {
	inst := haInstance()
	inst.ExpandedDevices["eth0"] = map[string]string{"type": "nic", "network": "lan"}
	m := &haMock{
		instances: map[string]api.Instance{"web": inst},
		pools:     map[string]string{"ha-shared": "linstor"},
		members:   twoMembers(),
		networks: map[string]map[string]api.Network{
			"a": {"lan": {Name: "lan", Managed: true, Status: "Created"}},
			"b": {"lan": {Name: "lan", Managed: true, Status: "Pending"}},
		},
	}
	insp, _, err := m.start(t).HAInspector(t.Context(), "", "web")
	if err != nil {
		t.Fatal(err)
	}
	present, managed, _ := insp.NetworkMembers("lan")
	if !managed || !present["a"] || present["b"] {
		t.Fatalf("present=%v", present)
	}
}

func TestDefaultProfileNICIssues(t *testing.T) {
	m := &haMock{
		members: threeMembers(),
		profile: &api.Profile{Name: "default", ProfilePut: api.ProfilePut{Devices: map[string]map[string]string{
			"eth0": {"type": "nic", "nictype": "bridged", "parent": "vmbr0", "name": "eth0"},
			"root": {"type": "disk", "path": "/", "pool": "p"},
		}}},
		networks: map[string]map[string]api.Network{
			"a": {"vmbr0": {Name: "vmbr0", Type: "bridge"}}, "b": {}, "c": {},
		},
	}
	issues, err := m.start(t).DefaultProfileNICIssues(t.Context())
	if err != nil || len(issues) != 1 || issues[0].Code != ha.ReasonNICNetworkNotOnAllMembers || !strings.Contains(issues[0].Detail, "b, c") {
		t.Fatalf("%+v %v", issues, err)
	}
	m.networks = map[string]map[string]api.Network{"a": {"vmbr0": {Name: "vmbr0"}}, "b": {"vmbr0": {Name: "vmbr0"}}, "c": {"vmbr0": {Name: "vmbr0"}}}
	b := m.start(t)
	if issues, err := b.DefaultProfileNICIssues(t.Context()); err != nil || len(issues) != 0 {
		t.Fatalf("portable profile must be clean: %+v %v", issues, err)
	}
	if _, err := (*IncusBackend)(nil).DefaultProfileNICIssues(t.Context()); !errors.Is(err, compute.ErrNotImplemented) {
		t.Fatalf("nil backend: %v", err)
	}
}
