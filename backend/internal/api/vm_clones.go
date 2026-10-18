package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

func (h *Handler) CloneVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req models.CloneVMRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateVMName(req.Name); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The clone's disks must land in a pool that holds disks. A clone of
	// a container is routed to Incus, which owns its own pools, so only
	// the KVM side is constrained here.
	if src, serr := h.compute.GetDomain(id); serr != nil || !isContainerVM(src) {
		if err := h.assertPoolPurpose(req.Pool, compute.PoolPurposeDisk); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// Quota: a clone counts against the source VM's owner (an admin
	// cloning their own infra stays exempt). All checks stay synchronous
	// so a quota/ACL violation fails fast with 4xx; only the actual
	// (slow) clone runs in the background job.
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		if o := h.ownerOf(id); o != "" {
			owner = o
		}
		// Estimate the clone's size from the source before cloning.
		src, err := h.compute.GetDomain(id)
		if err != nil {
			// M-04: fail closed — a failed lookup used to skip quota/ACL
			// entirely and let the clone through.
			slog.Error("clone_quota_check_failed", "err", err, "src", id)
			jsonErr(w, http.StatusServiceUnavailable, "cannot verify quota: "+err.Error())
			return
		}
		diskGB := vmTotalDiskGB(src)
		if err := h.checkQuota(owner, 1, int64(src.VCPUs), src.RAMMB, diskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		clonePool := req.Pool
		if clonePool == "" {
			clonePool = h.defaultPool()
		}
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := assertPoolAllowed(u, clonePool); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if req.Network != "" {
			if err := assertNetworkAllowed(u, req.Network); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
		if err := h.checkDiskQuota(owner, map[string]int64{clonePool: diskGB}); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}
	job := submitJob(jobOwner(r), "clone:"+req.Name, func() (any, error) {
		vm, err := h.compute.CloneDomain(id, req)
		if err != nil {
			return nil, err
		}
		// The clone copies the source's metadata wholesale; a copy of a
		// shared template must not come out shared with every user too.
		notShared := false
		upd := models.VMMetaUpdate{Shared: &notShared}
		if owner != "" {
			upd.OwnerID = &owner
		}
		_, _ = h.compute.UpdateVMMeta(vm.ID, upd)
		h.audit.Log(auditFor(r, "vm.clone", id, map[string]interface{}{"new_id": vm.ID, "name": req.Name}))
		return vm, nil
	})
	jsonResp(w, http.StatusAccepted, map[string]string{"job": job.ID})
}

// BatchCloneVM initiates parallel or batch cloning of a source VM into N replicas.
func (h *Handler) BatchCloneVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		BaseName string `json:"base_name"`
		Prefix   string `json:"prefix"`
		Count    int    `json:"count"`
		Pool     string `json:"pool"`
		Network  string `json:"network"`
		Linked   bool   `json:"linked"`
		Start    int    `json:"start"` // default: 1
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.BaseName == "" && req.Prefix != "" {
		req.BaseName = req.Prefix
	}
	req.BaseName = strings.TrimSpace(req.BaseName)
	if req.BaseName == "" {
		jsonErr(w, http.StatusBadRequest, "base_name is required")
		return
	}
	if req.Count <= 0 || req.Count > 50 {
		jsonErr(w, http.StatusBadRequest, "count must be between 1 and 50")
		return
	}
	if req.Start <= 0 {
		req.Start = 1
	}

	owner, role, ip := audit.FromRequest(r)
	if role != models.RoleAdmin {
		src, err := h.compute.GetDomain(id)
		if err != nil {
			jsonErr(w, http.StatusServiceUnavailable, "cannot verify quota: "+err.Error())
			return
		}
		diskGB := vmTotalDiskGB(src)
		if err := h.checkQuota(owner, int64(req.Count), int64(src.VCPUs)*int64(req.Count), src.RAMMB*int64(req.Count), diskGB*int64(req.Count)); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}

	jOwner := jobOwner(r)
	job := submitJob(jOwner, fmt.Sprintf("batch-clone:%s[%d]", req.BaseName, req.Count), func() (any, error) {
		var cloned []models.VM
		for i := 0; i < req.Count; i++ {
			instanceName := fmt.Sprintf("%s-%02d", req.BaseName, req.Start+i)
			cloneReq := models.CloneVMRequest{
				Name:    instanceName,
				Pool:    req.Pool,
				Network: req.Network,
				Linked:  req.Linked,
			}
			vm, err := h.compute.CloneDomain(id, cloneReq)
			if err != nil {
				return cloned, fmt.Errorf("failed creating %s: %w", instanceName, err)
			}
			notShared := false
			upd := models.VMMetaUpdate{Shared: &notShared}
			if owner != "" {
				upd.OwnerID = &owner
			}
			_, _ = h.compute.UpdateVMMeta(vm.ID, upd)
			cloned = append(cloned, vm)
		}
		h.audit.Log(audit.Entry{
			User:     owner,
			Role:     role,
			IP:       ip,
			Action:   "vm.batch_clone",
			Resource: id,
			Detail:   map[string]interface{}{"count": req.Count, "base_name": req.BaseName},
		})
		return cloned, nil
	})

	jsonResp(w, http.StatusAccepted, map[string]string{"job": job.ID})
}
