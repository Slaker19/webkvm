package api

import (
	"errors"
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
func (h *Handler) requireVMAccess(r *http.Request, vmID string) error {
	username, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin {
		return nil
	}
	owner := h.ownerOf(vmID)
	if owner == "" || owner != username {
		return errors.New("forbidden: you are not the owner of this VM")
	}
	return nil
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
