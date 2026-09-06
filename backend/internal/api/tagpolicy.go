package api

import (
	"net/http"

	"webkvm/internal/audit"
	"webkvm/internal/models"
)

// Tag policy (V13-D-01): tags are real RBAC policy, not visual stickers.
//
// A user whose profile carries AllowedTags may act on (and see) any VM
// whose metadata tags intersect that allowlist, in addition to the VMs
// they own. Admins are always exempt. This lets an operator grant
// fleet-wide access by tag (e.g. "manage everything tagged prod")
// without enumerating VMs.
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

// vmHasAnyTag reports whether the VM's metadata tags intersect the given
// set (empty set always returns false).
func (h *Handler) vmHasAnyTag(vmID string, allowed map[string]bool) bool {
	if len(allowed) == 0 || h.lv == nil {
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

// userCanAccessTag is the authz predicate: can this user act on a VM by
// tag grant alone?
func (h *Handler) userCanAccessTag(username, vmID string) bool {
	return h.vmHasAnyTag(vmID, h.userAllowedTags(username))
}

// filterVMsByTagACL prunes a VM list down to what a non-admin may see:
// VMs carrying one of their allowed tags. Admins and users without an
// AllowedTags policy see everything (unchanged behaviour). The VM is the
// API-facing struct (carries Tags/State/IP).
func (h *Handler) filterVMsByTagACL(r *http.Request, vms []models.VM) []models.VM {
	username, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin {
		return vms
	}
	allowed := h.userAllowedTags(username)
	if len(allowed) == 0 {
		return vms // no tag policy -> unchanged (ownership handled elsewhere)
	}
	out := make([]models.VM, 0, len(vms))
	for _, vm := range vms {
		if vmHasAnyTagFromSlice(vm.Tags, allowed) {
			out = append(out, vm)
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
