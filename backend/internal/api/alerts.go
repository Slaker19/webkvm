package api

import (
	"encoding/json"
	"net/http"

	"webkvm/internal/metrics"
)

// GetVMAlerterRules (V13-C-04) returns the alert rules applicable to a
// VM plus their live state machine status.
func (h *Handler) GetVMAlerterRules(w http.ResponseWriter, r *http.Request) {
	if h.alerter == nil {
		jsonErr(w, http.StatusServiceUnavailable, "alert engine not initialized")
		return
	}
	id := chiURLParam(r, "id")
	jsonResp(w, http.StatusOK, map[string]any{
		"rules":    h.alerter.RulesFor(id),
		"statuses": h.alerter.Statuses(id),
	})
}

// SetVMAlerterRules (admin) replaces the alert rules for a VM.
func (h *Handler) SetVMAlerterRules(w http.ResponseWriter, r *http.Request) {
	if h.alerter == nil {
		jsonErr(w, http.StatusServiceUnavailable, "alert engine not initialized")
		return
	}
	id := chiURLParam(r, "id")
	var req struct {
		Rules []metrics.AlertRule `json:"rules"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	// The UI edits per-VM rules: force VMID on each so a rule configured
	// on one VM never silently applies to the fleet.
	for i := range req.Rules {
		req.Rules[i].VMID = id
	}
	if err := h.alerter.SetRules(id, req.Rules); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "vm.alert.rules", id, map[string]any{"rules": len(req.Rules)}))
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"rules":    h.alerter.RulesFor(id),
		"statuses": h.alerter.Statuses(id),
	})
}

// ListActiveAlerts returns every currently-FIRING alert across the fleet,
// for the notification badge.
func (h *Handler) ListActiveAlerts(w http.ResponseWriter, r *http.Request) {
	if h.alerter == nil {
		jsonResp(w, http.StatusOK, map[string]any{"alerts": []any{}})
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"alerts": h.alerter.Active()})
}
