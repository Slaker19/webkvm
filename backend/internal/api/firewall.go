package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"webkvm/internal/firewall"
)

// GetVMFirewall returns the firewall rules + port forwards for a VM,
// with the current applied/pending state of each forward.
func (h *Handler) GetVMFirewall(w http.ResponseWriter, r *http.Request) {
	if h.fwStore == nil || h.fwMgr == nil {
		jsonErr(w, http.StatusServiceUnavailable, "firewall subsystem not initialized")
		return
	}
	id := chiURLParam(r, "id")
	jsonResp(w, http.StatusOK, h.fwStore.Get(id))
}

// SetVMFirewall (admin) replaces the rules + forwards for a VM and
// re-applies the whole ruleset atomically.
func (h *Handler) SetVMFirewall(w http.ResponseWriter, r *http.Request) {
	if h.fwStore == nil || h.fwMgr == nil {
		jsonErr(w, http.StatusServiceUnavailable, "firewall subsystem not initialized")
		return
	}
	id := chiURLParam(r, "id")
	var req struct {
		Rules    []firewall.Rule    `json:"rules"`
		Forwards []firewall.Forward `json:"forwards"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	// Strict validation: a rule with an unknown protocol or an
	// out-of-range port would silently never apply — refuse it so the
	// operator knows the rule won't work.
	for _, r := range req.Rules {
		if r.Proto != "tcp" && r.Proto != "udp" && r.Proto != "both" {
			jsonErr(w, http.StatusBadRequest, "invalid rule protocol "+strconv.Quote(r.Proto)+" (use tcp, udp or both)")
			return
		}
		if r.Port < 1 || r.Port > 65535 {
			jsonErr(w, http.StatusBadRequest, "rule port must be 1-65535")
			return
		}
		if r.Action != "allow" && r.Action != "drop" {
			jsonErr(w, http.StatusBadRequest, "rule action must be allow or drop")
			return
		}
	}
	for _, f := range req.Forwards {
		if f.Proto != "tcp" && f.Proto != "udp" && f.Proto != "both" {
			jsonErr(w, http.StatusBadRequest, "invalid forward protocol "+strconv.Quote(f.Proto)+" (use tcp, udp or both)")
			return
		}
		if f.HostPort < 1 || f.HostPort > 65535 || f.GuestPort < 1 || f.GuestPort > 65535 {
			jsonErr(w, http.StatusBadRequest, "forward ports must be 1-65535")
			return
		}
	}
	fw := firewall.VMFirewall{VMID: id, Rules: req.Rules, Forwards: req.Forwards}
	if err := h.fwStore.Set(fw); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	pending, err := h.fwMgr.Apply()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "vm.firewall", id, map[string]any{
			"rules":    len(req.Rules),
			"forwards": len(req.Forwards),
			"pending":  pending[id],
		}))
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"vm":      h.fwStore.Get(id),
		"pending": pending,
	})
}

// --- Host firewall (V13-C-01 / V13-C-02) ---

// fwHosts guards the host-firewall handlers: nil manager/store → 503.
func (h *Handler) fwHostReady() bool {
	return h.fwMgr != nil
}

// GetHostFirewall returns the confirmed host ruleset, the protected
// (non-deletable) management ports, and any in-flight Safe-Apply.
func (h *Handler) GetHostFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.fwHostReady() {
		jsonErr(w, http.StatusServiceUnavailable, "firewall subsystem not initialized")
		return
	}
	fw, ok := h.fwMgr.HostRules()
	if !ok {
		jsonErr(w, http.StatusServiceUnavailable, "host firewall store not initialized")
		return
	}
	resp := map[string]any{
		"firewall":        fw,
		"protected_ports": firewall.ProtectedPorts(h.cfg.Port),
	}
	if next, deadline, hasPending := h.fwMgr.PendingApply(); hasPending {
		resp["pending"] = map[string]any{
			"firewall": next,
			"deadline": deadline.Unix(),
		}
	}
	jsonResp(w, http.StatusOK, resp)
}

// hostFirewallRequest is the JSON body shared by preview/apply.
type hostFirewallRequest struct {
	Firewall firewall.HostFirewall `json:"firewall"`
}

// PreviewHostFirewall renders the exact nftables ruleset that would be
// applied, WITHOUT touching the kernel or the store. Lets the editor
// show what the rules compile to before the operator commits.
func (h *Handler) PreviewHostFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.fwHostReady() {
		jsonErr(w, http.StatusServiceUnavailable, "firewall subsystem not initialized")
		return
	}
	var req hostFirewallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	ruleset, err := h.fwMgr.RenderRuleset(req.Firewall)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"ruleset": ruleset})
}

// ApplyHostFirewall (admin) implements the Safe-Apply protocol: it
// validates, renders and applies the ruleset atomically, then starts a
// 30s confirm window. The response carries the deadline; the operator
// must POST confirm within it or the backend auto-rolls-back.
func (h *Handler) ApplyHostFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.fwHostReady() {
		jsonErr(w, http.StatusServiceUnavailable, "firewall subsystem not initialized")
		return
	}
	var req hostFirewallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	prev, deadline, err := h.fwMgr.StageHostApply(req.Firewall)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "firewall.host.apply", "webkvm", map[string]any{
			"input_rules": len(req.Firewall.Input), "forward_rules": len(req.Firewall.Forwards),
			"deadline": deadline.Unix(),
		}))
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"status":      "pending_confirm",
		"deadline":    deadline.Unix(),
		"window_secs": int(time.Until(deadline).Seconds()),
		"previous":    prev,
	})
}

// ConfirmHostFirewall (admin) confirms the staged Safe-Apply, making
// the rules the new persistent baseline.
func (h *Handler) ConfirmHostFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.fwHostReady() {
		jsonErr(w, http.StatusServiceUnavailable, "firewall subsystem not initialized")
		return
	}
	if err := h.fwMgr.ConfirmHostApply(); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	fw, _ := h.fwMgr.HostRules()
	if h.audit != nil {
		h.audit.Log(auditFor(r, "firewall.host.confirm", "webkvm", map[string]any{
			"input_rules": len(fw.Input), "forward_rules": len(fw.Forwards),
		}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"status": "confirmed", "firewall": fw})
}

// RollbackHostFirewall (admin) restores the previous ruleset and clears
// the pending state. Also the target of the UI's "Rollback" button.
func (h *Handler) RollbackHostFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.fwHostReady() {
		jsonErr(w, http.StatusServiceUnavailable, "firewall subsystem not initialized")
		return
	}
	rolled, err := h.fwMgr.RollbackHostApply()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !rolled {
		jsonResp(w, http.StatusOK, map[string]any{"status": "no_pending"})
		return
	}
	fw, _ := h.fwMgr.HostRules()
	if h.audit != nil {
		h.audit.Log(auditFor(r, "firewall.host.rollback", "webkvm", map[string]any{
			"input_rules": len(fw.Input), "forward_rules": len(fw.Forwards),
		}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"status": "rolled_back", "firewall": fw})
}

// ExportHostFirewall (admin) returns the current CONFIRMED host
// firewall as a portable JSON payload (the same shape Import consumes).
// The frontend saves it as a .json file for backup/migration.
func (h *Handler) ExportHostFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.fwHostReady() {
		jsonErr(w, http.StatusServiceUnavailable, "firewall subsystem not initialized")
		return
	}
	fw, ok := h.fwMgr.HostRules()
	if !ok {
		jsonErr(w, http.StatusServiceUnavailable, "host firewall store not initialized")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="webkvm-firewall.json"`)
	jsonResp(w, http.StatusOK, fw)
}

