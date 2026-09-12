package api

import (
	"net/http"
	"strings"

	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/models"
)

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req models.LoginRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Username == "" || req.Password == "" {
		jsonErr(w, http.StatusBadRequest, "username and password required")
		return
	}

	// Rate-limit per (ip, user) pair.
	if ok, retry := h.loginLimiter.Allow(r, req.Username); !ok {
		auth.WriteRateLimited(w, retry)
		return
	}

	u, ok := h.userStore.Validate(req.Username, req.Password)
	if !ok {
		h.loginLimiter.RecordFailure(r, req.Username)
		_, _, ip := audit.FromRequest(r)
		h.audit.Log(audit.Entry{
			Action: "auth.login_failed", Resource: req.Username, IP: ip,
			Error: "invalid credentials",
		})
		jsonErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.loginLimiter.RecordSuccess(r, req.Username)
	h.userStore.MarkLogin(u.Username)

	token, expiresAt, err := h.auth.GenerateTokenWithMustChange(u.Username, u.Role, u.MustChangePassword, u.SessionEpoch)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	csrf, err := auth.NewCSRFValue()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to generate csrf token")
		return
	}

	// V13-SEC-01: session rides an HttpOnly cookie; the CSRF value is a
	// second, JS-readable cookie (double-submit) + echoed in the JSON.
	auth.SetSessionCookie(w, token, h.auth.SecureCookies(), int(h.auth.TokenTTL().Seconds()))
	auth.SetCSRFCookie(w, csrf, h.auth.SecureCookies(), int(h.auth.TokenTTL().Seconds()))

	_, _, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: u.Username, Role: u.Role, IP: ip, Action: "auth.login",
		Resource: u.Username,
	})

	jsonResp(w, http.StatusOK, models.LoginResponse{
		Username:           u.Username,
		Role:               u.Role,
		MustChangePassword: u.MustChangePassword,
		ExpiresAt:          expiresAt,
		CSRF:               csrf,
	})
}

// Logout revokes the current session token (from the Authorization header
// or the session cookie) and clears both cookies. Idempotent.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token := extractBearer(r)
	if token == "" {
		token = auth.SessionToken(r)
	}
	if token != "" {
		_ = h.auth.Revoke(token)
	}
	auth.ClearSessionCookie(w, h.auth.SecureCookies())
	auth.ClearCSRFCookie(w, h.auth.SecureCookies())
	if u, role, ip := audit.FromRequest(r); u != "" {
		h.audit.Log(audit.Entry{
			User: u, Role: role, IP: ip, Action: "auth.logout", Resource: u,
		})
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// Refresh accepts the current (still-valid) token — via Authorization
// header or session cookie — and returns a freshly-rotated one. The old
// token is revoked atomically and a new session cookie is set.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	oldToken := extractBearer(r)
	if oldToken == "" {
		oldToken = auth.SessionToken(r)
	}
	if oldToken == "" {
		jsonErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	claims, err := h.auth.ValidateToken(oldToken)
	if err != nil {
		jsonErr(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}

	// Look up the current role (a user may have been demoted since
	// the old token was issued).
	u, err := h.userStore.Get(claims.Username)
	if err != nil || !u.Active {
		jsonErr(w, http.StatusForbidden, "user is not active")
		return
	}
	// Refresh is deliberately exempt from SessionEnforcer (an
	// authenticated-but-not-yet-consulted user still needs a path to
	// self-recover), but the epoch check itself must NOT be skipped
	// here: unlike must-change-password, a revoked session that could
	// still call refresh would just mint itself a fresh, now-matching
	// token, silently undoing the admin's "log out everywhere". Reject
	// explicitly instead — same 401 the enforcer would give this
	// request further down the chain, forcing a real re-login through
	// Login (which stamps the current epoch fresh).
	if claims.SessionEpoch != u.SessionEpoch {
		jsonErr(w, http.StatusUnauthorized, "session revoked, please log in again")
		return
	}

	newToken, newExp, err := h.auth.GenerateTokenWithMustChange(u.Username, u.Role, u.MustChangePassword, u.SessionEpoch)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	_ = h.auth.Revoke(oldToken)

	csrf := auth.CSRFValue(r)
	if csrf == "" {
		if v, cerr := auth.NewCSRFValue(); cerr == nil {
			csrf = v
		}
	}
	auth.SetSessionCookie(w, newToken, h.auth.SecureCookies(), int(h.auth.TokenTTL().Seconds()))
	if csrf != "" {
		auth.SetCSRFCookie(w, csrf, h.auth.SecureCookies(), int(h.auth.TokenTTL().Seconds()))
	}

	jsonResp(w, http.StatusOK, models.LoginResponse{
		Username:           u.Username,
		Role:               u.Role,
		MustChangePassword: u.MustChangePassword,
		ExpiresAt:          newExp,
		CSRF:               csrf,
	})
}

// Me returns the current user as a UserResponse. Frontend can call
// this on app load to validate a cached token + re-sync role
// (e.g. after an admin demotes you).
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, _, _ := audit.FromRequest(r)
	if user == "" {
		jsonErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	u, err := h.userStore.Get(user)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "user not found")
		return
	}
	if !u.Active {
		jsonErr(w, http.StatusForbidden, "user is not active")
		return
	}
	jsonResp(w, http.StatusOK, u.ToResponse())
}

// extractBearer extracts the JWT from the Authorization header only.
//
// Security: tokens are intentionally NOT accepted via the `?token=` query
// parameter. Query strings leak into access logs, proxy logs, browser
// history, and Referer headers, so accepting bearer tokens there would
// expose credentials. The SSE endpoint (/api/events) needs a token in the
// URL because EventSource cannot set request headers; it parses that
// parameter itself (see the events handler), not through this helper.
func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	return ""
}
