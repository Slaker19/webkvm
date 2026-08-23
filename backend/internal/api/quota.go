package api

import (
	"fmt"

	"webkvm/internal/models"
)

// quotaUsage is the current resource consumption of one owner.
type quotaUsage struct {
	VMs   int64
	VCPUs int64
	RAMMB int64
	Disk  int64 // GB
}

// ownerOf returns the owner_id recorded in a VM's metadata ("" = none).
func (h *Handler) ownerOf(vmID string) string {
	if h.lv == nil {
		return ""
	}
	meta, err := h.lv.GetVMMeta(vmID)
	if err != nil {
		return ""
	}
	return meta.OwnerID
}

// usageOf sums the resources of every VM owned by username.
// Admin-created VMs with no owner are not counted for any user.
func (h *Handler) usageOf(username string) (quotaUsage, error) {
	var u quotaUsage
	if h.lv == nil {
		return u, nil
	}
	vms, err := h.lv.ListDomains()
	if err != nil {
		return u, err
	}
	for _, vm := range vms {
		if h.ownerOf(vm.ID) != username {
			continue
		}
		u.VMs++
		u.VCPUs += int64(vm.VCPUs)
		u.RAMMB += vm.RAMMB
		u.Disk += vm.DiskGB
	}
	return u, nil
}

// effectiveQuota returns the quota that applies to a user. Admins are
// exempt (unlimited). Non-admin users use their own quota.
func (h *Handler) effectiveQuota(username string) (models.Quota, error) {
	if h.userStore == nil {
		return models.Quota{}, nil
	}
	u, err := h.userStore.Get(username)
	if err != nil {
		return models.Quota{}, fmt.Errorf("user %q not found", username)
	}
	if u.Role == models.RoleAdmin {
		return models.Quota{}, nil // admins unlimited
	}
	return u.Quota, nil
}

// bytesToGB converts a byte count into a rounded-up GB count used for
// quota accounting. Note the explicit parens: `n/(1<<30)` — Go's shift
// operators share the multiplication precedence, so `n/1<<30` would be
// `(n/1)<<30` = n*1GB, a completely wrong magnitude.
func bytesToGB(n int64) int64 {
	if n <= 0 {
		return 0
	}
	return n/(1<<30) + 1
}

// checkQuota verifies that adding the given resources for username will
// not exceed the user's quota. A zero quota dimension means unlimited.
// It returns a descriptive error when a limit would be exceeded.
func (h *Handler) checkQuota(username string, addVMs, addVCPUs, addRAMMB, addDiskGB int64) error {
	if h.userStore == nil || username == "" {
		return nil
	}
	// Admins are always exempt.
	u, err := h.userStore.Get(username)
	if err != nil {
		return nil
	}
	if u.Role == models.RoleAdmin {
		return nil
	}
	q := u.Quota
	if !q.Enabled() {
		return nil
	}
	cur, err := h.usageOf(username)
	if err != nil {
		return err
	}
	if q.MaxVMs > 0 && cur.VMs+addVMs > int64(q.MaxVMs) {
		return fmt.Errorf("quota exceeded: %s has %d/%d VMs", username, cur.VMs, q.MaxVMs)
	}
	if q.MaxVCPUs > 0 && cur.VCPUs+addVCPUs > int64(q.MaxVCPUs) {
		return fmt.Errorf("quota exceeded: %s uses %d/%d vCPUs", username, cur.VCPUs, q.MaxVCPUs)
	}
	if q.MaxRAMMB > 0 && cur.RAMMB+addRAMMB > int64(q.MaxRAMMB) {
		return fmt.Errorf("quota exceeded: %s uses %d/%d MB RAM", username, cur.RAMMB, q.MaxRAMMB)
	}
	if q.MaxDiskGB > 0 && cur.Disk+addDiskGB > int64(q.MaxDiskGB) {
		return fmt.Errorf("quota exceeded: %s uses %d/%d GB disk", username, cur.Disk, q.MaxDiskGB)
	}
	return nil
}
