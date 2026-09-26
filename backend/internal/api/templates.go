package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"webkvm/internal/audit"
	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// MakeVMTemplate (operator/admin) marks a VM as a template. The VM
// must be shut off (a running VM cannot be safely used as a template).
func (h *Handler) MakeVMTemplate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if vm.State != models.VMStateShutoff {
		jsonErr(w, http.StatusConflict, "a VM must be shut off before turning it into a template")
		return
	}
	// Always (re)start unshared: Shared is admin-only and must be an
	// explicit admin decision on the template itself, never inherited
	// from a flag the VM happened to carry (an imported archive's
	// metadata, a snapshot, a previous template life).
	t, f := true, false
	if _, err := h.compute.UpdateVMMeta(id, models.VMMetaUpdate{Template: &t, Shared: &f}); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.make_template", id, map[string]any{"name": vm.Name}))
	jsonResp(w, http.StatusOK, map[string]any{"status": "template", "id": id})
}

// UnsetVMTemplate (operator/admin) turns a template back into a
// normal VM.
func (h *Handler) UnsetVMTemplate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	f := false
	// Sharing only makes sense for templates: clear it too, so a VM that
	// is later re-templated does not silently come back shared.
	if _, err := h.compute.UpdateVMMeta(id, models.VMMetaUpdate{Template: &f, Shared: &f}); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.unset_template", id, nil))
	jsonResp(w, http.StatusOK, map[string]any{"status": "vm"})
}

// ListTemplates returns the VMs flagged as templates.
func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	vms, err := h.compute.ListDomains()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// ACL & visibility for non-admins, exactly as ListVMs does
	// (vms.go:43). Without this the template list is a full-fleet dump
	// for any authenticated caller: it names every VM in the install
	// that carries the template flag, including the owner's identity
	// and disks, and it is the directory InstantiateTemplate clones
	// from. The restriction that used to exist lived only in the UI.
	//
	// Exception: templates an admin flagged Shared are listed for every
	// user, whatever the ACL says (they are meant to be instantiated by
	// anyone). The ACL set is computed up front so a shared template the
	// ACL would drop can still be kept once its meta is read.
	allowed := make(map[string]bool, len(vms))
	for _, vm := range h.filterVMsByACL(r, vms) {
		allowed[vm.ID] = true
	}
	out := []models.VM{}
	for _, vm := range vms {
		meta, err := h.compute.GetVMMeta(vm.ID)
		if err != nil {
			continue
		}
		if meta.Template && (allowed[vm.ID] || meta.Shared) {
			// Hide disks/networks to keep the response light.
			vm.Disks = nil
			vm.Networks = nil
			out = append(out, vm)
		}
	}
	jsonResp(w, http.StatusOK, map[string]any{"templates": out})
}

