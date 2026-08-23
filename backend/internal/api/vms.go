package api

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"webkvm/internal/audit"
	"webkvm/internal/cloudinit"
	"webkvm/internal/config"
	"webkvm/internal/firewall"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/vmsched"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) ListVMs(w http.ResponseWriter, r *http.Request) {
	vms, err := h.lv.ListDomains()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, vms)
}

// humanizeStartError takes the raw libvirt error returned by
// StartDomain and turns it into something an operator can act on.
// The default virError(Code=..., Domain=..., Message=...) blob is
// technically complete but hostile to read, especially the "Cannot
// access storage file" case which is the most common failure mode
// after a fresh import (a CDROM ISO is missing on the destination).
func humanizeStartError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Cannot access storage file"):
		// Pull the path out of the libvirt error blob so we can
		// show a clean "ISO is missing" / "disk is missing" hint.
		if i := strings.Index(msg, "'/"); i >= 0 {
			if j := strings.Index(msg[i+1:], "'"); j >= 0 {
				path := msg[i+1 : i+1+j]
				_, statErr := os.Stat(path)
				if os.IsNotExist(statErr) {
					return fmt.Sprintf("Cannot start VM: storage file does not exist on this host: %s. Upload the ISO via the Storage page and attach it, or remove the disk/CDROM device from the VM configuration.", path)
				}
				return fmt.Sprintf("Cannot start VM: storage file is not readable: %s. Check file permissions and that the libvirt process can access it.", path)
			}
		}
		return "Cannot start VM: one of the configured storage files is missing or unreadable. Check the VM's disk and CDROM configuration."
	case strings.Contains(msg, "machine type"):
		return fmt.Sprintf("Cannot start VM: %s. The destination hypervisor does not support the machine type in the VM definition. Re-import the VM from a host with a compatible qemu version.", msg)
	case strings.Contains(msg, "Cannot find 'efi' firmware"):
		return "Cannot start VM: OVMF/EFI firmware is not installed on this host. Install the ovmf package (e.g. apt install ovmf) and try again."
	}
	return msg
}

func (h *Handler) GetVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.lv.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, vm)
}

