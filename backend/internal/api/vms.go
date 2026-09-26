package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/diskprobe"
	"webkvm/internal/firewall"
	"webkvm/internal/models"
	"webkvm/internal/safego"
	"webkvm/internal/vmsched"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/vzdump"
)

func (h *Handler) ListVMs(w http.ResponseWriter, r *http.Request) {
	vms, err := h.compute.ListDomains()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// ACL & visibility for non-admins (ownership, allowed groups, allowed tags).
	vms = h.filterVMsByACL(r, vms)
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
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, vm)
}

func (h *Handler) CreateVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "create_vm"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	var req models.CreateVMRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := validateVMName(req.Name); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TPMVersion != "" && !validTPMVersions[req.TPMVersion] {
		jsonErr(w, http.StatusBadRequest, "tpm_version must be \"1.2\" or \"2.0\"")
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

	// The pool has to match what is being created. A container lands in
	// an Incus pool, a KVM disk in a libvirt disk pool, and neither may
	// land in the ISO library — libvirt would accept that last one
	// happily (both are directory pools) and drop a qcow2 among the
	// install media. Applies to admins too: this is storage layout, not
	// permissions. Mirrors the frontend's pool selectors.
	isContainer := req.Type == "container" || req.Image != ""
	wantPurpose := compute.PoolPurposeDisk
	if isContainer {
		wantPurpose = compute.PoolPurposeContainer
	}

	// A non-admin container with no pool used to be ACL-checked and
	// quota-charged against the libvirt disk pool (h.defaultPool()) while
	// Incus put the root disk in a container pool of its own choosing, so
	// the ACL guarded the wrong pool and the quota was booked on it.
	// Resolve the effective container pool up front (as DeployAppliance
	// does) and pin the create to it, so ACL, quota and the actual root
	// disk all name the same pool. Admins keep the historical behaviour
	// (Incus resolves the pool from the profile).
	owner, role, _ := audit.FromRequest(r)
	if isContainer && strings.TrimSpace(req.StoragePool) == "" && role != models.RoleAdmin {
		pool := h.containerPoolName("")
		if pool == "" {
			// Fail closed: letting Incus pick would bypass the pool ACL
			// and the per-pool quota.
			jsonErr(w, http.StatusServiceUnavailable, "cannot resolve a default container storage pool; specify one explicitly")
			return
		}
		if exists, active, perr := h.poolExistsActive(pool); perr != nil {
			jsonErr(w, http.StatusServiceUnavailable, "cannot verify storage pool: "+perr.Error())
			return
		} else if !exists {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("storage pool %q does not exist", pool))
			return
		} else if !active {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("storage pool %q is not active", pool))
			return
		}
		req.StoragePool = pool
	}

	if err := h.assertPoolPurpose(req.StoragePool, wantPurpose); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ExistingDiskPool != "" {
		if err := h.assertPoolPurpose(req.ExistingDiskPool, compute.PoolPurposeDisk); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Quota enforcement: the requesting user owns the new VM.
	if role != models.RoleAdmin {
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := h.checkQuota(owner, 1, int64(req.VCPUs), req.RAMMB, req.DiskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		// Per-pool disk cap (a new blank disk lands on req.StoragePool
		// or the default pool — a container's pool was already resolved
		// into req.StoragePool above; an existing disk is pre-allocated so it
		// does not consume new disk quota). Pool ACL, however, must be
		// checked in both cases: without this, a user restricted to a
		// subset of pools could land a VM on a disallowed pool simply
		// by pointing at an existing disk on it.
		if req.ExistingDiskPool != "" && req.ExistingDiskName != "" {
			if err := assertPoolAllowed(u, req.ExistingDiskPool); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
		} else {
			pool := req.StoragePool
			if pool == "" {
				pool = h.defaultPool()
			}
			if err := assertPoolAllowed(u, pool); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
			if err := h.checkDiskQuota(owner, map[string]int64{pool: req.DiskGB}); err != nil {
				jsonErr(w, http.StatusConflict, err.Error())
				return
			}
		}
		// Network ACL: a restricted user can only land the VM's NIC on
		// an allowed bridge. An empty req.Network falls back to the
		// host's primary bridge (internal/libvirt's unexported
		// mainBridge()) — that fallback isn't checked here since there
		// is no API-layer equivalent of h.defaultPool() for networks
		// yet; a low-risk, documented gap, since the create form always
		// preselects a concrete network.
		if req.Network != "" {
			if err := assertNetworkAllowed(u, req.Network); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}

	// Drop snippet references the caller may not read before any
	// path (validation, seed build, Incus user-data) resolves them.
	h.scrubCloudInitSnippets(r, req.CloudInit)

	// Fail fast on invalid cloud-init payloads BEFORE the VM is created,
	// so a bad form gets a clean 400 instead of a half-provisioned VM.
	if req.CloudInit != nil {
		if err := (cloudinit.Config{
			User:           req.CloudInit.User,
			Password:       req.CloudInit.Password,
			SSHKey:         req.CloudInit.SSHKey,
			Hostname:       req.CloudInit.Hostname,
			CustomUserData: req.CloudInit.CustomUserData,
			SnippetID:      req.CloudInit.SnippetID,
		}).Validate(); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if req.VLANTag != nil && (*req.VLANTag < 1 || *req.VLANTag > 4094) {
		jsonErr(w, http.StatusBadRequest, "vlan_tag must be between 1 and 4094")
		return
	}

	// Advanced options validation (Fase 5).
	switch req.BootOrder {
	case "", "disk", "cdrom", "network":
	default:
		jsonErr(w, http.StatusBadRequest, "boot_order must be one of: disk, cdrom, network")
		return
	}
	for _, p := range req.Profiles {
		if p == "" {
			jsonErr(w, http.StatusBadRequest, "incus profile names must not be empty")
			return
		}
	}

	vm, err := h.compute.CreateDomain(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Record the owner in VM metadata for quota accounting.
	if owner != "" {
		_, _ = h.compute.UpdateVMMeta(vm.ID, models.VMMetaUpdate{OwnerID: &owner})
	}
	// Optional cloud-init provisioning (user / password / SSH key / hostname).
	var createdPassword string
	if req.CloudInit != nil {
		// Remember the cloud-init username for later password resets.
		if req.CloudInit.User != "" {
			u := req.CloudInit.User
			_, _ = h.compute.UpdateVMMeta(vm.ID, models.VMMetaUpdate{CiUser: &u})
		}
		createdPassword = req.CloudInit.Password
		// LXD containers already received their cloud-init natively at
		// creation (user.user-data / user.network-config config keys) —
		// the NoCloud ISO path is KVM-only and would 501 on a container.
		if vm.Hypervisor != "incus" {
			if err := h.applyCloudInit(vm.ID, vm.Name, req.CloudInit); err != nil {
				jsonResp(w, http.StatusCreated, map[string]any{
					"id":      vm.ID,
					"name":    vm.Name,
					"warning": "cloud-init failed: " + err.Error(),
				})
				return
			}
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
	if req.TPMVersion != nil && !validTPMVersions[*req.TPMVersion] {
		jsonErr(w, http.StatusBadRequest, "tpm_version must be \"1.2\" or \"2.0\"")
		return
	}

	// Quota: growing vCPU/RAM on a VM counts against its owner.
	if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		if req.VCPUs != nil || req.RAMMB != nil {
			if owner := h.ownerOf(id); owner != "" {
				cur, err := h.compute.GetDomain(id)
				if err != nil {
					// M-04: fail closed on growth — a failed lookup used to
					// let vCPU/RAM grow silently past quota.
					slog.Error("vm_update_quota_check_failed", "err", err, "vm", id)
					jsonErr(w, http.StatusServiceUnavailable, "cannot verify quota: "+err.Error())
					return
				}
				{
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

	if req.BootOrder != nil {
		switch *req.BootOrder {
		case "", "disk", "cdrom", "network":
		default:
			jsonErr(w, http.StatusBadRequest, "boot_order must be one of: disk, cdrom, network")
			return
		}
	}
	for _, p := range req.Profiles {
		if p == "" {
			jsonErr(w, http.StatusBadRequest, "incus profile names must not be empty")
			return
		}
	}

	vm, err := h.compute.UpdateDomain(id, req)
	if err != nil {
		if errors.Is(err, compute.ErrDomainMustBeStoppedToRename) {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.update", id, nil))
	jsonResp(w, http.StatusOK, vm)
}

func (h *Handler) DeleteVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "delete_vm"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	// Capture the VM name BEFORE undefining: the disk-cleanup naming
	// convention (<vm>.qcow2, <vm>-<dev>.qcow2) is name-based.
	vm, _ := h.compute.GetDomain(id)
	deleteDisks := r.URL.Query().Get("disks") == "true"
	if err := h.compute.DeleteDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	var disksDeleted []string
	// Disks the sweep deliberately left behind. Reported rather than
	// only logged: a disk that survives because linked clones are
	// still backed by it is something the operator has to know about,
	// not something to discover later as unexplained used space.
	var disksKept []string
	if deleteDisks && vm.Name != "" {
		// Pass the VM's ACTUAL disk filenames, not just the name-based
		// convention: after a rename the disk keeps its original filename
		// (<oldname>.qcow2), so a name-only sweep would orphan it.
		var diskNames []string
		for _, d := range vm.Disks {
			if d.Device == "disk" && d.Name != "" {
				diskNames = append(diskNames, d.Name)
			}
		}
		var derr error
		disksDeleted, disksKept, derr = h.compute.DeleteVMDiskFiles(vm.Name, diskNames...)
		if derr != nil {
			h.logError("vm_disk_cleanup_failed", derr, vm.Name)
		} else if len(disksKept) > 0 {
			h.logError("vm_disk_cleanup_skipped",
				fmt.Errorf("still in use, locked, or the backing file of a linked clone: %v", disksKept), vm.Name)
		}
		// The cloud-init seed ISO lives outside every storage pool
		// (see cloudinitDir's doc comment) and outside vm.Disks, so
		// DeleteVMDiskFiles' pool sweep never sees it. Confirmed live
		// during an audit: deleting a cloud-init VM with ?disks=true
		// removed the qcow2 but left seed-<vm>.iso behind forever,
		// since nothing else ever references or cleans that path.
		isoPath := filepath.Join(h.cloudinitDir(), cloudInitSeedName(vm.Name))
		if rerr := os.Remove(isoPath); rerr == nil {
			disksDeleted = append(disksDeleted, isoPath)
		} else if !errors.Is(rerr, os.ErrNotExist) {
			h.logError("vm_cloudinit_seed_cleanup_failed", rerr, vm.Name)
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
	h.pruneVMCoverFiles(id, "") // uploaded <id>.<ext> covers
	h.audit.Log(auditFor(r, "vm.delete", id, map[string]interface{}{
		"disks_deleted": disksDeleted,
		"disks_kept":    disksKept,
	}))
	jsonResp(w, http.StatusOK, map[string]any{
		"status":        "deleted",
		"disks_deleted": disksDeleted,
		"disks_kept":    disksKept,
	})
}

// vmActionErr writes the right HTTP status for an instance operation:
// compute.ErrNotImplemented → 501 (the hypervisor backend does not
// support this operation, e.g. an LXD container before Fase 2), anything
// else → 500. Keeps containers from surfacing confusing internal errors.
func (h *Handler) vmActionErr(w http.ResponseWriter, err error, humanize func(error) string) {
	if errors.Is(err, compute.ErrNotImplemented) {
		jsonErr(w, http.StatusNotImplemented, err.Error())
		return
	}
	// State conflicts (start while running, force-off while stopped,
	// resume while not paused, ...) are a 409, not a 500.
	if errors.Is(err, compute.ErrDomainNotRunning) ||
		errors.Is(err, compute.ErrDomainNotPaused) ||
		errors.Is(err, compute.ErrDomainAlreadyRunning) {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	if humanize != nil {
		jsonErr(w, http.StatusInternalServerError, humanize(err))
		return
	}
	jsonErr(w, http.StatusInternalServerError, err.Error())
}

func (h *Handler) StartVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.checkStartQuota(id); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	if err := h.compute.StartDomain(id); err != nil {
		h.vmActionErr(w, err, humanizeStartError)
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
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.compute.ShutdownDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	h.audit.Log(auditFor(r, "vm.shutdown", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "shutdown"})
}

func (h *Handler) ForceOffVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.compute.ForceOffDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	h.audit.Log(auditFor(r, "vm.forceoff", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "force off"})
}

func (h *Handler) RebootVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.compute.RebootDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
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
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.compute.SuspendDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	h.audit.Log(auditFor(r, "vm.suspend", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "suspended"})
}

func (h *Handler) ResumeVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.checkStartQuota(id); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	if err := h.compute.ResumeDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	h.audit.Log(auditFor(r, "vm.resume", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "resumed"})
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

// SnapshotWithVM is a models.Snapshot tagged with the VM it belongs
// to, for the cross-fleet snapshots view.
type SnapshotWithVM struct {
	models.Snapshot
	VMID   string `json:"vm_id"`
	VMName string `json:"vm_name"`
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

// Disk handlers

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
	if req.Source != "" {
		if err := h.validateDiskSourcePath(req.Source); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		// A disk image may be attached to at most one VM at a time.
		// Sharing one image between two VMs means two independent write
		// streams into the same qcow2/raw file — silent, inescapable
		// data corruption. Refuse unless the only existing attachment is
		// this same VM (idempotent re-attach).
		if pool, vol, ok := h.resolveVolumeFromSource(req.Source); ok {
			if atts, aerr := h.compute.FindVolumeAttachments(pool, vol); aerr == nil {
				for _, a := range atts {
					if a.VMID != id {
						jsonErr(w, http.StatusConflict,
							fmt.Sprintf("disk %s is already attached to VM %q (%s)", vol, a.VMName, a.Target))
						return
					}
				}
			}
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
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.disk_attach", id, map[string]interface{}{"bus": req.Bus, "device": req.Device}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "attached"})
}

// normalizeAttachDiskRequest defaults an omitted device to "disk" and
// rejects device/bus values libvirt would refuse. Without it a request
// with no device reached libvirt as an empty device (500) after the new
// volume had already been created.
func normalizeAttachDiskRequest(req *models.AttachDiskRequest) error {
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
	if err := h.validateDiskSourcePath(path); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}

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
	if err := h.validateDiskSourcePath(req.Source); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	if err := h.compute.UpdateDiskSource(id, dev, req.Source); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "updated"})
}

// validDiskBuses is the same set the "Add Disk" dialog offers.
var validDiskBuses = map[string]bool{"virtio": true, "sata": true, "scsi": true, "ide": true}

// validTPMVersions is the set of TPM versions the VM create/edit forms offer.
var validTPMVersions = map[string]bool{"1.2": true, "2.0": true}

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

func (h *Handler) GetBootDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	device, err := h.compute.GetBootDevice(id)
	if err != nil {
		if errors.Is(err, compute.ErrNotImplemented) {
			jsonResp(w, http.StatusOK, map[string]string{"boot_device": ""})
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"boot_device": device})
}

// GetVMLogs returns the trailing lines of the VM's hypervisor / guest log.
func (h *Handler) GetVMLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	lines := 200
	if l := r.URL.Query().Get("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			lines = n
		}
	}
	logs, err := h.compute.GetDomainLog(id, lines)
	if err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"logs": logs})
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
	if req.Device != "disk" && req.Device != "cdrom" && req.Device != "network" {
		jsonErr(w, http.StatusBadRequest, "device must be one of: disk, cdrom, network")
		return
	}
	if err := h.compute.SetBootDevice(id, req.Device); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.boot_set", id, map[string]interface{}{"device": req.Device}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "boot device updated"})
}

// GetAutostart returns the libvirtd autostart flag for a VM.
func (h *Handler) GetAutostart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	enabled, err := h.compute.GetDomainAutostart(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"autostart": enabled})
}

// ListIncusProfiles returns the profile names available on the Incus
// backend for the container creation form (empty list on KVM-only hosts).
func (h *Handler) ListIncusProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.compute.ListIncusProfiles()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if profiles == nil {
		profiles = []string{}
	}
	jsonResp(w, http.StatusOK, map[string][]string{"profiles": profiles})
}

// ListIncusImages returns available Incus container images (official catalog + cached local images).
func (h *Handler) ListIncusImages(w http.ResponseWriter, r *http.Request) {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}

	images, err := h.compute.ListIncusImages()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if images == nil {
		images = []models.IncusImageItem{}
	}

	jsonResp(w, http.StatusOK, models.IncusImagesResponse{
		Images:       images,
		HostArch:     arch,
		IncusEnabled: h.cfg.IncusEnabled,
	})
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
	if err := h.compute.SetDomainAutostart(id, req.Enabled); err != nil {
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
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	if vm.State == models.VMStateRunning && vm.Hypervisor != "incus" {
		jsonErr(w, http.StatusConflict, "VM must be shut off before exporting")
		return
	}

	// Pre-flight: verify we can read every disk file before we
	// commit to a streaming response. Without this, a permission
	// error on a single disk surfaces inside the streaming
	// goroutine (after the headers are already sent) and the
	// client receives a truncated download with HTTP 200 instead
	// of a clean 500 with an actionable error. LXD containers have no
	// local disk files to pre-flight (their export is streamed natively
	// by the daemon, even while running).
	if vm.Hypervisor != "incus" {
		if err := h.compute.ValidateDomainDisks(id); err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
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
	case "proxmox":
		// Proxmox vzdump LXC format — only valid for Incus containers.
		if vm.Hypervisor != "incus" {
			jsonErr(w, http.StatusBadRequest, "format=proxmox is only available for Incus containers")
			return
		}
		h.exportProxmox(w, r, id, vm.Name)
	default:
		jsonErr(w, http.StatusBadRequest, "format must be 'ova', 'backup', or 'proxmox'")
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

	opts := compute.ExportBackupOptions{
		Compress:    compress,
		ZstdLevel:   zstdLevel,
		RepackDisks: repack,
	}
	h.streamLibvirtWrite(w, r, func(pw io.Writer) error {
		_, err := h.compute.ExportDomain(r.Context(), id, opts, pw)
		return err
	})
}

// exportProxmox streams a Proxmox-compatible vzdump LXC tar.zst for an
// Incus container. The flow is:
//  1. ExportDomain() streams the native Incus backup to a pipe.
//  2. vzdump.ExportToProxmox() reads the Incus stream and re-encodes it
//     as ./rootfs/... + ./etc/vzdump/pct.conf inside a tar.zst that
//     Proxmox's `pct restore` can consume directly.
//
// Because ExportToProxmox buffers the whole Incus stream to a tmpfile
// before conversion (needed to probe rootfs format), Content-Length
// is not known up-front — we use chunked transfer like exportBackup.
func (h *Handler) exportProxmox(w http.ResponseWriter, r *http.Request, id, name string) {
	zstdLevel := 19
	if v := r.URL.Query().Get("zstdlevel"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 22 {
			zstdLevel = n
		}
	}

	filename := vzdump.VzdumpFilename(name, time.Now())

	// Pipe: ExportDomain → pr → ExportToProxmox → tmpfile
	pr, pw2 := io.Pipe()
	var exportErr error
	go func() {
		opts := compute.ExportBackupOptions{
			Compress:  "",
			ZstdLevel: 0,
		}
		_, exportErr = h.compute.ExportDomain(r.Context(), id, opts, pw2)
		pw2.CloseWithError(exportErr)
	}()

	// Write to a tmpfile first so the zstd stream is fully terminated
	// before we start sending bytes to the client.
	tmpOut, tmpErr := os.CreateTemp("", "webkvm-export-*.tar.zst")
	if tmpErr != nil {
		jsonErr(w, http.StatusInternalServerError, "create tmpfile: "+tmpErr.Error())
		return
	}
	tmpPath := tmpOut.Name()
	defer os.Remove(tmpPath)
	defer tmpOut.Close()

	if err := vzdump.ExportToProxmox(pr, tmpOut, zstdLevel); err != nil {
		slog.Error("exportProxmox: convert failed", "id", id, "err", err)
		jsonErr(w, http.StatusInternalServerError, "export failed: "+err.Error())
		return
	}

	// Flush and close the tmpfile so its size is final.
	if err := tmpOut.Sync(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "sync tmpfile: "+err.Error())
		return
	}
	tmpOut.Close()

	// Now stream the complete file to the client.
	tmpInfo, err := os.Stat(tmpPath)
	if err != nil {
		slog.Error("exportProxmox: stat tmpfile failed", "id", id, "err", err)
		jsonErr(w, http.StatusInternalServerError, "stat tmpfile: "+err.Error())
		return
	}
	srcF, err := os.Open(tmpPath)
	if err != nil {
		slog.Error("exportProxmox: open tmpfile failed", "id", id, "err", err)
		jsonErr(w, http.StatusInternalServerError, "open tmpfile: "+err.Error())
		return
	}
	defer srcF.Close()

	w.Header().Set("Content-Type", "application/zstd")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", strconv.FormatInt(tmpInfo.Size(), 10))
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Headers are sent: a copy failure (client gone, disk error) can
	// only be logged, the truncated body is the client's signal.
	if _, err := io.Copy(w, srcF); err != nil {
		slog.Warn("exportProxmox: stream to client failed", "id", id, "err", err)
	}
}

func (h *Handler) exportOVA(w http.ResponseWriter, r *http.Request, id, target string) {
	var ovaTarget compute.OVATarget
	switch target {
	case "vmware", "":
		ovaTarget = compute.OVATargetVMware
	case "libvirt":
		ovaTarget = compute.OVATargetLibvirt
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

	opts := compute.OVAOptions{
		Target:    ovaTarget,
		Compress:  compute.OVACompressZstd,
		ZstdLevel: zstdLevel,
	}
	h.streamLibvirtWrite(w, r, func(pw io.Writer) error {
		return h.compute.ExportDomainOVA(r.Context(), id, opts, pw)
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
		defer safego.Recover("vm_stream")
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
	r.Body = http.MaxBytesReader(w, r.Body, 100<<30) // 100GB upload cap
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB in-memory buffer before disk spill
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
	if newName != "" {
		if err := validateVMName(newName); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	pool := strings.TrimSpace(r.FormValue("pool"))
	if pool == "" {
		pool = config.DiskPoolName
	}
	// An import writes the archive's contents into pool. This one
	// endpoint serves both worlds — a vzdump LXC becomes an Incus
	// container, everything else a KVM VM — and the format is only
	// known after the upload is parsed, so both natures are accepted
	// here and the format-specific routing below narrows it further
	// (containerPoolName remaps a mismatched pool for the LXC path).
	// What is refused outright is a pool that holds neither: the ISO
	// library and the backup/template shelves.
	if err := h.assertPoolPurposeAny(pool,
		compute.PoolPurposeDisk, compute.PoolPurposeContainer); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Optional overrides passed through to the importer. All are
	// optional: an empty/zero field keeps the archive's value. Both
	// name and network are validated against the same libvirt-safe
	// charset as a normal VM name: they are spliced into the domain
	// XML by the importer, and an unvalidated value there is an XML/
	// QEMU-argument injection vector (e.g. breaking out of a <name> or
	// <source network='...'/> element).
	network := strings.TrimSpace(r.FormValue("network"))
	if network != "" {
		if err := validateVMName(network); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid network: "+err.Error())
			return
		}
	}
	importOpts := compute.ImportOpts{
		Network: network,
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
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := assertPoolAllowed(u, pool); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if err := h.checkQuota(owner, 1, int64(vcpus), int64(ram), diskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		if err := h.checkDiskQuota(owner, map[string]int64{pool: diskGB}); err != nil {
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
	poolPath, err := h.compute.GetPoolPath(pool)
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

	// A vzdump LXC upload becomes an Incus container whose root lands in
	// containerPoolName(pool), not in pool (which only staged the upload
	// above). The format is only knowable now that the bytes are on
	// disk, so this is the earliest point the real target can be
	// authorised — still before anything reaches it: the staging file is
	// removed by the deferred cleanup on every refusal below. Pinning
	// pool to the resolved container pool makes importLocalArchive's own
	// containerPoolName(pool) return that exact pool, so the ACL/quota
	// check, the import and the post-import re-check all name one pool.
	isLXCImport := false
	if !requireOVA {
		if ok, _ := vzdump.IsVzdumpLXC(tmpPath); ok {
			isLXCImport = true
			ctPool := h.containerPoolName(pool)
			if owner, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
				if ctPool == "" {
					// Fail closed: letting Incus pick from its profile
					// would bypass the pool ACL and per-pool quota.
					jsonErr(w, http.StatusServiceUnavailable, "cannot resolve a default container storage pool; specify one explicitly")
					return
				}
				u, uerr := h.userStore.Get(owner)
				if uerr != nil {
					jsonErr(w, http.StatusUnauthorized, "user not found")
					return
				}
				if err := assertPoolAllowed(u, ctPool); err != nil {
					jsonErr(w, http.StatusForbidden, err.Error())
					return
				}
				if err := h.checkDiskQuota(owner, map[string]int64{ctPool: bytesToGB(hdr.Size)}); err != nil {
					jsonErr(w, http.StatusConflict, err.Error())
					return
				}
			}
			if ctPool != "" {
				pool = ctPool
			}
		}
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
	// Assign the importing user as the owner for quota accounting, and
	// drop any Shared flag the archive's own metadata carried (see
	// importedMetaUpdate) in the same update.
	owner, _, _ := audit.FromRequest(r)
	if _, merr := h.compute.UpdateVMMeta(uuid, importedMetaUpdate(owner)); merr != nil {
		slog.Warn("import_meta_reset_failed", "id", uuid, "err", merr)
	}
	if owner != "" {

		// V13-DATA-01 (anti-TOCTOU): quota was checked against the
		// *declared* import size before streaming. Re-check against the
		// REAL on-disk footprint of the materialized disk; on exceed,
		// delete the just-imported VM and its disk and fail the request
		// (no orphan, no silent over-quota import).
		//
		// A container has no libvirt volume to measure (Incus exposes
		// no GetStorageVolume and its root disk has no Source path, so
		// the volume re-check below always failed and rolled back every
		// quota'd LXC import). Its root is already counted in the
		// owner's usage now that OwnerID is set, so the re-check is a
		// pure current-usage check on the real container pool.
		if isLXCImport {
			if rerr := h.checkDiskQuota(owner, nil); rerr != nil {
				_ = h.compute.DeleteDomain(uuid)
				h.audit.Log(auditFor(r, "vm.import_quota_rollback", uuid, map[string]interface{}{
					"name": resolvedName, "pool": pool, "error": rerr.Error(),
				}))
				jsonErr(w, http.StatusConflict, "Cuota excedida tras la importación del disco. Recursos limpiados por seguridad.")
				return
			}
		} else if imported, gerr := h.compute.GetDomain(uuid); gerr == nil && len(imported.Disks) > 0 {
			vol := filepath.Base(imported.Disks[0].Source)
			if rerr := h.recheckDiskQuota(owner, pool, vol); rerr != nil {
				_ = h.compute.DeleteDomain(uuid)
				if imported.Name != "" {
					// Exact basenames: the name-prefix sweep alone misses
					// disks not named after the VM (e.g. base-* images).
					var diskNames []string
					for _, d := range imported.Disks {
						if d.Source != "" {
							diskNames = append(diskNames, filepath.Base(d.Source))
						}
					}
					_, _, _ = h.compute.DeleteVMDiskFiles(imported.Name, diskNames...)
				}
				h.audit.Log(auditFor(r, "vm.import_quota_rollback", uuid, map[string]interface{}{
					"name": imported.Name, "volume": vol, "error": rerr.Error(),
				}))
				jsonErr(w, http.StatusConflict, "Cuota excedida tras la importación del disco. Recursos limpiados por seguridad.")
				return
			}
		}
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
func (h *Handler) importLocalArchive(sourcePath, sourceFilename string, sourceSize int64, newName, pool string, requireOVA bool, opts compute.ImportOpts) (uuid, resolvedName string, warnings []string, format string, err error) {
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

	// Detect Proxmox vzdump LXC format: a tar (gzip/zstd/plain) whose
	// structure contains ./rootfs/ + ./etc/vzdump/pct.conf.
	// We probe the file independently from the format switch above so the
	// magic-byte check (isGzip/isZstd) is not disturbed.
	// NOTE: plain (uncompressed) vzdump tars must also be detected here —
	// e.g. vzdump-lxc-101-2026_01_01-00_00_00.tar without any compression.
	isVzdumpLXC := false
	if !isOVA {
		if ok, _ := vzdump.IsVzdumpLXC(sourcePath); ok {
			isVzdumpLXC = true
			format = ".tar.zst" // we always emit zstd on the import side
		}
	}

	switch {
	case isOVA:
		uuid, resolvedName, err = h.compute.ImportOVA(sourcePath, newName, pool)

	case isVzdumpLXC:
		// Convert Proxmox vzdump LXC → Incus backup tar, then import.
		convDest, cerr := os.CreateTemp("", "webkvm-vzdump-conv-*.tar")
		if cerr != nil {
			return "", "", nil, format, fmt.Errorf("vzdump conv tmpfile: %w", cerr)
		}
		convPath := convDest.Name()
		convDest.Close()
		defer os.Remove(convPath)

		// A vzdump LXC becomes an Incus container, so the pool written
		// into the generated backup.yaml must be an Incus pool. The
		// caller's choice is a libvirt disk pool by default, which
		// names nothing Incus knows; passing it through would embed a
		// bogus pool in the archive.
		ctPool := h.containerPoolName(pool)
		if cerr := vzdump.ImportFromProxmox(sourcePath, convPath, newName, ctPool); cerr != nil {
			return "", "", nil, format, fmt.Errorf("vzdump convert: %w", cerr)
		}
		uuid, resolvedName, warnings, err = h.compute.ImportDomain(convPath, newName, ctPool, opts)

	default:
		uuid, resolvedName, warnings, err = h.compute.ImportDomain(sourcePath, newName, pool, opts)
	}
	if err != nil {
		return "", "", nil, format, err
	}
	_ = sourceFilename // reserved for future audit enrichment
	_ = sourceSize     // reserved for future audit enrichment
	return uuid, resolvedName, warnings, format, nil
}

// importedMetaUpdate is the metadata update applied to every domain
// defined from a user-supplied archive (upload import, restore-as-VM).
// The archive's domain.xml <metadata> is kept verbatim by the import, so
// it can claim any webkvm flag; Shared is admin-only and grants every
// user the right to instantiate the VM as a template, so it is always
// cleared here (an admin re-shares explicitly). The importing user, when
// known, becomes the owner in the same update.
func importedMetaUpdate(owner string) models.VMMetaUpdate {
	notShared := false
	upd := models.VMMetaUpdate{Shared: &notShared}
	if owner != "" {
		upd.OwnerID = &owner
	}
	return upd
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
