package api

import (
	"fmt"
	"net/http"
	"strings"
)

// A linked clone is a qcow2 overlay whose backing file is another
// image. The dependency lives inside the overlay's own header, not in
// any VM definition, so the attachment guards ("is this volume attached
// to a VM?") cannot see it: the disk attached to the VM is the overlay,
// never the image underneath.
//
// Deleting that image leaves every overlay built on it unopenable — the
// clone does not degrade, it stops existing. Templates instantiated as
// linked clones, appliances deployed copy-on-write and cached base
// images are all backed this way.

// assertNoBackingDependents refuses an operation that would destroy an
// image other disks are layered on, naming them. Returns true when the
// request was refused.
//
// A backend that cannot answer the question is not treated as a "no":
// the check is there to prevent silent data loss, so failing to run it
// blocks the destructive path rather than waving it through.
func (h *Handler) assertNoBackingDependents(w http.ResponseWriter, what, pool, volume string) bool {
	if h.compute == nil {
		return false
	}
	deps, err := h.compute.BackingDependents(pool, volume)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError,
			fmt.Sprintf("cannot determine whether %s is still in use as a backing file: %v", what, err))
		return true
	}
	return h.refuseForDependents(w, what, deps)
}

// refuseForDependents writes the 409 when the dependants list is not
// empty. Returns true when the request was refused.
func (h *Handler) refuseForDependents(w http.ResponseWriter, what string, deps []string) bool {
	if len(deps) == 0 {
		return false
	}
	// A long list helps nobody; the first few name the problem.
	shown := deps
	if len(shown) > 8 {
		shown = append(shown[:8:8], fmt.Sprintf("and %d more", len(deps)-8))
	}
	jsonResp(w, http.StatusConflict, map[string]any{
		"error": fmt.Sprintf(
			"%s is the backing file of: %s — those disks would stop working; delete them first",
			what, strings.Join(shown, ", ")),
		"dependents": deps,
	})
	return true
}

// assertNoBackingDependentsByPath is the same guard for a file that is
// not a pool volume, such as an image cached outside any pool.
func (h *Handler) assertNoBackingDependentsByPath(w http.ResponseWriter, what, path string) bool {
	if h.compute == nil {
		return false
	}
	deps, err := h.compute.BackingDependentsOfPath(path)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError,
			fmt.Sprintf("cannot determine whether %s is still in use as a backing file: %v", what, err))
		return true
	}
	return h.refuseForDependents(w, what, deps)
}
