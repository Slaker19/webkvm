package api

import (
	"net/http"

	"webkvm/internal/audit"
	"webkvm/internal/models"
)

// Group & Tag policy (ACL / RBAC):
// Non-admin users are restricted to:
// 1. VMs they own (vm.OwnerID == username).
// 2. VMs belonging to one of their AllowedGroups.
// 3. VMs carrying one of their AllowedTags.
// Admins are always exempt.

func (h *Handler) userAllowedTags(username string) map[string]bool {
	if h.userStore == nil {
		return nil
	}
	u, err := h.userStore.Get(username)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(u.AllowedTags))
	for _, t := range u.AllowedTags {
		out[t] = true
	}
	return out
}

func (h *Handler) userAllowedGroups(username string) map[string]bool {
	if h.userStore == nil {
		return nil
	}
	u, err := h.userStore.Get(username)
	if err != nil {
		return nil
	}
	out := make(map[string]bool, len(u.AllowedGroups))
	for _, g := range u.AllowedGroups {
		out[g] = true
	}
	return out
}

// vmHasAnyTag reports whether the VM's metadata tags intersect the given set.
func (h *Handler) vmHasAnyTag(vmID string, allowed map[string]bool) bool {
	if len(allowed) == 0 || h.compute == nil {
		return false
	}
	meta, err := h.compute.GetVMMeta(vmID)
	if err != nil {
		return false
	}
	for _, t := range meta.Tags {
		if allowed[t] {
			return true
		}
	}
	return false
}

// vmHasAnyGroup reports whether the VM's metadata groups intersect the given set.
func (h *Handler) vmHasAnyGroup(vmID string, allowed map[string]bool) bool {
	if len(allowed) == 0 || h.compute == nil {
		return false
	}
	meta, err := h.compute.GetVMMeta(vmID)
	if err != nil {
		return false
	}
	for _, g := range meta.Groups {
		if allowed[g] {
			return true
		}
	}
	return false
}

func (h *Handler) userCanAccessTag(username, vmID string) bool {
	return h.vmHasAnyTag(vmID, h.userAllowedTags(username))
}

func (h *Handler) userCanAccessGroup(username, vmID string) bool {
	return h.vmHasAnyGroup(vmID, h.userAllowedGroups(username))
}

// filterVMsByACL filters the VM fleet for the caller:
// Admins see all VMs. Non-admins see only:
// 1. VMs owned by them.
// 2. VMs in groups in user.AllowedGroups.
// 3. VMs with tags in user.AllowedTags.
func (h *Handler) filterVMsByACL(r *http.Request, vms []models.VM) []models.VM {
	username, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin {
		return vms
	}

	allowedGroups := h.userAllowedGroups(username)
	allowedTags := h.userAllowedTags(username)

	out := make([]models.VM, 0, len(vms))
	for _, vm := range vms {
		// 1. Direct ownership
		owner := vm.OwnerID
		if owner == "" {
			owner = h.ownerOf(vm.ID)
		}
		if owner != "" && owner == username {
			out = append(out, vm)
			continue
		}

		// 2. Group match
		if len(allowedGroups) > 0 && vmHasAnyGroupFromSlice(vm.Groups, allowedGroups) {
			out = append(out, vm)
			continue
		}

		// 3. Tag match
		if len(allowedTags) > 0 && vmHasAnyTagFromSlice(vm.Tags, allowedTags) {
			out = append(out, vm)
			continue
		}
	}
	return out
}

func vmHasAnyTagFromSlice(tags []string, allowed map[string]bool) bool {
	for _, t := range tags {
		if allowed[t] {
			return true
		}
	}
	return false
}

func vmHasAnyGroupFromSlice(groups []string, allowed map[string]bool) bool {
	for _, g := range groups {
		if allowed[g] {
			return true
		}
	}
	return false
}
