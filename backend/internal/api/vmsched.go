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
		StartCron string `json:"start_cron"`
		StopCron  string `json:"stop_cron"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	sch := vmsched.Schedule{StartCron: req.StartCron, StopCron: req.StopCron}
	if err := h.vmSchedStore.Set(id, sch); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.vmScheduler.Rebuild()
	h.audit.Log(auditFor(r, "vm.schedule_set", id, map[string]any{
		"start": req.StartCron,
		"stop":  req.StopCron,
	}))
	jsonResp(w, http.StatusOK, h.vmSchedStore.Get(id))
}

// PowerVMNow (operator/admin) starts/stops a VM immediately via the
// scheduler path (reused for testing a schedule without waiting).
func (h *Handler) PowerVMNow(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	action := chi.URLParam(r, "action")
	switch action {
	case "start":
		if err := h.lv.StartDomain(id); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	case "stop":
		if err := h.lv.ShutdownDomain(id); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	default:
		jsonErr(w, http.StatusBadRequest, "action must be start or stop")
		return
	}
	h.audit.Log(auditFor(r, "vm.schedule_manual", id, map[string]any{"action": action}))
	jsonResp(w, http.StatusOK, map[string]string{"status": action})
}
