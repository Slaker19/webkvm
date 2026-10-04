package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// WebAuthnRegisterBegin initiates passkey / security key registration.
func (h *Handler) WebAuthnRegisterBegin(w http.ResponseWriter, r *http.Request) {
	if h.webauthn == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webauthn not initialized")
		return
	}
	username, _, _ := audit.FromRequest(r)
	if username == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	u, err := h.userStore.Get(username)
	if err != nil || !u.Active {
		jsonErr(w, http.StatusNotFound, "user not found or inactive")
		return
	}

	creation, sessionID, err := h.webauthn.BeginRegistration(u, r)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"publicKey":  creation.Response,
		"session_id": sessionID,
	})
}

// WebAuthnRegisterFinish completes passkey registration and persists the credential.
func (h *Handler) WebAuthnRegisterFinish(w http.ResponseWriter, r *http.Request) {
	if h.webauthn == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webauthn not initialized")
		return
	}
	username, role, ip := audit.FromRequest(r)
	if username == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	u, err := h.userStore.Get(username)
	if err != nil || !u.Active {
		jsonErr(w, http.StatusNotFound, "user not found or inactive")
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "failed to read body")
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = r.Header.Get("X-WebAuthn-Session")
	}
	credName := r.URL.Query().Get("name")

	credBytes := bodyBytes

	// If body is wrapped in a container object with session_id / name / response:
	var wrapper struct {
		SessionID string          `json:"session_id"`
		Name      string          `json:"name"`
		Response  json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(bodyBytes, &wrapper); err == nil && wrapper.SessionID != "" {
		sessionID = wrapper.SessionID
		if wrapper.Name != "" {
			credName = wrapper.Name
		}
		if len(wrapper.Response) > 0 {
			credBytes = wrapper.Response
		}
	}

	if sessionID == "" {
		jsonErr(w, http.StatusBadRequest, "session_id is required")
		return
	}

	cred, err := h.webauthn.FinishRegistration(u, sessionID, credName, credBytes, r)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "registration failed: "+err.Error())
		return
	}

	if err := h.userStore.AddWebAuthnCredential(username, *cred); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if h.audit != nil {
		h.audit.Log(audit.Entry{
			User: username, Role: role, IP: ip, Action: "user.passkey_added", Resource: cred.Name,
		})
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"credential": cred,
	})
}

// WebAuthnListCredentials returns the user's registered passkeys.
func (h *Handler) WebAuthnListCredentials(w http.ResponseWriter, r *http.Request) {
	username, _, _ := audit.FromRequest(r)
	if username == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	creds, err := h.userStore.GetWebAuthnCredentials(username)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if creds == nil {
		creds = []models.WebAuthnCredential{}
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"credentials": creds,
	})
}

// WebAuthnUpdateCredential renames an existing passkey.
func (h *Handler) WebAuthnUpdateCredential(w http.ResponseWriter, r *http.Request) {
	username, _, _ := audit.FromRequest(r)
	if username == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		jsonErr(w, http.StatusBadRequest, "credential id is required")
		return
	}

	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		jsonErr(w, http.StatusBadRequest, "valid name is required")
		return
	}

	if err := h.userStore.UpdateWebAuthnCredentialName(username, id, strings.TrimSpace(req.Name)); err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// WebAuthnDeleteCredential deletes a registered passkey.
func (h *Handler) WebAuthnDeleteCredential(w http.ResponseWriter, r *http.Request) {
	username, role, ip := audit.FromRequest(r)
	if username == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	id := chi.URLParam(r, "id")
	if id == "" {
		jsonErr(w, http.StatusBadRequest, "credential id is required")
		return
	}

	if err := h.userStore.DeleteWebAuthnCredential(username, id); err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}

	if h.audit != nil {
		h.audit.Log(audit.Entry{
			User: username, Role: role, IP: ip, Action: "user.passkey_deleted", Resource: id,
		})
	}

	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// WebAuthnLoginBegin initiates 1-click passwordless Passkey login or 2FA assertion.
