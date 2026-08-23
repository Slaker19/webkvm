package api

import (
	"encoding/json"
	"net/http"
	"strconv"

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
