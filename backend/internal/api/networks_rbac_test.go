package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/auth"
	"webkvm/internal/models"
)

// TestNetworksRBAC_RouteMapping verifies network route RBAC:
// read and lease inspection are available to authenticated users,
// lifecycle ops (start/stop/release-lease) require operator,
// and host mutations (create/update/delete) are admin-only.
func TestNetworksRBAC_RouteMapping(t *testing.T) {
	okStub := func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }

	newMux := func() *chi.Mux {
		r := chi.NewRouter()
		r.Route("/api/networks", func(r chi.Router) {
			r.Get("/", okStub)
			r.Get("/{id}/leases", okStub)
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireAtLeast("operator"))
				r.Post("/{id}/start", okStub)
				r.Post("/{id}/stop", okStub)
				r.Delete("/{id}/leases/{mac}", okStub)
			})
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(models.RoleAdmin))
				r.Post("/", okStub)
				r.Put("/{id}", okStub)
				r.Delete("/{id}", okStub)
			})
		})
		return r
	}

	mux := newMux()
	operator := http.Header{"X-User": {"op"}, "X-Role": {"operator"}}
	admin := http.Header{"X-User": {"root"}, "X-Role": {"admin"}}

	cases := []struct {
		name       string
		method     string
		path       string
		hdr        http.Header
		wantStatus int
	}{
		{"operator GET networks", http.MethodGet, "/api/networks", operator, http.StatusOK},
		{"operator GET leases", http.MethodGet, "/api/networks/vmbr0/leases", operator, http.StatusOK},
		{"operator POST start", http.MethodPost, "/api/networks/vmbr0/start", operator, http.StatusOK},
		{"operator POST stop", http.MethodPost, "/api/networks/vmbr0/stop", operator, http.StatusOK},
		{"operator DELETE lease", http.MethodDelete, "/api/networks/vmbr0/leases/aa:bb:cc:dd:ee:ff", operator, http.StatusOK},
		{"operator POST create network -> 403", http.MethodPost, "/api/networks", operator, http.StatusForbidden},
		{"operator PUT update network -> 403", http.MethodPut, "/api/networks/vmbr0", operator, http.StatusForbidden},
		{"operator DELETE network -> 403", http.MethodDelete, "/api/networks/vmbr0", operator, http.StatusForbidden},
		{"admin POST create network", http.MethodPost, "/api/networks", admin, http.StatusOK},
		{"admin PUT update network", http.MethodPut, "/api/networks/vmbr0", admin, http.StatusOK},
		{"admin DELETE network", http.MethodDelete, "/api/networks/vmbr0", admin, http.StatusOK},
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

func TestValidateNetworkName(t *testing.T) {
	valid := []string{"vmbr0", "br0", "dmz.1", "guest-net_2", "a", "123456789012345"}
	for _, n := range valid {
		if err := validateNetworkName(n); err != nil {
			t.Errorf("validateNetworkName(%q) = %v, want nil", n, err)
		}
	}
	invalid := []string{
		"", "x;id", "a$b", "a'b", "a b", "a/b", "a\\b", "a|b", "a&b",
		"..", ".", "virbr0", "docker0", "1234567890123456",
	}
	for _, n := range invalid {
		if err := validateNetworkName(n); err == nil {
			t.Errorf("validateNetworkName(%q) = nil, want error", n)
		}
	}
}

func TestValidateNetworkCIDR(t *testing.T) {
	good := []string{"", "10.9.9.0/24", "192.168.1.0/24", "2001:db8::/32"}
	for _, c := range good {
		if err := validateNetworkCIDR(c); err != nil {
			t.Errorf("validateNetworkCIDR(%q) = %v, want nil", c, err)
		}
	}
	bad := []string{"10.9.9.0/24;id", "10.9.9.0/33", "nope", "10.0.0.1", "10.0.0.0/24x"}
	for _, c := range bad {
		if err := validateNetworkCIDR(c); err == nil {
			t.Errorf("validateNetworkCIDR(%q) = nil, want error", c)
		}
	}
}