func (h *Handler) WebAuthnLoginBegin(w http.ResponseWriter, r *http.Request) {
	if h.webauthn == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webauthn not initialized")
		return
	}

	// Check perimeter jail before attempting authentication
	_, _, ip := audit.FromRequest(r)
	if h.jail != nil && h.jail.IsBanned(ip) {
		jsonErr(w, http.StatusForbidden, "IP address temporarily banned due to excessive failed attempts")
		return
	}

	var req struct {
		Username string `json:"username"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	if req.Username != "" && h.loginLimiter != nil {
		if ok, retry := h.loginLimiter.Allow(r, req.Username); !ok {
			auth.WriteRateLimited(w, retry)
			return
		}
	}

	var u *models.User
	if req.Username != "" {
		var err error
		u, err = h.userStore.Get(req.Username)
		if err != nil || !u.Active {
			jsonErr(w, http.StatusNotFound, "user not found or inactive")
			return
		}
		if len(u.WebAuthnCredentials) == 0 {
			jsonErr(w, http.StatusBadRequest, "no passkeys registered for this user")
			return
		}
	}

	assertion, sessionID, err := h.webauthn.BeginLogin(u, r)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"publicKey":  assertion.Response,
		"session_id": sessionID,
	})
}

// WebAuthnLoginFinish completes authentication via Passkey.
func (h *Handler) WebAuthnLoginFinish(w http.ResponseWriter, r *http.Request) {
	if h.webauthn == nil {
		jsonErr(w, http.StatusServiceUnavailable, "webauthn not initialized")
		return
	}

	// Check perimeter jail before attempting authentication
	_, _, ip := audit.FromRequest(r)
	if h.jail != nil && h.jail.IsBanned(ip) {
		jsonErr(w, http.StatusForbidden, "IP address temporarily banned due to excessive failed attempts")
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "failed to read body")
		return
	}

	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		sessionID = r.Header.Get("X-WebAuthn-Session")
	}

	credBytes := bodyBytes

	var wrapper struct {
		SessionID string          `json:"session_id"`
		Response  json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(bodyBytes, &wrapper); err == nil && wrapper.SessionID != "" {
		sessionID = wrapper.SessionID
		if len(wrapper.Response) > 0 {
			credBytes = wrapper.Response
		}
	}

	if sessionID == "" {
		jsonErr(w, http.StatusBadRequest, "session_id is required")
		return
	}

	findUser := func(rawID, userHandle []byte) (*models.User, error) {
		idStr := base64.RawURLEncoding.EncodeToString(rawID)
		u, _, err := h.userStore.FindUserByWebAuthnCredentialID(idStr)
		if err != nil {
			idPadded := base64.URLEncoding.EncodeToString(rawID)
			u, _, err = h.userStore.FindUserByWebAuthnCredentialID(idPadded)
		}
		if err != nil {
			return nil, errors.New("no user matches this passkey")
		}
		return u, nil
	}

	u, cred, err := h.webauthn.FinishLogin(sessionID, findUser, credBytes, r)
	if err != nil {
		_, _, ip := audit.FromRequest(r)
		if h.loginLimiter != nil {
			userKey := "passkey"
			if u != nil && u.Username != "" {
				userKey = u.Username
			}
			h.loginLimiter.RecordFailure(r, userKey)
		}
		if h.jail != nil {
			h.jail.RecordFailure(ip, "failed passkey login: "+err.Error())
		}
		if h.audit != nil {
			h.audit.Log(audit.Entry{
				Action: "auth.passkey_failed", Resource: "passkey", IP: ip,
				Error: err.Error(),
			})
		}
		jsonErr(w, http.StatusUnauthorized, "passkey authentication failed: "+err.Error())
		return
	}

	if !u.Active {
		jsonErr(w, http.StatusForbidden, "user not active")
		return
	}

	if cred != nil {
		_ = h.userStore.RecordWebAuthnUsage(u.Username, cred.ID, cred.SignCount)
	}

	if h.loginLimiter != nil {
		h.loginLimiter.RecordSuccess(r, u.Username)
	}
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

	_, _, ip = audit.FromRequest(r)
	if h.audit != nil {
		h.audit.Log(audit.Entry{
			User: u.Username, Role: u.Role, IP: ip, Action: "auth.login_passkey",
			Resource: u.Username,
		})
	}

	jsonResp(w, http.StatusOK, models.LoginResponse{
		Username:           u.Username,
		Role:               u.Role,
		MustChangePassword: u.MustChangePassword,
		ExpiresAt:          expiresAt,
		CSRF:               csrf,
	})
}
