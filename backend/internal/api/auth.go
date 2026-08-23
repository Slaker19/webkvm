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

	token, expiresAt, err := h.auth.GenerateTokenWithMustChange(u.Username, u.Role, u.MustChangePassword)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to generate token")
		return
	}

	_, _, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: u.Username, Role: u.Role, IP: ip, Action: "auth.login",
		Resource: u.Username,
	})

	jsonResp(w, http.StatusOK, models.LoginResponse{
		Token:              token,
		ExpiresAt:          expiresAt,
		Username:           u.Username,
		Role:               u.Role,
		MustChangePassword: u.MustChangePassword,
	})
}

// Logout revokes the current bearer token. Idempotent — calling it
// twice is harmless. The token's remaining lifetime is spent on the
// blacklist.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token := extractBearer(r)
	if token != "" {
		_ = h.auth.Revoke(token)
	}
	if u, role, ip := audit.FromRequest(r); u != "" {
		h.audit.Log(audit.Entry{
			User: u, Role: role, IP: ip, Action: "auth.logout", Resource: u,
		})
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// Refresh accepts the current (still-valid) bearer token and returns
// a freshly-rotated one. The old token is revoked atomically.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	oldToken := extractBearer(r)
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

	newToken, newExp, err := h.auth.GenerateTokenWithMustChange(u.Username, u.Role, u.MustChangePassword)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	_ = h.auth.Revoke(oldToken)

	jsonResp(w, http.StatusOK, models.LoginResponse{
		Token:              newToken,
		ExpiresAt:          newExp,
		Username:           u.Username,
		Role:               u.Role,
		MustChangePassword: u.MustChangePassword,
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
