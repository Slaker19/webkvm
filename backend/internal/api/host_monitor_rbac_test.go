package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/auth"
	"webkvm/internal/models"
)

// TestHostMonitorRBAC_RouteMapping verifies host monitor route RBAC:
// basic host info is available to all authenticated users,
// but detailed monitoring (/monitor, /processes) requires operator role.
func TestHostMonitorRBAC_RouteMapping(t *testing.T) {
	okStub := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

	newMux := func() *chi.Mux {
		r := chi.NewRouter()
		r.Route("/api/host", func(r chi.Router) {
			// Basic host info - available to all authenticated users
			r.Get("/", okStub)
			r.Get("/stats", okStub)
			r.Get("/metrics", okStub)
			r.Get("/interfaces", okStub)
			r.Get("/disks", okStub)
			r.Get("/capabilities", okStub)

			// Detailed monitoring - requires operator role
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireAtLeast("operator"))
				r.Get("/monitor", okStub)
				r.Get("/processes", okStub)
			})

			// Device enumeration - requires operator role
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireAtLeast("operator"))
				r.Get("/usb-devices", okStub)
				r.Get("/pci-devices", okStub)
			})

			// Host mutations - admin only
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(models.RoleAdmin))
				r.Post("/disks/wipe", okStub)
				r.Post("/raid", okStub)
				// Docker container/image management - admin only
				r.Post("/docker/containers/{id}/{action}", okStub)
				r.Delete("/docker/containers/{id}", okStub)
				r.Delete("/docker/images/{id}", okStub)
				r.Post("/docker/images/prune", okStub)
			})
		})
		return r
	}

	mux := newMux()
	viewer := http.Header{"X-User": {"viewer"}, "X-Role": {"viewer"}}
	operator := http.Header{"X-User": {"op"}, "X-Role": {"operator"}}
	admin := http.Header{"X-User": {"root"}, "X-Role": {"admin"}}

	cases := []struct {
		name       string
		method     string
		path       string
		hdr        http.Header
		wantStatus int
	}{
		// Basic host info - all roles can access
		{"viewer GET host info", http.MethodGet, "/api/host", viewer, http.StatusOK},
		{"viewer GET host stats", http.MethodGet, "/api/host/stats", viewer, http.StatusOK},
		{"viewer GET host metrics", http.MethodGet, "/api/host/metrics", viewer, http.StatusOK},
		{"viewer GET interfaces", http.MethodGet, "/api/host/interfaces", viewer, http.StatusOK},
		{"viewer GET disks", http.MethodGet, "/api/host/disks", viewer, http.StatusOK},
		{"viewer GET capabilities", http.MethodGet, "/api/host/capabilities", viewer, http.StatusOK},

		// Detailed monitoring - viewer cannot access
		{"viewer GET monitor -> 403", http.MethodGet, "/api/host/monitor", viewer, http.StatusForbidden},
		{"viewer GET processes -> 403", http.MethodGet, "/api/host/processes", viewer, http.StatusForbidden},

		// Detailed monitoring - operator can access
		{"operator GET monitor", http.MethodGet, "/api/host/monitor", operator, http.StatusOK},
		{"operator GET processes", http.MethodGet, "/api/host/processes", operator, http.StatusOK},

		// Detailed monitoring - admin can access
		{"admin GET monitor", http.MethodGet, "/api/host/monitor", admin, http.StatusOK},
		{"admin GET processes", http.MethodGet, "/api/host/processes", admin, http.StatusOK},

		// Device enumeration - viewer cannot access
		{"viewer GET usb-devices -> 403", http.MethodGet, "/api/host/usb-devices", viewer, http.StatusForbidden},
		{"viewer GET pci-devices -> 403", http.MethodGet, "/api/host/pci-devices", viewer, http.StatusForbidden},

		// Device enumeration - operator can access
		{"operator GET usb-devices", http.MethodGet, "/api/host/usb-devices", operator, http.StatusOK},
		{"operator GET pci-devices", http.MethodGet, "/api/host/pci-devices", operator, http.StatusOK},

		// Host mutations - operator cannot access
		{"operator POST disks/wipe -> 403", http.MethodPost, "/api/host/disks/wipe", operator, http.StatusForbidden},
		{"operator POST raid -> 403", http.MethodPost, "/api/host/raid", operator, http.StatusForbidden},
		{"operator POST docker container start -> 403", http.MethodPost, "/api/host/docker/containers/abc123/start", operator, http.StatusForbidden},
		{"operator DELETE docker container -> 403", http.MethodDelete, "/api/host/docker/containers/abc123", operator, http.StatusForbidden},
		{"operator DELETE docker image -> 403", http.MethodDelete, "/api/host/docker/images/sha256:abc", operator, http.StatusForbidden},
		{"operator POST docker images prune -> 403", http.MethodPost, "/api/host/docker/images/prune", operator, http.StatusForbidden},

		// Host mutations - viewer cannot access
		{"viewer POST disks/wipe -> 403", http.MethodPost, "/api/host/disks/wipe", viewer, http.StatusForbidden},
		{"viewer POST raid -> 403", http.MethodPost, "/api/host/raid", viewer, http.StatusForbidden},
		{"viewer POST docker container start -> 403", http.MethodPost, "/api/host/docker/containers/abc123/start", viewer, http.StatusForbidden},
		{"viewer DELETE docker container -> 403", http.MethodDelete, "/api/host/docker/containers/abc123", viewer, http.StatusForbidden},
		{"viewer DELETE docker image -> 403", http.MethodDelete, "/api/host/docker/images/sha256:abc", viewer, http.StatusForbidden},
		{"viewer POST docker images prune -> 403", http.MethodPost, "/api/host/docker/images/prune", viewer, http.StatusForbidden},

		// Host mutations - admin can access
		{"admin POST disks/wipe", http.MethodPost, "/api/host/disks/wipe", admin, http.StatusOK},
		{"admin POST raid", http.MethodPost, "/api/host/raid", admin, http.StatusOK},
		{"admin POST docker container start", http.MethodPost, "/api/host/docker/containers/abc123/start", admin, http.StatusOK},
		{"admin POST docker container stop", http.MethodPost, "/api/host/docker/containers/abc123/stop", admin, http.StatusOK},
		{"admin POST docker container restart", http.MethodPost, "/api/host/docker/containers/abc123/restart", admin, http.StatusOK},
		{"admin DELETE docker container", http.MethodDelete, "/api/host/docker/containers/abc123", admin, http.StatusOK},
		{"admin DELETE docker image", http.MethodDelete, "/api/host/docker/images/sha256:abc", admin, http.StatusOK},
		{"admin POST docker images prune", http.MethodPost, "/api/host/docker/images/prune", admin, http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			for k, vs := range tc.hdr {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, rr.Code)
			}
		})
	}
}
