package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"go.uber.org/goleak"
)
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}


func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager("test-secret-key-very-long-and-random-1234567890", nil)
	defer m.Close()
	t.Cleanup(m.Close)
	return m
}

func TestNewManager_HasBlacklist(t *testing.T) {
	m := NewManager("x", nil)
	defer m.Close()
	if m.Blacklist() == nil {
		t.Fatal("Blacklist() is nil")
	}
}

func TestGenerateToken_AndValidate(t *testing.T) {
	m := newTestManager(t)
	tok, exp, err := m.GenerateToken("alice", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if tok == "" {
		t.Fatal("empty token")
	}
	if exp <= time.Now().Unix() {
		t.Errorf("exp %d in past", exp)
	}
	claims, err := m.ValidateToken(tok)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if claims.Username != "alice" {
		t.Errorf("username = %q, want alice", claims.Username)
	}
	if claims.Role != "admin" {
		t.Errorf("role = %q, want admin", claims.Role)
	}
	if claims.JTI == "" {
		t.Error("jti empty")
	}
	if claims.Issuer != "webkvm" {
		t.Errorf("issuer = %q, want webkvm", claims.Issuer)
	}
}

func TestGenerateTokenWithJTI(t *testing.T) {
	m := newTestManager(t)
	customJTI := "my-custom-jti-42"
	tok, _, err := m.GenerateTokenWithJTI("bob", "operator", customJTI)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := m.ValidateToken(tok)
	if err != nil {
		t.Fatal(err)
	}
	if claims.JTI != customJTI {
		t.Errorf("jti = %q, want %q", claims.JTI, customJTI)
	}
}

func TestValidateToken_WrongSecret(t *testing.T) {
	m1 := NewManager("secret-A-12345678901234567890", nil)
	defer m1.Close()
	m2 := NewManager("secret-B-12345678901234567890", nil)
	defer m2.Close()

	tok, _, err := m1.GenerateToken("u", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.ValidateToken(tok); err == nil {
		t.Error("expected error validating with wrong secret")
	}
}

func TestValidateToken_TamperedSignature(t *testing.T) {
	m := newTestManager(t)
	tok, _, _ := m.GenerateToken("u", "admin")
	// Corrupt the signature: find the last dot (separator between
	// payload and signature) and flip the first byte of the
	// base64url-encoded signature.
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected token parts: %d", len(parts))
	}
	sig := parts[2]
	if sig == "" {
		t.Fatal("empty signature")
	}
	if sig[0] == 'A' {
		parts[2] = "B" + sig[1:]
	} else {
		parts[2] = "A" + sig[1:]
	}
	tampered := strings.Join(parts, ".")
	if _, err := m.ValidateToken(tampered); err == nil {
		t.Error("expected error on tampered signature")
	}
}

func TestValidateToken_Expired(t *testing.T) {
	m := newTestManager(t)
	// Generate a token that's already expired by setting iat/exp
	// in the past.
	exp := time.Now().Add(-1 * time.Hour)
	claims := Claims{
		Username: "u",
		Role:     "admin",
		JTI:      "x",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(exp.Add(-1 * time.Hour)),
			Issuer:    "webkvm",
			ID:        "x",
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(m.secret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ValidateToken(signed); err == nil {
		t.Error("expected error on expired token")
	}
}

func TestValidateToken_NotAJWT(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.ValidateToken("not.a.jwt"); err == nil {
		t.Error("expected error on garbage token")
	}
}

func TestValidateToken_Revoked(t *testing.T) {
	m := newTestManager(t)
	tok, _, _ := m.GenerateToken("u", "admin")

	// Should validate before revoke.
	if _, err := m.ValidateToken(tok); err != nil {
		t.Fatalf("pre-revoke: %v", err)
	}
	if err := m.Revoke(tok); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, err := m.ValidateToken(tok); err == nil {
		t.Error("expected error on revoked token")
	}
}

func TestRevoke_AlreadyExpiredTokenIsNoop(t *testing.T) {
	m := newTestManager(t)
	// Create a token signed by the same secret, but with claims that
	// can't be parsed cleanly (e.g. random bytes).
	if err := m.Revoke("garbage-but-non-empty-token"); err != nil {
		t.Errorf("Revoke garbage: %v", err)
	}
}

func TestMiddleware_NoAuthHeader_401(t *testing.T) {
	m := newTestManager(t)
	called := false
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("GET", "/api/vms", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
	if called {
		t.Error("downstream handler was called without auth")
	}
}

func TestMiddleware_ValidBearer_AllowsRequest(t *testing.T) {
	m := newTestManager(t)
	tok, _, _ := m.GenerateToken("alice", "admin")

	var gotUser, gotRole string
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get(HeaderUser)
		gotRole = r.Header.Get(HeaderRole)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/api/vms", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	if gotUser != "alice" {
		t.Errorf("user = %q, want alice", gotUser)
	}
	if gotRole != "admin" {
		t.Errorf("role = %q, want admin", gotRole)
	}
}

func TestMiddleware_EventsRejectsRawTokenInQueryString(t *testing.T) {
	// Regression test: /api/events (SSE) must NOT accept a raw,
	// long-lived JWT via ?token=... — that leaks into access/proxy
	// logs, browser history, and Referer headers. It authenticates via
	// a short-lived single-use ticket instead (see the next test).
	m := newTestManager(t)
	tok, _, _ := m.GenerateToken("alice", "admin")

	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/api/events?token="+tok, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (raw query token must be rejected)", rr.Code)
	}
}

func TestMiddleware_EventsTicketInQueryString(t *testing.T) {
	m := newTestManager(t)
	tk, err := IssueTicket("alice", "admin")
	if err != nil {
		t.Fatalf("IssueTicket: %v", err)
	}

	var gotUser string
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = r.Header.Get(HeaderUser)
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/api/events?ticket="+tk, nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (ticket allowed)", rr.Code)
	}
	if gotUser != "alice" {
		t.Errorf("user = %q, want alice", gotUser)
	}
}

