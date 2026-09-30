package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/models"
	"webkvm/internal/zvol"
)

// Seams for tests: the real implementations shell out to zfs(8).
var (
	zvolGet  = zvol.Get
	zvolList = zvol.List
)

// ZVolInfo is a host ZFS volume plus the VM currently using it, if any.
type ZVolInfo struct {
	zvol.Volume
	AttachedVMID   string `json:"attached_vm_id,omitempty"`
	AttachedVMName string `json:"attached_vm_name,omitempty"`
}

// ListZVols returns the host's ZFS volumes for the Add Disk picker.
// Admin only (the route sits in the admin group): a zvol is raw host
// storage outside every WebKVM pool, quota and ACL.
func (h *Handler) ListZVols(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	vols, err := zvolList(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to list ZFS volumes: "+err.Error())
		return
	}
	users := h.zvolUsers()
	out := make([]ZVolInfo, 0, len(vols))
	for _, v := range vols {
		info := ZVolInfo{Volume: v}
		if vm, ok := users[v.Dev]; ok {
			info.AttachedVMID, info.AttachedVMName = vm.ID, vm.Name
		}
		out = append(out, info)
	}
	jsonResp(w, http.StatusOK, out)
}

// zvolUsers maps each block device path in use by a VM to that VM.
// Best effort: a listing failure yields an empty map.
func (h *Handler) zvolUsers() map[string]models.VM {
	users := map[string]models.VM{}
	if h.compute == nil {
		return users
	}
	vms, err := h.compute.ListDomains()
	if err != nil {
		return users
	}
	for _, vm := range vms {
		for _, d := range vm.Disks {
			if d.BlockDev != "" {
				users[d.BlockDev] = vm
			}
		}
	}
	return users
}

// checkZVolAttach validates an attach request naming a zvol and returns
// the HTTP status to answer with on failure. The same guards the file
// path has apply: one VM per disk, and no silent reuse of a disk that
// already holds data unless force is set.
func (h *Handler) checkZVolAttach(r *http.Request, vmID string, req *models.AttachDiskRequest) (int, error) {
	if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		return http.StatusForbidden, fmt.Errorf("attaching a ZFS volume requires the admin role")
	}
	if req.Device != "disk" {
		return http.StatusBadRequest, fmt.Errorf("a zvol can only be attached as a disk")
	}
	if req.Source != "" || req.SizeGB > 0 {
		return http.StatusBadRequest, fmt.Errorf("zvol cannot be combined with source or size_gb")
	}
	if err := zvol.ValidateName(req.ZVol); err != nil {
		return http.StatusBadRequest, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	vol, err := zvolGet(ctx, req.ZVol)
	if err != nil {
		if errors.Is(err, zvol.ErrZFSUnavailable) {
			return http.StatusServiceUnavailable, err
		}
		return http.StatusBadRequest, err
	}
	// Two VMs writing one block device is silent corruption.
	if vm, ok := h.zvolUsers()[vol.Dev]; ok && vm.ID != vmID {
		return http.StatusConflict, fmt.Errorf("zvol %s is already attached to VM %q", vol.Name, vm.Name)
	}
	if vol.HasData && !req.Force {
		return http.StatusConflict, fmt.Errorf("zvol %s already contains data (%d bytes written); pass force=true to attach it anyway",
			vol.Name, vol.WrittenBytes)
	}
	return 0, nil
}
