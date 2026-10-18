package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/firewall"
	"webkvm/internal/models"
	"webkvm/internal/vmsched"
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
	if req.RAMMB < 0 || req.VCPUs < 0 || req.DiskGB < 0 {
		jsonErr(w, http.StatusBadRequest, "vcpus, ram_mb and disk_gb must be non-negative")
		return
	}
	if req.RAMMB == 0 {
		req.RAMMB = 2048
	}
	if req.VCPUs == 0 {
		req.VCPUs = 2
	}
	if req.DiskGB == 0 {
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

	req.Scope = h.mediaScope(r)
	vm, err := h.compute.CreateDomain(req)
	if err != nil {
		if errors.Is(err, compute.ErrForbidden) {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "resolve ISO") || strings.Contains(err.Error(), "media file") || strings.Contains(err.Error(), "ambiguous media") {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
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

	req.Scope = h.mediaScope(r)
	vm, err := h.compute.UpdateDomain(id, req)
	if err != nil {
		if errors.Is(err, compute.ErrForbidden) {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
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
	deleteDisks := r.URL.Query().Get("disks") == "true" ||
		r.URL.Query().Get("delete_disks") == "true" ||
		r.URL.Query().Get("deleteDisks") == "true" ||
		r.URL.Query().Get("disks") == "1"
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

// validTPMVersions is the set of TPM versions the VM create/edit forms offer.
var validTPMVersions = map[string]bool{"1.2": true, "2.0": true}

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
