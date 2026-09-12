package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionEnforcer_BlocksWhenFlagTrue(t *testing.T) {
	lookup := func(username string) (bool, int, error) {
		if username == "alice" {
			return true, 0, nil
		}
		return false, 0, nil
	}
	mw := SessionEnforcer(lookup, "/api/auth/", "/api/users/me/password")

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

func TestSessionEnforcer_AllowsWhenFlagFalse(t *testing.T) {
	lookup := func(username string) (bool, int, error) {
		return false, 0, nil
	}
	mw := SessionEnforcer(lookup)

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

func TestSessionEnforcer_AllowsExceptedPaths(t *testing.T) {
	lookup := func(username string) (bool, int, error) {
		return true, 0, nil // always must change
	}
	mw := SessionEnforcer(lookup, "/api/auth/", "/api/users/me/password")

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

func TestSessionEnforcer_PassesThroughUnauthenticated(t *testing.T) {
	lookup := func(username string) (bool, int, error) {
		t.Fatalf("lookup should NOT be called for unauthenticated requests")
		return false, 0, nil
	}
	mw := SessionEnforcer(lookup)

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

func TestSessionEnforcer_LookupErrorFailsClosed(t *testing.T) {
	lookup := func(username string) (bool, int, error) {
		return false, 0, errSentinel
	}
	mw := SessionEnforcer(lookup)

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

// --- Session-epoch (forced logout) tests ---

func TestSessionEnforcer_AllowsMatchingEpoch(t *testing.T) {
	lookup := func(username string) (bool, int, error) {
		return false, 3, nil
	}
	mw := SessionEnforcer(lookup)

	hit := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	req.Header.Set(HeaderUser, "alice")
	req.Header.Set(HeaderTokenEpoch, "3")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !hit {
		t.Fatalf("downstream should be hit when token epoch matches the account's current epoch")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestSessionEnforcer_BlocksStaleEpoch(t *testing.T) {
	lookup := func(username string) (bool, int, error) {
		return false, 2, nil // account was bumped to epoch 2 since this token was issued
	}
	mw := SessionEnforcer(lookup)

	hit := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	req.Header.Set(HeaderUser, "alice")
	req.Header.Set(HeaderTokenEpoch, "1")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if hit {
		t.Fatalf("downstream should NOT be hit for a stale (revoked) session epoch")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if !strings.Contains(body["error"], "revoked") {
		t.Fatalf("expected error to mention the revoked session, got %q", body["error"])
	}
}

func TestSessionEnforcer_MissingEpochHeaderReadsAsZero(t *testing.T) {
	// A ticket/API-token-authenticated request never carries
	// HeaderTokenEpoch (no JWT claims flow through that path). It must
	// read as epoch 0 — matching every pre-epoch-feature token — so it
	// is only ever rejected once the account has actually been revoked.
	lookup := func(username string) (bool, int, error) {
		return false, 0, nil
	}
	mw := SessionEnforcer(lookup)

	hit := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	req.Header.Set(HeaderUser, "alice")
	// HeaderTokenEpoch deliberately not set.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !hit {
		t.Fatalf("a request with no epoch header should pass when the account is still at epoch 0")
	}
}

type sentinelErr string

func (s sentinelErr) Error() string { return string(s) }

const errSentinel sentinelErr = "store broken"
