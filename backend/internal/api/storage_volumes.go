package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/diskprobe"
	"webkvm/internal/models"
)

func (h *Handler) ListVolumes(w http.ResponseWriter, r *http.Request) {
	poolName := r.URL.Query().Get("pool")
	if poolName == "" {
		h.listAllVolumes(w, r)
		return
	}
	if !h.poolVisibleTo(r, poolName) {
		jsonResp(w, http.StatusOK, []models.StorageVolume{})
		return
	}
	vols, err := h.compute.ListStorageVolumes(poolName)
	if err != nil {
		h.writeVolumeListError(w, poolName, err)
		return
	}
	jsonResp(w, http.StatusOK, vols)
}

// listAllVolumes answers GET /storage/volumes without ?pool=: the volumes
// of every visible pool. An inactive pool (or one whose listing fails) is
// skipped and named in X-WebKVM-Skipped-Pools instead of failing the whole
// listing; the raw libvirt error is logged, never returned.
func (h *Handler) listAllVolumes(w http.ResponseWriter, r *http.Request) {
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		h.logError("storage_volumes_list_failed", err, "")
		jsonErrCode(w, http.StatusBadGateway, "volumes_unavailable", "the storage pools could not be listed")
		return
	}
	out := []models.StorageVolume{}
	var skipped []string
	for _, p := range pools {
		if !h.poolVisibleTo(r, p.Name) {
			continue
		}
		if p.State != "" && p.State != "active" {
			skipped = append(skipped, p.Name)
			continue
		}
		vols, err := h.compute.ListStorageVolumes(p.Name)
		if err != nil {
			h.logError("storage_volumes_list_failed", err, p.Name)
			skipped = append(skipped, p.Name)
			continue
		}
		out = append(out, vols...)
	}
	if len(skipped) > 0 {
		sort.Strings(skipped)
		w.Header().Set("X-WebKVM-Skipped-Pools", strings.Join(skipped, ","))
	}
	jsonResp(w, http.StatusOK, out)
}

// writeVolumeListError maps a failed listing of one pool to a typed error:
// pool_inactive (409), pool_not_found (404) or volumes_unavailable (502).
func (h *Handler) writeVolumeListError(w http.ResponseWriter, pool string, err error) {
	h.logError("storage_volumes_list_failed", err, pool)
	status, code, msg := http.StatusBadGateway, "volumes_unavailable", "the volumes of this pool could not be listed"
	if pools, perr := h.compute.ListStoragePools(); perr == nil {
		found := false
		for _, p := range pools {
			if p.Name != pool {
				continue
			}
			found = true
			if p.State != "" && p.State != "active" {
				status, code, msg = http.StatusConflict, "pool_inactive", "the storage pool is not active; start it first"
			}
		}
		if !found {
			status, code, msg = http.StatusNotFound, "pool_not_found", "the storage pool does not exist"
		}
	}
	jsonResp(w, status, map[string]string{"error": msg, "code": code, "pool": pool})
}

// ProbeStorageDisk inspects a pool disk image by absolute path. Body:
// {"path": "...", "deep": bool}. Read-only.
func (h *Handler) ProbeStorageDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		Deep bool   `json:"deep"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Path == "" {
		jsonErr(w, http.StatusBadRequest, "path is required")
		return
	}
	safePath, err := h.validateDiskSourcePath(req.Path)
	if err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	pool, vol, ok := h.resolveVolumeFromSource(safePath)
	if !ok {
		jsonErr(w, http.StatusForbidden, "source path is not inside a known storage pool")
		return
	}
	if !h.poolVisibleTo(r, pool) {
		jsonErr(w, http.StatusForbidden, "access denied to storage pool")
		return
	}
	if atts, err := h.compute.FindVolumeAttachments(pool, vol); err == nil && len(atts) > 0 {
		for _, att := range atts {
			if err := h.requireVMAccess(r, att.VMID); err != nil {
				jsonErr(w, http.StatusForbidden, "access denied: volume is in use by another instance")
				return
			}
		}
	}
	if req.Deep {
		inspector, ok := diskprobe.InspectorAvailable()
		if !ok {
			inspector = ""
		}
		job := submitJob(jobOwner(r), "disk-probe", func() (any, error) {
			cctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			return diskprobe.Deep(cctx, req.Path, inspector), nil
		})
		jsonResp(w, http.StatusAccepted, job)
		return
	}
	res, err := diskprobe.Basic(r.Context(), req.Path)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, res)
}

func (h *Handler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	var req models.CreateVolumeRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Capacity <= 0 {
		jsonErr(w, http.StatusBadRequest, "name and capacity are required")
		return
	}
	if req.Pool == "" {
		req.Pool = config.DiskPoolName
	}
	if !h.requireDiskPool(w, req.Pool) {
		return
	}
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := assertPoolAllowed(u, req.Pool); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if err := h.checkDiskQuota(owner, map[string]int64{req.Pool: req.Capacity}); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}
	vol, err := h.compute.CreateStorageVolume(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusCreated, vol)
}

func (h *Handler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		jsonErr(w, http.StatusBadRequest, "invalid volume name: path traversal not allowed")
		return
	}
	if !h.poolVisibleTo(r, pool) {
		jsonErr(w, http.StatusForbidden, "access denied to storage pool")
		return
	}
	if atts, err := h.compute.FindVolumeAttachments(pool, name); err == nil && len(atts) > 0 {
		volumeInUse(w, fmt.Sprintf("volume %s/%s", pool, name), atts)
		return
	}
	if h.assertNoBackingDependents(w, fmt.Sprintf("volume %s/%s", pool, name), pool, name) {
		return
	}

	if err := h.compute.DeleteStorageVolume(pool, name); err != nil {
		if errors.Is(err, compute.ErrVolumeNotFound) || isNotFoundError(err) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) ResizeVolume(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		jsonErr(w, http.StatusBadRequest, "invalid volume name: path traversal not allowed")
		return
	}
	var req struct {
		Capacity int64 `json:"capacity"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Capacity <= 0 {
		jsonErr(w, http.StatusBadRequest, "capacity must be positive")
		return
	}
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := assertPoolAllowed(u, pool); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if cur, err := h.compute.GetStorageVolume(pool, name); err == nil {
			delta := req.Capacity - bytesToGB(cur.Capacity)
			if delta > 0 {
				if err := h.checkDiskQuota(owner, map[string]int64{pool: delta}); err != nil {
					jsonErr(w, http.StatusConflict, err.Error())
					return
				}
			}
		}
	}
	if err := h.compute.ResizeStorageVolume(pool, name, req.Capacity); err != nil {
		if errors.Is(err, compute.ErrVolumeNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "resized"})
}