func (h *Handler) CreateVM(w http.ResponseWriter, r *http.Request) {
	var req models.CreateVMRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateVMName(req.Name); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.RAMMB <= 0 {
		req.RAMMB = 2048
	}
	if req.VCPUs <= 0 {
		req.VCPUs = 2
	}
	if req.DiskGB <= 0 {
		req.DiskGB = 20
	}

	// Quota enforcement: the requesting user owns the new VM.
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		if err := h.checkQuota(owner, 1, int64(req.VCPUs), req.RAMMB, req.DiskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}

	vm, err := h.lv.CreateDomain(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Record the owner in the VM metadata for quota accounting.
	if owner != "" {
		_, _ = h.lv.UpdateVMMeta(vm.ID, models.VMMetaUpdate{OwnerID: &owner})
	}
	// Optional cloud-init provisioning (user / password / SSH key / hostname).
	var createdPassword string
	// Fail fast on invalid cloud-init payloads BEFORE the VM is created,
	// so a bad form gets a clean 400 instead of a half-provisioned VM.
	if req.CloudInit != nil {
		if err := (cloudinit.Config{User: req.CloudInit.User, Password: req.CloudInit.Password,
			SSHKey: req.CloudInit.SSHKey, Hostname: req.CloudInit.Hostname}).Validate(); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.CloudInit != nil {
		// Remember the cloud-init username for later password resets.
		if req.CloudInit.User != "" {
			u := req.CloudInit.User
			_, _ = h.lv.UpdateVMMeta(vm.ID, models.VMMetaUpdate{CiUser: &u})
		}
		createdPassword = req.CloudInit.Password
		if err := h.applyCloudInit(vm.ID, vm.Name, req.CloudInit); err != nil {
			jsonResp(w, http.StatusCreated, map[string]any{
				"id":      vm.ID,
				"name":    vm.Name,
				"warning": "cloud-init failed: " + err.Error(),
			})
			return
		}
	}
	h.audit.Log(auditFor(r, "vm.create", vm.ID, map[string]interface{}{"name": vm.Name}))
	resp := map[string]any{"id": vm.ID, "name": vm.Name}
	if createdPassword != "" {
		resp["password"] = createdPassword
		resp["password_warning"] = "Save this password! It won't be shown again."
	}
	jsonResp(w, http.StatusCreated, resp)
}

func (h *Handler) UpdateVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req models.UpdateVMRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != nil {
		if err := validateVMName(*req.Name); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Quota: growing vCPU/RAM on a VM counts against its owner.
	if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		if req.VCPUs != nil || req.RAMMB != nil {
			if owner := h.ownerOf(id); owner != "" {
				cur, err := h.lv.GetDomain(id)
				if err == nil {
					addVCPU := int64(0)
					addRAM := int64(0)
					if req.VCPUs != nil && *req.VCPUs > cur.VCPUs {
						addVCPU = int64(*req.VCPUs - cur.VCPUs)
					}
					if req.RAMMB != nil && *req.RAMMB > cur.RAMMB {
						addRAM = *req.RAMMB - cur.RAMMB
					}
					if addVCPU > 0 || addRAM > 0 {
						if err := h.checkQuota(owner, 0, addVCPU, addRAM, 0); err != nil {
							jsonErr(w, http.StatusConflict, err.Error())
							return
						}
					}
				}
			}
		}
	}

	vm, err := h.lv.UpdateDomain(id, req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.update", id, nil))
	jsonResp(w, http.StatusOK, vm)
}

func (h *Handler) DeleteVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Capture the VM name BEFORE undefining: the disk-cleanup naming
	// convention (<vm>.qcow2, <vm>-<dev>.qcow2) is name-based.
	vm, _ := h.lv.GetDomain(id)
	deleteDisks := r.URL.Query().Get("disks") == "true"
	if err := h.lv.DeleteDomain(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var disksDeleted []string
	if deleteDisks && vm.Name != "" {
		var skipped []string
		var derr error
		disksDeleted, skipped, derr = h.lv.DeleteVMDiskFiles(vm.Name)
		if derr != nil {
			h.logError("vm_disk_cleanup_failed", derr, vm.Name)
		} else if len(skipped) > 0 {
			h.logError("vm_disk_cleanup_skipped", fmt.Errorf("still attached or locked: %v", skipped), vm.Name)
		}
	}
	// Clean up any orphaned per-VM state so the stores never keep
	// rules/schedules for a VM that no longer exists.
	if h.fwStore != nil {
		if err := h.fwStore.Set(firewall.VMFirewall{VMID: id}); err != nil {
			h.logError("firewall_cleanup_after_delete_failed", err, id)
		} else if h.fwMgr != nil {
			if _, ferr := h.fwMgr.Apply(); ferr != nil {
				h.logError("firewall_reapply_after_delete_failed", ferr, id)
			}
		}
	}
	if h.vmSchedStore != nil {
		if err := h.vmSchedStore.Set(id, vmsched.Schedule{}); err != nil {
			h.logError("vmsched_cleanup_after_delete_failed", err, id)
		} else if h.vmScheduler != nil {
			h.vmScheduler.Rebuild()
		}
	}
	h.audit.Log(auditFor(r, "vm.delete", id, map[string]interface{}{"disks_deleted": disksDeleted}))
	jsonResp(w, http.StatusOK, map[string]any{"status": "deleted", "disks_deleted": disksDeleted})
}

func (h *Handler) StartVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.lv.StartDomain(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, humanizeStartError(err))
		return
	}
	// Re-apply the firewall so port forwards for this VM take effect
	// once it boots and grabs its IP. Best-effort: a failure here is
	// logged, never fatal to the start.
	if h.fwMgr != nil {
		if _, ferr := h.fwMgr.Apply(); ferr != nil {
			h.logError("firewall_reapply_after_start_failed", ferr, id)
		}
	}
	h.audit.Log(auditFor(r, "vm.start", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "started"})
}

