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
	Disk  int64 // GB (global total, summed across pools)
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

// vmTotalDiskGB returns the VM's total disk usage in GB, summing every
// disk reported by libvirt. This is accurate for multi-disk VMs spread
// across several storage pools. The legacy vm.DiskGB field only reads the
// first <capacity> tag in the domain XML, so it undercounts such VMs;
// prefer the per-disk breakdown and fall back to vm.DiskGB only when the
// per-disk list is empty.
func vmTotalDiskGB(vm models.VM) int64 {
	var total int64
	for _, d := range vm.Disks {
		total += d.SizeGB
	}
	if total == 0 {
		return vm.DiskGB
	}
	return total
}

// vmTotalDiskByPool groups a VM's disk usage by storage pool.
func vmTotalDiskByPool(vm models.VM) map[string]int64 {
	m := map[string]int64{}
	if len(vm.Disks) > 0 {
		for _, d := range vm.Disks {
			m[d.Pool] += d.SizeGB
		}
		return m
	}
	m[""] = vm.DiskGB // fallback when disk details are unavailable
	return m
}

// usageOf sums the resources of every VM owned by username.
// Admin-created VMs with no owner are not counted for any user.
// The disk figure is the accurate per-disk total (see vmTotalDiskGB).
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
		u.Disk += vmTotalDiskGB(vm)
	}
	return u, nil
}

