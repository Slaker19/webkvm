package api

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// SnapshotWithVM is a models.Snapshot tagged with the VM it belongs
// to, for the cross-fleet snapshots view.
type SnapshotWithVM struct {
	models.Snapshot
	VMID   string `json:"vm_id"`
	VMName string `json:"vm_name"`
}

func (h *Handler) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	snaps, err := h.compute.ListSnapshots(id)
	if err != nil {
		if errors.Is(err, compute.ErrNotImplemented) {
			jsonResp(w, http.StatusOK, []models.Snapshot{})
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if snaps == nil {
		snaps = []models.Snapshot{}
	}
	jsonResp(w, http.StatusOK, snaps)
}

// ListAllSnapshots aggregates snapshots across every VM. This is N+1
// over libvirt (one ListSnapshots call per domain), which is fine at
// the domain counts this product targets (homelab/small-team, not a
// hyperscaler); a VM whose snapshot list fails to load is skipped
// rather than failing the whole request, so one broken domain doesn't
// take down the fleet view.
func (h *Handler) ListAllSnapshots(w http.ResponseWriter, r *http.Request) {
	vms, err := h.compute.ListDomains()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Scope to the caller's own/group/tag VMs. Without this the fleet
	// snapshot view leaked every VM's snapshot names to any viewer.
	vms = h.filterVMsByACL(r, vms)

	var mu sync.Mutex
	out := make([]SnapshotWithVM, 0, len(vms))
	var wg sync.WaitGroup
	// Limit concurrency to avoid hammering the backend unnecessarily.
	// 10 concurrent requests strikes a good balance for libvirt.
	sem := make(chan struct{}, 10)

	for _, vm := range vms {
		wg.Add(1)
		go func(vm models.VM) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			snaps, err := h.compute.ListSnapshots(vm.ID)
			if err != nil {
				return
			}

			if len(snaps) > 0 {
				mu.Lock()
				for _, s := range snaps {
					out = append(out, SnapshotWithVM{Snapshot: s, VMID: vm.ID, VMName: vm.Name})
				}
				mu.Unlock()
			}
		}(vm)
	}

	wg.Wait()

	jsonResp(w, http.StatusOK, map[string]any{"snapshots": out})
}

func (h *Handler) CreateSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "snapshots"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	var req models.CreateSnapshotRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	// A memory (RAM+disk) snapshot can take a while on a busy VM; run it
	// as a background job so the request returns immediately (202) and
	// the client polls /api/jobs/{id}.
	job := submitJob(jobOwner(r), "snapshot:"+req.Name, func() (any, error) {
		snap, err := h.compute.CreateSnapshot(id, req)
		if err != nil {
			return nil, err
		}
		h.audit.Log(auditFor(r, "vm.snapshot_create", id, map[string]interface{}{
			"snap":            snap.Name,
			"allocated_bytes": snap.SizeAtSnapBytes,
		}))
		return snap, nil
	})
	jsonResp(w, http.StatusAccepted, map[string]string{"job": job.ID})
}

func (h *Handler) DeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "snapshots"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	sid := chi.URLParam(r, "sid")
	allocated, err := h.compute.DeleteSnapshot(id, sid)
	if err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	h.audit.Log(auditFor(r, "vm.snapshot_delete", id, map[string]interface{}{
		"snap":            sid,
		"allocated_bytes": allocated,
	}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) RevertSnapshot(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "snapshots"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	sid := chi.URLParam(r, "sid")
	// A revert restores the snapshot's domain XML, <metadata> included,
	// so it would also roll back the admin-only Shared flag (re-sharing
	// a template an admin since unshared). Keep the current value. An
	// unreadable meta fails safe to "not shared".
	prevShared := false
	if m, merr := h.compute.GetVMMeta(id); merr == nil {
		prevShared = m.Shared
	}
	if err := h.compute.RevertSnapshot(id, sid); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	if m, merr := h.compute.GetVMMeta(id); merr != nil || m.Shared != prevShared {
		if _, uerr := h.compute.UpdateVMMeta(id, models.VMMetaUpdate{Shared: &prevShared}); uerr != nil {
			slog.Warn("snapshot_revert_shared_restore_failed", "id", id, "err", uerr)
		}
	}
	h.audit.Log(auditFor(r, "vm.snapshot_revert", id, map[string]interface{}{"snap": sid}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "reverted"})
}