func (h *Handler) ShutdownVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.lv.ShutdownDomain(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.shutdown", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "shutdown"})
}

func (h *Handler) ForceOffVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.lv.ForceOffDomain(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.forceoff", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "force off"})
}

func (h *Handler) RebootVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.lv.RebootDomain(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.fwMgr != nil {
		if _, ferr := h.fwMgr.Apply(); ferr != nil {
			h.logError("firewall_reapply_after_reboot_failed", ferr, id)
		}
	}
	h.audit.Log(auditFor(r, "vm.reboot", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "reboot"})
}

func (h *Handler) SuspendVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.lv.SuspendDomain(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.suspend", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "suspended"})
}

func (h *Handler) ResumeVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.lv.ResumeDomain(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.resume", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "resumed"})
}

func (h *Handler) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	snaps, err := h.lv.ListSnapshots(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, snaps)
}

func (h *Handler) CreateSnapshot(w http.ResponseWriter, r *http.Request) {
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
	snap, err := h.lv.CreateSnapshot(id, req)
	if err != nil {
		if errors.Is(err, libvirt.ErrMemorySnapshotRequiresRunning) {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.snapshot_create", id, map[string]interface{}{
		"snap":            snap.Name,
		"allocated_bytes": snap.SizeAtSnapBytes,
	}))
	jsonResp(w, http.StatusCreated, snap)
}

func (h *Handler) DeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sid := chi.URLParam(r, "sid")
	allocated, err := h.lv.DeleteSnapshot(id, sid)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.snapshot_delete", id, map[string]interface{}{
		"snap":            sid,
		"allocated_bytes": allocated,
	}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) RevertSnapshot(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sid := chi.URLParam(r, "sid")
	if err := h.lv.RevertSnapshot(id, sid); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.snapshot_revert", id, map[string]interface{}{"snap": sid}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "reverted"})
}

// Disk handlers

func (h *Handler) ListDisks(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.lv.GetDomain(id)
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
	if err := h.lv.AttachDisk(id, req); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.disk_attach", id, map[string]interface{}{"bus": req.Bus, "device": req.Device}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "attached"})
}

func (h *Handler) DeleteDisk(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	dev := chi.URLParam(r, "dev")
	if err := h.lv.DetachDisk(id, dev); err != nil {
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
	if err := h.lv.UpdateDiskSource(id, dev, req.Source); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "updated"})
}

// Network interface handlers

func (h *Handler) ListNetIfaces(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.lv.GetDomain(id)
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
	if err := h.lv.AttachNetworkIface(id, req); err != nil {
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
	if err := h.lv.DetachNetworkIface(id, mac); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.net_detach", id, map[string]interface{}{"mac": mac}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "detached"})
}

// Clone handler

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
	// Quota: a clone counts against the source VM's owner (an admin
	// cloning their own infra stays exempt).
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		if o := h.ownerOf(id); o != "" {
			owner = o
		}
		// Estimate the clone's size from the source before cloning.
		src, err := h.lv.GetDomain(id)
		if err == nil {
			if err := h.checkQuota(owner, 1, int64(src.VCPUs), src.RAMMB, src.DiskGB); err != nil {
				jsonErr(w, http.StatusConflict, err.Error())
				return
			}
		}
	}
	vm, err := h.lv.CloneDomain(id, req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if owner != "" {
		_, _ = h.lv.UpdateVMMeta(vm.ID, models.VMMetaUpdate{OwnerID: &owner})
	}
	h.audit.Log(auditFor(r, "vm.clone", id, map[string]interface{}{"new_id": vm.ID, "name": req.Name}))
	jsonResp(w, http.StatusCreated, vm)
}

