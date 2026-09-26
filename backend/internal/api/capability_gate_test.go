package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/user"

	"github.com/go-chi/chi/v5"
)

// capBackend owns every VM it is asked about, so requireVMAccess always
// passes. That isolates the variable under test: any 403 observed here
// comes from a missing capability, never from VM access.
type capBackend struct {
	compute.Backend
	owner string
}

func (c *capBackend) GetVMMeta(id string) (models.VMMeta, error) {
	return models.VMMeta{OwnerID: c.owner}, nil
}

// capHandler builds a handler backed by a real user store containing one
// operator whose capabilities are set by perms.
func capHandler(t *testing.T, perms *models.UserPermissions) *Handler {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")

	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	if _, err := us.Create(models.CreateUserRequest{
		Username:    "op",
		Password:    "Str0ng-Pass#2026",
		Role:        models.RoleOperator,
		Permissions: perms,
	}); err != nil {
		t.Fatalf("create operator: %v", err)
	}
	return &Handler{userStore: us, compute: &capBackend{owner: "op"}}
}

// capMux mirrors the capability gating of the real /api/vms/{id} subtree
// in router.go. The handlers are stubs: what is under test is which gate
// each route sits behind, not what the handler does.
func capMux(h *Handler) *chi.Mux {
	ok := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }
	r := chi.NewRouter()
	r.Route("/api/vms/{id}", func(r chi.Router) {
		r.Use(h.requireVMOwnership)

		r.Group(func(r chi.Router) {
			r.Use(h.requireCapability("console"))
			r.Get("/graphics", ok)
			r.Get("/vnc", ok)
			r.Get("/serial", ok)
			r.Post("/console-ticket", ok)
			r.Post("/vnc-ticket", ok)
			r.Get("/rdp", ok)
			r.Get("/spice", ok)
			r.Post("/clipboard", ok)
		})
		r.With(h.requireCapability("export")).Get("/export", ok)
		r.Group(func(r chi.Router) {
			r.Use(h.requireCapability("backups"))
			r.Get("/backup/targets", ok)
			r.Post("/backup", ok)
			r.Get("/backup/jobs", ok)
		})
	})
	return r
}

type capRoute struct {
	method string
	path   string
}

// consoleRoutes is every route that hands the caller interactive control
// of a VM. Each one was reachable with can_console=false.
var consoleRoutes = []capRoute{
	{http.MethodGet, "/api/vms/vm1/graphics"},
	{http.MethodGet, "/api/vms/vm1/vnc"},
	{http.MethodGet, "/api/vms/vm1/serial"},
	{http.MethodPost, "/api/vms/vm1/console-ticket"},
	{http.MethodPost, "/api/vms/vm1/vnc-ticket"},
	{http.MethodGet, "/api/vms/vm1/rdp"},
	{http.MethodGet, "/api/vms/vm1/spice"},
	{http.MethodPost, "/api/vms/vm1/clipboard"},
}

func doCapRequest(t *testing.T, mux *chi.Mux, rt capRoute) int {
	t.Helper()
	req := httptest.NewRequest(rt.method, rt.path, nil)
	req.Header.Set("X-User", "op")
	req.Header.Set("X-Role", models.RoleOperator)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr.Code
}

// Denying console must deny every interactive path, even for a user who
// legitimately owns the VM. Owning a machine and being allowed to take
// over its screen, keyboard and clipboard are separate grants.
func TestCapability_ConsoleDenialBlocksEveryConsoleRoute(t *testing.T) {
	h := capHandler(t, &models.UserPermissions{CanConsole: boolFalse()})
	mux := capMux(h)

	for _, rt := range consoleRoutes {
		t.Run(rt.path, func(t *testing.T) {
			if got := doCapRequest(t, mux, rt); got != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403: can_console=false must block console access",
					rt.method, rt.path, got)
			}
		})
	}
}

// The same routes must keep working when console is granted, otherwise
// the fix would just be a blanket denial.
func TestCapability_ConsoleGrantAllowsEveryConsoleRoute(t *testing.T) {
	h := capHandler(t, &models.UserPermissions{CanConsole: boolTrue()})
	mux := capMux(h)

	for _, rt := range consoleRoutes {
		t.Run(rt.path, func(t *testing.T) {
			if got := doCapRequest(t, mux, rt); got != http.StatusOK {
				t.Fatalf("%s %s = %d, want 200: can_console=true must allow console access",
					rt.method, rt.path, got)
			}
		})
	}
}

// Exporting streams every byte of a VM's disks. It has its own gate, so
// denying export must block it even when console is granted.
func TestCapability_ExportIsSeparateFromConsole(t *testing.T) {
	h := capHandler(t, &models.UserPermissions{
		CanConsole: boolTrue(),
		CanExport:  boolFalse(),
	})
	rt := capRoute{http.MethodGet, "/api/vms/vm1/export"}
	if got := doCapRequest(t, capMux(h), rt); got != http.StatusForbidden {
		t.Fatalf("GET /export = %d, want 403: can_export=false must block disk export even with console granted", got)
	}

	h2 := capHandler(t, &models.UserPermissions{
		CanConsole: boolFalse(),
		CanExport:  boolTrue(),
	})
	if got := doCapRequest(t, capMux(h2), rt); got != http.StatusOK {
		t.Fatalf("GET /export = %d, want 200: can_export=true must allow export independently of console", got)
	}
}

// Per-VM backups are gated on the backups capability, which previously
// granted nothing at all because every backup route was admin-only.
func TestCapability_BackupsGatesPerVMBackupRoutes(t *testing.T) {
	routes := []capRoute{
		{http.MethodGet, "/api/vms/vm1/backup/targets"},
		{http.MethodPost, "/api/vms/vm1/backup"},
		{http.MethodGet, "/api/vms/vm1/backup/jobs"},
	}

	denied := capMux(capHandler(t, &models.UserPermissions{CanBackups: boolFalse()}))
	granted := capMux(capHandler(t, &models.UserPermissions{CanBackups: boolTrue()}))

	for _, rt := range routes {
		t.Run(rt.path, func(t *testing.T) {
			if got := doCapRequest(t, denied, rt); got != http.StatusForbidden {
				t.Fatalf("%s %s = %d, want 403 with can_backups=false", rt.method, rt.path, got)
			}
			if got := doCapRequest(t, granted, rt); got != http.StatusOK {
				t.Fatalf("%s %s = %d, want 200 with can_backups=true", rt.method, rt.path, got)
			}
		})
	}
}

// A handler with no user store must deny rather than fall open.
func TestCapability_NoUserStoreFailsClosed(t *testing.T) {
	h := &Handler{compute: &capBackend{owner: "op"}}
	mux := capMux(h)
	for _, rt := range consoleRoutes {
		if got := doCapRequest(t, mux, rt); got != http.StatusForbidden {
			t.Fatalf("%s %s = %d, want 403 when the user store is unavailable", rt.method, rt.path, got)
		}
	}
}

func boolTrue() *bool  { b := true; return &b }
func boolFalse() *bool { b := false; return &b }
