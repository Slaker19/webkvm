package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"webkvm/internal/audit"
	"webkvm/internal/metrics"
	"webkvm/internal/models"
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
	// Each entry carries a vm_id, so the unfiltered list told any
	// viewer the id (and, through the rule, the name and thresholds) of
	// every VM on the host — including the ones their group/tag ACL is
	// meant to hide. Reuse filterVMsByACL so there is exactly one
	// definition of "VMs this caller may see".
	jsonResp(w, http.StatusOK, map[string]any{"alerts": h.filterAlertsByACL(r, h.alerter.Active())})
}

// filterAlertsByACL drops alerts whose vm_id the caller cannot see.
// Admins get everything; host-wide alerts (empty vm_id) are kept for
// everyone since they carry no per-VM information.
func (h *Handler) filterAlertsByACL(r *http.Request, alerts []map[string]any) []map[string]any {
	_, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin {
		return alerts
	}

	all, err := h.compute.ListDomains()
	if err != nil {
		// Fail closed: without the VM list there is no way to tell
		// which alerts are in scope, so show only host-wide ones.
		out := make([]map[string]any, 0, len(alerts))
		for _, a := range alerts {
			if vmID, _ := a["vm_id"].(string); vmID == "" {
				out = append(out, a)
			}
		}
		return out
	}
	visible := map[string]bool{}
	for _, vm := range h.filterVMsByACL(r, all) {
		visible[vm.ID] = true
	}

	out := make([]map[string]any, 0, len(alerts))
	for _, a := range alerts {
		vmID, _ := a["vm_id"].(string)
		if vmID == "" || visible[vmID] {
			out = append(out, a)
		}
	}
	return out
}

// AlertmanagerWebhook receives webhook payloads from Prometheus Alertmanager.
func (h *Handler) AlertmanagerWebhook(w http.ResponseWriter, r *http.Request) {
	if h.alerter == nil {
		jsonErr(w, http.StatusServiceUnavailable, "alert engine not initialized")
		return
	}

	_, _, ip := audit.FromRequest(r)
	if h.jail != nil && h.jail.IsBanned(ip) {
		jsonErr(w, http.StatusForbidden, "IP address temporarily banned")
		return
	}

	if h.settings != nil {
		expectedSecret := h.settings.GetString("alerts.webhook_secret")
		if expectedSecret != "" {
			authHeader := r.Header.Get("Authorization")
			token := strings.TrimPrefix(authHeader, "Bearer ")
			secretParam := r.URL.Query().Get("secret")
			customHeader := r.Header.Get("X-Webhook-Secret")
			if token != expectedSecret && secretParam != expectedSecret && customHeader != expectedSecret {
				if h.jail != nil {
					h.jail.RecordFailure(ip, "unauthorized alertmanager webhook")
				}
				jsonErr(w, http.StatusUnauthorized, "invalid webhook secret")
				return
			}
		}
	}

	var payload metrics.AlertmanagerPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid alertmanager payload: "+err.Error())
		return
	}
	ingested, err := h.alerter.IngestAlertmanager(payload)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"ingested": len(ingested),
	})
}

// ListIncidents returns all tracked incidents across the system.
func (h *Handler) ListIncidents(w http.ResponseWriter, r *http.Request) {
	if h.alerter == nil {
		jsonResp(w, http.StatusOK, map[string]any{"incidents": []any{}})
		return
	}
	incidents := h.alerter.Incidents()
	_, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin && h.compute != nil {
		if all, err := h.compute.ListDomains(); err == nil {
			visible := map[string]bool{}
			for _, vm := range h.filterVMsByACL(r, all) {
				visible[vm.ID] = true
			}
			filtered := make([]metrics.Incident, 0, len(incidents))
			for _, inc := range incidents {
				if inc.VMID == "" || visible[inc.VMID] {
					filtered = append(filtered, inc)
				}
			}
			incidents = filtered
		}
	}
	jsonResp(w, http.StatusOK, map[string]any{"incidents": incidents})
}

// ClearResolvedIncidents clears all resolved incidents from the feed. Admin only.
func (h *Handler) ClearResolvedIncidents(w http.ResponseWriter, r *http.Request) {
	if h.alerter == nil {
		jsonErr(w, http.StatusServiceUnavailable, "alert engine not initialized")
		return
	}
	h.alerter.ClearResolvedIncidents()
	if h.audit != nil {
		h.audit.Log(auditFor(r, "alerts.incidents.clear_resolved", "", nil))
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}