func (h *Handler) GetBootDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	device, err := h.lv.GetBootDevice(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"boot_device": device})
}

func (h *Handler) SetBootDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Device string `json:"device"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Device == "" {
		jsonErr(w, http.StatusBadRequest, "device is required")
		return
	}
	if err := h.lv.SetBootDevice(id, req.Device); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.boot_set", id, map[string]interface{}{"device": req.Device}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "boot device updated"})
}

// GetAutostart returns the libvirtd autostart flag for a VM.
func (h *Handler) GetAutostart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	enabled, err := h.lv.GetDomainAutostart(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"autostart": enabled})
}

// SetAutostart toggles the libvirtd autostart flag for a VM.
func (h *Handler) SetAutostart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.lv.SetDomainAutostart(id, req.Enabled); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.autostart_set", id, map[string]interface{}{"enabled": req.Enabled}))
	jsonResp(w, http.StatusOK, map[string]bool{"autostart": req.Enabled})
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
	// Quota: growing a disk counts against its owner. We approximate
	// the delta by comparing with the VM's reported DiskGB.
	if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		if owner := h.ownerOf(id); owner != "" {
			if cur, err := h.lv.GetDomain(id); err == nil && req.SizeGB > cur.DiskGB {
				delta := req.SizeGB - cur.DiskGB
				if err := h.checkQuota(owner, 0, 0, 0, delta); err != nil {
					jsonErr(w, http.StatusConflict, err.Error())
					return
				}
			}
		}
	}
	newBytes, err := h.lv.ResizeDomainDisk(r.Context(), id, dev, req.SizeGB)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.disk_resize", id, map[string]interface{}{"dev": dev, "size_gb": req.SizeGB}))
	jsonResp(w, http.StatusOK, map[string]interface{}{
		"status":    "resized",
		"dev":       dev,
		"size_gb":   req.SizeGB,
		"size_bytes": newBytes,
	})
}

// ExportVM streams a .tar.gz backup of the VM (XML + disk images) directly
// to the HTTP response. No temp file is created on disk: a goroutine writes
// the tar+gzip stream to an io.Pipe and the handler copies that pipe to
// the response writer. The download starts as soon as the first bytes
// are produced, and works for arbitrarily large disks without using
// extra disk space.
// ExportVM streams a backup of the VM to the client. The format
// depends on query parameters:
//
//	?format=ova&target=vmware  -> .ova with VMDK (VirtualBox/VMware compatible)
//	?format=ova&target=libvirt -> .ova with qcow2 (Proxmox/libvirt/GNOME Boxes)
//	?format=backup (default)   -> WebKVM backup (zstd compressed by default)
//	?compress=1                -> (backup only) re-pack disks with qemu-img -c
//	?legacy=gzip               -> (backup only) use gzip instead of zstd
//
// Content-Length is set so the browser can show a progress bar; the
// estimate is the upper bound on the output size.
func (h *Handler) ExportVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.lv.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	if vm.State == models.VMStateRunning {
		jsonErr(w, http.StatusConflict, "VM must be shut off before exporting")
		return
	}

	// Pre-flight: verify we can read every disk file before we
	// commit to a streaming response. Without this, a permission
	// error on a single disk surfaces inside the streaming
	// goroutine (after the headers are already sent) and the
	// client receives a truncated download with HTTP 200 instead
	// of a clean 500 with an actionable error.
	if err := h.lv.ValidateDomainDisks(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	format := strings.ToLower(r.URL.Query().Get("format"))
	target := strings.ToLower(r.URL.Query().Get("target"))
	if format == "" {
		format = "backup"
	}

	switch format {
	case "ova":
		h.exportOVA(w, r, id, target)
	case "backup":
		h.exportBackup(w, r, id)
	default:
		jsonErr(w, http.StatusBadRequest, "format must be 'ova' or 'backup'")
	}
}

func (h *Handler) exportBackup(w http.ResponseWriter, r *http.Request, id string) {
	repack := r.URL.Query().Get("compress") == "1"
	compress := strings.ToLower(r.URL.Query().Get("legacy"))
	if compress == "" {
		compress = "zstd"
	}
	zstdLevel := 19
	if v := r.URL.Query().Get("zstdlevel"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 22 {
			zstdLevel = n
		}
	}

	// No Content-Length: the response is zstd-compressed and the
	// compressed size is not known until the stream is produced.
	// Chunked transfer encoding lets the browser accept the bytes
	// as they arrive without expecting a specific total.

	ext := "tar.zst"
	contentType := "application/zstd"
	if compress == "gzip" {
		ext = "tar.gz"
		contentType = "application/gzip"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, id, ext))
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	opts := libvirt.ExportBackupOptions{
		Compress:    compress,
		ZstdLevel:   zstdLevel,
		RepackDisks: repack,
	}
	h.streamLibvirtWrite(w, r, func(pw io.Writer) error {
		_, err := h.lv.ExportDomain(r.Context(), id, opts, pw)
		return err
	})
}

func (h *Handler) exportOVA(w http.ResponseWriter, r *http.Request, id, target string) {
	var ovaTarget libvirt.OVATarget
	switch target {
	case "vmware", "":
		ovaTarget = libvirt.OVATargetVMware
	case "libvirt":
		ovaTarget = libvirt.OVATargetLibvirt
	default:
		jsonErr(w, http.StatusBadRequest, "target must be 'vmware' or 'libvirt'")
		return
	}

	zstdLevel := 19
	if v := r.URL.Query().Get("zstdlevel"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 22 {
			zstdLevel = n
		}
	}

	// No Content-Length: the tar size is not known until qemu-img
	// has produced the disk(s), and we stream as soon as the first
	// bytes are ready. Omitting it makes both OVA targets use
	// chunked transfer encoding.
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s.ova"`, id, ovaTarget))
	w.Header().Set("X-Export-Target", string(ovaTarget))
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	opts := libvirt.OVAOptions{
		Target:    ovaTarget,
		Compress:  libvirt.OVACompressZstd,
		ZstdLevel: zstdLevel,
	}
	h.streamLibvirtWrite(w, r, func(pw io.Writer) error {
		return h.lv.ExportDomainOVA(r.Context(), id, opts, pw)
	})
}

