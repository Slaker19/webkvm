package api

import (
	"encoding/json"
	"net/http"

	"webkvm/internal/audit"
	"webkvm/internal/cloudinit"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// MakeVMTemplate (operator/admin) marks a VM as a template. The VM
// must be shut off (a running VM cannot be safely used as a template).
func (h *Handler) MakeVMTemplate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.lv.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if vm.State != models.VMStateShutoff {
		jsonErr(w, http.StatusConflict, "a VM must be shut off before turning it into a template")
		return
	}
	t := true
	if _, err := h.lv.UpdateVMMeta(id, models.VMMetaUpdate{Template: &t}); err != nil {
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
	if _, err := h.lv.UpdateVMMeta(id, models.VMMetaUpdate{Template: &f}); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.unset_template", id, nil))
	jsonResp(w, http.StatusOK, map[string]any{"status": "vm"})
}

// ListTemplates returns the VMs flagged as templates.
func (h *Handler) ListTemplates(w http.ResponseWriter, r *http.Request) {
	vms, err := h.lv.ListDomains()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []models.VM{}
	for _, vm := range vms {
		meta, err := h.lv.GetVMMeta(vm.ID)
		if err != nil {
			continue
		}
		if meta.Template {
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
		Name      string                   `json:"name"`
		Pool      string                   `json:"pool,omitempty"`
		Network   string                   `json:"network,omitempty"`
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
	meta, err := h.lv.GetVMMeta(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !meta.Template {
		jsonErr(w, http.StatusConflict, "source VM is not a template")
		return
	}

	// Owner for quota + accounting.
	owner, role, _ := audit.FromRequest(r)
	if role != models.RoleAdmin {
		o := meta.OwnerID
		if o == "" {
			o = owner
		}
		src, serr := h.lv.GetDomain(id)
		if serr == nil {
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
			if err := h.checkQuota(o, 1, int64(src.VCPUs), src.RAMMB, diskGB); err != nil {
				jsonErr(w, http.StatusConflict, err.Error())
				return
			}
			if err := h.checkDiskQuota(o, map[string]int64{h.defaultPool(): diskGB}); err != nil {
				jsonErr(w, http.StatusConflict, err.Error())
				return
			}
		}
	}

	// Fail fast on invalid cloud-init payloads before cloning/creating.
	if req.CloudInit != nil {
		if err := (cloudinit.Config{User: req.CloudInit.User, Password: req.CloudInit.Password,
			SSHKey: req.CloudInit.SSHKey, Hostname: req.CloudInit.Hostname}).Validate(); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	cloneReq := models.CloneVMRequest{Name: req.Name, Pool: req.Pool, Network: req.Network}
	vm, err := h.lv.CloneDomain(id, cloneReq)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The clone must NOT inherit the template flag or the template's
	// group membership — it is a fresh, normal VM.
	noTemplate := false
	emptyGroups := []string{}
	if _, err := h.lv.UpdateVMMeta(vm.ID, models.VMMetaUpdate{
		Template: &noTemplate,
		Groups:   &emptyGroups,
	}); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Cloud-init seed (optional).
	var createdPassword string
	if req.CloudInit != nil {
		if req.CloudInit.User != "" {
			u := req.CloudInit.User
			_, _ = h.lv.UpdateVMMeta(vm.ID, models.VMMetaUpdate{CiUser: &u})
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

	// Owner bookkeeping.
	if owner != "" {
		_, _ = h.lv.UpdateVMMeta(vm.ID, models.VMMetaUpdate{OwnerID: &owner})
	}
	h.audit.Log(auditFor(r, "vm.instantiate", id, map[string]any{"new_id": vm.ID, "name": req.Name}))
	resp := map[string]any{"id": vm.ID, "name": vm.Name}
	if createdPassword != "" {
		resp["password"] = createdPassword
		resp["password_warning"] = "Save this password! It won't be shown again."
	}
	jsonResp(w, http.StatusCreated, resp)
}
