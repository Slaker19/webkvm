package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

func TestRemoteLibvirtRoutesRequireAdmin(t *testing.T) {
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Create("remote", "qemu+ssh://user@example.test/system"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{nodes: reg}
	called := false
	guard := h.rejectUnsupportedRemoteVMRoute(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	for _, role := range []string{models.RoleOperator, models.RoleViewer, ""} {
		for _, target := range []string{"remote", "no-such-node"} {
			called = false
			req := httptest.NewRequest(http.MethodPost, "/api/vms/vm-1/start?node="+target, nil)
			req.Header.Set("X-Role", role)
			res := httptest.NewRecorder()
			guard.ServeHTTP(res, req)
			if res.Code != http.StatusForbidden || called {
				t.Errorf("role %q node %q: status=%d called=%v, want 403", role, target, res.Code, called)
			}
		}
	}
	// A local VM addressed by the machine's own hostname is still local and
	// keeps the per-VM ACL path for non-admins.
	req := httptest.NewRequest(http.MethodGet, "/api/vms/vm-1?node="+localNodeName(), nil)
	req.Header.Set("X-Role", models.RoleOperator)
	res := httptest.NewRecorder()
	called = false
	guard.ServeHTTP(res, req)
	if res.Code != http.StatusOK || !called {
		t.Fatalf("local by hostname blocked: %d", res.Code)
	}
}

func migrateRequest(id, query, body, role string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(http.MethodPost, "/api/vms/"+id+"/migrate"+query, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Role", role)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	return httptest.NewRecorder(), req
}

func TestMigrateVMRemoteSourceToLocalIsAllowedAndSameNodeRejected(t *testing.T) {
	reg, _ := nodes.New(t.TempDir(), "qemu:///system")
	reg.Create("remote-a", "qemu+ssh://u@a.example/system")
	h := &Handler{nodes: reg}
	// remote -> local is no longer refused up front: with no pool wired it
	// reaches the orchestration step (503), not the old 400.
	res, req := migrateRequest("vm1", "?node=remote-a", `{"node":"local","live":true}`, models.RoleAdmin)
	h.MigrateVM(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Errorf("remote->local: status=%d body=%s", res.Code, res.Body.String())
	}
	// source == destination is a clear 400.
	res, req = migrateRequest("vm1", "?node=remote-a", `{"node":"remote-a","live":true}`, models.RoleAdmin)
	h.MigrateVM(res, req)
	if res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "same") {
		t.Errorf("same node: status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestMigrateVMRemoteInvolvementRequiresAdmin(t *testing.T) {
	reg, _ := nodes.New(t.TempDir(), "qemu:///system")
	reg.Create("remote-a", "qemu+ssh://u@a.example/system")
	reg.Create("remote-b", "qemu+ssh://u@b.example/system")
	// An operator that DOES hold control_power must still be refused.
	h, _ := setupSchedHandler(t, &fakeSchedBackend{owner: "op"}, &models.UserPermissions{CanControlPower: boolPtr(true)})
	h.nodes = reg
	for name, tc := range map[string]struct{ query, body string }{
		"remote->remote": {"?node=remote-a", `{"node":"remote-b","live":true}`},
		"local->remote":  {"", `{"node":"remote-b","live":true}`},
		"remote->local":  {"?node=remote-a", `{"node":"local","live":true}`},
	} {
		res, req := migrateRequest("vm1", tc.query, tc.body, models.RoleOperator)
		req.Header.Set("X-User", "op")
		h.MigrateVM(res, req)
		if res.Code != http.StatusForbidden || !strings.Contains(res.Body.String(), "administrators") {
			t.Errorf("%s: status=%d body=%s, want admin-only 403", name, res.Code, res.Body.String())
		}
	}
}

func TestLocalNodeCannotBeDisabled(t *testing.T) {
	reg, _ := nodes.New(t.TempDir(), "qemu:///system")
	off := false
	if _, err := reg.Update("local", "", "", &off); err == nil {
		t.Fatal("disabling the local node must be rejected")
	}
	on := true
	if _, err := reg.Update("local", "", "", &on); err != nil {
		t.Fatalf("enabling local: %v", err)
	}
}