// streamLibvirtWrite is the shared chunked-streaming helper. It
// starts the producer in a goroutine, pipes its output to the HTTP
// response, and cancels the producer if the client disconnects.
//
// If the producer returns an error after the headers have been
// committed (HTTP 200 already sent), the response is necessarily
// truncated — we can't retroactively change the status code. In that
// case we log the error prominently so the operator can investigate;
// the client will see a short/invalid download and its decompressor
// or archive reader will report the corruption.
func (h *Handler) streamLibvirtWrite(w http.ResponseWriter, r *http.Request, producer func(io.Writer) error) {
	flusher, _ := w.(http.Flusher)
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		defer close(errCh)
		defer pw.Close()
		if err := producer(pw); err != nil {
			_ = pw.CloseWithError(err)
			errCh <- err
			return
		}
		errCh <- nil
	}()

	buf := make([]byte, 64*1024)
	for {
		n, rerr := pr.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				// Client disconnected. Cancel the producer
				// by closing the read side; the goroutine's
				// next pw.Write will fail and it will exit.
				_ = pr.CloseWithError(werr)
				break
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			if rerr != io.EOF {
				// Producer failed mid-stream. The response
				// is already committed (200 + headers), so
				// we can't change the status code. Log the
				// error so the operator can investigate;
				// the client will see a truncated download.
				slog.Warn("export_stream_truncated", "err", rerr)
			}
			break
		}
	}
	// Drain the error channel. If the producer reported an error,
	// it's already been logged above; we just need to unblock the
	// goroutine's defer close(errCh).
	<-errCh
}

