package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"webkvm/internal/models"
	"webkvm/internal/tokens"
)

// TokenCreateRequest is the body of POST /api/tokens.
type TokenCreateRequest struct {
	Name     string   `json:"name"`
	Scopes   []string `json:"scopes,omitempty"`
	TTLHours int      `json:"ttl_hours,omitempty"` // 0 = 30 days
}

// TokenCreateResponse is returned by POST /api/tokens. The plain
// token is only present in this single response — the user must
// copy it then.
type TokenCreateResponse struct {
	Token tokens.Token `json:"token"`
	Plain string       `json:"plain"`
}

// ListTokens returns the caller's own tokens (or all if admin).
func (h *Handler) ListTokens(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil {
		jsonErr(w, http.StatusServiceUnavailable, "tokens store not initialized")
		return
	}
	role := r.Header.Get("X-Role")
	username := r.Header.Get("X-User")
	filter := username
	if role == "admin" && r.URL.Query().Get("all") == "1" {
		filter = ""
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"tokens": h.tokens.List(filter),
	})
}

// CreateToken issues a new API token. The plain text is shown ONCE.
func (h *Handler) CreateToken(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil {
		jsonErr(w, http.StatusServiceUnavailable, "tokens store not initialized")
		return
	}
	var req TokenCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Name) > 64 {
		jsonErr(w, http.StatusBadRequest, "name must be ≤ 64 chars")
		return
	}
	ttl := time.Duration(req.TTLHours) * time.Hour
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	if ttl > 365*24*time.Hour {
		jsonErr(w, http.StatusBadRequest, "ttl_hours must be ≤ 8760 (1 year)")
		return
	}
	username := r.Header.Get("X-User")
	role := r.Header.Get("X-Role")
	if slices.Contains(req.Scopes, "*") && role != models.RoleAdmin {
		jsonErr(w, http.StatusForbidden, "scope '*' requires admin role")
		return
	}
	tok, plain, err := h.tokens.Create(req.Name, username, role, req.Scopes, ttl)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "token.create", tok.ID, map[string]any{"name": req.Name}))
	}
	jsonResp(w, http.StatusCreated, TokenCreateResponse{Token: tok, Plain: plain})
}

// RevokeToken marks a token as revoked. Owner or admin only.
func (h *Handler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil {
		jsonErr(w, http.StatusServiceUnavailable, "tokens store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	username := r.Header.Get("X-User")
	role := r.Header.Get("X-Role")
	if err := h.tokens.Revoke(id, username, role == "admin"); err != nil {
		status := http.StatusForbidden
		if role == "admin" {
			status = http.StatusNotFound
		}
		jsonErr(w, status, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "token.revoke", id, nil))
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// DeleteToken removes a token entirely. Owner or admin only.
func (h *Handler) DeleteToken(w http.ResponseWriter, r *http.Request) {
	if h.tokens == nil {
		jsonErr(w, http.StatusServiceUnavailable, "tokens store not initialized")
		return
	}
	id := chiURLParam(r, "id")
	username := r.Header.Get("X-User")
	role := r.Header.Get("X-Role")
	if err := h.tokens.Delete(id, username, role == "admin"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "token.delete", id, nil))
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// chiURLParam is a thin wrapper to keep the imports tidy. Defined
// in groups.go to avoid duplication.
