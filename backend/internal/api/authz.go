package api

import (
	"errors"
	"fmt"
	"net/http"

	"webkvm/internal/audit"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// requireVMAccess returns an error unless the caller is an admin or the
// recorded owner of vmID. A VM with no recorded owner is treated as
// admin-only for these operations: leaving it open to any operator would
// (and previously did) let every operator act on, console into, or export
// any unowned VM with no quota/ACL accounting at all.
//
// V13-D-01 (tags as policy): an owner may ALSO access a VM whose tags
// intersect their AllowedTags (e.g. a user granted tag "prod" may act on
// every VM carrying that tag). Admins are always exempt.
func (h *Handler) requireVMAccess(r *http.Request, vmID string) error {
	username, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin {
		return nil
	}
	owner := h.ownerOf(vmID)
	if owner != "" && owner == username {
		return nil
	}
	// Fall back to group-based access: the caller's AllowedGroups vs the VM's groups.
	if h.userCanAccessGroup(username, vmID) {
		return nil
	}
	// Fall back to tag-based access: the caller's AllowedTags vs the VM's
	// metadata tags.
	if h.userCanAccessTag(username, vmID) {
		return nil
	}
	return errors.New("forbidden: you do not have permission to access this VM")
}

// requirePermission checks whether the calling user holds the requested capability.
// Admins are always authorized.
func (h *Handler) requirePermission(r *http.Request, perm string) error {
	username, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin {
		return nil
	}
	if h.userStore == nil {
		// Fail CLOSED: an un-wired user store must never silently grant a
		// named permission to a non-admin. Without the store we cannot
		// verify the capability, so deny.
		return errors.New("forbidden: authorization unavailable")
	}
	u, err := h.userStore.Get(username)
	if err != nil {
		return errors.New("forbidden: user not found")
	}
	if !u.HasPermission(perm) {
		return fmt.Errorf("forbidden: user lacks '%s' permission", perm)
	}
	return nil
}

// requireCapability is chi middleware enforcing a named capability for
// every route nested under it. Access to a VM and the right to perform a
// given action on it are two separate questions: requireVMOwnership
// answers "may you touch this VM at all", this answers "may you do this
// kind of thing anywhere". Both must pass.
func (h *Handler) requireCapability(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := h.requirePermission(r, perm); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requireVMOwnership is chi middleware that enforces requireVMAccess for
// every request nested under a route with a {id} URL param naming a VM.
func (h *Handler) requireVMOwnership(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if err := h.requireVMAccess(r, id); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		next.ServeHTTP(w, r)
	})
}
