package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/diskprobe"
	"webkvm/internal/models"
	"webkvm/internal/zvol"
)

// validDiskBuses is the same set the "Add Disk" dialog offers.
var validDiskBuses = map[string]bool{"virtio": true, "sata": true, "scsi": true, "ide": true}

// normalizeAttachDiskRequest defaults an omitted device to "disk" and
// rejects device/bus values libvirt would refuse. Without it a request
// with no device reached libvirt as an empty device (500) after the new
// volume had already been created.
func normalizeAttachDiskRequest(req *models.AttachDiskRequest) error {
	if req.ZVol != "" {
		if req.Source != "" {
			return errors.New("cannot specify both 'source' and 'zvol'")
		}
		if req.Device == "cdrom" {
			return errors.New("cannot attach ZFS volume as cdrom device")
		}
		req.Device = "disk"
	}
	if req.Device == "" {
		req.Device = "disk"
	}
	if req.Device != "disk" && req.Device != "cdrom" {
		return fmt.Errorf("invalid device %q (expected disk or cdrom)", req.Device)
	}
	// An empty bus is fine: AttachDisk picks virtio for disks and sata
	// for cdroms.
	if req.Bus != "" && !validDiskBuses[req.Bus] {
		return fmt.Errorf("invalid bus %q (expected virtio, sata, scsi or ide)", req.Bus)
	}
	return nil
}

func (h *Handler) ListDisks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, vm.Disks)
}

