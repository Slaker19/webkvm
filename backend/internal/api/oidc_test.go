package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"webkvm/internal/auth"
	"webkvm/internal/config"
	"webkvm/internal/models"
	oidcclient "webkvm/internal/oidc"
	"webkvm/internal/user"
)

type fakeOIDC struct {
	cfg      oidcclient.Config
	claims   *oidc.IDTokenClaims
	state    string
	nonce    string
	verifier string
}

func (f *fakeOIDC) Enabled() bool             { return f.cfg.Enabled }
func (f *fakeOIDC) Config() oidcclient.Config { return f.cfg }
func (f *fakeOIDC) AuthURL(state, verifier string) string {
	return "https://idp.example/auth?state=" + state
}
func (f *fakeOIDC) Exchange(context.Context, string, string, string) (*oidc.IDTokenClaims, error) {
	return f.claims, nil
}
func (f *fakeOIDC) SignState(state, nonce, verifier string) (string, error) {
	f.state, f.nonce, f.verifier = state, nonce, verifier
	return "signed-state", nil
}
func (f *fakeOIDC) VerifyState(string) (string, string, string, error) {
	return f.state, f.nonce, f.verifier, nil
}

func oidcTestHandler(t *testing.T) *Handler {
	t.Helper()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")
	us, err := user.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mgr := auth.NewManager("test-secret-oidc", nil)
	mgr.SetSecureCookies(false)
	t.Cleanup(mgr.Close)
	return &Handler{
		auth: mgr, userStore: us, loginLimiter: auth.NewLoginRateLimiter(),
		cfg: &config.Config{},
	}
}

func TestOIDCStatusAndLogin(t *testing.T) {
	h := oidcTestHandler(t)
	f := &fakeOIDC{cfg: oidcclient.Config{Enabled: true, ButtonLabel: "SSO"}}
	SetOIDCProvider(f)
	defer SetOIDCProvider(nil)

	rec := httptest.NewRecorder()
	h.GetOIDCStatus(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/status", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Fatalf("status: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.OIDCLogin(rec, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil))
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "state=") {
		t.Fatalf("login: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == oidcStateCookie {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly {
		t.Fatal("state cookie missing or not HttpOnly")
	}
}

func TestOIDCCallbackProvisionsUserAndSetsSession(t *testing.T) {
	h := oidcTestHandler(t)
	f := &fakeOIDC{
		cfg: oidcclient.Config{
			Enabled: true, AutoProvision: true, DefaultRole: "operator",
			UsernameClaim: "preferred_username", ButtonLabel: "SSO",
		},
	}
	SetOIDCProvider(f)
	defer SetOIDCProvider(nil)

	login := httptest.NewRecorder()
	h.OIDCLogin(login, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil))
	state := f.state
	f.claims = &oidc.IDTokenClaims{
		TokenClaims:     oidc.TokenClaims{Nonce: f.nonce},
		UserInfoProfile: oidc.UserInfoProfile{PreferredUsername: "alice"},
		UserInfoEmail:   oidc.UserInfoEmail{Email: "alice@example.com"},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=abc&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: "signed-state"})
	rec := httptest.NewRecorder()
	h.OIDCCallback(rec, req)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("callback: %d %s %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	var session, csrf bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.Value != "" {
			session = true
		}
		if c.Name == auth.CSRFCookieName && c.Value != "" {
			csrf = true
		}
	}
	if !session || !csrf {
		t.Fatalf("session cookies missing: %v", rec.Result().Cookies())
	}
	u, err := h.userStore.Get("alice")
	if err != nil || u.Role != string(models.RoleOperator) {
		t.Fatalf("provisioned user = %+v, %v", u, err)
	}
}

func TestOIDCCallbackRejectsBadNonceAndState(t *testing.T) {
	h := oidcTestHandler(t)
	f := &fakeOIDC{cfg: oidcclient.Config{Enabled: true, AutoProvision: true, DefaultRole: "viewer", UsernameClaim: "preferred_username"}}
	SetOIDCProvider(f)
	defer SetOIDCProvider(nil)

	login := httptest.NewRecorder()
	h.OIDCLogin(login, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil))
	state := f.state
	f.claims = &oidc.IDTokenClaims{TokenClaims: oidc.TokenClaims{Nonce: "wrong", Subject: "sub-1"}, UserInfoProfile: oidc.UserInfoProfile{PreferredUsername: "bob"}}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=abc&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: "signed-state"})
	rec := httptest.NewRecorder()
	h.OIDCCallback(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oidc_error=nonce") {
		t.Fatalf("bad nonce: %d %s", rec.Code, rec.Header().Get("Location"))
	}

	req = httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=abc&state=other", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: "signed-state"})
	rec = httptest.NewRecorder()
	h.OIDCCallback(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "oidc_error=bad_state") {
		t.Fatalf("bad state: %d %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestLocalLoginDisabledExceptAdmin(t *testing.T) {
	f := &fakeOIDC{cfg: oidcclient.Config{Enabled: true, DisableLocalLogin: true}}
	SetOIDCProvider(f)
	defer SetOIDCProvider(nil)
	if !oidcLocalLoginDisabled("alice") {
		t.Fatal("non-admin local login should be disabled")
	}
	if oidcLocalLoginDisabled("admin") {
		t.Fatal("admin must keep the local break-glass login")
	}
}

func TestOIDCCallbackRejectsLocalAccountTakeover(t *testing.T) {
	h := oidcTestHandler(t)
	if _, err := h.userStore.Create(models.CreateUserRequest{
		Username: "alice", Password: "Local-Pass#2026", Role: "viewer",
	}); err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{cfg: oidcclient.Config{
		Enabled: true, AutoProvision: true, DefaultRole: "admin",
		UsernameClaim: "preferred_username",
	}}
	SetOIDCProvider(f)
	defer SetOIDCProvider(nil)

	login := httptest.NewRecorder()
	h.OIDCLogin(login, httptest.NewRequest(http.MethodGet, "/api/auth/oidc/login", nil))
	f.claims = &oidc.IDTokenClaims{
		TokenClaims:     oidc.TokenClaims{Nonce: f.nonce, Subject: "attacker-sub"},
		UserInfoProfile: oidc.UserInfoProfile{PreferredUsername: "alice"},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oidc/callback?code=abc&state="+f.state, nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: "signed-state"})
	rec := httptest.NewRecorder()
	h.OIDCCallback(rec, req)
	if rec.Code != http.StatusFound || !strings.Contains(rec.Header().Get("Location"), "account_conflict") {
		t.Fatalf("takeover: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if u, _ := h.userStore.Get("alice"); u.OIDCSubject != "" || u.Role != "viewer" {
		t.Fatalf("local account was modified: %+v", u)
	}
}
