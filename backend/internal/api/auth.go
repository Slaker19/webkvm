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

	// Check perimeter jail before attempting authentication
	_, _, ip := audit.FromRequest(r)
	if h.jail != nil && h.jail.IsBanned(ip) {
		jsonErr(w, http.StatusForbidden, "IP address temporarily banned due to excessive failed attempts")
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
		if h.jail != nil {
			h.jail.RecordFailure(ip, "failed login for "+req.Username)
		}
		h.audit.Log(audit.Entry{
			// User carries the asserted identity so the user filter
			// finds this account's failed logins (it may not exist).
			User:   req.Username,
			Action: "auth.login_failed", Resource: req.Username, IP: ip,
			Error: "invalid credentials",
		})
		jsonErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	// If 2FA is enabled for the user, require the second factor.
	if u.TOTPEnabled {
		mfaToken, err := h.auth.GenerateMFAToken(u.Username)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "failed to generate mfa token")
			return
		}
		jsonResp(w, http.StatusOK, map[string]any{
			"mfa_required": true,
			"mfa_token":    mfaToken,
			"username":     u.Username,
		})
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

	_, _, ip = audit.FromRequest(r)
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

// Login2FA completes authentication for 2FA-enabled accounts using TOTP code or backup code.
func (h *Handler) Login2FA(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MFAToken string `json:"mfa_token"`
		Code     string `json:"code"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.MFAToken == "" || req.Code == "" {
		jsonErr(w, http.StatusBadRequest, "mfa_token and code are required")
		return
	}

	username, err := h.auth.ValidateMFAToken(req.MFAToken)
	if err != nil {
		jsonErr(w, http.StatusUnauthorized, "invalid or expired mfa session")
		return
	}

	// Per-(ip, user) throttle, mirroring Login: without it a 6-digit
	// TOTP code is brute-forceable within the MFA token lifetime
	// (the global per-IP bucket alone does not stop distributed
	// guessing across many source IPs).
	if ok, retry := h.loginLimiter.Allow(r, username); !ok {
		auth.WriteRateLimited(w, retry)
		return
	}

	u, err := h.userStore.Get(username)
	if err != nil || !u.Active {
		jsonErr(w, http.StatusForbidden, "user not active")
		return
	}

	validCode := auth.ValidateTOTP(u.TOTPSecret, req.Code)
	if !validCode {
		// Try consuming backup code
		usedBackup, _ := h.userStore.ConsumeBackupCode(username, req.Code)
		if !usedBackup {
			h.loginLimiter.RecordFailure(r, username)
			_, _, ip := audit.FromRequest(r)
			h.audit.Log(audit.Entry{
				User: username, Action: "auth.2fa_failed", Resource: username, IP: ip,
				Error: "invalid 2fa verification code",
			})
			jsonErr(w, http.StatusUnauthorized, "código de verificación 2FA inválido")
			return
		}
	}

	h.loginLimiter.RecordSuccess(r, u.Username)
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

	auth.SetSessionCookie(w, token, h.auth.SecureCookies(), int(h.auth.TokenTTL().Seconds()))
	auth.SetCSRFCookie(w, csrf, h.auth.SecureCookies(), int(h.auth.TokenTTL().Seconds()))

	_, _, ip := audit.FromRequest(r)
	h.audit.Log(audit.Entry{
		User: u.Username, Role: u.Role, IP: ip, Action: "auth.login_2fa",
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

// Setup2FA initiates TOTP configuration for the logged-in user.
func (h *Handler) Setup2FA(w http.ResponseWriter, r *http.Request) {
	username, _, _ := audit.FromRequest(r)
	if username == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to generate secret")
		return
	}
	backupCodes := auth.GenerateBackupCodes(8)
	otpURL := auth.BuildOTPAuthURL("WebKVM", username, secret)

	jsonResp(w, http.StatusOK, map[string]any{
		"secret":       secret,
		"otpauth_url":  otpURL,
		"backup_codes": backupCodes,
	})
}

// Enable2FA verifies the initial code and saves the TOTP configuration for the user.
func (h *Handler) Enable2FA(w http.ResponseWriter, r *http.Request) {
	username, role, ip := audit.FromRequest(r)
	if username == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Secret      string   `json:"secret"`
		Code        string   `json:"code"`
		BackupCodes []string `json:"backup_codes"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Secret == "" || req.Code == "" {
		jsonErr(w, http.StatusBadRequest, "secret and code required")
		return
	}
	if !auth.ValidateTOTP(req.Secret, req.Code) {
		jsonErr(w, http.StatusBadRequest, "código de verificación incorrecto, asegúrate de que la hora de tu dispositivo esté sincronizada")
		return
	}

	if err := h.userStore.EnableTOTP(username, req.Secret, req.BackupCodes); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(audit.Entry{
		User: username, Role: role, IP: ip, Action: "user.2fa_enabled", Resource: username,
	})
	jsonResp(w, http.StatusOK, map[string]string{"status": "enabled"})
}

// Disable2FA disables 2FA for the user after verifying current password.
func (h *Handler) Disable2FA(w http.ResponseWriter, r *http.Request) {
	username, role, ip := audit.FromRequest(r)
	if username == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if _, ok := h.userStore.Validate(username, req.Password); !ok {
		jsonErr(w, http.StatusUnauthorized, "contraseña incorrecta")
		return
	}

	if err := h.userStore.DisableTOTP(username); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(audit.Entry{
		User: username, Role: role, IP: ip, Action: "user.2fa_disabled", Resource: username,
	})
	jsonResp(w, http.StatusOK, map[string]string{"status": "disabled"})
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