// ImportVM uploads a backup and creates a new VM from it. The
// archive format is auto-detected: gzip legacy (.tar.gz), zstd
// (.tar.zst, the new default), or OVA (.ova, OVF descriptor + VMDK
// or qcow2 disks). For OVA uploads, the implementation delegates to
// ImportOVA so the same flow covers both /import and /import-ova.
func (h *Handler) ImportVM(w http.ResponseWriter, r *http.Request) {
	h.importArchive(w, r, false)
}

// ImportOVA is the explicit OVA upload endpoint. It is a thin
// wrapper around importArchive that requires OVA format.
func (h *Handler) ImportOVA(w http.ResponseWriter, r *http.Request) {
	h.importArchive(w, r, true)
}

func (h *Handler) importArchive(w http.ResponseWriter, r *http.Request, requireOVA bool) {
	if err := r.ParseMultipartForm(1 << 30); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "file field required")
		return
	}
	defer file.Close()

	newName := strings.TrimSpace(r.FormValue("name"))
	pool := strings.TrimSpace(r.FormValue("pool"))
	if pool == "" {
		pool = config.DiskPoolName
	}

	// Optional overrides passed through to the importer. All are
	// optional: an empty/zero field keeps the archive's value.
	importOpts := libvirt.ImportOpts{
		Network: strings.TrimSpace(r.FormValue("network")),
	}
	if v := r.FormValue("vcpus"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			importOpts.VCPUs = n
		}
	}
	if v := r.FormValue("ram_mb"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			importOpts.RAMMB = n
		}
	}
	if v := r.FormValue("autostart"); v != "" {
		b := v == "true" || v == "1"
		importOpts.Autostart = &b
	}

	// Quota check (non-admins): estimate the import's footprint from
	// the upload size and the requested overrides.
	if owner, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		vcpus := importOpts.VCPUs
		if vcpus == 0 {
			vcpus = 2
		}
		ram := importOpts.RAMMB
		if ram == 0 {
			ram = 2048
		}
		diskGB := bytesToGB(hdr.Size)
		if err := h.checkQuota(owner, 1, int64(vcpus), int64(ram), diskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}

	// Log the import attempt up front. The handler can take many
	// minutes for multi-GB disks, and a crash mid-import would
	// otherwise leave no trace in the audit log (vm.create only
	// fires on success).
	h.audit.Log(auditFor(r, "vm.import", "pending", map[string]interface{}{
		"action":     "start",
		"filename":   hdr.Filename,
		"size_bytes": hdr.Size,
		"pool":       pool,
		"name":       newName,
		"ova":        requireOVA,
	}))

	// Resolve the pool path early so we can stream the upload
	// straight into the pool directory instead of /tmp. This is
	// important because /tmp is often a small tmpfs and large
	// uploads (OVA/WebKVM backups of multi-GB disks) can fill it
	// up, causing the import to abort with a network error.
	poolPath, err := h.lv.GetPoolPath(pool)
	if err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "resolve pool path: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, "resolve pool path: "+err.Error())
		return
	}

	// Write the upload to a .tmp file inside the destination pool.
	// The .tmp extension makes it obvious that the file is partial
	// and should be ignored by libvirt until the import finishes.
	tmp, err := os.CreateTemp(poolPath, "webkvm-import-*.tmp")
	if err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "create temp file: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, "create temp file in pool: "+err.Error())
		return
	}
	tmpPath := tmp.Name()
	defer tmp.Close()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, file); err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "save upload: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, "save upload: "+err.Error())
		return
	}
	if err := tmp.Sync(); err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "sync upload: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Close before passing to the importer so qemu-img can read it
	// on platforms that disallow open-for-write/read concurrency.
	if err := tmp.Close(); err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    "close upload: " + err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	uuid, resolvedName, warnings, format, err := h.importLocalArchive(tmpPath, hdr.Filename, hdr.Size, newName, pool, requireOVA, importOpts)
	if err != nil {
		h.audit.Log(auditFor(r, "vm.import_failed", "unknown", map[string]interface{}{
			"filename": hdr.Filename,
			"error":    err.Error(),
		}))
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Assign the importing user as the owner for quota accounting.
	if owner, _, _ := audit.FromRequest(r); owner != "" {
		_, _ = h.lv.UpdateVMMeta(uuid, models.VMMetaUpdate{OwnerID: &owner})
	}

	h.audit.Log(auditFor(r, "vm.import", uuid, map[string]interface{}{
		"action":     "done",
		"filename":   hdr.Filename,
		"name":       resolvedName,
		"size_bytes": hdr.Size,
		"warnings":   len(warnings),
	}))
	resp := map[string]any{
		"status":         "imported",
		"id":             uuid,
		"name":           resolvedName,
		"requested_name": newName,
		"filename":       hdr.Filename,
		"format":         format,
	}
	if len(warnings) > 0 {
		resp["warnings"] = warnings
	}
	jsonResp(w, http.StatusCreated, resp)
}

