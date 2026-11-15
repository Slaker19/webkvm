package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

func TestEveryVMRouteHasARemoteClass(t *testing.T) {
	h := &Handler{}
	mux := chi.NewRouter()
	h.mountVMRoutes(mux, modeLocal)
	seen := map[string]bool{}
	counts := map[routeClass]int{}
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		key := method + " " + route
		seen[key] = true
		c, ok := vmRouteClasses[key]
		if !ok {
			t.Errorf("route %q has no remote class (A/B/L)", key)
		}
		counts[c]++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for k := range vmRouteClasses {
		if !seen[k] {
			t.Errorf("class table entry %q matches no mounted route", k)
		}
	}
	t.Logf("route classes: A=%d B=%d L=%d", counts[classA], counts[classB], counts[classL])
}

func TestUnclassifiedRemoteRouteIs501(t *testing.T) {
	h := &Handler{remote: &remoteTarget{node: nodes.Node{ID: "n1", Name: "n1"}}}
	mux := chi.NewRouter()
	cr := &classRouter{Router: mux, h: h}
	cr.Get("/not-in-table", func(http.ResponseWriter, *http.Request) { t.Error("handler must not run") })
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/not-in-table", nil))
	if res.Code != http.StatusNotImplemented || !strings.Contains(res.Body.String(), "remote_unsupported") {
		t.Fatalf("got %d %s", res.Code, res.Body.String())
	}
}

type dispatchFixture struct {
	h      *Handler
	router http.Handler
	remote nodes.Node
	nextOK *bool
	nextQ  *string
	fail   *error
}

func newDispatchFixture(t *testing.T) dispatchFixture {
	t.Helper()
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := reg.Create("remote", "qemu+ssh://user@example.test/system")
	if err != nil {
		t.Fatal(err)
	}
	pool := libvirt.NewPool(func(n nodes.Node) (string, error) { return n.URI, nil })
	t.Cleanup(pool.Close)
	var failErr error
	pool.SetConnectFunc(func(_ context.Context, uri string) (*libvirt.Connector, error) {
		if failErr != nil {
			return nil, failErr
		}
		return libvirt.NewTestConnector(uri), nil
	}, func(*libvirt.Connector) bool { return true })
	h := &Handler{nodes: reg, libvirtPool: pool}
	nextOK, nextQ := new(bool), new(string)
	r := chi.NewRouter()
	r.Route("/api/vms/{id}", func(r chi.Router) {
		r.Use(h.nodeDispatch)
		r.Use(func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				*nextOK, *nextQ = true, req.URL.RawQuery
				w.WriteHeader(http.StatusOK)
			})
		})
		h.mountVMRoutes(r, modeLocal)
	})
	return dispatchFixture{h: h, router: r, remote: remote, nextOK: nextOK, nextQ: nextQ, fail: &failErr}
}

func (f dispatchFixture) do(method, path, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Role", role)
	res := httptest.NewRecorder()
	f.router.ServeHTTP(res, req)
	return res
}

func decode(t *testing.T, res *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	m := map[string]string{}
	_ = json.Unmarshal(res.Body.Bytes(), &m)
	return m
}

func TestNodeDispatchRemoteLocalOnlyRouteIs501(t *testing.T) {
	f := newDispatchFixture(t)
	res := f.do(http.MethodGet, "/api/vms/vm1/logs?node=remote&hv=kvm", models.RoleAdmin)
	b := decode(t, res)
	if res.Code != http.StatusNotImplemented || b["code"] != "remote_unsupported" || b["hint"] != "open_node_ui" || b["node"] != "remote" {
		t.Fatalf("got %d %v", res.Code, b)
	}
}

