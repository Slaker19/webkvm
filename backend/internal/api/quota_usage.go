package api

import (
	"net/http"

	"webkvm/internal/audit"
	"webkvm/internal/models"
)

// Quota usage reporting.
//
// checkQuota has always computed live usage in order to reject requests,
// but nothing ever surfaced those numbers. An admin setting "max 8 vCPU"
// could not see that the user was already running 6, and the user hitting
// the limit only learned about it as a refusal. These endpoints expose the
// figures the enforcement path already relies on.

// quotaDimension pairs a live usage figure with its limit. Limit 0 means
// unlimited, matching Quota's own convention.
type quotaDimension struct {
	Used  int64 `json:"used"`
	Limit int64 `json:"limit"`
}

// poolQuotaUsage is a per-pool disk figure.
type poolQuotaUsage struct {
	Pool  string `json:"pool"`
	Used  int64  `json:"used_gb"`
	Limit int64  `json:"limit_gb"`
}

// quotaUsageResponse is the body of the usage endpoints.
type quotaUsageResponse struct {
	Username string `json:"username"`
	// Enabled reports whether any quota dimension is set at all. When
	// false the limits are all zero and the used figures are still
	// meaningful, so the UI can show consumption without implying a cap.
	Enabled bool           `json:"enabled"`
	VMs     quotaDimension `json:"vms"`
	VCPUs   quotaDimension `json:"vcpus"`
	RAMMB   quotaDimension `json:"ram_mb"`
	DiskGB  quotaDimension `json:"disk_gb"`
	// Pools lists every pool the user consumes space in, plus every pool
	// carrying an explicit limit even when nothing is stored there yet:
	// a limit with no usage is exactly what an admin needs to see.
	Pools []poolQuotaUsage `json:"pools"`
}

// buildQuotaUsage assembles the live usage/limit report for a user.
func (h *Handler) buildQuotaUsage(username string) (quotaUsageResponse, error) {
	resp := quotaUsageResponse{Username: username, Pools: []poolQuotaUsage{}}

	used, err := h.usageOf(username)
	if err != nil {
		return resp, err
	}
	byPool, err := h.diskUsageByPool(username)
	if err != nil {
		return resp, err
	}

	var q models.Quota
	if h.userStore != nil {
		if u, err := h.userStore.Get(username); err == nil {
			q = u.Quota
			// Admins are exempt from quota entirely, so reporting their
			// limits would suggest a cap that will never be applied.
			if u.Role == models.RoleAdmin {
				q = models.Quota{}
			}
		}
	}

	resp.Enabled = q.Enabled()
	resp.VMs = quotaDimension{Used: used.VMs, Limit: int64(q.MaxVMs)}
	resp.VCPUs = quotaDimension{Used: used.VCPUs, Limit: int64(q.MaxVCPUs)}
	resp.RAMMB = quotaDimension{Used: used.RAMMB, Limit: int64(q.MaxRAMMB)}
	resp.DiskGB = quotaDimension{Used: used.Disk, Limit: int64(q.MaxDiskGB)}

	seen := map[string]bool{}
	for pool, gb := range byPool {
		if pool == "" {
			// Disk usage that could not be attributed to a pool. Folding
			// it into a named pool would misreport that pool's headroom.
			continue
		}
		seen[pool] = true
		resp.Pools = append(resp.Pools, poolQuotaUsage{
			Pool: pool, Used: gb, Limit: int64(q.PoolQuotas[pool]),
		})
	}
	for pool, limit := range q.PoolQuotas {
		if !seen[pool] {
			resp.Pools = append(resp.Pools, poolQuotaUsage{
				Pool: pool, Used: 0, Limit: int64(limit),
			})
		}
	}
	sortPoolUsage(resp.Pools)
	return resp, nil
}

func sortPoolUsage(p []poolQuotaUsage) {
	for i := 1; i < len(p); i++ {
		for j := i; j > 0 && p[j].Pool < p[j-1].Pool; j-- {
			p[j], p[j-1] = p[j-1], p[j]
		}
	}
}

// GetUserQuotaUsage reports a named user's quota consumption. Admin-only:
// the figures reveal how much of the host a given account is using.
func (h *Handler) GetUserQuotaUsage(w http.ResponseWriter, r *http.Request) {
	username := chiURLParam(r, "username")
	if username == "" {
		jsonErr(w, http.StatusBadRequest, "username is required")
		return
	}
	if h.userStore != nil {
		if _, err := h.userStore.Get(username); err != nil {
			jsonErr(w, http.StatusNotFound, "user not found")
			return
		}
	}
	resp, err := h.buildQuotaUsage(username)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, resp)
}

// GetMyQuotaUsage reports the caller's own consumption, so a user can see
// their headroom instead of discovering a limit by being refused.
func (h *Handler) GetMyQuotaUsage(w http.ResponseWriter, r *http.Request) {
	username, _, _ := audit.FromRequest(r)
	if username == "" {
		jsonErr(w, http.StatusUnauthorized, "authentication required")
		return
	}
	resp, err := h.buildQuotaUsage(username)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, resp)
}
