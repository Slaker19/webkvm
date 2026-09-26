package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/auth"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
	"webkvm/internal/user"

	"github.com/go-chi/chi/v5"
)

// viewerBackend answers every metadata lookup with the test user as the
// owner, so requireVMAccess always passes. Any 403 seen in these tests
// therefore comes from a role or capability gate, never from ownership.
type viewerBackend struct {
	compute.Backend
	owner string
}

func (v *viewerBackend) GetVMMeta(string) (models.VMMeta, error) {
	return models.VMMeta{OwnerID: v.owner}, nil
}

func (v *viewerBackend) ListDomains() ([]models.VM, error) {
	return []models.VM{{Name: "vm1"}}, nil
}

func (v *viewerBackend) GetVNCInfo(string) (compute.GraphicsInfo, error) {
	return compute.GraphicsInfo{}, nil
}

func (v *viewerBackend) GetDomain(string) (models.VM, error) {
	return models.VM{Name: "vm1"}, nil
}

func (v *viewerBackend) GetDomainIP(string) string {
	return "192.168.1.100"
}

// liveRouter builds the router that actually ships, wired to a real user
// store holding one account. Walking the route table is not enough here:
// what broke in production was the ORDER of middlewares, and only
// sending a request through the real chain can observe that.
func liveRouter(t *testing.T, username, role string, perms *models.UserPermissions) (*chi.Mux, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")

	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	if _, err := us.Create(models.CreateUserRequest{
		Username:    username,
		Password:    "Str0ng-Pass#2026",
		Role:        role,
		Permissions: perms,
	}); err != nil {
		t.Fatalf("create %s: %v", username, err)
	}

	authMgr := auth.NewManager("router-viewer-console-test-secret", nil)
	t.Cleanup(authMgr.Close)

	token, _, err := authMgr.GenerateToken(username, role)
	if err != nil {
		t.Fatalf("token: %v", err)
	}

	r := NewRouter(
		&config.Config{DataDir: dir},
		nil, &viewerBackend{owner: username}, authMgr, nil, nil, us, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
	)
	return r, token
}

func liveStatus(t *testing.T, r *chi.Mux, token, method, path string) int {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr.Code
}

// A viewer granted console must actually reach the console routes.
//
// This is the support-technician case the product explicitly supports:
// read-only everywhere, but allowed to look at a screen. The console
// group was nested inside RequireAtLeast("operator"), so the role gate
// answered 403 before requireCapability ever ran and the grant was
// unreachable — the checkbox existed but could never take effect.
func TestRouter_ViewerWithConsoleReachesConsoleRoutes(t *testing.T) {
	r, token := liveRouter(t, "viewuser", models.RoleViewer,
		&models.UserPermissions{CanConsole: boolTrue()})

	for _, rt := range []capRoute{
		{http.MethodGet, "/api/vms/vm1/graphics"},
		{http.MethodPost, "/api/vms/vm1/console-ticket"},
		{http.MethodPost, "/api/vms/vm1/vnc-ticket"},
		{http.MethodGet, "/api/vms/vm1/rdp"},
		{http.MethodGet, "/api/vms/vm1/spice"},
	} {
		t.Run(rt.path, func(t *testing.T) {
			if got := liveStatus(t, r, token, rt.method, rt.path); got == http.StatusForbidden {
				t.Fatalf("%s %s = 403: a viewer with can_console=true must pass the gates; "+
					"a role gate is shadowing the console capability", rt.method, rt.path)
			}
		})
	}
}

// The mirror image: console denied must still be denied for a viewer.
// Without this, "fixing" the above by dropping the gate entirely would
// go unnoticed.
func TestRouter_ViewerWithoutConsoleIsDenied(t *testing.T) {
	r, token := liveRouter(t, "viewuser", models.RoleViewer,
		&models.UserPermissions{CanConsole: boolFalse()})

	for _, rt := range []capRoute{
		{http.MethodGet, "/api/vms/vm1/graphics"},
		{http.MethodPost, "/api/vms/vm1/vnc-ticket"},
		{http.MethodGet, "/api/vms/vm1/spice"},
	} {
		t.Run(rt.path, func(t *testing.T) {
			if got := liveStatus(t, r, token, rt.method, rt.path); got != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403: can_console=false must block a viewer",
					rt.method, rt.path, got)
			}
		})
	}
}

// Moving the console group out of the operator gate must not hand
// viewers anything else. State-changing routes stay operator-only.
func TestRouter_ViewerStillCannotMutateVMs(t *testing.T) {
	r, token := liveRouter(t, "viewuser", models.RoleViewer,
		&models.UserPermissions{CanConsole: boolTrue()})

	for _, rt := range []capRoute{
		{http.MethodPost, "/api/vms/vm1/start"},
		{http.MethodPost, "/api/vms/vm1/shutdown"},
		{http.MethodDelete, "/api/vms/vm1"},
		{http.MethodPost, "/api/vms/vm1/snapshots"},
		{http.MethodPut, "/api/vms/vm1/meta"},
		{http.MethodGet, "/api/vms/vm1/export"},
	} {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			if got := liveStatus(t, r, token, rt.method, rt.path); got != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403: a viewer must not change or export a VM",
					rt.method, rt.path, got)
			}
		})
	}
}

// An operator with console explicitly denied must still be blocked,
// through the real chain rather than the stand-in mux.
func TestRouter_OperatorConsoleDenialHoldsInRealRouter(t *testing.T) {
	r, token := liveRouter(t, "opuser", models.RoleOperator,
		&models.UserPermissions{CanConsole: boolFalse()})

	if got := liveStatus(t, r, token, http.MethodPost, "/api/vms/vm1/vnc-ticket"); got != http.StatusForbidden {
		t.Fatalf("POST /vnc-ticket = %d, want 403 for an operator with can_console=false", got)
	}
}
