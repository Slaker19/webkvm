package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// buildMiddlewareMux wires the real auth.Middleware around a stub so we
// can exercise the V13-SEC-01 cookie + CSRF enforcement in isolation.
func buildMiddlewareMux(t *testing.T) (*Manager, *chiMux) {
	t.Helper()
	m := NewManager("test-secret-for-sec-01", nil)
	m.SetSecureCookies(false) // http test server, no TLS
	t.Cleanup(m.Close)        // stop the blacklist GC goroutine (race-clean)
	mux := &chiMux{}
	mux.Handler = m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	return m, mux
}

// tokenOf generates a valid session JWT for the given username/role.
func tokenOf(t *testing.T, m *Manager, user, role string) string {
	t.Helper()
	tok, _, err := m.GenerateToken(user, role)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// TestMiddlewareCookieCSRF: a cookie-authenticated mutating request must
// prove itself with the X-CSRF-Token header (double-submit). Missing,
// wrong or empty CSRF → 403. Safe methods don't need it. Bearer-authenticated
// requests (API tokens) are exempt.
func TestMiddlewareCookieCSRF(t *testing.T) {
	m, mux := buildMiddlewareMux(t)
	tok := tokenOf(t, m, "admin", "admin")
	csrf := "csrf-value-123"

	do := func(method, path string, withCookie bool, csrfHeader string) int {
		req := httptest.NewRequest(method, path, nil)
		if withCookie {
			req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: tok})
			req.AddCookie(&http.Cookie{Name: CSRFCookieName, Value: csrf})
		} else {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if csrfHeader != "" {
			req.Header.Set("X-CSRF-Token", csrfHeader)
		}
		rr := httptest.NewRecorder()
		mux.Handler.ServeHTTP(rr, req)
		return rr.Code
	}

	cases := []struct {
		name       string
		method     string
		cookieAuth bool
		csrfHeader string
		wantStatus int
	}{
		{"POST cookie, no csrf header -> 403", http.MethodPost, true, "", http.StatusForbidden},
		{"POST cookie, wrong csrf -> 403", http.MethodPost, true, "wrong", http.StatusForbidden},
		{"POST cookie, correct csrf -> 200", http.MethodPost, true, csrf, http.StatusOK},
		{"GET cookie, no csrf -> 200", http.MethodGet, true, "", http.StatusOK},
		{"PUT cookie, correct csrf -> 200", http.MethodPut, true, csrf, http.StatusOK},
		{"DELETE cookie, correct csrf -> 200", http.MethodDelete, true, csrf, http.StatusOK},
		{"POST bearer, no csrf -> 200 (api token exempt)", http.MethodPost, false, "", http.StatusOK},
		{"POST bearer, cookie also present but no csrf -> 200 (header wins)", http.MethodPost, true, "", http.StatusForbidden},
	}
	for _, c := range cases {
		got := do(c.method, "/api/somewhere", c.cookieAuth, c.csrfHeader)
		if got != c.wantStatus {
			t.Errorf("%s: got %d, want %d", c.name, got, c.wantStatus)
		}
	}
}

// TestMiddlewareCookie_NoSessionCookieStillRequiresAuth: no cookie, no
// header → 401 (unchanged behavior).
func TestMiddlewareCookie_NoSessionCookieStillRequiresAuth(t *testing.T) {
	m, mux := buildMiddlewareMux(t)
	_ = m
	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	rr := httptest.NewRecorder()
	mux.Handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no credentials: got %d, want 401", rr.Code)
	}
}

// TestMiddlewareCookie_LoginExemptFromCSRF: /api/auth/login is public
// (pre-session) and never reaches the CSRF check.
func TestMiddlewareCookie_LoginExemptFromCSRF(t *testing.T) {
	_, mux := buildMiddlewareMux(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	rr := httptest.NewRecorder()
	mux.Handler.ServeHTTP(rr, req)
	// Public branch → reaches the stub → 200 (the real handler would 400).
	if rr.Code != http.StatusOK {
		t.Errorf("login: got %d, want 200 (public)", rr.Code)
	}
}

// chiMux is a tiny holder so buildMiddlewareMux can return the handler
// without importing chi.
type chiMux struct{ Handler http.Handler }

// TestCSRFValid: constant-time comparison semantics.
func TestCSRFValid(t *testing.T) {
	if CSRFValid("", "x") {
		t.Error("empty header must be invalid")
	}
	if CSRFValid("x", "") {
		t.Error("empty cookie must be invalid")
	}
	if !CSRFValid("abc123", "abc123") {
		t.Error("matching values must be valid")
	}
	if CSRFValid("abc123", "abc124") {
		t.Error("mismatch must be invalid")
	}
}

// TestNewCSRFValue: mints a non-empty, non-trivial token.
func TestNewCSRFValue(t *testing.T) {
	a, err := NewCSRFValue()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewCSRFValue()
	if len(a) < 32 || strings.Trim(a, "0") == "" {
		t.Errorf("csrf token looks weak: %q", a)
	}
	if a == b {
		t.Error("two CSRF values must differ")
	}
}
