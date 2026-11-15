package incus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

type recordedReq struct {
	Method, Path, Target, Body string
}

// clusterMock is a tiny clustered Incus daemon over a unix socket.
type clusterMock struct {
	mu        sync.Mutex
	reqs      []recordedReq
	members   []api.ClusterMember
	instances map[string]api.Instance
	snaps     map[string][]api.InstanceSnapshot
	opStatus  string // "Success" (default) or "Failure"
	opErr     string
}

func (m *clusterMock) calls(method, pathSuffix string) []recordedReq {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []recordedReq
	for _, r := range m.reqs {
		if r.Method == method && strings.HasSuffix(r.Path, pathSuffix) {
			out = append(out, r)
		}
	}
	return out
}

func (m *clusterMock) start(t *testing.T) *IncusBackend {
	t.Helper()
	if m.opStatus == "" {
		m.opStatus = "Success"
	}
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
	async := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		code := 200
		if m.opStatus != "Success" {
			code = 400
		}
		meta, _ := json.Marshal(map[string]any{"id": "op1", "status": m.opStatus, "status_code": code, "err": m.opErr})
		fmt.Fprintf(w, `{"type":"async","status":"Operation created","status_code":100,"operation":"/1.0/operations/op1","metadata":%s}`, meta)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"api_extensions":["instances","snapshots","network","clustering","clustering_evacuation","snapshot_expiry_creation","instance_pool_move"],"api_status":"stable","api_version":"1.0","auth":"trusted","public":false,"environment":{"server_version":"7.4.0-fake","server_clustered":true,"server_name":"a","certificate":""}}}`)
	})
	mux.HandleFunc("/1.0/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.reqs = append(m.reqs, recordedReq{r.Method, r.URL.Path, r.URL.Query().Get("target"), string(body)})
		m.mu.Unlock()
		p := strings.TrimPrefix(r.URL.Path, "/1.0/")
		parts := strings.Split(p, "/")
		switch {
		case p == "cluster/members":
			reply(w, m.members)
		case len(parts) == 4 && parts[0] == "cluster" && parts[3] == "state" && r.Method == "POST":
			async(w)
		case p == "networks/br0":
			reply(w, api.Network{Name: "br0", Type: "bridge"})
		case p == "instances" && r.Method == "POST":
			async(w)
		case parts[0] == "instances" && len(parts) == 2:
			inst, ok := m.instances[parts[1]]
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(404)
				fmt.Fprint(w, `{"type":"error","error":"Instance not found","error_code":404}`)
				return
			}
			if r.Method == "GET" {
				reply(w, inst)
			} else {
				async(w)
			}
		case parts[0] == "instances" && len(parts) == 3 && parts[2] == "state":
			reply(w, api.InstanceState{Status: "Running"})
		case parts[0] == "instances" && len(parts) == 3 && parts[2] == "snapshots":
			if r.Method == "POST" {
				async(w)
				return
			}
			reply(w, m.snaps[parts[1]])
		case parts[0] == "instances" && len(parts) == 4 && parts[2] == "snapshots":
			for _, s := range m.snaps[parts[1]] {
				if shortSnapshotName(s.Name) == parts[3] {
					if r.Method == "DELETE" {
						async(w)
					} else {
						reply(w, s)
					}
					return
				}
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			fmt.Fprint(w, `{"type":"error","error":"Snapshot not found","error_code":404}`)
		default:
			http.NotFound(w, r)
		}
	})
	go func() { _ = http.Serve(l, mux) }()
	t.Cleanup(func() { _ = l.Close(); _ = os.Remove(sock) })
	client, err := incus.ConnectIncusUnix(sock, &incus.ConnectionArgs{})
	if err != nil {
		t.Fatal(err)
	}
	// socketPath "" = remote-style network lookup (GetNetwork), host-independent.
	return newIncusBackend(client, "")
}

func twoMembers() []api.ClusterMember {
	return []api.ClusterMember{
		{ServerName: "a", URL: "https://a:8443", Status: "Online"},
		{ServerName: "b", URL: "https://b:8443", Status: "Online"},
		{ServerName: "c", URL: "https://c:8443", Status: "Offline"},
	}
}

func inst(name, typ, status, loc string) api.Instance {
	return api.Instance{Name: name, Type: typ, Status: status, Location: loc}
}

func TestCreateWithTargetMember(t *testing.T) {
	m := &clusterMock{members: twoMembers(), instances: map[string]api.Instance{"web": inst("web", "container", "Stopped", "b")}}
	b := m.start(t)
	vm, err := b.CreateDomain(models.CreateVMRequest{Name: "web", Image: "images:alpine/3.20", Network: "br0", TargetMember: "b"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if vm.Node != "b" {
		t.Errorf("Node = %q, want b", vm.Node)
	}
	posts := m.calls("POST", "/1.0/instances")
	if len(posts) != 1 || posts[0].Target != "b" {
		t.Fatalf("expected one create with ?target=b, got %+v", posts)
	}
}

func TestCreateWithoutTargetHasNoTargetParam(t *testing.T) {
	m := &clusterMock{members: twoMembers(), instances: map[string]api.Instance{"web": inst("web", "container", "Stopped", "a")}}
	b := m.start(t)
	if _, err := b.CreateDomain(models.CreateVMRequest{Name: "web", Image: "images:alpine/3.20", Network: "br0"}); err != nil {
		t.Fatal(err)
	}
	if posts := m.calls("POST", "/1.0/instances"); len(posts) != 1 || posts[0].Target != "" {
		t.Fatalf("unexpected target: %+v", posts)
	}
}

func TestCreateTargetUnknownAndOffline(t *testing.T) {
	m := &clusterMock{members: twoMembers(), instances: map[string]api.Instance{}}
	b := m.start(t)
	_, err := b.CreateDomain(models.CreateVMRequest{Name: "x", Image: "images:alpine/3.20", Network: "br0", TargetMember: "zzz"})
	if !errors.Is(err, compute.ErrMemberNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	_, err = b.CreateDomain(models.CreateVMRequest{Name: "x", Image: "images:alpine/3.20", Network: "br0", TargetMember: "c"})
	if !errors.Is(err, compute.ErrMemberOffline) {
		t.Fatalf("offline: %v", err)
	}
	if posts := m.calls("POST", "/1.0/instances"); len(posts) != 0 {
		t.Fatalf("no create must be sent: %+v", posts)
	}
}

func TestMoveRunningContainerWithoutLiveNeedsStop(t *testing.T) {
	m := &clusterMock{members: twoMembers(), instances: map[string]api.Instance{"ct": inst("ct", "container", "Running", "a")}}
	b := m.start(t)
	err := b.MoveInstance(context.Background(), "ct", "b", false)
	if !errors.Is(err, compute.ErrNeedsStop) {
		t.Fatalf("got %v", err)
	}
	if len(m.calls("POST", "/1.0/instances/ct")) != 0 {
		t.Fatal("no migration may be sent")
	}
}

func TestMoveStoppedContainerAndLiveVM(t *testing.T) {
	m := &clusterMock{members: twoMembers(), instances: map[string]api.Instance{
		"ct": inst("ct", "container", "Stopped", "a"),
		"vm": inst("vm", "virtual-machine", "Running", "a"),
	}}
	b := m.start(t)
	if err := b.MoveInstance(context.Background(), "ct", "b", false); err != nil {
		t.Fatalf("stopped move: %v", err)
	}
	if err := b.MoveInstance(context.Background(), "vm", "b", true); err != nil {
		t.Fatalf("live vm move: %v", err)
	}
	ct := m.calls("POST", "/1.0/instances/ct")
	vm := m.calls("POST", "/1.0/instances/vm")
	if len(ct) != 1 || ct[0].Target != "b" || !strings.Contains(ct[0].Body, `"migration":true`) || !strings.Contains(ct[0].Body, `"live":false`) {
		t.Fatalf("ct request: %+v", ct)
	}
	if len(vm) != 1 || vm[0].Target != "b" || !strings.Contains(vm[0].Body, `"live":true`) {
		t.Fatalf("vm request: %+v", vm)
	}
}

func TestMoveRefusals(t *testing.T) {
	m := &clusterMock{members: twoMembers(), instances: map[string]api.Instance{"ct": inst("ct", "container", "Stopped", "a")}}
	b := m.start(t)
	ctx := context.Background()
	if err := b.MoveInstance(ctx, "ct", "a", false); !errors.Is(err, compute.ErrSameMember) {
		t.Errorf("same member: %v", err)
	}
	if err := b.MoveInstance(ctx, "ct", "nope", false); !errors.Is(err, compute.ErrMemberNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if err := b.MoveInstance(ctx, "ct", "c", false); !errors.Is(err, compute.ErrMemberOffline) {
		t.Errorf("offline: %v", err)
	}
	if err := b.MoveInstance(ctx, "ghost", "b", false); err == nil {
		t.Error("missing instance must fail")
	}
}

func TestMoveOperationFailure(t *testing.T) {
	m := &clusterMock{members: twoMembers(), opStatus: "Failure", opErr: "boom",
		instances: map[string]api.Instance{"ct": inst("ct", "container", "Stopped", "a")}}
	b := m.start(t)
	if err := b.MoveInstance(context.Background(), "ct", "b", false); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("got %v", err)
	}
}

func TestEvacuateAndRestore(t *testing.T) {
	m := &clusterMock{members: twoMembers(), instances: map[string]api.Instance{}}
	b := m.start(t)
	ctx := context.Background()
	if err := b.EvacuateMember(ctx, "b", "stop"); err != nil {
		t.Fatalf("evacuate: %v", err)
	}
	if err := b.RestoreMember(ctx, "b"); err != nil {
		t.Fatalf("restore: %v", err)
	}
	st := m.calls("POST", "/1.0/cluster/members/b/state")
	if len(st) != 2 || !strings.Contains(st[0].Body, `"action":"evacuate"`) || !strings.Contains(st[0].Body, `"mode":"stop"`) || !strings.Contains(st[1].Body, `"action":"restore"`) {
		t.Fatalf("state calls: %+v", st)
	}
	if err := b.EvacuateMember(ctx, "zzz", ""); !errors.Is(err, compute.ErrMemberNotFound) {
		t.Errorf("unknown: %v", err)
	}
	if err := b.EvacuateMember(ctx, "b", "rm -rf"); err == nil {
		t.Error("bad mode must be rejected")
	}
}

func TestEvacuateOperationFailureAndCancelledCtx(t *testing.T) {
	m := &clusterMock{members: twoMembers(), opStatus: "Failure", opErr: "no space", instances: map[string]api.Instance{}}
	b := m.start(t)
	if err := b.EvacuateMember(context.Background(), "b", ""); err == nil || !strings.Contains(err.Error(), "no space") {
		t.Fatalf("got %v", err)
	}
	if err := b.RestoreMember(context.Background(), "b"); err == nil {
		t.Fatal("restore failure must surface")
	}
}

func TestWaitOperationCtxCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitOperationCtx(ctx, fakeRunningOp{}, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

type fakeRunningOp struct{ incus.Operation }

func (fakeRunningOp) Get() api.Operation { return api.Operation{Status: "Running"} }
func (fakeRunningOp) Cancel() error      { return nil }
func (fakeRunningOp) Refresh() error     { return nil }

func TestNotClusteredErrors(t *testing.T) {
	b := fakeNonClustered(t)
	if err := b.MoveInstance(context.Background(), "x", "b", false); !errors.Is(err, compute.ErrNotClustered) {
		t.Fatalf("got %v", err)
	}
	if err := b.RestoreMember(context.Background(), "b"); !errors.Is(err, compute.ErrNotClustered) {
		t.Fatalf("got %v", err)
	}
}

func fakeNonClustered(t *testing.T) *IncusBackend {
	t.Helper()
	b, err := NewIncusBackend(fakeInstanceServer(t, api.Instance{Name: "x"}))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestClusterMembersAndLocationAndMemberOnline(t *testing.T) {
	m := &clusterMock{members: twoMembers(), instances: map[string]api.Instance{
		"on":  inst("on", "container", "Running", "a"),
		"off": inst("off", "container", "Running", "c"),
	}}
	b := m.start(t)
	members, err := b.ClusterMembers()
	if err != nil || len(members) != 3 || members[2].Status != "Offline" {
		t.Fatalf("members: %+v %v", members, err)
	}
	var cb compute.ClusterBackend = b
	if loc, err := cb.InstanceLocation("off"); err != nil || loc != "c" {
		t.Fatalf("location: %q %v", loc, err)
	}
	vm, err := b.GetDomain("off")
	if err != nil || vm.MemberOnline == nil || *vm.MemberOnline {
		t.Fatalf("offline member flag: %+v %v", vm.MemberOnline, err)
	}
	vm, _ = b.GetDomain("on")
	if vm.MemberOnline == nil || !*vm.MemberOnline {
		t.Fatalf("online member flag: %+v", vm.MemberOnline)
	}
}

func snapFixture(name string, stateful bool, size int64, at time.Time) api.InstanceSnapshot {
	return api.InstanceSnapshot{
		InstanceSnapshotPut: api.InstanceSnapshotPut{},
		Name:                name, Stateful: stateful, Size: size, CreatedAt: at,
	}
}

func TestSnapshotsCRUDForContainerAndVM(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	for _, typ := range []string{"container", "virtual-machine"} {
		t.Run(typ, func(t *testing.T) {
			m := &clusterMock{members: twoMembers(),
				instances: map[string]api.Instance{"i": inst("i", typ, "Running", "a")},
				snaps: map[string][]api.InstanceSnapshot{"i": {
					snapFixture("s2", false, 20, t0.Add(time.Hour)),
					snapFixture("i/s1", true, 10, t0),
				}}}
			b := m.start(t)
			list, err := b.ListSnapshots("i")
			if err != nil || len(list) != 2 || list[0].ID != "s1" || !list[0].HasMemory || list[1].ID != "s2" {
				t.Fatalf("list: %+v %v", list, err)
			}
			if list[0].CreationTime != t0.Unix() {
				t.Errorf("creation time: %d", list[0].CreationTime)
			}
			m.snaps["i"] = append(m.snaps["i"], snapFixture("s3", true, 5, t0))
			snap, err := b.CreateSnapshot("i", models.CreateSnapshotRequest{Name: "s3", Memory: true})
			if err != nil || snap.ID != "s3" {
				t.Fatalf("create: %+v %v", snap, err)
			}
			if c := m.calls("POST", "/1.0/instances/i/snapshots"); len(c) != 1 || !strings.Contains(c[0].Body, `"name":"s3"`) || !strings.Contains(c[0].Body, `"stateful":true`) {
				t.Fatalf("create body: %+v", c)
			}
			if _, err := b.CreateSnapshot("i", models.CreateSnapshotRequest{Name: "bad name!"}); err == nil {
				t.Error("invalid name must fail")
			}
			if err := b.RevertSnapshot("i", "s1"); err != nil {
				t.Fatalf("revert: %v", err)
			}
			put := m.calls("PUT", "/1.0/instances/i")
			if len(put) != 1 || !strings.Contains(put[0].Body, `"restore":"s1"`) || !strings.Contains(put[0].Body, `"stateful":true`) {
				t.Fatalf("restore body: %+v", put)
			}
			size, err := b.DeleteSnapshot("i", "s2")
			if err != nil || size != 20 {
				t.Fatalf("delete: %d %v", size, err)
			}
			if _, err := b.DeleteSnapshot("i", "nope"); !errors.Is(err, compute.ErrNotFound) {
				t.Errorf("missing snapshot: %v", err)
			}
			if err := b.RevertSnapshot("i", "nope"); !errors.Is(err, compute.ErrNotFound) {
				t.Errorf("missing revert: %v", err)
			}
		})
	}
}

func TestMemorySnapshotRequiresRunning(t *testing.T) {
	m := &clusterMock{members: twoMembers(), instances: map[string]api.Instance{"i": inst("i", "container", "Stopped", "a")}}
	b := m.start(t)
	_, err := b.CreateSnapshot("i", models.CreateSnapshotRequest{Name: "s", Memory: true})
	if !errors.Is(err, compute.ErrMemorySnapshotRequiresRunning) {
		t.Fatalf("got %v", err)
	}
}

func TestSnapshotOperationFailure(t *testing.T) {
	m := &clusterMock{members: twoMembers(), opStatus: "Failure", opErr: "disk full",
		instances: map[string]api.Instance{"i": inst("i", "container", "Stopped", "a")}}
	b := m.start(t)
	if _, err := b.CreateSnapshot("i", models.CreateSnapshotRequest{Name: "s"}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("got %v", err)
	}
}
