package api

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/cloudinit"
	"webkvm/internal/models"
)

// cloudinitDir returns the directory that hosts cloud-init seed ISOs
// ({dataDir}/cloudinit). Kept outside the ISO pool so seeds never
// show up in the ISO library or the config backup.
func (h *Handler) cloudinitDir() string {
	return filepath.Join(h.cfg.DataDir, "cloudinit")
}

// applyCloudInit generates a NoCloud seed ISO for vmName and attaches
// it to the VM as a SATA cdrom. Returns the ISO path on success.
// A nil/empty config is a no-op. Failures are returned so the caller
// can surface them (provisioning is optional; a bad seed is not fatal
// to the VM itself, but the operator should know).
func (h *Handler) applyCloudInit(vmID, vmName string, req *models.CloudInitRequest) error {
	if req == nil {
		return nil
	}

	customUD := req.CustomUserData
	if customUD == "" && h.snippets != nil {
		var parts []string
		if len(req.SnippetIDs) > 0 {
			for _, id := range req.SnippetIDs {
				if sn, ok := h.snippets.Get(id); ok && sn.Content != "" {
					parts = append(parts, sn.Content)
				}
			}
		} else if req.SnippetID != "" {
			if sn, ok := h.snippets.Get(req.SnippetID); ok && sn.Content != "" {
				parts = append(parts, sn.Content)
			}
		}
		if len(parts) > 0 {
			rawContent := strings.Join(parts, "\n")
			var vars cloudinit.TemplateVars
			vars.VM.Hostname = strings.TrimSpace(req.Hostname)
			vars.VM.User = strings.TrimSpace(req.User)
			expanded, err := cloudinit.ExpandTemplate(rawContent, vars)
			if err == nil {
				customUD = expanded
			} else {
				customUD = rawContent
			}
		}
	}

	var nets []cloudinit.NetworkConfig
	for _, n := range req.Networks {
		nets = append(nets, cloudinit.NetworkConfig{
			Interface: n.Interface,
			IPv4:      n.IPv4,
			Gateway4:  n.Gateway4,
			IPv6:      n.IPv6,
			Gateway6:  n.Gateway6,
			DNS:       n.DNS,
			Search:    n.Search,
		})
	}

	// Trim whitespace/newlines: a pasted SSH key often ends with a
	// trailing newline, which would otherwise be rejected.
	cfg := cloudinit.Config{
		User:            strings.TrimSpace(req.User),
		Password:        req.Password,
		SSHKey:          strings.TrimSpace(req.SSHKey),
		Hostname:        strings.TrimSpace(req.Hostname),
		Networks:        nets,
		ProvisionScript: req.ProvisionScript,
		CustomUserData:  customUD,
		SnippetID:       req.SnippetID,
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	dir := h.cloudinitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	isoPath := filepath.Join(dir, "seed-"+sanitizeFilename(vmName)+".iso")
	if _, err := cloudinit.BuildNoCloudISO(isoPath, cfg); err != nil {
		return err
	}
	attach := models.AttachDiskRequest{
		Device: "cdrom",
		Bus:    "sata",
		Source: isoPath,
	}
	if err := h.compute.AttachDisk(vmID, attach); err != nil {
		_ = os.Remove(isoPath)
		return err
	}
	return nil
}

// sanitizeFilename makes a name safe to use as a filename component.
func sanitizeFilename(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		out = []byte("vm")
	}
	return string(out)
}

// ListCloudInitSnippets returns all presets and custom cloud-init snippets
// visible to the caller (presets + owned snippets for non-admins; all for admin).
func (h *Handler) ListCloudInitSnippets(w http.ResponseWriter, r *http.Request) {
	if h.snippets == nil {
		jsonResp(w, http.StatusOK, []cloudinit.Snippet{})
		return
	}
	all := h.snippets.List()
	user, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin {
		jsonResp(w, http.StatusOK, all)
		return
	}
	filtered := make([]cloudinit.Snippet, 0, len(all))
	for _, sn := range all {
		if snippetReadable(sn, user, role) {
			filtered = append(filtered, sn)
		}
	}
	jsonResp(w, http.StatusOK, filtered)
}