func TestNodeDispatchOrderingAndDisabled(t *testing.T) {
	f := newDispatchFixture(t)
	// 403 before 404, for any non-admin.
	for _, role := range []string{models.RoleOperator, ""} {
		for _, n := range []string{"remote", "ghost"} {
			if res := f.do(http.MethodGet, "/api/vms/vm1/logs?node="+n, role); res.Code != http.StatusForbidden {
				t.Errorf("role %q node %s: %d", role, n, res.Code)
			}
		}
	}
	if res := f.do(http.MethodGet, "/api/vms/vm1/logs?node=ghost", models.RoleAdmin); res.Code != http.StatusNotFound {
		t.Errorf("unknown node: %d", res.Code)
	}
	off := false
	if _, err := f.h.nodes.Update(f.remote.ID, "", "", &off); err != nil {
		t.Fatal(err)
	}
	res := f.do(http.MethodGet, "/api/vms/vm1/logs?node=remote", models.RoleAdmin)
	if res.Code != http.StatusNotFound || decode(t, res)["code"] != "node_disabled" {
		t.Errorf("disabled: %d %s", res.Code, res.Body.String())
	}
}

func TestNodeDispatchPoolErrorsAreTyped(t *testing.T) {
	f := newDispatchFixture(t)
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{libvirt.ErrSSHAuthDenied, 502, "ssh_auth_denied"},
		{libvirt.ErrHostKeyUnpinned, 409, "host_key_unpinned"},
		{libvirt.ErrHostKeyChanged, 409, "host_key_changed"},
		{libvirt.ErrTransportUnsupported, 501, "transport_unsupported"},
		{errors.New("ssh: secret stderr 10.1.2.3 permission denied (publickey)"), 502, "ssh_auth_denied"},
	} {
		f.h.invalidateNode(f.remote.ID)
		*f.fail = tc.err
		res := f.do(http.MethodGet, "/api/vms/vm1/logs?node=remote", models.RoleAdmin)
		b := decode(t, res)
		if res.Code != tc.status || b["code"] != tc.code || b["node"] != "remote" {
			t.Errorf("%v: %d %v", tc.err, res.Code, b)
		}
		if strings.Contains(res.Body.String(), "secret") || strings.Contains(res.Body.String(), "10.1.2.3") {
			t.Errorf("raw stderr leaked: %s", res.Body.String())
		}
	}
}

func TestNodeDispatchIncusMemberNotInRegistryIsLocal(t *testing.T) {
	f := newDispatchFixture(t)
	res := f.do(http.MethodGet, "/api/vms/ct1?node=incus-member-2&hv=incus", models.RoleViewer)
	if res.Code != http.StatusOK || !*f.nextOK {
		t.Fatalf("member not resolved as local: %d %s", res.Code, res.Body.String())
	}
	if strings.Contains(*f.nextQ, "node=") || !strings.Contains(*f.nextQ, "hv=incus") {
		t.Errorf("query not normalized: %q", *f.nextQ)
	}
	// Same name as the registered KVM node but hv=incus: still the local member.
	*f.nextOK = false
	if res := f.do(http.MethodGet, "/api/vms/ct1?node=remote&hv=incus", models.RoleViewer); res.Code != 200 || !*f.nextOK {
		t.Errorf("hv=incus on KVM node name: %d", res.Code)
	}
	if res := f.do(http.MethodGet, "/api/vms/ct1?node=remote&hv=bogus", models.RoleAdmin); res.Code != http.StatusBadRequest {
		t.Errorf("bogus hv: %d", res.Code)
	}
}

func TestRemoteRouterCachedAndInvalidated(t *testing.T) {
	f := newDispatchFixture(t)
	c1 := libvirt.NewTestConnector("a")
	r1 := f.h.remoteRouter(f.remote, c1)
	if r2 := f.h.remoteRouter(f.remote, c1); r1 != r2 {
		t.Fatal("router rebuilt for the same node and connector")
	}
	if r3 := f.h.remoteRouter(f.remote, libvirt.NewTestConnector("b")); r3 == r1 {
		t.Fatal("router kept a stale connector")
	}
	r4 := f.h.remoteRouter(f.remote, c1)
	f.h.invalidateNode(f.remote.ID)
	if r5 := f.h.remoteRouter(f.remote, c1); r5 == r4 {
		t.Fatal("invalidate did not drop the router")
	}
}