// InstantiateTemplate clones a template into a new VM, optionally
// applying cloud-init provisioning.
func (h *Handler) InstantiateTemplate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Name    string `json:"name"`
		Pool    string `json:"pool,omitempty"`
		Network string `json:"network,omitempty"`
		// Linked creates the new VM's disk as a qcow2 overlay backed by
		// the template's disk instead of a full copy: near-instant and
		// initially near-zero on disk, at the cost of staying dependent
		// on the template (which is why templates are flagged read-only
		// in the UI). See CloneVMRequest.Linked.
		Linked    bool                     `json:"linked,omitempty"`
		CloudInit *models.CloudInitRequest `json:"cloud_init,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if err := validateVMName(req.Name); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// The source must be a template.
	meta, err := h.compute.GetVMMeta(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !meta.Template {
		jsonErr(w, http.StatusConflict, "source VM is not a template")
		return
	}

	// Access to the SOURCE, not just the destination. Every check below
	// (pool, quota, network) is about where the new VM lands and who
	// pays for it; none of them ask whether the caller may read the
	// template. Without this, any authenticated operator can clone any
	// template in the install — and with linked:true the result is a
	// qcow2 overlay that keeps reading the original template's disk for
	// as long as it exists, which is a standing read of another user's
	// data rather than a one-off copy.
	//
	// This is the same guard /api/vms/{id} gets from requireVMOwnership.
	//
	// A template an admin shared with all users skips this check (see
	// canInstantiateTemplate); nothing below is relaxed for it.
	if err := h.canInstantiateTemplate(r, id, meta); err != nil {
		h.audit.Log(auditFor(r, "vm.instantiate_denied", id, nil))
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}

	// Instantiating produces a working VM, not another golden image, so
	// its disks belong in a disk pool — not in the template shelf, the
	// ISO library or an Incus pool.
	if err := h.assertPoolPurpose(req.Pool, compute.PoolPurposeDisk); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Owner for quota + accounting. The QUOTA OWNER IS THE CALLER:
	// instantiating someone else's (typically admin-owned) template must
	// count against the user who creates the new VM, not the template's
	// owner — otherwise an admin template would be quota-exempt for
	// everyone who instantiates it.
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		o := owner
		if o == "" {
			o = meta.OwnerID
		}
		src, serr := h.compute.GetDomain(id)
		if serr != nil {
			// M-04: fail closed — previously a failing GetDomain silently
			// skipped quota/ACL enforcement and allowed the clone anyway.
			slog.Error("instantiate_template_quota_check_failed", "err", serr, "tpl", id)
			jsonErr(w, http.StatusServiceUnavailable, "cannot verify quota: "+serr.Error())
			return
		}
		diskGB := vmTotalDiskGB(src)
		u, uerr := h.userStore.Get(o)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		tplPool := req.Pool
		if tplPool == "" {
			tplPool = h.defaultPool()
		}
		if err := assertPoolAllowed(u, tplPool); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if req.Network != "" {
			if err := assertNetworkAllowed(u, req.Network); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
		if err := h.checkQuota(o, 1, int64(src.VCPUs), src.RAMMB, diskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		// A-01: charge the pool the VM will actually land on, not the
		// default pool — a user with a small cap on tplPool could
		// otherwise bypass it.
		if err := h.checkDiskQuota(o, map[string]int64{tplPool: diskGB}); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}

	// Drop snippet references the caller may not read before any
	// path (validation, seed build, Incus user-data) resolves them.
	h.scrubCloudInitSnippets(r, req.CloudInit)

	// Fail fast on invalid cloud-init payloads before cloning/creating.
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
	cloneReq := models.CloneVMRequest{Name: req.Name, Pool: req.Pool, Network: req.Network, Linked: req.Linked}
	vm, err := h.compute.CloneDomain(id, cloneReq)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The clone must NOT inherit the template flag, the shared flag or
	// the template's group or tag membership — it is a fresh, normal VM
	// (inherited tags would grant the template's tag holders access to
	// the instantiator's VM). The owner is set in the same update, before
	// cloud-init, so a provisioning failure below cannot leave the clone
	// owned by the template's owner.
	noTemplate := false
	emptyGroups := []string{}
	emptyTags := []string{}
	upd := models.VMMetaUpdate{
		Template: &noTemplate,
		Shared:   &noTemplate,
		Groups:   &emptyGroups,
		Tags:     &emptyTags,
	}
	if owner != "" {
		upd.OwnerID = &owner
	}
	if _, err := h.compute.UpdateVMMeta(vm.ID, upd); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Cloud-init seed (optional).
	var createdPassword string
	if req.CloudInit != nil {
		if req.CloudInit.User != "" {
			u := req.CloudInit.User
			_, _ = h.compute.UpdateVMMeta(vm.ID, models.VMMetaUpdate{CiUser: &u})
			createdPassword = req.CloudInit.Password
		}
		if err := h.applyCloudInit(vm.ID, vm.Name, req.CloudInit); err != nil {
			// Provisioning failed: still return the VM but warn.
			jsonResp(w, http.StatusCreated, map[string]any{
				"id":       vm.ID,
				"name":     vm.Name,
				"warning":  "cloud-init failed: " + err.Error(),
				"template": true,
			})
			h.audit.Log(auditFor(r, "vm.instantiate", vm.ID, map[string]any{"name": req.Name, "cloud_init_warning": err.Error()}))
			return
		}
	}

	h.audit.Log(auditFor(r, "vm.instantiate", id, map[string]any{"new_id": vm.ID, "name": req.Name, "linked": req.Linked}))
	resp := map[string]any{"id": vm.ID, "name": vm.Name}
	if createdPassword != "" {
		resp["password"] = createdPassword
		resp["password_warning"] = "Save this password! It won't be shown again."
	}
	jsonResp(w, http.StatusCreated, resp)
}

// canInstantiateTemplate decides whether the caller may clone the
// template {id}: a template an admin flagged Shared is instantiable by
// every user; otherwise the caller needs regular access to the source
// (owner, group or tag, or admin). Callers must already have checked
// that meta.Template is set.
func (h *Handler) canInstantiateTemplate(r *http.Request, id string, meta models.VMMeta) error {
	if meta.Template && meta.Shared {
		return nil
	}
	return h.requireVMAccess(r, id)
}