func TestMiddleware_InvalidToken_401(t *testing.T) {
	m := newTestManager(t)
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/api/vms", nil)
	req.Header.Set("Authorization", "Bearer not.a.jwt")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

func TestMiddleware_BypassPaths(t *testing.T) {
	m := newTestManager(t)
	cases := []string{
		"/api/auth/login",
		"/api/health",
		"/api/system/cert",
		"/static/index.html",
		"/api/covers/vm-1.png",
		"/", // not /api/ (SPA shell)
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			called := false
			h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
			}))
			req := httptest.NewRequest("GET", path, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if !called {
				t.Errorf("path %q: downstream not called (middleware did not bypass)", path)
			}
		})
	}
}

// The console (noVNC page + VNC/RDP/SPICE endpoints) requires
// authentication: without a token it must return 401, not bypass. The
// token is accepted via query string for these paths only (a new tab
// cannot set headers).
func TestMiddleware_ConsoleRequiresAuth(t *testing.T) {
	m := newTestManager(t)
	cases := []string{
		"/console/vm-1",
		"/api/vms/abc-123/vnc",
		"/api/vms/abc-123/rdp",
		"/api/vms/abc-123/spice",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			called := false
			h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
			}))
			req := httptest.NewRequest("GET", path, nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if called {
				t.Errorf("path %q: downstream called without auth (console must require auth)", path)
			}
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("path %q: status = %d, want 401", path, rr.Code)
			}
			// With a token in the query string it must pass through.
			tok, _, _ := m.GenerateToken("admin", "admin")
			req2 := httptest.NewRequest("GET", path+"?token="+tok, nil)
			rr2 := httptest.NewRecorder()
			h.ServeHTTP(rr2, req2)
			if !called {
				t.Errorf("path %q: console with valid query token not allowed through", path)
			}
		})
	}
}

func TestMiddleware_AuthorizationHeaderNotBearer(t *testing.T) {
	m := newTestManager(t)
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/api/vms", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (non-Bearer scheme)", rr.Code)
	}
}

func TestMiddleware_RejectsOtherSigningMethod(t *testing.T) {
	m := newTestManager(t)
	// Forge a token with alg=none.
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, Claims{
		Username: "attacker",
		Role:     "admin",
	})
	signed, err := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	h := m.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/api/vms", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (alg=none must be rejected)", rr.Code)
	}
	// Body should mention 'invalid or expired'.
	if !strings.Contains(rr.Body.String(), "invalid or expired") {
		t.Errorf("body = %q, want 'invalid or expired'", rr.Body.String())
	}
}

// fakeSettings implements SettingsProvider with settable values so
// tests can verify the Manager picks up changes without restart.
type fakeSettings struct {
	ttl    time.Duration
	allow  bool
}

func (f *fakeSettings) GetDuration(key string) time.Duration { return f.ttl }
func (f *fakeSettings) GetBool(key string) bool             { return f.allow }

func TestManager_TokenTTL_Default(t *testing.T) {
	m := NewManager("x", nil)
	defer m.Close()
	if got := m.TokenTTL(); got != defaultTokenTTL {
		t.Errorf("TokenTTL() with no settings = %v, want %v", got, defaultTokenTTL)
	}
}

func TestManager_TokenTTL_LiveReload(t *testing.T) {
	fs := &fakeSettings{ttl: 1 * time.Hour}
	m := NewManager("x", fs)
	defer m.Close()
	if got := m.TokenTTL(); got != 1*time.Hour {
		t.Errorf("TokenTTL() = %v, want 1h", got)
	}
	// Change the setting at runtime; the next call should reflect it.
	fs.ttl = 5 * time.Minute
	if got := m.TokenTTL(); got != 5*time.Minute {
		t.Errorf("after settings change, TokenTTL() = %v, want 5m", got)
	}
}

func TestManager_AllowAPITokens_Default(t *testing.T) {
	m := NewManager("x", nil)
	defer m.Close()
	if !m.AllowAPITokens() {
		t.Error("default AllowAPITokens() should be true")
	}
}

func TestManager_AllowAPITokens_LiveReload(t *testing.T) {
	fs := &fakeSettings{allow: true}
	m := NewManager("x", fs)
	defer m.Close()
	if !m.AllowAPITokens() {
		t.Error("AllowAPITokens() should be true when settings allow")
	}
	fs.allow = false
	if m.AllowAPITokens() {
		t.Error("AllowAPITokens() should be false after settings flip")
	}
}

func TestManager_GenerateToken_UsesLiveTTL(t *testing.T) {
	fs := &fakeSettings{ttl: 1 * time.Minute}
	m := NewManager("x", fs)
	defer m.Close()
	_, exp, err := m.GenerateToken("alice", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if exp-time.Now().Unix() > 90 {
		t.Errorf("token expiry = %d, want ~60s ahead", exp-time.Now().Unix())
	}
	// Change TTL to 24h; the next token reflects it without
	// invalidating the previous one (which keeps its own exp).
	fs.ttl = 24 * time.Hour
	_, exp2, _ := m.GenerateToken("bob", "admin")
	if exp2-time.Now().Unix() < 24*3600-30 {
		t.Errorf("second token expiry = %d, want ~24h ahead", exp2-time.Now().Unix())
	}
}
