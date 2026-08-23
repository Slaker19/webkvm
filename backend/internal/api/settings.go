package api

import (
	"encoding/json"
	"net/http"

	"webkvm/internal/configstore"
)

// SettingsSchemaResponse is the GET /api/settings/schema response.
type SettingsSchemaResponse struct {
	Schema configstore.Schema `json:"schema"`
}

// SettingsGetResponse is the GET /api/settings response.
type SettingsGetResponse struct {
	Values         map[string]interface{} `json:"values"`
	PendingRestart []string               `json:"pending_restart"`
}

// SettingsSetRequest is the PUT /api/settings request body.
type SettingsSetRequest struct {
	Values map[string]interface{} `json:"values"`
}

// SettingsSetResponse is the PUT /api/settings response.
type SettingsSetResponse struct {
	Applied        []string             `json:"applied"`
	Failed         map[string]string    `json:"failed,omitempty"`
	PendingRestart []string             `json:"pending_restart"`
}

// SettingsResetResponse is the POST /api/settings/reset response.
type SettingsResetResponse struct {
	OK             bool     `json:"ok"`
	PendingRestart []string `json:"pending_restart"`
}

// GetSettingsSchema returns the field schema. Read-only, viewer+.
func (h *Handler) GetSettingsSchema(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		jsonErr(w, http.StatusServiceUnavailable, "settings store not initialized")
		return
	}
	jsonResp(w, http.StatusOK, SettingsSchemaResponse{Schema: h.settings.Schema()})
}

// GetSettings returns the current values, with secrets masked.
func (h *Handler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		jsonErr(w, http.StatusServiceUnavailable, "settings store not initialized")
		return
	}
	jsonResp(w, http.StatusOK, SettingsGetResponse{
		Values:         h.settings.Snapshot(),
		PendingRestart: h.settings.PendingRestart(),
	})
}

// SetSettings applies a batch of values. Admin only.
func (h *Handler) SetSettings(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		jsonErr(w, http.StatusServiceUnavailable, "settings store not initialized")
		return
	}
	var req SettingsSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	applied, failed, err := h.settings.SetMany(req.Values)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "save failed: "+err.Error())
		return
	}
	if len(failed) > 0 {
		jsonResp(w, http.StatusBadRequest, SettingsSetResponse{Failed: failed})
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "settings.update", "config", map[string]any{"keys": applied}))
	}
	jsonResp(w, http.StatusOK, SettingsSetResponse{
		Applied:        applied,
		PendingRestart: h.settings.PendingRestart(),
	})
}

// ResetSettings restores every value to its default. Admin only.
func (h *Handler) ResetSettings(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		jsonErr(w, http.StatusServiceUnavailable, "settings store not initialized")
		return
	}
	if err := h.settings.Reset(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "reset failed: "+err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "settings.reset", "config", nil))
	}
	jsonResp(w, http.StatusOK, SettingsResetResponse{
		OK:             true,
		PendingRestart: h.settings.PendingRestart(),
	})
}