// GetCloudInitSnippet returns a single snippet by ID. Non-admin callers
// may only read presets or snippets they own (returns 404 to avoid enumeration).
func (h *Handler) GetCloudInitSnippet(w http.ResponseWriter, r *http.Request) {
	if h.snippets == nil {
		jsonErr(w, http.StatusNotFound, "snippet store not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	sn, ok := h.snippets.Get(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "snippet not found")
		return
	}
	user, role, _ := audit.FromRequest(r)
	if !snippetReadable(sn, user, role) {
		jsonErr(w, http.StatusNotFound, "snippet not found")
		return
	}
	jsonResp(w, http.StatusOK, sn)
}

// CreateCloudInitSnippet adds a new custom snippet.
func (h *Handler) CreateCloudInitSnippet(w http.ResponseWriter, r *http.Request) {
	if h.snippets == nil {
		jsonErr(w, http.StatusInternalServerError, "snippet store not initialized")
		return
	}
	var sn cloudinit.Snippet
	if err := decodeBody(r, &sn); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	user, _, _ := audit.FromRequest(r)
	sn.Owner = user
	created, err := h.snippets.Create(sn)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "cloudinit.snippet_create", created.ID, map[string]any{"name": created.Name}))
	}
	jsonResp(w, http.StatusCreated, created)
}

// UpdateCloudInitSnippet updates a custom snippet.
func (h *Handler) UpdateCloudInitSnippet(w http.ResponseWriter, r *http.Request) {
	if h.snippets == nil {
		jsonErr(w, http.StatusInternalServerError, "snippet store not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	existing, ok := h.snippets.Get(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "snippet not found")
		return
	}
	if existing.Owner != "" && !h.isSnippetOwnerOrAdmin(r, existing.Owner) {
		jsonErr(w, http.StatusForbidden, "forbidden: you do not own this snippet")
		return
	}

	var sn cloudinit.Snippet
	if err := decodeBody(r, &sn); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	sn.ID = id
	user, _, _ := audit.FromRequest(r)
	sn.Owner = user
	updated, err := h.snippets.Update(sn)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "cloudinit.snippet_update", updated.ID, map[string]any{"name": updated.Name}))
	}
	jsonResp(w, http.StatusOK, updated)
}

// DeleteCloudInitSnippet removes a custom snippet.
func (h *Handler) DeleteCloudInitSnippet(w http.ResponseWriter, r *http.Request) {
	if h.snippets == nil {
		jsonErr(w, http.StatusInternalServerError, "snippet store not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	existing, ok := h.snippets.Get(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "snippet not found")
		return
	}
	if existing.Owner != "" && !h.isSnippetOwnerOrAdmin(r, existing.Owner) {
		jsonErr(w, http.StatusForbidden, "forbidden: you do not own this snippet")
		return
	}

	if err := h.snippets.Delete(id); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "cloudinit.snippet_delete", id, nil))
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// snippetReadable is the single read rule for cloud-init snippets:
// presets and legacy unowned snippets are public, everything else is
// visible to its owner and to admins only.
func snippetReadable(sn cloudinit.Snippet, user, role string) bool {
	return sn.IsPreset || sn.Owner == "" || role == models.RoleAdmin || (user != "" && sn.Owner == user)
}

// scrubSnippetRefs drops every snippet reference the caller may not
// read, so no resolution path (preview, VM/template/appliance create,
// reapply, the Incus backend) can expand another user's snippet.
// Unreadable ids are treated exactly like unknown ids — silently
// ignored — so the result is not an existence oracle.
func (h *Handler) scrubSnippetRefs(r *http.Request, id *string, ids *[]string) {
	if h.snippets == nil {
		return
	}
	user, role, _ := audit.FromRequest(r)
	readable := func(sid string) bool {
		sn, ok := h.snippets.Get(sid)
		return ok && snippetReadable(sn, user, role)
	}
	if id != nil && *id != "" && !readable(*id) {
		*id = ""
	}
	if ids != nil && len(*ids) > 0 {
		kept := (*ids)[:0:0]
		for _, sid := range *ids {
			if readable(sid) {
				kept = append(kept, sid)
			}
		}
		*ids = kept
	}
}

// scrubCloudInitSnippets applies scrubSnippetRefs to a CloudInitRequest.
func (h *Handler) scrubCloudInitSnippets(r *http.Request, req *models.CloudInitRequest) {
	if req == nil {
		return
	}
	h.scrubSnippetRefs(r, &req.SnippetID, &req.SnippetIDs)
}

func (h *Handler) isSnippetOwnerOrAdmin(r *http.Request, owner string) bool {
	user, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin {
		return true
	}
	return user != "" && user == owner
}

// PreviewCloudInitRequest is the payload for POST /api/cloudinit/preview.
type PreviewCloudInitRequest struct {
	SnippetID      string                 `json:"snippet_id,omitempty"`
	SnippetIDs     []string               `json:"snippet_ids,omitempty"`
	CustomUserData string                 `json:"custom_user_data,omitempty"`
	User           string                 `json:"user,omitempty"`
	Password       string                 `json:"password,omitempty"`
	SSHKey         string                 `json:"ssh_key,omitempty"`
	Hostname       string                 `json:"hostname,omitempty"`
	IP             string                 `json:"ip,omitempty"`
	Extra          map[string]interface{} `json:"extra,omitempty"`
}

