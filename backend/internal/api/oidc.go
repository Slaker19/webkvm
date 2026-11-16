package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/zitadel/oidc/v3/pkg/oidc"

	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/models"
	oidcclient "webkvm/internal/oidc"
)

type OIDCProvider interface {
	Enabled() bool
	Config() oidcclient.Config
	AuthURL(state, verifier string) string
	Exchange(ctx context.Context, code, verifier, nonce string) (*oidc.IDTokenClaims, error)
	SignState(state, nonce, verifier string) (string, error)
	VerifyState(raw string) (state, nonce, verifier string, err error)
}

var oidcProvider OIDCProvider

const oidcStateCookie = "webkvm_oidc_state"

var oidcUsernameClean = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SetOIDCProvider wires the optional SSO provider. nil disables the routes
// and leaves local password login untouched.
func SetOIDCProvider(p OIDCProvider) { oidcProvider = p }

func (h *Handler) GetOIDCStatus(w http.ResponseWriter, r *http.Request) {
	if oidcProvider == nil || !oidcProvider.Enabled() {
		jsonResp(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	cfg := oidcProvider.Config()
	jsonResp(w, http.StatusOK, map[string]any{
		"enabled":             true,
		"button_label":        cfg.ButtonLabel,
		"disable_local_login": cfg.DisableLocalLogin,
	})
}

// OIDCLogin starts the authorization-code + PKCE flow.
func (h *Handler) OIDCLogin(w http.ResponseWriter, r *http.Request) {
	if oidcProvider == nil || !oidcProvider.Enabled() {
		http.NotFound(w, r)
		return
	}
	state, nonce, verifier, err := oidcclient.NewState()
	if err != nil {
		oidcErrorRedirect(w, r, "state")
		return
	}
	signed, err := oidcProvider.SignState(state, nonce, verifier)
	if err != nil {
		oidcErrorRedirect(w, r, "state")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    signed,
		Path:     "/api/auth/oidc",
		MaxAge:   300,
		HttpOnly: true,
		Secure:   h.auth.SecureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, oidcProvider.AuthURL(state, verifier), http.StatusFound)
}

// OIDCCallback validates state/nonce, exchanges the code, maps the user and
// issues the normal WebKVM session cookies.
func (h *Handler) OIDCCallback(w http.ResponseWriter, r *http.Request) {
	if oidcProvider == nil || !oidcProvider.Enabled() {
		http.NotFound(w, r)
		return
	}
	if e := r.URL.Query().Get("error"); e != "" {
		oidcErrorRedirect(w, r, e)
		return
	}
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		oidcErrorRedirect(w, r, "missing_code")
		return
	}
	cookie, err := r.Cookie(oidcStateCookie)
	if err != nil {
		oidcErrorRedirect(w, r, "missing_state")
		return
	}
	wantState, nonce, verifier, err := oidcProvider.VerifyState(cookie.Value)
	if err != nil || wantState != state {
		oidcErrorRedirect(w, r, "bad_state")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: "", Path: "/api/auth/oidc", MaxAge: -1, HttpOnly: true,
		Secure: h.auth.SecureCookies(), SameSite: http.SameSiteLaxMode,
	})
	claims, err := oidcProvider.Exchange(r.Context(), code, verifier, nonce)
	if err != nil {
		oidcErrorRedirect(w, r, "exchange")
		return
	}
	if claims.Nonce != nonce {
		oidcErrorRedirect(w, r, "nonce")
		return
	}
	cfg := oidcProvider.Config()
	username := oidcUsername(claims, cfg.UsernameClaim)
	if username == "" {
		oidcErrorRedirect(w, r, "username")
		return
	}
	u, uerr := h.userStore.Get(username)
	if uerr != nil {
		if !cfg.AutoProvision {
			oidcErrorRedirect(w, r, "not_provisioned")
			return
		}
		role := oidcRole(claims, cfg)
		password, perr := randomPassword()
		if perr != nil {
			oidcErrorRedirect(w, r, "provision")
			return
		}
		u, uerr = h.userStore.Create(models.CreateUserRequest{
			Username:    username,
			Password:    password,
			OIDCSubject: claims.Subject,
			Role:        role,
			Email:       claims.Email,
		})
		if uerr != nil {
			oidcErrorRedirect(w, r, "provision")
			return
		}
	} else if !u.Active {
		oidcErrorRedirect(w, r, "inactive")
		return
	} else if u.OIDCSubject == "" || claims.Subject == "" || u.OIDCSubject != claims.Subject {
		// Never attach an existing local account (or another IdP subject)
		// to this identity: a matching display name is not proof of ownership.
		oidcErrorRedirect(w, r, "account_conflict")
		return
	}

	token, _, err := h.auth.GenerateTokenWithMustChange(u.Username, u.Role, u.MustChangePassword, u.SessionEpoch)
	if err != nil {
		oidcErrorRedirect(w, r, "session")
		return
	}
	csrf, err := auth.NewCSRFValue()
	if err != nil {
		oidcErrorRedirect(w, r, "session")
		return
	}
	auth.SetSessionCookie(w, token, h.auth.SecureCookies(), int(h.auth.TokenTTL().Seconds()))
	auth.SetCSRFCookie(w, csrf, h.auth.SecureCookies(), int(h.auth.TokenTTL().Seconds()))
	_, _, ip := audit.FromRequest(r)
	if h.audit != nil {
		h.audit.Log(audit.Entry{
			User: u.Username, Role: u.Role, IP: ip, Action: "auth.oidc_login",
			Resource: u.Username,
		})
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

func oidcErrorRedirect(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/#/login?oidc_error="+url.QueryEscape(code), http.StatusFound)
}

func oidcUsername(claims *oidc.IDTokenClaims, claim string) string {
	raw := claimString(claims, claim)
	if raw == "" {
		raw = claims.PreferredUsername
	}
	if raw == "" {
		raw = claims.Email
	}
	if raw == "" {
		raw = claims.Subject
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if i := strings.LastIndex(raw, "@"); i > 0 {
		raw = raw[:i]
	}
	raw = oidcUsernameClean.ReplaceAllString(raw, "-")
	raw = strings.Trim(raw, ".-_")
	if raw == "" {
		raw = "oidc-user"
	}
	if len(raw) > 64 {
		raw = raw[:64]
	}
	return raw
}

func claimString(claims *oidc.IDTokenClaims, key string) string {
	if claims == nil || key == "" || claims.Claims == nil {
		return ""
	}
	switch v := claims.Claims[key].(type) {
	case string:
		return v
	case []string:
		if len(v) > 0 {
			return v[0]
		}
	case []any:
		if len(v) > 0 {
			if s, ok := v[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

func oidcRole(claims *oidc.IDTokenClaims, cfg oidcclient.Config) string {
	best := cfg.DefaultRole
	if best == "" {
		best = "viewer"
	}
	if len(cfg.RoleMap) == 0 {
		return best
	}
	var values []string
	if claims != nil && claims.Claims != nil {
		switch v := claims.Claims[cfg.RoleClaim].(type) {
		case string:
			values = append(values, v)
		case []string:
			values = append(values, v...)
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					values = append(values, s)
				}
			}
		}
	}
	rank := map[string]int{"viewer": 1, "operator": 2, "admin": 3}
	for _, v := range values {
		if mapped, ok := cfg.RoleMap[v]; ok {
			if rank[mapped] > rank[best] {
				best = mapped
			}
		}
	}
	return best
}

func randomPassword() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func oidcLocalLoginDisabled(username string) bool {
	if oidcProvider == nil || !oidcProvider.Enabled() {
		return false
	}
	cfg := oidcProvider.Config()
	if !cfg.DisableLocalLogin {
		return false
	}
	// Keep the built-in admin as a break-glass account: an IdP outage must
	// not lock the only administrator out of the appliance.
	return !strings.EqualFold(username, "admin")
}
