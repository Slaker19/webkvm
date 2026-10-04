package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"webkvm/internal/netguard"
)

// ListJailedIPs returns all currently banned IPs.
func (h *Handler) ListJailedIPs(w http.ResponseWriter, r *http.Request) {
	if h.jail == nil {
		jsonResp(w, http.StatusOK, []any{})
		return
	}
	jsonResp(w, http.StatusOK, h.jail.ListBanned())
}

// ManualBanIP bans an IP address manually.
func (h *Handler) ManualBanIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP          string `json:"ip"`
		Reason      string `json:"reason"`
		Jail        string `json:"jail"`
		DurationSec int    `json:"duration_sec"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.jail == nil {
		jsonErr(w, http.StatusServiceUnavailable, "jail not initialized")
		return
	}

	duration := time.Duration(req.DurationSec) * time.Second
	if duration <= 0 {
		duration = 24 * time.Hour
	}
	if req.Reason == "" {
		req.Reason = "Manual ban by administrator"
	}
	if req.Jail == "" {
		req.Jail = "manual"
	}

	if err := h.jail.ManualBan(req.IP, req.Reason, req.Jail, duration); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.audit != nil {
		h.audit.Log(auditFor(r, "jail.manual_ban", req.IP, map[string]any{
			"reason":   req.Reason,
			"jail":     req.Jail,
			"duration": duration.String(),
		}))
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"status":   "banned",
		"ip":       req.IP,
		"reason":   req.Reason,
		"jail":     req.Jail,
		"duration": duration.String(),
	})
}

// UnbanJailedIP unbans an IP manually.
func (h *Handler) UnbanJailedIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP string `json:"ip"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.jail == nil {
		jsonErr(w, http.StatusServiceUnavailable, "jail not initialized")
		return
	}
	if err := h.jail.Unban(req.IP); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if h.audit != nil {
		h.audit.Log(auditFor(r, "jail.unban", req.IP, nil))
	}

	jsonResp(w, http.StatusOK, map[string]string{"status": "unbanned", "ip": req.IP})
}

// GetJailWhitelist returns the unbannable CIDR / IP list.
func (h *Handler) GetJailWhitelist(w http.ResponseWriter, r *http.Request) {
	if h.jail == nil {
		jsonResp(w, http.StatusOK, map[string]any{"whitelist": []any{}})
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"whitelist": h.jail.GetWhitelist()})
}

// AddJailWhitelist adds a CIDR or IP to the whitelist.
func (h *Handler) AddJailWhitelist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CIDR        string `json:"cidr"`
		Description string `json:"description"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.jail == nil {
		jsonErr(w, http.StatusServiceUnavailable, "jail not initialized")
		return
	}

	if err := h.jail.AddWhitelist(req.CIDR, req.Description); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.audit != nil {
		h.audit.Log(auditFor(r, "jail.whitelist_add", req.CIDR, map[string]any{
			"description": req.Description,
		}))
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"status":    "added",
		"whitelist": h.jail.GetWhitelist(),
	})
}

// RemoveJailWhitelist removes a CIDR or IP from the whitelist.
func (h *Handler) RemoveJailWhitelist(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CIDR string `json:"cidr"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.jail == nil {
		jsonErr(w, http.StatusServiceUnavailable, "jail not initialized")
		return
	}

	if err := h.jail.RemoveWhitelist(req.CIDR); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.audit != nil {
		h.audit.Log(auditFor(r, "jail.whitelist_remove", req.CIDR, nil))
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"status":    "removed",
		"whitelist": h.jail.GetWhitelist(),
	})
}

// GetJailConfig returns complete configuration for WebKVM jail, SSH jail, and custom jails.
func (h *Handler) GetJailConfig(w http.ResponseWriter, r *http.Request) {
	if h.jail == nil {
		jsonResp(w, http.StatusOK, netguard.DefaultConfig())
		return
	}
	jsonResp(w, http.StatusOK, h.jail.GetConfig())
}

// UpdateJailConfig updates jail configurations.
func (h *Handler) UpdateJailConfig(w http.ResponseWriter, r *http.Request) {
	var cfg netguard.JailConfig
	if err := decodeBody(r, &cfg); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.jail == nil {
		jsonErr(w, http.StatusServiceUnavailable, "jail not initialized")
		return
	}

	if err := h.jail.UpdateConfig(cfg); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if h.audit != nil {
		h.audit.Log(auditFor(r, "jail.config_update", "", map[string]any{
			"enabled":     cfg.Enabled,
			"ssh_enabled": cfg.SSHJail.Enabled,
			"web_enabled": cfg.WebKVMJail.Enabled,
		}))
	}

	jsonResp(w, http.StatusOK, h.jail.GetConfig())
}

// AddCustomJail adds a new custom jail definition.
func (h *Handler) AddCustomJail(w http.ResponseWriter, r *http.Request) {
	var req netguard.JailDefinition
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.jail == nil {
		jsonErr(w, http.StatusServiceUnavailable, "jail not initialized")
		return
	}

	if err := h.jail.AddCustomJail(req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.audit != nil {
		h.audit.Log(auditFor(r, "jail.custom_add", req.Name, map[string]any{
			"id": req.ID,
		}))
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"status": "created",
		"config": h.jail.GetConfig(),
	})
}

// DeleteCustomJail removes a custom jail definition.
func (h *Handler) DeleteCustomJail(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		jsonErr(w, http.StatusBadRequest, "missing jail id")
		return
	}
	if h.jail == nil {
		jsonErr(w, http.StatusServiceUnavailable, "jail not initialized")
		return
	}

	if err := h.jail.DeleteCustomJail(id); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if h.audit != nil {
		h.audit.Log(auditFor(r, "jail.custom_delete", id, nil))
	}

	jsonResp(w, http.StatusOK, map[string]any{
		"status": fmt.Sprintf("jail %s deleted", id),
		"config": h.jail.GetConfig(),
	})
}