func (h *Handler) CreateDisk(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req models.AttachDiskRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := normalizeAttachDiskRequest(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	req.Scope = h.mediaScope(r)
	if req.ZVol != "" {
		_, role, _ := audit.FromRequest(r)
		if role != models.RoleAdmin {
			jsonErr(w, http.StatusForbidden, "attaching ZFS volumes is restricted to administrators")
			return
		}
		cleanZVol := strings.TrimPrefix(strings.TrimSpace(req.ZVol), "/dev/zvol/")
		if err := zvol.ValidName(cleanZVol); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// ZVol collision guard: 409 if another VM is already using this zvol
		if atts, aerr := h.compute.FindZVolAttachments(cleanZVol); aerr == nil {
			for _, a := range atts {
				if a.VMID != id {
					jsonErr(w, http.StatusConflict,
						fmt.Sprintf("ZFS volume %s is already attached to VM %q (%s)", cleanZVol, a.VMName, a.Target))
					return
				}
			}
		}
		zinfo, zerr := zvol.Resolve(r.Context(), cleanZVol)
		if zerr != nil {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("invalid ZFS volume %q: %v", cleanZVol, zerr))
			return
		}
		// Data inspection guard: 409 without force if zvol already holds data
		if !req.Force {
			if probe, perr := diskprobe.Basic(r.Context(), zinfo.Device); perr == nil && probe.HasData {
				detail := fmt.Sprintf("ZFS volume %s already contains data (format %s, %d bytes allocated)",
					cleanZVol, probe.Format, probe.Allocated)
				if probe.BackingFile != "" {
					detail += fmt.Sprintf(", backed by %s", filepath.Base(probe.BackingFile))
				}
				jsonErr(w, http.StatusConflict,
					detail+"; pass force=true to attach it anyway")
				return
			}
		}
	}
	if req.Source != "" {
		if err := h.validateDiskSourceForCaller(r, req.Source); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		// A disk image may be attached to at most one VM at a time.
		// Sharing one image between two VMs means two independent write
		// streams into the same qcow2/raw file — silent, inescapable
		// data corruption. Refuse unless the only existing attachment is
		// this same VM (idempotent re-attach).
		if err := h.checkSourceNotAttachedElsewhere(id, req.Source); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		// Refuse to attach a disk image that already contains data unless
		// the operator explicitly forces it. A disk that already holds a
		// guest OS or another VM's data will not behave like a blank disk
		// — the resulting VM silently fails to boot, or its filesystem is
		// clobbered. Only .img/.qcow2 style images are inspected; ISOs
		// (cdrom) are never blocked.
		if req.Device == "disk" && !req.Force {
			if probe, perr := diskprobe.Basic(r.Context(), req.Source); perr == nil && probe.HasData {
				detail := fmt.Sprintf("disk image %s already contains data (format %s, %d bytes allocated)",
					filepath.Base(req.Source), probe.Format, probe.Allocated)
				if probe.BackingFile != "" {
					detail += fmt.Sprintf(", backed by %s", filepath.Base(probe.BackingFile))
				}
				jsonErr(w, http.StatusConflict,
					detail+"; pass force=true to attach it anyway")
				return
			}
		}
	}
	// A brand new disk must be allocated in a pool that holds disks.
	// Only checked when a disk is actually created (SizeGB > 0): an
	// attachment of an existing file names its pool for bookkeeping
	// only, and a cdrom legitimately points into the ISO pool.
	if req.SizeGB > 0 && req.Device != "cdrom" {
		if err := h.assertPoolPurpose(req.Pool, compute.PoolPurposeDisk); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	// Per-pool disk quota: a newly attached disk consumes space in its
	// pool (cdrom/ISO attachments don't allocate a disk image).
	if req.SizeGB > 0 {
		if owner := h.ownerOf(id); owner != "" {
			if u, gerr := h.userStore.Get(owner); gerr == nil && u.Role != models.RoleAdmin {
				pool := req.Pool
				if pool == "" {
					pool = h.defaultPool()
				}
				if err := assertPoolAllowed(u, pool); err != nil {
					jsonErr(w, http.StatusForbidden, err.Error())
					return
				}
				if err := h.checkDiskQuota(owner, map[string]int64{pool: req.SizeGB}); err != nil {
					jsonErr(w, http.StatusConflict, err.Error())
					return
				}
			}
		}
	}
	if err := h.compute.AttachDisk(id, req); err != nil {
		if strings.Contains(err.Error(), "resolve cdrom media") || strings.Contains(err.Error(), "media file") || strings.Contains(err.Error(), "ambiguous media") {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.disk_attach", id, map[string]interface{}{"bus": req.Bus, "device": req.Device, "zvol": req.ZVol}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "attached"})
}

// ProbeDisk inspects a VM disk's underlying file, reporting its format,
// allocated/virtual size, whether it already contains recognizable
// data and, when libguestfs is installed and deep=1 is requested, which
// OS/partitions it holds. Read-only: this never modifies the disk.
func (h *Handler) ProbeDisk(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dev := chi.URLParam(r, "dev")
	var path string
	if vm, err := h.compute.GetDomain(id); err == nil {
		for _, d := range vm.Disks {
			if d.Target == dev && d.Device != "cdrom" {
				path = d.Source
				break
			}
		}
	}
	if path == "" {
		jsonErr(w, http.StatusNotFound, "disk device not found")
		return
	}
	safePath, err := h.validateDiskSourcePath(path)
	if err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	path = safePath

	deep := r.URL.Query().Get("deep") == "1"
	if deep {
		// Deep inspection boots a libguestfs appliance and can take tens
		// of seconds; run it in a background job so the HTTP request does
		// not block, and let the caller poll the job like any other.
		inspector, ok := diskprobe.InspectorAvailable()
		job := submitJob(jobOwner(r), "disk-probe", func() (any, error) {
			cctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			if !ok {
				return diskprobe.Deep(cctx, path, ""), nil
			}
			return diskprobe.Deep(cctx, path, inspector), nil
		})
		jsonResp(w, http.StatusAccepted, job)
		return
	}

	res, err := diskprobe.Basic(r.Context(), path)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, res)
}

func (h *Handler) DeleteDisk(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dev := chi.URLParam(r, "dev")
	if err := h.compute.DetachDisk(id, dev); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.disk_detach", id, map[string]interface{}{"dev": dev}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "detached"})
}

func (h *Handler) UpdateDisk(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dev := chi.URLParam(r, "dev")
	var req struct {
		Source string `json:"source"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.validateDiskSourceForCaller(r, req.Source); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	// Same attachment guard as CreateDisk: swapping a disk's source must
	// not let the caller grab an image that belongs to another VM.
	if err := h.checkSourceNotAttachedElsewhere(id, req.Source); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	if err := h.compute.UpdateDiskSource(id, dev, req.Source); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ChangeDiskBus switches an existing disk/cdrom to a different bus.
// Bus can't be changed in place (it's baked into the device's target
// naming and address), so this detaches and reattaches the same
// source file on the new bus — see ChangeDiskBus in the libvirt
// package for the mechanics.
func (h *Handler) ChangeDiskBus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dev := chi.URLParam(r, "dev")
	var req struct {
		Bus string `json:"bus"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validDiskBuses[req.Bus] {
		jsonErr(w, http.StatusBadRequest, "bus must be one of: virtio, sata, scsi, ide")
		return
	}
	if err := h.compute.ChangeDiskBus(id, dev, req.Bus); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.disk_bus_change", id, map[string]interface{}{"dev": dev, "bus": req.Bus}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ResizeDomainDisk grows the underlying file of a VM disk.
func (h *Handler) ResizeDomainDisk(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dev := chi.URLParam(r, "dev")
	var req struct {
		SizeGB int64 `json:"size_gb"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.SizeGB <= 0 {
		jsonErr(w, http.StatusBadRequest, "size_gb must be positive")
		return
	}
	// Quota: growing a disk counts against its owner, against both the
	// global disk cap and this disk's pool cap. We use the resized
	// disk's actual current size/pool (not the VM's first-disk figure).
	if owner := h.ownerOf(id); owner != "" {
		if u, gerr := h.userStore.Get(owner); gerr == nil && u.Role != models.RoleAdmin {
			cur, err := h.compute.GetDomain(id)
			if err != nil {
				// M-04: fail closed on growth — a failed lookup used to
				// skip the disk-cap check silently.
				slog.Error("disk_resize_quota_check_failed", "err", err, "vm", id)
				jsonErr(w, http.StatusServiceUnavailable, "cannot verify quota: "+err.Error())
				return
			}
			{
				pool := h.defaultPool()
				curSize := cur.DiskGB
				for _, d := range cur.Disks {
					if d.Target == dev {
						pool = d.Pool
						curSize = d.SizeGB
						break
					}
				}
				if req.SizeGB > curSize {
					delta := req.SizeGB - curSize
					if err := h.checkDiskQuota(owner, map[string]int64{pool: delta}); err != nil {
						jsonErr(w, http.StatusConflict, err.Error())
						return
					}
				}
			}
		}
	}
	newBytes, err := h.compute.ResizeDomainDisk(r.Context(), id, dev, req.SizeGB)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.disk_resize", id, map[string]interface{}{"dev": dev, "size_gb": req.SizeGB}))
	jsonResp(w, http.StatusOK, map[string]interface{}{
		"status":     "resized",
		"dev":        dev,
		"size_gb":    req.SizeGB,
		"size_bytes": newBytes,
	})
}

// Network interface handlers

func (h *Handler) ListNetIfaces(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, vm.Networks)
}

func (h *Handler) CreateNetIface(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req models.AttachNetRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Network == "" {
		jsonErr(w, http.StatusBadRequest, "network is required")
		return
	}
	if req.VLANTag != nil && (*req.VLANTag < 1 || *req.VLANTag > 4094) {
		jsonErr(w, http.StatusBadRequest, "vlan_tag must be between 1 and 4094")
		return
	}
	if owner, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := assertNetworkAllowed(u, req.Network); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
	}
	if err := h.compute.AttachNetworkIface(id, req); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.net_attach", id, map[string]interface{}{"network": req.Network}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "attached"})
}

func (h *Handler) DeleteNetIface(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	mac, err := url.PathUnescape(chi.URLParam(r, "mac"))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid mac encoding")
		return
	}
	if err := h.compute.DetachNetworkIface(id, mac); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.net_detach", id, map[string]interface{}{"mac": mac}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "detached"})
}
