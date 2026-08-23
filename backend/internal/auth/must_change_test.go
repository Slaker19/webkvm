package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMustChangeEnforcer_BlocksWhenFlagTrue(t *testing.T) {
	lookup := func(username string) (bool, error) {
		if username == "alice" {
			return true, nil
		}
		return false, nil
	}
	mw := MustChangeEnforcer(lookup, "/api/auth/", "/api/users/me/password")

	hit := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	req.Header.Set(HeaderUser, "alice")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if hit {
		t.Fatalf("downstream should NOT be hit for user with must_change=true")
	}
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(body["error"], "password change") {
		t.Fatalf("expected error to mention password change, got %q", body["error"])
	}
}

func TestMustChangeEnforcer_AllowsWhenFlagFalse(t *testing.T) {
	lookup := func(username string) (bool, error) {
		return false, nil
	}
	mw := MustChangeEnforcer(lookup)

	hit := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	req.Header.Set(HeaderUser, "alice")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !hit {
		t.Fatalf("downstream should be hit for user with must_change=false")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestMustChangeEnforcer_AllowsExceptedPaths(t *testing.T) {
	lookup := func(username string) (bool, error) {
		return true, nil // always must change
	}
	mw := MustChangeEnforcer(lookup, "/api/auth/", "/api/users/me/password")

	allowedPaths := []string{
		"/api/auth/me",
		"/api/auth/refresh",
		"/api/auth/logout",
		"/api/users/me/password",
	}
	for _, p := range allowedPaths {
		t.Run(p, func(t *testing.T) {
			hit := false
			h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hit = true
			}))
			req := httptest.NewRequest(http.MethodGet, p, nil)
			req.Header.Set(HeaderUser, "alice")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if !hit {
				t.Fatalf("expected %s to be allowed even with must_change=true", p)
			}
		})
	}
}

func TestMustChangeEnforcer_PassesThroughUnauthenticated(t *testing.T) {
	lookup := func(username string) (bool, error) {
		t.Fatalf("lookup should NOT be called for unauthenticated requests")
		return false, nil
	}
	mw := MustChangeEnforcer(lookup)

	hit := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	// No X-User header set: auth middleware has not run yet.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !hit {
		t.Fatalf("unauthenticated request should pass through; auth middleware will reject it")
	}
}

func TestMustChangeEnforcer_LookupErrorFailsClosed(t *testing.T) {
	lookup := func(username string) (bool, error) {
		return false, errSentinel
	}
	mw := MustChangeEnforcer(lookup)

	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("downstream should NOT be hit when lookup errors")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	req.Header.Set(HeaderUser, "alice")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 on lookup error (fail-closed), got %d", rr.Code)
	}
}

type sentinelErr string

func (s sentinelErr) Error() string { return string(s) }

const errSentinel sentinelErr = "store broken"
