package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// Moving storage between pools.
//
// Both handlers enqueue a job and answer 202: a multi-gigabyte copy far
// outlives an HTTP request, and a client that polls the job can show
// real progress instead of a spinner that may or may not still be
// attached to live work.
//
// Everything that can be decided synchronously IS decided synchronously
// — RBAC, pool purpose, free space, whether the instance is running.
// Those failures must reach the operator as a status code they can act
// on, not as a job that dies quietly two minutes later.

// moveErrStatus maps a move failure to the HTTP status that describes
// it. Left at 500 only for genuinely unexpected errors.
func moveErrStatus(err error) int {
	switch {
	case errors.Is(err, compute.ErrDomainMustBeStoppedToMove),
		errors.Is(err, compute.ErrSamePool),
		errors.Is(err, compute.ErrVolumeInUse),
		errors.Is(err, compute.ErrVolumeHasDependents),
		errors.Is(err, compute.ErrBackingCheckUnavailable),
		errors.Is(err, compute.ErrInsufficientSpace):
		return http.StatusConflict
	case errors.Is(err, compute.ErrCrossBackendMove):
		return http.StatusBadRequest
	case errors.Is(err, compute.ErrNotImplemented):
		return http.StatusNotImplemented
	}
	return http.StatusInternalServerError
}

// assertMoveAllowed runs the checks shared by both move endpoints:
// the caller's pool ACL, and that the destination exists and is usable.
func (h *Handler) assertMoveAllowed(w http.ResponseWriter, r *http.Request, pools ...string) bool {
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return false
		}
		// Both ends are checked. Authorising only the destination would
		// let a user move data OUT of a pool they were never granted.
		for _, p := range pools {
			if p == "" {
				continue
			}
			if err := assertPoolAllowed(u, p); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return false
			}
		}
	}
	return true
}