// PreviewCloudInit renders the full user-data for testing/preview before VM creation.
func (h *Handler) PreviewCloudInit(w http.ResponseWriter, r *http.Request) {
	var req PreviewCloudInitRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.scrubSnippetRefs(r, &req.SnippetID, &req.SnippetIDs)

	content := req.CustomUserData
	if content == "" && h.snippets != nil {
		if len(req.SnippetIDs) > 0 {
			var parts []string
			for _, id := range req.SnippetIDs {
				if sn, ok := h.snippets.Get(id); ok && sn.Content != "" {
					parts = append(parts, sn.Content)
				}
			}
			content = strings.Join(parts, "\n")
		} else if req.SnippetID != "" {
			if sn, ok := h.snippets.Get(req.SnippetID); ok {
				content = sn.Content
			}
		}
	}

	var vars cloudinit.TemplateVars
	vars.VM.Hostname = req.Hostname
	vars.VM.User = req.User
	vars.VM.IP = req.IP
	vars.Extra = req.Extra

	expanded, err := cloudinit.ExpandTemplate(content, vars)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "template expansion error: "+err.Error())
		return
	}

	cfg := cloudinit.Config{
		User:           req.User,
		Password:       req.Password,
		SSHKey:         req.SSHKey,
		Hostname:       req.Hostname,
		CustomUserData: expanded,
	}

	rendered := cloudinit.BuildUserData(cfg)
	jsonResp(w, http.StatusOK, map[string]string{
		"user_data": rendered,
		"expanded":  expanded,
	})
}

// cloudInitSeedName returns the deterministic seed ISO filename for a
// VM, matching applyCloudInit's naming so both can find/replace the
// same file.
func cloudInitSeedName(vmName string) string {
	return "seed-" + sanitizeFilename(vmName) + ".iso"
}

// findCloudInitCdrom returns the target device (e.g. "sdb") of the
// VM's cloud-init seed cdrom, if one is currently attached. Matched
// by filename rather than full path, since the ISO always lives in
// cloudinitDir() but the pool/data-dir prefix isn't worth hard-coding
// here.
func findCloudInitCdrom(disks []models.DiskInfo, vmName string) *models.DiskInfo {
	seedName := cloudInitSeedName(vmName)
	for i := range disks {
		d := &disks[i]
		if d.Device == "cdrom" && filepath.Base(d.Source) == seedName {
			return d
		}
	}
	return nil
}

// CloudInitStatusResponse describes the cloud-init state WebKVM knows
// about for a VM: whether a seed ISO is currently attached, and the
// last username/hostname the operator provisioned with (best-effort —
// only the username survives in VM metadata; other fields aren't
// persisted anywhere since the seed ISO itself is opaque).
type CloudInitStatusResponse struct {
	HasSeedISO bool   `json:"has_seed_iso"`
	CdromDev   string `json:"cdrom_dev,omitempty"`
	User       string `json:"user,omitempty"`
	Supported  bool   `json:"supported"`
}

// GetCloudInitStatus reports whether a VM currently has a WebKVM
// cloud-init seed ISO attached, and the last known provisioned
// username (from VM metadata, set at creation/reapply time).
func (h *Handler) GetCloudInitStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	resp := CloudInitStatusResponse{Supported: vm.Hypervisor != "incus"}
	if !resp.Supported {
		jsonResp(w, http.StatusOK, resp)
		return
	}
	if cd := findCloudInitCdrom(vm.Disks, vm.Name); cd != nil {
		resp.HasSeedISO = true
		resp.CdromDev = cd.Target
	}
	if meta, err := h.compute.GetVMMeta(id); err == nil {
		resp.User = meta.CiUser
	}
	jsonResp(w, http.StatusOK, resp)
}

