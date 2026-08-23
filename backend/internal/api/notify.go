package api

import (
	"encoding/json"
	"net/http"

	"webkvm/internal/notify"
)

// GetNotifyConfig returns the notification configuration WITHOUT any
// secrets — only booleans indicating whether each secret is set. This
// guarantees credentials never reach the browser.
func (h *Handler) GetNotifyConfig(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		jsonErr(w, http.StatusServiceUnavailable, "notifications not initialized")
		return
	}
	jsonResp(w, http.StatusOK, h.notifier.Status())
}

// UpdateNotifyConfig (admin) applies a notification config mutation.
// Security: secret fields that arrive empty are treated as "keep the
// existing value", so a routine form save never clears a stored
// credential. To clear all secrets the client sends clear_secret:true.
func (h *Handler) UpdateNotifyConfig(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		jsonErr(w, http.StatusServiceUnavailable, "notifications not initialized")
		return
	}
	var req struct {
		Config         notify.Config `json:"config"`
		WebhookSecret  string        `json:"webhook_secret"`
		SMTPUser       string        `json:"smtp_user"`
		SMTPPassword   string        `json:"smtp_password"`
		ClearSecret    bool          `json:"clear_secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if err := h.notifier.Update(req.Config, req.WebhookSecret, req.SMTPUser, req.SMTPPassword, req.ClearSecret); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "notify.config_update", "", map[string]any{
			"webhook_enabled": req.Config.WebhookEnabled,
			"smtp_enabled":    req.Config.SMTPEnabled,
		}))
	}
	jsonResp(w, http.StatusOK, h.notifier.Status())
}

// TestNotify sends a test alert to confirm the configured channels.
func (h *Handler) TestNotify(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		jsonErr(w, http.StatusServiceUnavailable, "notifications not initialized")
		return
	}
	if err := h.notifier.SendTest(); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"ok": true, "message": "test alert sent"})
}

// ListNotifyEvents returns the recent alert history (newest first).
func (h *Handler) ListNotifyEvents(w http.ResponseWriter, r *http.Request) {
	if h.notifier == nil {
		jsonErr(w, http.StatusServiceUnavailable, "notifications not initialized")
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"events": h.notifier.Events()})
}