// MoveVMStorage relocates every disk of a VM (or a container's root) to
// another pool of the same hypervisor.
func (h *Handler) MoveVMStorage(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Pool string `json:"pool"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	destPool := strings.TrimSpace(req.Pool)
	if destPool == "" {
		jsonErr(w, http.StatusBadRequest, "a destination pool is required")
		return
	}

	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "instance not found")
		return
	}
	if !h.assertMoveAllowed(w, r, destPool) {
		return
	}

	// The destination has to suit what is being moved: a container
	// belongs in a container pool, a VM in a disk pool. Checking the
	// purpose here turns a deep backend failure into an immediate 400.
	purpose, ok := h.lookupPoolPurpose(destPool)
	if !ok {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("storage pool %q does not exist", destPool))
		return
	}
	wantPurpose := compute.PoolPurposeDisk
	if isContainerVM(vm) {
		wantPurpose = compute.PoolPurposeContainer
	}
	// A template pool is the one place a VM may live that is not a disk
	// pool, and only if the VM is actually flagged as a template. This
	// is what the "template" purpose means in practice: a shelf for the
	// golden images, kept apart from the disks that are in daily use.
	//
	// Containers are excluded: Incus owns their storage and has no
	// notion of a libvirt template pool.
	if !compute.HasPurpose(purpose, wantPurpose) {
		if isContainerVM(vm) || !compute.HasPurpose(purpose, compute.PoolPurposeTemplate) {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf(
				"pool %q cannot hold a %s", destPool, instanceKind(vm)))
			return
		}
		meta, merr := h.compute.GetVMMeta(id)
		if merr != nil || !meta.Template {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf(
				"pool %q only holds templates; mark %q as a template first", destPool, id))
			return
		}
	}

	// Refusing a running instance up front is friendlier than letting
	// the backend discover it: the operator gets the reason before any
	// job appears in their list.
	if vm.State == models.VMStateRunning || vm.State == models.VMStatePaused {
		jsonErr(w, http.StatusConflict, compute.ErrDomainMustBeStoppedToMove.Error())
		return
	}

	user, role, ip := audit.FromRequest(r)
	job := submitJobWithPool(jobOwner(r), fmt.Sprintf("move %s → %s", id, destPool), destPool, func(progress func(float64, string)) (any, error) {
		if err := h.compute.MoveDomainStorage(id, destPool, progress); err != nil {
			return nil, err
		}
		return map[string]any{"vm": id, "pool": destPool}, nil
	})

	if h.audit != nil {
		h.audit.Log(audit.Entry{
			User: user, Role: role, IP: ip, Action: "vm.move_storage",
			Resource: id, Detail: map[string]any{"pool": destPool, "job": job.ID},
		})
	}
	jsonResp(w, http.StatusAccepted, job)
}

// MoveVolume relocates a single volume or ISO between two pools.
func (h *Handler) MoveVolume(w http.ResponseWriter, r *http.Request) {
	srcPool := chi.URLParam(r, "pool")
	volName := chi.URLParam(r, "name")
	var req struct {
		Pool    string `json:"pool"`
		NewName string `json:"new_name,omitempty"`
		Copy    bool   `json:"copy,omitempty"`
		Kind    string `json:"kind,omitempty"` // "disk" (default) or "iso"
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	destPool := strings.TrimSpace(req.Pool)
	if destPool == "" {
		jsonErr(w, http.StatusBadRequest, "a destination pool is required")
		return
	}
	if destPool == srcPool {
		jsonErr(w, http.StatusConflict, compute.ErrSamePool.Error())
		return
	}
	kind := req.Kind
	if kind == "" {
		kind = "disk"
	}
	if kind != "disk" && kind != "iso" {
		jsonErr(w, http.StatusBadRequest, `kind must be "disk" or "iso"`)
		return
	}
	if req.NewName != "" {
		// Same filename rules as a rename: no separators, no "..", no
		// control characters. A move must not be a way to write outside
		// the destination pool.
		safe, nerr := safeISOFilename(req.NewName)
		if nerr != nil {
			jsonErr(w, http.StatusBadRequest, nerr.Error())
			return
		}
		req.NewName = safe
	}
	if !h.assertMoveAllowed(w, r, srcPool, destPool) {
		return
	}

	// An ISO belongs in an ISO pool and a disk in a disk pool. Mixing
	// them is exactly the kind of silent misfiling this whole feature
	// exists to avoid.
	//
	// A cached base cloud image is the one disk that a template pool
	// may also hold: it is a golden image, not a disk in daily use,
	// which is what the template purpose is for. The check mirrors
	// baseImagePoolPurpose so the move endpoint and the Image Hub
	// agree on where an image is allowed to live.
	wantPurpose := compute.PoolPurposeDisk
	if kind == "iso" {
		wantPurpose = compute.PoolPurposeISO
	}
	isBaseImage := kind == "disk" && isBaseImageFilename(volName)
	for _, p := range []string{srcPool, destPool} {
		purpose, ok := h.lookupPoolPurpose(p)
		if !ok {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("storage pool %q does not exist", p))
			return
		}
		if isBaseImage {
			if !baseImagePoolPurpose(purpose) {
				jsonErr(w, http.StatusBadRequest, fmt.Sprintf(
					"pool %q is not designated for disk or template files", p))
				return
			}
			continue
		}
		if !compute.HasPurpose(purpose, wantPurpose) {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf(
				"pool %q is not designated for %s files", p, kind))
			return
		}
	}

	user, role, ip := audit.FromRequest(r)
	verb := "move"
	if req.Copy {
		verb = "copy"
	}
	job := submitJobWithPool(
		jobOwner(r), fmt.Sprintf("%s %s → %s", verb, volName, destPool), destPool,
		func(progress func(float64, string)) (any, error) {
			err := h.compute.MoveVolume(srcPool, volName, destPool, compute.MoveVolumeOpts{
				Kind:       kind,
				NewName:    req.NewName,
				KeepSource: req.Copy,
				OnProgress: progress,
			})
			if err != nil {
				return nil, err
			}
			return map[string]any{"volume": volName, "pool": destPool, "copied": req.Copy}, nil
		})

	if h.audit != nil {
		h.audit.Log(audit.Entry{
			User: user, Role: role, IP: ip, Action: "storage.volume_" + verb,
			Resource: srcPool + "/" + volName,
			Detail: map[string]any{
				"dest_pool": destPool, "kind": kind, "job": job.ID, "new_name": req.NewName,
			},
		})
	}
	jsonResp(w, http.StatusAccepted, job)
}

// isContainerVM reports whether an instance is an Incus container.
func isContainerVM(vm models.VM) bool {
	return strings.EqualFold(vm.Type, "container") || strings.EqualFold(vm.Type, "lxc")
}

func instanceKind(vm models.VM) string {
	if isContainerVM(vm) {
		return "container"
	}
	return "VM"
}
