package api

import (
	"encoding/json"
	"net/http"

	"webkvm/internal/vmsched"

	"github.com/go-chi/chi/v5"
)

// GetVMSchedule returns a VM's power schedule (empty object = none).
func (h *Handler) GetVMSchedule(w http.ResponseWriter, r *http.Request) {
	if h.vmSchedStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "scheduler not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	jsonResp(w, http.StatusOK, h.vmSchedStore.Get(id))
}

// SetVMSchedule (operator/admin) sets or clears a VM's power schedule.
func (h *Handler) SetVMSchedule(w http.ResponseWriter, r *http.Request) {
	if h.vmSchedStore == nil || h.vmScheduler == nil {
		jsonErr(w, http.StatusServiceUnavailable, "scheduler not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	var req struct {
		StartCron    string `json:"start_cron"`
		StopCron     string `json:"stop_cron"`
		SnapshotCron string `json:"snapshot_cron"`
		SnapshotMax  int    `json:"snapshot_max"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	existing := h.vmSchedStore.Get(id)
	// Changing or setting power schedule requires "control_power"
	if req.StartCron != existing.StartCron || req.StopCron != existing.StopCron {
		if err := h.requirePermission(r, "control_power"); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
	}
	// Changing or setting snapshot schedule requires "snapshots"
	if req.SnapshotCron != existing.SnapshotCron || req.SnapshotMax != existing.SnapshotMax {
		if err := h.requirePermission(r, "snapshots"); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
	}

	sch := vmsched.Schedule{
		StartCron:    req.StartCron,
		StopCron:     req.StopCron,
		SnapshotCron: req.SnapshotCron,
		SnapshotMax:  req.SnapshotMax,
	}
	if err := h.vmSchedStore.Set(id, sch); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.vmScheduler.Rebuild()
	h.audit.Log(auditFor(r, "vm.schedule_set", id, map[string]any{
		"start":    req.StartCron,
		"stop":     req.StopCron,
		"snapshot": req.SnapshotCron,
		"max_snap": req.SnapshotMax,
	}))
	jsonResp(w, http.StatusOK, h.vmSchedStore.Get(id))
}

// PowerVMNow (operator/admin) starts/stops a VM immediately via the
// scheduler path (reused for testing a schedule without waiting).
func (h *Handler) PowerVMNow(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	action := chi.URLParam(r, "action")
	switch action {
	case "start":
		if err := h.checkStartQuota(id); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		if err := h.compute.StartDomain(id); err != nil {
			h.vmActionErr(w, err, humanizeStartError)
			return
		}
		if h.fwMgr != nil {
			if _, ferr := h.fwMgr.Apply(); ferr != nil {
				h.logError("firewall_reapply_after_start_failed", ferr, id)
			}
		}
		h.audit.Log(auditFor(r, "vm.start", id, map[string]any{"trigger": "schedule_manual"}))
	case "stop":
		if err := h.compute.ShutdownDomain(id); err != nil {
			h.vmActionErr(w, err, nil)
			return
		}
		h.audit.Log(auditFor(r, "vm.shutdown", id, map[string]any{"trigger": "schedule_manual"}))
	default:
		jsonErr(w, http.StatusBadRequest, "action must be start or stop")
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": action})
}