// ImportHostFirewall (admin) applies an uploaded firewall JSON through
// the EXACT same hardened chain as the manual editor (V13-D-03):
// strict validation incl. anti-lockout, atomic nft -c + nft -f, and the
// Safe-Apply protocol with a 30s confirm window / auto-rollback. The
// body may be either a bare HostFirewall {"input":…,"forwards":…} or the
// exported {"firewall":{…}} envelope.
func (h *Handler) ImportHostFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.fwHostReady() {
		jsonErr(w, http.StatusServiceUnavailable, "firewall subsystem not initialized")
		return
	}
	// Max 1 MB — a firewall config is a handful of rules; anything bigger
	// is not a legitimate import.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	var fw firewall.HostFirewall
	if envelope, ok := raw["firewall"]; ok {
		if err := json.Unmarshal(envelope, &fw); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid firewall payload: "+err.Error())
			return
		}
	} else {
		data, _ := json.Marshal(raw)
		if err := json.Unmarshal(data, &fw); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid firewall payload: "+err.Error())
			return
		}
	}
	// StageHostApply = ValidateHostFirewall (anti-lockout) + atomic
	// nft apply + Safe-Apply window. Exactly the editor's chain.
	prev, deadline, err := h.fwMgr.StageHostApply(fw)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "firewall.host.import", "webkvm", map[string]any{
			"input_rules": len(fw.Input), "forward_rules": len(fw.Forwards),
			"deadline": deadline.Unix(),
		}))
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"status":      "pending_confirm",
		"deadline":    deadline.Unix(),
		"window_secs": int(time.Until(deadline).Seconds()),
		"previous":    prev,
	})
}
