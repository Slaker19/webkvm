package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/auth"
	"webkvm/internal/config"
	"webkvm/internal/models"
	"webkvm/internal/user"
)

// TestLogin_SetsHttpOnlyCookieAndOmitsToken (V13-SEC-01): el login emite
// la sesión como cookie HttpOnly + SameSite=Lax y NO devuelve el JWT en el
// cuerpo JSON (el token nunca llega a JavaScript).
func TestLogin_SetsHttpOnlyCookieAndOmitsToken(t *testing.T) {
	dir := t.TempDir()
	// NewStore seeds the default admin with WEBKVM_ADMIN_PASSWORD (or a
	// random one); pin it so we can log in with a known password.
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")
	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = us // admin already seeded

	mgr := auth.NewManager("test-secret-sec01", nil)
	mgr.SetSecureCookies(false)
	t.Cleanup(mgr.Close)

	h := &Handler{
		auth:         mgr,
		userStore:    us,
		loginLimiter: auth.NewLoginRateLimiter(),
		cfg:          &config.Config{},
	}

	body := `{"username":"admin","password":"Str0ng-Pass#2026"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.Login(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200", rr.Code)
	}

	// The response body must NOT contain a JWT token.
	var resp models.LoginResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rr.Body.String(), `"token"`) {
		t.Error("response still ships the JWT to JavaScript")
	}
	if resp.Username != "admin" || resp.Role != string(models.RoleAdmin) {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.CSRF == "" {
		t.Error("login must return the CSRF value for the SPA")
	}

	// The session cookie must be HttpOnly + SameSite=Lax and carry a JWT.
	var session *http.Cookie
	var csrfCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			session = c
		}
		if c.Name == auth.CSRFCookieName {
			csrfCookie = c
		}
	}
	if session == nil {
		t.Fatal("session cookie not set")
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	if session.SameSite != http.SameSiteLaxMode {
		t.Error("session cookie must be SameSite=Lax")
	}
	if session.Value == "" || strings.Count(session.Value, ".") < 2 {
		t.Error("session cookie must carry a JWT")
	}
	if csrfCookie == nil || csrfCookie.Value != resp.CSRF {
		t.Error("CSRF cookie value must match the returned CSRF token")
	}

	// The returned JWT must validate (the cookie is a real session token).
	if _, err := mgr.ValidateToken(session.Value); err != nil {
		t.Errorf("cookie JWT does not validate: %v", err)
	}
}