// ReapplyCloudInit regenerates a VM's NoCloud seed ISO from the given
// config and (re)attaches it. Unlike creation-time provisioning, this
// works on an existing VM in any power state:
//   - If a WebKVM seed cdrom is already attached, its source is
//     swapped in place (UpdateDiskSource) — no duplicate cdrom slot.
//   - Otherwise the seed is attached fresh as a new SATA cdrom.
//
// The regenerated ISO always gets a fresh instance-id (timestamped),
// so cloud-init treats it as new configuration instead of a no-op
// ("already provisioned this instance-id, skipping"). If the VM is
// running and looks like Linux, a best-effort guest-exec re-runs
// cloud-init immediately instead of waiting for the next reboot.
func (h *Handler) ReapplyCloudInit(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req models.CloudInitRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	h.scrubCloudInitSnippets(r, &req)

	vm, err := h.compute.GetDomain(id)
	if err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	if vm.Hypervisor == "incus" {
		jsonErr(w, http.StatusNotImplemented, "cloud-init reprovisioning applies to KVM VMs only; Incus containers use native cloud-init user-data set at creation")
		return
	}

	isoPath, err := h.buildCloudInitSeed(vm.Name, &req)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	existing := findCloudInitCdrom(vm.Disks, vm.Name)
	if existing != nil {
		if err := h.compute.UpdateDiskSource(id, existing.Target, isoPath); err != nil {
			_ = os.Remove(isoPath)
			jsonErr(w, http.StatusInternalServerError, "attach seed: "+err.Error())
			return
		}
	} else {
		attach := models.AttachDiskRequest{Device: "cdrom", Bus: "sata", Source: isoPath}
		if err := h.compute.AttachDisk(id, attach); err != nil {
			_ = os.Remove(isoPath)
			jsonErr(w, http.StatusInternalServerError, "attach seed: "+err.Error())
			return
		}
	}

	if req.User != "" {
		u := req.User
		_, _ = h.compute.UpdateVMMeta(id, models.VMMetaUpdate{CiUser: &u})
	}

	reprovisioned := false
	if vm.State == models.VMStateRunning {
		if err := h.compute.RunCloudInitReprovision(id); err == nil {
			reprovisioned = true
		} else {
			slog.Debug("cloudinit_reprovision_skip", "vm", id, "err", err)
		}
	}

	h.audit.Log(auditFor(r, "vm.cloudinit_reapply", id, map[string]interface{}{"user": req.User, "hostname": req.Hostname}))
	jsonResp(w, http.StatusOK, map[string]any{
		"attached":      true,
		"reprovisioned": reprovisioned,
	})
}

// buildCloudInitSeed renders and writes the NoCloud ISO for vmName,
// stamping a fresh timestamped instance-id so a reapply is never a
// cloud-init no-op ("already ran for this instance-id"). Shares
// validation and snippet-expansion logic with applyCloudInit, but
// always regenerates the file (applyCloudInit is create-time-only and
// assumes the ISO doesn't exist yet).
func (h *Handler) buildCloudInitSeed(vmName string, req *models.CloudInitRequest) (string, error) {
	customUD := req.CustomUserData
	if customUD == "" && h.snippets != nil {
		var parts []string
		if len(req.SnippetIDs) > 0 {
			for _, id := range req.SnippetIDs {
				if sn, ok := h.snippets.Get(id); ok && sn.Content != "" {
					parts = append(parts, sn.Content)
				}
			}
		} else if req.SnippetID != "" {
			if sn, ok := h.snippets.Get(req.SnippetID); ok && sn.Content != "" {
				parts = append(parts, sn.Content)
			}
		}
		if len(parts) > 0 {
			rawContent := strings.Join(parts, "\n")
			var vars cloudinit.TemplateVars
			vars.VM.Hostname = strings.TrimSpace(req.Hostname)
			vars.VM.User = strings.TrimSpace(req.User)
			if expanded, err := cloudinit.ExpandTemplate(rawContent, vars); err == nil {
				customUD = expanded
			} else {
				customUD = rawContent
			}
		}
	}

	var nets []cloudinit.NetworkConfig
	for _, n := range req.Networks {
		nets = append(nets, cloudinit.NetworkConfig{
			Interface: n.Interface,
			IPv4:      n.IPv4,
			Gateway4:  n.Gateway4,
			IPv6:      n.IPv6,
			Gateway6:  n.Gateway6,
			DNS:       n.DNS,
			Search:    n.Search,
		})
	}

	cfg := cloudinit.Config{
		User:             strings.TrimSpace(req.User),
		Password:         req.Password,
		SSHKey:           strings.TrimSpace(req.SSHKey),
		Hostname:         strings.TrimSpace(req.Hostname),
		Networks:         nets,
		CustomUserData:   customUD,
		SnippetID:        req.SnippetID,
		InstanceIDSuffix: strconv.FormatInt(time.Now().Unix(), 10),
	}
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	dir := h.cloudinitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	isoPath := filepath.Join(dir, cloudInitSeedName(vmName))
	return cloudinit.BuildNoCloudISO(isoPath, cfg)
}
