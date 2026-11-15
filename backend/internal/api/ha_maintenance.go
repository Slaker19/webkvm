package api

import (
	"net/http"
	"time"

	"webkvm/internal/ha"
	"webkvm/internal/sysutil"
)

// MaintenanceHAWorkload POST /workloads/{kind}/{name}/maintenance
// {action: begin|end, ttl_s}. begin unmanages the workload (Pacemaker stops
// reacting to it) with an expiry; end re-manages and verifies it is Started.
// WebKVM never ends a window by itself: an expired one only raises the
// workload_unmanaged_expired readiness warning.
func (h *Handler) MaintenanceHAWorkload(w http.ResponseWriter, r *http.Request) {
	key, bad := keyFromRequest(r)
	if bad != nil {
		writeAPIErr(w, bad)
		return
	}
	var req struct {
		Action string `json:"action"`
		TTLS   int64  `json:"ttl_s"`
	}
	if err := decodeBody(r, &req); err != nil || (req.Action != "begin" && req.Action != "end") {
		jsonErrCode(w, http.StatusBadRequest, "invalid_body", "action must be begin or end")
		return
	}
	opts := ha.MaintOptions{CPGroup: h.cpGroup(), Timeout: workloadTuning.StartTimeout, PollInterval: workloadTuning.Poll}
	var (
		res ha.MaintResult
		err error
	)
	if req.Action == "begin" {
		ttl := time.Duration(req.TTLS) * time.Second
		if req.TTLS == 0 {
			ttl = time.Hour
		}
		res, err = ha.BeginMaintenance(r.Context(), sysutil.DefaultRunner, key, ttl, opts)
	} else {
		res, err = ha.EndMaintenance(r.Context(), sysutil.DefaultRunner, key, opts)
	}
	if err != nil {
		e := opToAPI(err)
		h.auditWorkload(r, "ha_workload.maintenance", key, map[string]any{"action": req.Action, "result": "error", "code": e.Code})
		writeAPIErr(w, e)
		return
	}
	h.auditWorkload(r, "ha_workload.maintenance", key, map[string]any{"action": req.Action, "result": "ok", "changed": res.Changed, "until": res.Until})
	jsonResp(w, http.StatusOK, map[string]any{"changed": res.Changed, "state": res.State, "until": res.Until})
}
