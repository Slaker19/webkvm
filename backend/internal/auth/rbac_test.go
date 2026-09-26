package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireRole_AllowsMatchingRole(t *testing.T) {
	mw := RequireRole("admin")
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(HeaderRole, "admin")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if !called {
		t.Error("downstream not called")
	}
}

func TestRequireRole_RejectsWrongRole(t *testing.T) {
	mw := RequireRole("admin")
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(HeaderRole, "viewer")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
	if called {
		t.Error("downstream called despite wrong role")
	}
}

func TestRequireRole_MissingHeader_401(t *testing.T) {
	mw := RequireRole("admin")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("GET", "/x", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestRequireRole_AllowsAnyOf(t *testing.T) {
	mw := RequireRole("admin", "operator")
	for _, role := range []string{"admin", "operator"} {
		t.Run(role, func(t *testing.T) {
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("GET", "/x", nil)
			req.Header.Set(HeaderRole, role)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Errorf("role %s: status = %d, want 200", role, rr.Code)
			}
		})
	}
}

func TestRequireAtLeast_Admin(t *testing.T) {
	mw := RequireAtLeast("admin")
	cases := []struct {
		role     string
		wantCode int
	}{
		{"admin", http.StatusOK},
		{"operator", http.StatusForbidden},
		{"viewer", http.StatusForbidden},
		{"", http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("GET", "/x", nil)
			if tc.role != "" {
				req.Header.Set(HeaderRole, tc.role)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Errorf("role %q: status = %d, want %d", tc.role, rr.Code, tc.wantCode)
			}
		})
	}
}

func TestRequireAtLeast_Operator(t *testing.T) {
	mw := RequireAtLeast("operator")
	cases := []struct {
		role     string
		wantCode int
	}{
		{"admin", http.StatusOK},
		{"operator", http.StatusOK},
		{"viewer", http.StatusForbidden},
	}
	for _, tc := range cases {
		h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set(HeaderRole, tc.role)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != tc.wantCode {
			t.Errorf("role %q: status = %d, want %d", tc.role, rr.Code, tc.wantCode)
		}
	}
}

func TestRequireAtLeast_Viewer(t *testing.T) {
	mw := RequireAtLeast("viewer")
	for _, role := range []string{"admin", "operator", "viewer"} {
		t.Run(role, func(t *testing.T) {
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest("GET", "/x", nil)
			req.Header.Set(HeaderRole, role)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Errorf("role %s: status = %d, want 200", role, rr.Code)
			}
		})
	}
}

func TestRequireAtLeast_UnknownRoleTreatedAsExact(t *testing.T) {
	// An unknown role ("guest") expands to ["guest"] exactly, so
	// only "guest" passes.
	mw := RequireAtLeast("guest")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set(HeaderRole, "guest")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("guest: status = %d, want 200", rr.Code)
	}

	req2 := httptest.NewRequest("GET", "/x", nil)
	req2.Header.Set(HeaderRole, "admin")
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusForbidden {
		t.Errorf("admin: status = %d, want 403", rr2.Code)
	}
}
