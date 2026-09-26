package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"webkvm/internal/auth"
	"webkvm/internal/config"

	"github.com/go-chi/chi/v5"
)

// realRouterRoutes walks the actual router built by NewRouter and returns
// the set of registered "METHOD path" pairs.
//
// The point of walking the real router rather than rebuilding a
// representative one is that a hand-rolled copy drifts: the capability
// gates could be added here and forgotten in router.go and every test
// would still pass. This asserts against the tree that ships.
func realRouterRoutes(t *testing.T) map[string]bool {
	t.Helper()
	authMgr := auth.NewManager("router-capability-test-secret", nil)
	t.Cleanup(authMgr.Close)

	r := NewRouter(
		&config.Config{DataDir: t.TempDir()},
		nil, nil, authMgr, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)

	out := map[string]bool{}
	if err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out[method+" "+strings.TrimSuffix(route, "/")] = true
		return nil
	}); err != nil {
		t.Fatalf("walk router: %v", err)
	}
	return out
}

// The per-VM backup routes must exist in the shipped router. Without
// them the 'backups' capability grants nothing at all: every other
// backup route is admin-only, so an operator holding the permission
// previously got a 403 everywhere it could have applied.
func TestRouter_PerVMBackupRoutesAreRegistered(t *testing.T) {
	routes := realRouterRoutes(t)
	want := []string{
		"GET /api/vms/{id}/backup/targets",
		"POST /api/vms/{id}/backup",
		"GET /api/vms/{id}/backup/jobs",
	}
	for _, w := range want {
		if !routes[w] {
			var have []string
			for r := range routes {
				if strings.Contains(r, "backup") {
					have = append(have, r)
				}
			}
			sort.Strings(have)
			t.Errorf("route %q is not registered; backup routes present:\n  %s",
				w, strings.Join(have, "\n  "))
		}
	}
}

// The admin-only backup surface must stay admin-only. These endpoints
// expose fleet-wide destinations along with their hosts, paths and
// usernames, and running one backs up whatever its filter selects, so
// they must not be relaxed to the 'backups' capability.
func TestRouter_AdminBackupSurfaceStillExists(t *testing.T) {
	routes := realRouterRoutes(t)
	for _, w := range []string{
		"GET /api/backup/targets",
		"POST /api/backup/targets/{id}/run",
		"GET /api/backup/jobs",
	} {
		if !routes[w] {
			t.Errorf("admin backup route %q disappeared", w)
		}
	}
}

// Console and export routes must remain registered under /api/vms/{id}.
// This guards the refactor that moved them into capability groups: a
// mistyped path there would silently unregister the endpoint, which the
// capability tests (that use their own mux) could not notice.
func TestRouter_ConsoleAndExportRoutesAreRegistered(t *testing.T) {
	routes := realRouterRoutes(t)
	for _, w := range []string{
		"GET /api/vms/{id}/graphics",
		"GET /api/vms/{id}/vnc",
		"GET /api/vms/{id}/serial",
		"POST /api/vms/{id}/console-ticket",
		"POST /api/vms/{id}/vnc-ticket",
		"GET /api/vms/{id}/rdp",
		"GET /api/vms/{id}/spice",
		"POST /api/vms/{id}/clipboard",
		"GET /api/vms/{id}/export",
	} {
		if !routes[w] {
			t.Errorf("route %q is not registered", w)
		}
	}
}
