package api

import (
	"context"
	"encoding/json"
	"net/http"

	computeincus "webkvm/internal/compute/incus"
	"webkvm/internal/ha"
	"webkvm/internal/sysutil"
)

type incusHealingResponse struct {
	Available        bool     `json:"available"`
	Enabled          bool     `json:"enabled"`
	ThresholdSeconds int64    `json:"threshold_seconds"`
	MembersOnline    int      `json:"members_online"`
	MembersTotal     int      `json:"members_total"`
	CanChange        bool     `json:"can_change"`
	Blockers         []string `json:"blockers"`
}

// incusHealingGuards reports why automatic healing must not be enabled yet.
// Healing evacuates an instance from an offline member, so it is only safe
// once the cluster has quorum, a tested watchdog fence, shared workload
// storage and every member online.
func (h *Handler) incusHealingGuards(ctx context.Context, backend *computeincus.IncusBackend) (online, total int, blockers []string) {
	blockers = []string{}
	if !h.clusterHAEnabled() {
		// Automatic healing depends on fencing and quorum; without the
		// optional module there is nothing to probe and nothing to enable.
		return 0, 0, []string{"cluster_ha_module_disabled"}
	}
	pacemaker := ha.ProbePacemaker(ctx, sysutil.DefaultRunner)
	if pacemaker.Quorum == nil || !*pacemaker.Quorum {
		blockers = append(blockers, "pacemaker_quorum_missing")
	}
	if pacemaker.FencingEnabled == nil || !*pacemaker.FencingEnabled {
		blockers = append(blockers, "pacemaker_fencing_unconfigured")
	}
	if pacemaker.WatchdogFencing == nil || !*pacemaker.WatchdogFencing {
		blockers = append(blockers, "incus_healing_watchdog_required")
	}
	if !h.fencingRecorded(pacemaker) {
		blockers = append(blockers, "fencing_test_required")
	}
	if !h.hasSharedWorkloadStorage() {
		blockers = append(blockers, "shared_storage_not_verified")
	}
	members, err := backend.ClusterMembers()
	if err != nil {
		return 0, 0, append(blockers, "incus_healing_members_unavailable")
	}
	for _, member := range members {
		if member.Status == "Online" {
			online++
		}
	}
	total = len(members)
	if total < 2 || online < total {
		blockers = append(blockers, "incus_healing_members_offline")
	}
	return online, total, blockers
}

// GetIncusHealing reports the local cluster's healing setting and the guards.
func (h *Handler) GetIncusHealing(w http.ResponseWriter, r *http.Request) {
	backend, err := computeincus.NewIncusBackend("")
	if err != nil {
		jsonResp(w, http.StatusOK, incusHealingResponse{
			Available: false, Blockers: []string{"incus_not_clustered"},
		})
		return
	}
	defer backend.Close()

	healing, err := backend.ClusterHealing()
	if err != nil {
		jsonResp(w, http.StatusOK, incusHealingResponse{
			Available: false, Blockers: []string{"incus_not_clustered"},
		})
		return
	}
	online, total, blockers := h.incusHealingGuards(r.Context(), backend)
	jsonResp(w, http.StatusOK, incusHealingResponse{
		Available:        true,
		Enabled:          healing.Enabled,
		ThresholdSeconds: healing.ThresholdSeconds,
		MembersOnline:    online,
		MembersTotal:     total,
		CanChange:        len(blockers) == 0,
		Blockers:         blockers,
	})
}

// UpdateIncusHealing enables healing with a threshold, or disables it with 0.
func (h *Handler) UpdateIncusHealing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ThresholdSeconds int64 `json:"threshold_seconds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.ThresholdSeconds != 0 && (req.ThresholdSeconds < 60 || req.ThresholdSeconds > 86400) {
		jsonErr(w, http.StatusBadRequest, "threshold_seconds must be 0 or between 60 and 86400")
		return
	}

	// Pacemaker is the only HA engine: native Incus healing may only be
	// turned off, never on.
	if req.ThresholdSeconds > 0 {
		jsonResp(w, http.StatusConflict, map[string]any{
			"error": "automatic healing is managed by Pacemaker; enable HA on the instance instead",
			"code":  "healing_managed_by_pacemaker",
		})
		return
	}

	backend, err := computeincus.NewIncusBackend("")
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "Incus is not available")
		return
	}
	defer backend.Close()

	healing, err := backend.SetClusterHealing(req.ThresholdSeconds)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "update Incus healing: "+err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "incus_healing_update", "incus", map[string]any{
			"threshold_seconds": healing.ThresholdSeconds,
		}))
	}
	online, total, blockers := h.incusHealingGuards(r.Context(), backend)
	jsonResp(w, http.StatusOK, incusHealingResponse{
		Available:        true,
		Enabled:          healing.Enabled,
		ThresholdSeconds: healing.ThresholdSeconds,
		MembersOnline:    online,
		MembersTotal:     total,
		CanChange:        len(blockers) == 0,
		Blockers:         blockers,
	})
}

// incusHealingProbe reports the native Incus healing threshold in seconds.
// ok is false when Incus is absent or not clustered. It is a variable so
// tests do not need a real Incus.
var incusHealingProbe = func(context.Context) (threshold int64, ok bool) {
	backend, err := computeincus.NewIncusBackend("")
	if err != nil {
		return 0, false
	}
	defer backend.Close()
	healing, err := backend.ClusterHealing()
	if err != nil {
		return 0, false
	}
	return healing.ThresholdSeconds, true
}