// diskUsageByPool sums each owned VM's disk usage grouped by pool. The
// disk quota is global across all of a user's VMs (regardless of power
// state) and may additionally be capped per pool via Quota.PoolQuotas.
func (h *Handler) diskUsageByPool(username string) (map[string]int64, error) {
	out := map[string]int64{}
	if h.lv == nil {
		return out, nil
	}
	vms, err := h.lv.ListDomains()
	if err != nil {
		return out, err
	}
	for _, vm := range vms {
		if h.ownerOf(vm.ID) != username {
			continue
		}
		for pool, gb := range vmTotalDiskByPool(vm) {
			out[pool] += gb
		}
	}
	return out, nil
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

// defaultPool returns the connector's default disk pool name.
func (h *Handler) defaultPool() string {
	if h.lv == nil {
		return ""
	}
	return h.lv.DiskPoolName()
}

// enforceDiskQuota is the pure disk-quota check: given the owner's
// current per-pool usage (cur) and the per-pool delta being added (add),
// it verifies both the global MaxDiskGB cap and each per-pool
// PoolQuotas limit. A negative delta (e.g. a shrink) is always allowed.
// It is separated from checkDiskQuota so the logic can be unit-tested
// without a live libvirt connection.
func enforceDiskQuota(username string, q models.Quota, cur, add map[string]int64) error {
	// Global disk cap across all pools.
	if q.MaxDiskGB > 0 {
		var curTotal, addTotal int64
		for _, v := range cur {
			curTotal += v
		}
		for _, v := range add {
			addTotal += v
		}
		if curTotal+addTotal > int64(q.MaxDiskGB) {
			return fmt.Errorf("quota exceeded: %s uses %d/%d GB disk (global)", username, curTotal+addTotal, q.MaxDiskGB)
		}
	}
	// Per-pool disk caps.
	for pool, limit := range q.PoolQuotas {
		if limit <= 0 {
			continue
		}
		if cur[pool]+add[pool] > int64(limit) {
			return fmt.Errorf("quota exceeded: %s uses %d/%d GB disk on pool %q", username, cur[pool]+add[pool], limit, pool)
		}
	}
	return nil
}

// checkDiskQuota verifies that adding addByPool (pool -> GB delta) keeps
// the owner within both the global MaxDiskGB cap (if set) and each
// per-pool PoolQuotas limit (if set). A nil addByPool performs a pure
// "current usage" check, used when powering on a VM whose disk footprint
// does not change.
func (h *Handler) checkDiskQuota(username string, addByPool map[string]int64) error {
	if h.userStore == nil || username == "" {
		return nil
	}
	u, err := h.userStore.Get(username)
	if err != nil {
		// Fail closed: we can't tell whether this user is quota-exempt
		// (admin) or restricted, so a transient/corrupted lookup must
		// not silently grant unlimited disk usage.
		return fmt.Errorf("cannot verify quota for %q: %w", username, err)
	}
	if u.Role == models.RoleAdmin {
		return nil
	}
	q := u.Quota
	if !q.Enabled() {
		return nil
	}
	cur, err := h.diskUsageByPool(username)
	if err != nil {
		return err
	}
	return enforceDiskQuota(username, q, cur, addByPool)
}

// runningUsageOf sums the vCPU/RAM of the owner's *active* VMs
// (running/paused/crashed), excluding excludeID. Disk is intentionally not
// counted here — the disk quota is global across all owned VMs regardless
// of their power state (see checkDiskQuota).
func (h *Handler) runningUsageOf(username, excludeID string) (quotaUsage, error) {
	var u quotaUsage
	if h.lv == nil {
		return u, nil
	}
	vms, err := h.lv.ListDomains()
	if err != nil {
		return u, err
	}
	for _, vm := range vms {
		if vm.ID == excludeID {
			continue
		}
		if h.ownerOf(vm.ID) != username {
			continue
		}
		switch vm.State {
		case models.VMStateRunning, models.VMStatePaused, models.VMStateCrashed:
			u.VCPUs += int64(vm.VCPUs)
			u.RAMMB += vm.RAMMB
		}
	}
	return u, nil
}

// enforceStartQuota is the pure running-footprint check used when
// powering on / resuming a VM: it verifies the owner's running vCPU/RAM
// (curRunVCPUs/curRunRAMMB) plus the VM being started stay within quota.
// Separated from checkStartQuota so it can be unit-tested directly.
func enforceStartQuota(username string, q models.Quota, curRunVCPUs, curRunRAMMB int64, vmVCPUs int, vmRAMMB int64) error {
	if q.MaxVCPUs > 0 && curRunVCPUs+int64(vmVCPUs) > int64(q.MaxVCPUs) {
		return fmt.Errorf("quota exceeded on start: %s would use %d/%d vCPUs (running)", username, curRunVCPUs+int64(vmVCPUs), q.MaxVCPUs)
	}
	if q.MaxRAMMB > 0 && curRunRAMMB+int64(vmRAMMB) > int64(q.MaxRAMMB) {
		return fmt.Errorf("quota exceeded on start: %s would use %d/%d MB RAM (running)", username, curRunRAMMB+int64(vmRAMMB), q.MaxRAMMB)
	}
	return nil
}

// checkStartQuota blocks powering on / resuming a VM when doing so would
// push the owner's running vCPU/RAM or global/per-pool disk beyond quota.
// Admins and quota-disabled users are exempt. Disk is checked globally
// (all owned VMs, on or off) since powering on does not change disk usage.
func (h *Handler) checkStartQuota(id string) error {
	if h.lv == nil {
		return nil
	}
	owner := h.ownerOf(id)
	if owner == "" {
		return nil
	}
	if h.userStore == nil {
		return nil
	}
	u, err := h.userStore.Get(owner)
	if err != nil {
		// Fail closed (see checkDiskQuota for rationale).
		return fmt.Errorf("cannot verify quota for %q: %w", owner, err)
	}
	if u.Role == models.RoleAdmin {
		return nil
	}
	q := u.Quota
	if !q.Enabled() {
		return nil
	}
	vm, err := h.lv.GetDomain(id)
	if err != nil {
		// Can't size the VM; let libvirt report the start error instead.
		return nil
	}
	curRun, err := h.runningUsageOf(owner, id)
	if err != nil {
		return err
	}
	if err := enforceStartQuota(owner, q, curRun.VCPUs, curRun.RAMMB, vm.VCPUs, vm.RAMMB); err != nil {
		return err
	}
	// Disk is global across all owned VMs (on or off), no delta on start.
	return h.checkDiskQuota(owner, nil)
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
// It returns a descriptive error when a limit would be exceeded. The disk
// dimension checked here is the global cap (MaxDiskGB); per-pool caps are
// enforced separately at the endpoints that know the target pool via
// checkDiskQuota.
func (h *Handler) checkQuota(username string, addVMs, addVCPUs, addRAMMB, addDiskGB int64) error {
	if h.userStore == nil || username == "" {
		return nil
	}
	// Admins are always exempt.
	u, err := h.userStore.Get(username)
	if err != nil {
		// Fail closed (see checkDiskQuota for rationale).
		return fmt.Errorf("cannot verify quota for %q: %w", username, err)
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