func TestForBackendSharesState(t *testing.T) {
	h := &Handler{}
	c := h.forBackend(nil, nil, nodes.Node{ID: "x", Name: "x"})
	if c.deployState() != h.deployState() || c.haState() != h.haState() || c.routerCache() != h.routerCache() {
		t.Fatal("clone does not share state pointers")
	}
	if !c.isRemote() || h.isRemote() {
		t.Fatal("remote flag wrong")
	}
}

type fakeCluster struct {
	compute.Backend
	moved string
	err   error
}

func (f *fakeCluster) ClusterMembers() ([]compute.MemberInfo, error) { return nil, nil }
func (f *fakeCluster) MoveInstance(_ context.Context, _ string, t string, _ bool) error {
	f.moved = t
	return f.err
}
func (f *fakeCluster) EvacuateMember(context.Context, string, string) error { return f.err }
func (f *fakeCluster) RestoreMember(context.Context, string) error          { return f.err }
func (f *fakeCluster) InstanceLocation(string) (string, error)              { return "", nil }
func (f *fakeCluster) GetDomain(id string) (models.VM, error) {
	return models.VM{ID: id, Hypervisor: "incus"}, nil
}

func TestMoveVMErrorMapping(t *testing.T) {
	fc := &fakeCluster{}
	h := &Handler{compute: compute.NewCombined(fc, fc)}
	for _, tc := range []struct {
		err  error
		code int
	}{
		{nil, 200}, {compute.ErrMemberNotFound, 404}, {compute.ErrMemberOffline, 409},
		{compute.ErrNeedsStop, 409}, {compute.ErrSameMember, 409}, {compute.ErrNotClustered, 409},
	} {
		fc.err = tc.err
		req := httptest.NewRequest(http.MethodPost, "/api/vms/ct1/move", strings.NewReader(`{"target_member":"m2","live":true}`))
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", "ct1")
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
		res := httptest.NewRecorder()
		h.MoveVM(res, req)
		if res.Code != tc.code {
			t.Errorf("%v: %d", tc.err, res.Code)
		}
	}
}

func TestWorkloadManagedPersistsAndListsDefaultTrue(t *testing.T) {
	dir := t.TempDir()
	s := newWorkloadStore(dir)
	if !s.Managed("n1", "kvm", "v1") {
		t.Fatal("default must be managed")
	}
	if err := s.SetManaged("n1", "kvm", "v1", false); err != nil {
		t.Fatal(err)
	}
	s2 := newWorkloadStore(dir)
	if s2.Managed("n1", "kvm", "v1") || !s2.Managed("n1", "incus", "v1") {
		t.Fatal("state not keyed/persisted by (node,hv,id)")
	}
	vms := []models.VM{{ID: "v1", NodeID: "n1", Hypervisor: "kvm"}, {ID: "v2", Hypervisor: "kvm"}}
	h := &Handler{workloads: s2}
	h.applyManaged(vms)
	if *vms[0].Managed || !*vms[1].Managed {
		t.Fatal("applyManaged wrong")
	}
}

func TestNodeDispatchRemoteUnsupportedCarriesUIURL(t *testing.T) {
	f := newDispatchFixture(t)
	res := f.do(http.MethodGet, "/api/vms/vm1/logs?node=remote&hv=kvm", models.RoleAdmin)
	if _, has := decode(t, res)["ui_url"]; has {
		t.Fatal("ui_url must be absent when unset")
	}
	u := "https://192.168.50.31:8080"
	if _, err := f.h.nodes.UpdateWithUI(f.remote.ID, "", "", &u, nil); err != nil {
		t.Fatal(err)
	}
	res = f.do(http.MethodGet, "/api/vms/vm1/logs?node=remote&hv=kvm", models.RoleAdmin)
	b := decode(t, res)
	if res.Code != http.StatusNotImplemented || b["ui_url"] != u || b["hint"] != "open_node_ui" {
		t.Fatalf("got %d %v", res.Code, b)
	}
}
