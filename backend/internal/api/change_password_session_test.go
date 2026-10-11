package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/auth"
	"webkvm/internal/config"
	"webkvm/internal/user"
)

// #17: changing your own password closes every other session (epoch bump)
// but re-issues a fresh, valid token + cookie to the caller.
func TestChangeMyPassword_BumpsEpochAndReissuesSession(t *testing.T) {
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")
	us, err := user.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := auth.NewManager("test-secret-chpw", nil)
	mgr.SetSecureCookies(false)
	t.Cleanup(mgr.Close)
	h := &Handler{auth: mgr, userStore: us, loginLimiter: auth.NewLoginRateLimiter(), cfg: &config.Config{}}

	before, _ := us.Get("admin")
	oldTok, _, err := mgr.GenerateTokenWithMustChange("admin", before.Role, false, before.SessionEpoch)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPut, "/api/users/me/password",
		strings.NewReader(`{"old_password":"Str0ng-Pass#2026","new_password":"An0ther-Str0ng#2026"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", before.Role)
	req.Header.Set("Authorization", "Bearer "+oldTok)
	rr := httptest.NewRecorder()
	h.ChangeMyPassword(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	after, _ := us.Get("admin")
	if after.SessionEpoch != before.SessionEpoch+1 {
		t.Fatalf("epoch %d -> %d, want +1", before.SessionEpoch, after.SessionEpoch)
	}
	if _, err := mgr.ValidateToken(oldTok); err == nil {
		t.Fatal("old token must be revoked")
	}
	var fresh string
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			fresh = c.Value
		}
	}
	if fresh == "" {
		t.Fatal("no session cookie re-issued")
	}
	claims, err := mgr.ValidateToken(fresh)
	if err != nil {
		t.Fatalf("fresh token invalid: %v", err)
	}
	if claims.SessionEpoch != after.SessionEpoch || claims.Username != "admin" {
		t.Fatalf("fresh token claims = %+v", claims)
	}
}