// importLocalArchive is the core "import a VM from a local
// archive" routine, shared between the multipart upload path
// (POST /api/vms/import, /api/vms/import-ova) and the restore
// path (POST /api/backup/targets/{id}/restore-as-vm).
//
// The caller is responsible for putting the bytes at
// sourcePath — multipart handlers stream the upload to a
// tmpfile in the pool first; restore handlers pass the path
// of an existing backup file on the target's directory.
//
// The sniff-then-delegate logic (gzip/zstd/none detection,
// OVA vs WebKVM branch) lives here so both entry points stay
// in lock-step. The audit logging stays in the caller
// because the audit event names differ ("vm.import" vs
// "vm.restore").
func (h *Handler) importLocalArchive(sourcePath, sourceFilename string, sourceSize int64, newName, pool string, requireOVA bool, opts libvirt.ImportOpts) (uuid, resolvedName string, warnings []string, format string, err error) {
	f, err := os.Open(sourcePath)
	if err != nil {
		return "", "", nil, "", fmt.Errorf("open source: %w", err)
	}
	defer f.Close()

	// Sniff the first 4 bytes to detect the compression format
	// (gzip/zstd/none). OVA is the *container* format — its inner
	// content is the same tar/zstd structure as a WebKVM backup.
	// We distinguish them by the requireOVA flag (which means
	// the caller explicitly asked for OVA processing).
	br := bufio.NewReader(f)
	head, _ := br.Peek(4)

	isGzip := len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b
	isZstd := len(head) >= 4 && head[0] == 0x28 && head[1] == 0xb5 && head[2] == 0x2f && head[3] == 0xfd

	isOVA := requireOVA
	if isOVA {
		// OVA files are tar archives; they may be compressed
		// (gzip/zstd) or not. We pick the suffix accordingly.
		format = ".ova"
	} else {
		switch {
		case isGzip:
			format = ".tar.gz"
		case isZstd:
			format = ".tar.zst"
		default:
			format = ".bin"
		}
	}

	switch {
	case isOVA:
		uuid, resolvedName, err = h.lv.ImportOVA(sourcePath, newName, pool)
	default:
		uuid, resolvedName, warnings, err = h.lv.ImportDomain(sourcePath, newName, pool, opts)
	}
	if err != nil {
		return "", "", nil, format, err
	}
	_ = sourceFilename // reserved for future audit enrichment
	_ = sourceSize     // reserved for future audit enrichment
	return uuid, resolvedName, warnings, format, nil
}

// validVMNameRE matches libvirt-safe VM names: alphanumeric, dots,
// hyphens, underscores, 1-64 chars.
var validVMNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func validateVMName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !validVMNameRE.MatchString(name) {
		return fmt.Errorf("invalid VM name: must be 1-64 characters, alphanumeric, dots, hyphens, or underscores")
	}
	return nil
}
