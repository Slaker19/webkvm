package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/fsutil"
	"webkvm/internal/models"
)

// mediaScope builds the libvirt-layer path scope for the caller. Admins may
// reference regular files outside the pools; everybody else is confined to
// the pools their ACL allows (audit #5/#7/#8). A forgotten call leaves the
// zero MediaScope, which is the restrictive default.
func (h *Handler) mediaScope(r *http.Request) models.MediaScope {
	user, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin {
		return models.MediaScope{Admin: true}
	}
	if user == "" || h.userStore == nil {
		return models.MediaScope{}
	}
	u, err := h.userStore.Get(user)
	if err != nil {
		return models.MediaScope{AllowedPools: map[string]bool{}}
	}
	canNest := u.HasPermission("nesting")
	set, all := poolAllowSet(u)
	if all {
		return models.MediaScope{CanNesting: canNest}
	}
	return models.MediaScope{AllowedPools: set, CanNesting: canNest}
}

// poolNameOfPath returns the pool whose directory contains path (symlinks
// resolved on both sides).
func (h *Handler) poolNameOfPath(path string) (string, bool) {
	if h.compute == nil {
		return "", false
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	if real, rerr := filepath.EvalSymlinks(resolved); rerr == nil {
		resolved = real
	}
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		return "", false
	}
	for _, p := range pools {
		if p.Path == "" {
			continue
		}
		base := filepath.Clean(p.Path)
		if realBase, berr := filepath.EvalSymlinks(base); berr == nil {
			base = filepath.Clean(realBase)
		}
		rel, rerr := filepath.Rel(base, resolved)
		if rerr == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return p.Name, true
		}
	}
	return "", false
}

// validateDiskSourceForCaller is validateDiskSourcePath plus the per-caller
// rules: the target must be a regular file (never a device node, socket or
// FIFO, for any role), and a non-admin may only use files inside pools their
// ACL allows.
func (h *Handler) validateDiskSourceForCaller(r *http.Request, path string) error {
	if path == "" {
		return nil
	}
	safePath, err := h.validateDiskSourcePath(path)
	if err != nil {
		return err
	}
	if _, err := fsutil.ResolveRegular(safePath); err != nil {
		return fmt.Errorf("source path %q is not a usable regular file", path)
	}
	if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		pool, ok := h.poolNameOfPath(safePath)
		if !ok || !h.poolVisibleTo(r, pool) {
			return fmt.Errorf("source path %q is not inside a storage pool available to this user", path)
		}
	}
	return nil
}

// createUploadFile creates destPath as a NEW file (O_EXCL, never following a
// symlink out of its directory). An upload can therefore never replace an
// existing volume or ISO, including one attached to a running VM (audit #7).
// The mode is 0666 before umask, i.e. what os.Create used, so libvirt/QEMU
// access to uploaded media is unchanged.
func createUploadFile(destPath string) (*os.File, error) {
	return fsutil.CreateExclusive(filepath.Dir(destPath), filepath.Base(destPath), 0o666)
}

// uploadCreateStatus maps a createUploadFile error to an HTTP status:
// 409 when the name is taken, 500 otherwise.
func uploadCreateStatus(err error) int {
	if errors.Is(err, os.ErrExist) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// createUploadFileOverwriteExisting tries createUploadFile first. When the
// name is taken, it replaces the file only when it is a *ghost*: present on
// disk but unknown to libvirt (a previous upload that landed before
// RefreshPool failed). Anything libvirt knows about is refused with the
// original 409 — a registered volume may be a VM disk, an ISO, or a base
// image every linked clone depends on, and the storage UI already exposes
// an explicit, attachment-aware delete for those.
func (h *Handler) createUploadFileOverwriteExisting(destPath, poolName string) (*os.File, error) {
	dst, err := createUploadFile(destPath)
	if err == nil {
		return dst, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, err
	}

	volName := filepath.Base(destPath)

	// Registered volume: never clobber in place.
	if _, gerr := h.compute.GetStorageVolume(poolName, volName); gerr == nil {
		return nil, err
	} else if !errors.Is(gerr, compute.ErrVolumeNotFound) {
		return nil, err // unknown registry state: fail closed
	}

	// Unregistered, but a domain could still point at the file directly
	// (e.g. its volume entry vanished). Refuse if anything references it.
	atts, aerr := h.compute.FindVolumeAttachments(poolName, volName)
	if aerr != nil {
		return nil, err // cannot prove it is unused: fail closed
	}
	if len(atts) > 0 {
		return nil, err
	}

	if rerr := os.Remove(destPath); rerr != nil && !os.IsNotExist(rerr) {
		return nil, fmt.Errorf("remove stale upload %q: %w", volName, rerr)
	}
	return createUploadFile(destPath)
}

// errSourceAttached is returned by checkSourceNotAttachedElsewhere.
type errSourceAttached struct{ msg string }

func (e errSourceAttached) Error() string { return e.msg }

// checkSourceNotAttachedElsewhere refuses a disk image already attached to a
// VM other than vmID: two writers on one qcow2/raw is silent corruption (and,
// for non-admins, a way to read another tenant's disk). Used by both
// CreateDisk and UpdateDisk so they cannot diverge.
func (h *Handler) checkSourceNotAttachedElsewhere(vmID, source string) error {
	pool, vol, ok := h.resolveVolumeFromSource(source)
	if !ok {
		return nil
	}
	atts, err := h.compute.FindVolumeAttachments(pool, vol)
	if err != nil {
		return nil
	}
	for _, a := range atts {
		if a.VMID != vmID {
			return errSourceAttached{fmt.Sprintf("disk %s is already attached to VM %q (%s)", vol, a.VMName, a.Target)}
		}
	}
	return nil
}
