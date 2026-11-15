package api

import (
	"log/slog"
	"net/http"

	computeincus "webkvm/internal/compute/incus"
)

// GetIncusClusterMembers returns the members of the local Incus cluster with
// their current status. Read-only and administrator-only.
func (h *Handler) GetIncusClusterMembers(w http.ResponseWriter, r *http.Request) {
	// Like GetHACompat: with Incus disabled in the config, do not even try
	// to open its socket.
	if h.cfg == nil || !h.cfg.IncusEnabled {
		jsonResp(w, http.StatusOK, map[string]any{
			"clustered": false,
			"members":   []computeincus.ClusterMember{},
		})
		return
	}
	backend, err := computeincus.NewIncusBackend("")
	if err != nil {
		// Incus may not be installed or enabled on a KVM-only host.
		jsonResp(w, http.StatusOK, map[string]any{
			"clustered": false,
			"members":   []computeincus.ClusterMember{},
		})
		return
	}
	defer backend.Close()

	members, err := backend.ClusterMembers()
	if err != nil {
		slog.Warn("incus_cluster_members_failed", "err", err)
		jsonErr(w, http.StatusBadGateway, "cannot read the Incus cluster members")
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"clustered": len(members) > 0,
		"members":   members,
	})
}
