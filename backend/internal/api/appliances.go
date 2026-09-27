package api

import (
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"webkvm/internal/appliances"
	"webkvm/internal/audit"
	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// builtinAppMeta describes a well-known appliance app so WebKVM can show
// connection details (URL path and database credentials) in the UI after
// deploy. DBPass is filled at deploy time for apps whose scripts carry the
// {{WEBKVM_DB_PASS}} placeholder; static-credential apps (odoo) ship it.
type builtinAppMeta struct {
	App    string `json:"app"`
	Path   string `json:"path"`
	Engine string `json:"engine,omitempty"`
	DBName string `json:"db_name,omitempty"`
	DBUser string `json:"db_user,omitempty"`
	DBPass string `json:"db_pass,omitempty"`
}

func appMetaFor(id string) builtinAppMeta {
	switch id {
	case "wordpress":
		return builtinAppMeta{App: "WordPress", Path: "/", Engine: "mysql", DBName: "wordpress", DBUser: "wpuser"}
	case "nextcloud":
		return builtinAppMeta{App: "Nextcloud", Path: "/nextcloud", Engine: "mariadb", DBName: "nextcloud", DBUser: "ncuser"}
	case "moodle":
		return builtinAppMeta{App: "Moodle", Path: "/moodle", Engine: "mariadb", DBName: "moodle", DBUser: "moodle"}
	case "odoo":
		return builtinAppMeta{App: "Odoo", Path: ":8069", Engine: "postgresql", DBUser: "odoo", DBPass: "odoo"}
	case "pihole":
		return builtinAppMeta{App: "Pi-hole", Path: ":80/admin"}
	case "adguard-home":
		return builtinAppMeta{App: "AdGuard Home", Path: ":3000"}
	case "uptime-kuma":
		return builtinAppMeta{App: "Uptime Kuma", Path: ":3001"}
	case "beszel":
		return builtinAppMeta{App: "Beszel", Path: ":8090"}
	case "vaultwarden":
		return builtinAppMeta{App: "Vaultwarden", Path: ":8080"}
	case "n8n":
		return builtinAppMeta{App: "n8n Workflow Automation", Path: ":5678"}
	case "stirling-pdf":
		return builtinAppMeta{App: "Stirling-PDF", Path: ":8080"}
	case "nginx-proxy-manager":
		return builtinAppMeta{App: "Nginx Proxy Manager", Path: ":81"}
	case "home-assistant":
		return builtinAppMeta{App: "Home Assistant", Path: ":8123"}
	case "jellyfin":
		return builtinAppMeta{App: "Jellyfin Media Server", Path: ":8096"}
	case "grafana":
		return builtinAppMeta{App: "Grafana OSS", Path: ":3000"}
	case "portainer-ce":
		return builtinAppMeta{App: "Portainer CE", Path: ":9443"}
	case "wireguard-easy":
		return builtinAppMeta{App: "WireGuard Easy", Path: ":51821"}
	case "gitea":
		return builtinAppMeta{App: "Gitea", Path: ":3001"}
	default:
		return builtinAppMeta{App: id, Path: "/"}
	}
}

// ApplianceRequest is the JSON body for creating/updating an appliance
// from the admin UI. The provision script is never accepted here.
type ApplianceRequest struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Description        string `json:"description,omitempty"`
	Category           string `json:"category"`
	URL                string `json:"url"`
	Format             string `json:"format"`
	Compression        string `json:"compression"`
	SizeBytes          int64  `json:"size_bytes,omitempty"`
	VCPUs              int    `json:"vcpus"`
	RAMMB              int64  `json:"ram_mb"`
	DiskGB             int64  `json:"disk_gb"`
	CloudInitSupported bool   `json:"cloud_init_supported"`
	Notes              string `json:"notes,omitempty"`
	BaseImageID        string `json:"base_image_id,omitempty"`
	DefaultType        string `json:"default_type,omitempty"`
	BaseImage          string `json:"base_image,omitempty"`
	Port               int    `json:"port,omitempty"`
	WebPath            string `json:"web_path,omitempty"`
	Logo               string `json:"logo,omitempty"`
	DocumentationURL   string `json:"documentation_url,omitempty"`
	IsHelperScript     bool   `json:"is_helper_script,omitempty"`
	// ProvisionScript is optional and admin-only. A pointer is used so an
	// omitted field means "keep current" while "" means "clear/revert to
	// the embedded default for builtins".
	ProvisionScript *string `json:"provision_script,omitempty"`
}

// ToAppliance converts the request into an appliances.Appliance.
func (r *ApplianceRequest) ToAppliance() appliances.Appliance {
	a := appliances.Appliance{
		ID:                 strings.TrimSpace(r.ID),
		Name:               strings.TrimSpace(r.Name),
		Description:        strings.TrimSpace(r.Description),
		Category:           strings.TrimSpace(r.Category),
		URL:                strings.TrimSpace(r.URL),
		Format:             strings.TrimSpace(r.Format),
		Compression:        strings.TrimSpace(r.Compression),
		SizeBytes:          r.SizeBytes,
		VCPUs:              r.VCPUs,
		RAMMB:              r.RAMMB,
		DiskGB:             r.DiskGB,
		CloudInitSupported: r.CloudInitSupported,
		Notes:              strings.TrimSpace(r.Notes),
		BaseImageID:        strings.TrimSpace(r.BaseImageID),
		DefaultType:        strings.TrimSpace(r.DefaultType),
		BaseImage:          strings.TrimSpace(r.BaseImage),
		Port:               r.Port,
		WebPath:            strings.TrimSpace(r.WebPath),
		Logo:               strings.TrimSpace(r.Logo),
		DocumentationURL:   strings.TrimSpace(r.DocumentationURL),
		IsHelperScript:     r.IsHelperScript,
	}
	if r.ProvisionScript != nil {
		a.ProvisionScript = *r.ProvisionScript
	}
	if a.Compression == "" {
		a.Compression = "none"
	}
	if a.Format == "" {
		a.Format = "qcow2"
	}
	return a
}

// ListAppliances returns the appliance catalog (from the persistent,
// operator-editable store).
func (h *Handler) ListAppliances(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, map[string]any{"appliances": h.appStore.List()})
}

// CreateAppliance (admin) adds a new appliance to the catalog. The URL
// is validated against the official-source whitelist.
func (h *Handler) CreateAppliance(w http.ResponseWriter, r *http.Request) {
	var req ApplianceRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	a := req.ToAppliance()
	if err := h.appStore.Create(a); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "appliance.create", a.ID, map[string]interface{}{"name": a.Name, "has_script": a.ProvisionScript != ""}))
	jsonResp(w, http.StatusCreated, a)
}

// GetApplianceProvision (operator+) returns the effective provisioning
// script for an appliance: the stored script (admin override or custom)
// with builtins falling back to the copy embedded in the binary.
func (h *Handler) GetApplianceProvision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	script, isBuiltin, overridden, ok := h.appStore.ProvisionInfo(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "appliance not found")
		return
	}
	h.audit.Log(auditFor(r, "appliance.provision_view", id, nil))
	jsonResp(w, http.StatusOK, map[string]any{
		"script":     script,
		"is_builtin": isBuiltin,
		"overridden": overridden,
	})
}

// UpdateAppliance (admin) updates an editable appliance (URL, name,
// description, resources, etc.). The URL is validated against the
// official-source whitelist.
func (h *Handler) UpdateAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req ApplianceRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	a := req.ToAppliance()
	if err := h.appStore.Update(id, a); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ProvisionScript != nil {
		// Explicit script change (or "" to restore the embedded default
		// for builtins). NormalizeScript runs inside SetProvision.
		if err := h.appStore.SetProvision(id, *req.ProvisionScript); err != nil {
			jsonErr(w, http.StatusBadRequest, "provision script: "+err.Error())
			return
		}
		h.audit.Log(auditFor(r, "appliance.provision_update", id, map[string]interface{}{"len": len(*req.ProvisionScript)}))
	}
	h.audit.Log(auditFor(r, "appliance.update", id, map[string]interface{}{"name": a.Name}))
	jsonResp(w, http.StatusOK, a)
}

// DeleteAppliance (admin) removes an appliance from the catalog.
func (h *Handler) DeleteAppliance(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.appStore.Delete(id); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "appliance.delete", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// DeployAppliance downloads a catalog image and creates a ready VM.
// The request body is optional: {name, network, cloud_init}.
func (h *Handler) DeployAppliance(w http.ResponseWriter, r *http.Request) {
	if h.lv == nil {
		jsonErr(w, http.StatusServiceUnavailable, "libvirt not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	app, ok := h.appStore.Get(id)
	if !ok {
		// A "cs:<slug>" ID refers to an imported community script rather
		// than a stored appliance. Resolving it into the same Appliance
		// shape here — instead of adding a second deploy endpoint — means
		// third-party installs go through exactly the RBAC, quota, pool,
		// network and name-collision checks that everything else does.
		// A parallel path would have been a standing invitation to drift.
		var csErr error
		app, csErr = h.communityAppliance(id)
		if csErr != nil {
			jsonErr(w, http.StatusNotFound, csErr.Error())
			return
		}
	}
	var req struct {
		Name      string                   `json:"name"`
		Type      string                   `json:"type"` // "container" (LXC helper app) or "vm" (KVM)
		Network   string                   `json:"network"`
		Pool      string                   `json:"pool"`
		VCPUs     int                      `json:"vcpus,omitempty"`
		RAMMB     int64                    `json:"ram_mb,omitempty"`
		DiskGB    int64                    `json:"disk_gb,omitempty"`
		CloudInit *models.CloudInitRequest `json:"cloud_init,omitempty"`

		// Container-only knobs. Every one of these is dropped for a KVM
		// target rather than silently misapplied: Incus and libvirt do
		// not share a configuration model, and a field that looks
		// accepted but does nothing is worse than one that is absent.
		Profiles   []string `json:"profiles,omitempty"`
		Nesting    *bool    `json:"nesting,omitempty"`
		Privileged *bool    `json:"privileged,omitempty"`
		// Autostart applies to both targets (Incus boot.autostart and
		// libvirt's domain autostart flag).
		Autostart *bool `json:"autostart,omitempty"`
		// GPU shares the host's render devices with the container. This
		// is NOT the exclusive PCI passthrough a VM gets: the host keeps
		// using the card, which is what makes it safe to offer here.
		GPU *bool `json:"gpu,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.VCPUs > 0 {
		app.VCPUs = req.VCPUs
	}
	if req.RAMMB > 0 {
		app.RAMMB = req.RAMMB
	}
	if req.DiskGB > 0 {
		app.DiskGB = req.DiskGB
	}
	vmName := strings.TrimSpace(req.Name)
	if vmName == "" {
		vmName = app.ID
	}
	if err := validateVMName(vmName); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// The target type decides which storage world the pool belongs to, so
	// it has to be resolved BEFORE the pool is validated. Containers live
	// on Incus pools and VMs on libvirt disk pools; validating every
	// deploy against the libvirt rules was why a container deploy
	// insisted on a libvirt pool it then never used.
	targetType := strings.TrimSpace(req.Type)
	if targetType == "" {
		targetType = app.DefaultType
		if targetType == "" {
			targetType = "container"
		}
	}
	supportsContainers := false
	if comb, ok := h.compute.(*compute.Combined); ok && comb != nil {
		supportsContainers = comb.Secondary() != nil
	}
	deployingContainer := targetType == "container" && supportsContainers

	// V13-DATA-02: the deploy may target any eligible storage pool (the
	// frontend offers a selector); see deployPoolName for the defaults.
	owner, role, _ := audit.FromRequest(r)
	poolName := h.deployPoolName(req.Pool, deployingContainer, role)

	// V13-DATA-02 strict RBAC: the requested pool must be in the caller's
	// AllowedPools. Admins are always exempt. Evaluated BEFORE any job or
	// I/O so a forbidden pool is an immediate 403.
	if role != models.RoleAdmin {
		if u, uerr := h.userStore.Get(owner); uerr == nil {
			// Fail closed: if no container pool could be resolved there
			// is nothing to authorise, and letting Incus pick one would
			// bypass the ACL and per-pool quota.
			if poolName == "" {
				jsonErr(w, http.StatusServiceUnavailable, "cannot resolve a default container storage pool; specify one explicitly")
				return
			}
			if err := assertPoolAllowed(u, poolName); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
			if req.Network != "" {
				if err := assertNetworkAllowed(u, req.Network); err != nil {
					jsonErr(w, http.StatusForbidden, err.Error())
					return
				}
			}
		}
	}

	// V13-DATA-02 pre-flight: the target pool must exist and be ACTIVE
	// before we enqueue anything — a missing/inactive pool is a clean
	// 400, never a job that dies minutes later and dirties the job store
	// with a zombie. A named pool is checked whichever backend owns it;
	// an empty one only happens for containers, where Incus resolves it.
	if poolName != "" {
		if exists, active, perr := h.poolExistsActive(poolName); perr != nil {
			jsonErr(w, http.StatusServiceUnavailable, "cannot verify storage pool: "+perr.Error())
			return
		} else if !exists {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("storage pool %q does not exist", poolName))
			return
		} else if !active {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("storage pool %q is not active", poolName))
			return
		}
	}

	// A VM deploy writes a disk image, so its target must be a libvirt
	// disk pool — checked synchronously so a wrong pool is an immediate
	// 400 instead of a job that dies later. A container writes no image:
	// its root lives in an Incus pool, whose purpose is "container", and
	// demanding a disk pool here rejected every valid Incus pool.
	if !deployingContainer && !h.requireDiskPool(w, poolName) {
		return
	}
	// The mirror image: a container's root must land in an Incus pool.
	// An empty name means "inherit from the profile" and names nothing
	// to check, but a libvirt disk pool named here is a world mix-up.
	if deployingContainer {
		if err := h.assertPoolPurpose(poolName, compute.PoolPurposeContainer); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Fail fast BEFORE any job/I/O if the target already
	// exists. The job re-checks under the name lock (TOCTOU window), but
	// this gives the operator an immediate 409 instead of a job that dies
	// after the fact. Fail-closed: if libvirt cannot answer, refuse.
	//
	// The volume half of this check only means anything for a VM, which
	// materialises a disk file inside a libvirt pool. The container path
	// checks the instance name separately, below.
	if !deployingContainer {
		exists, verr := h.verifyDeployTargetFree(poolName, vmName)
		if verr != nil {
			jsonErr(w, http.StatusServiceUnavailable, verr.Error())
			return
		}
		if exists {
			jsonErr(w, http.StatusConflict, fmt.Sprintf("deploy target taken: a VM or volume named %q already exists", vmName))
			return
		}
	}

	// Quota: the deploy creates one VM for the owner on poolName.
	if role != models.RoleAdmin {
		if err := h.checkQuota(owner, 1, int64(app.VCPUs), app.RAMMB, app.DiskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		if err := h.checkDiskQuota(owner, map[string]int64{poolName: app.DiskGB}); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
	}

	// Validate the target network BEFORE any job is created.
	// A bogus network name used to surface only after minutes of
	// download+decompress (→ orphan cleanup); now it's an immediate 400.
	// An empty name keeps the historical fallback to the default network
	// (the deploy job resolves it).
	if req.Network != "" {
		ok, nerr := h.networkExists(req.Network)
		if nerr != nil {
			jsonErr(w, http.StatusServiceUnavailable, "cannot verify network before deploy: "+nerr.Error())
			return
		}
		if !ok {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("network %q does not exist", req.Network))
			return
		}
	}

	// Drop snippet references the caller may not read before any
	// path (validation, seed build, Incus user-data) resolves them.
	h.scrubCloudInitSnippets(r, req.CloudInit)

	if deployingContainer {
		if _, err := h.compute.GetDomain(vmName); err == nil {
			jsonErr(w, http.StatusConflict, fmt.Sprintf("deploy target taken: an instance named %q already exists", vmName))
			return
		}

		provisionScript, _, _, _ := h.appStore.ProvisionInfo(app.ID)
		// An imported script is not in the appliance store, so its
		// provisioning travels on the resolved entry itself.
		if provisionScript == "" && app.IsHelperScript {
			provisionScript = app.ProvisionScript
		}
		var meta *builtinAppMeta
		if provisionScript != "" {
			m := appMetaFor(app.ID)
			// An imported script is not in appMetaFor's table, so it would
			// fall through to the default and show the operator the raw
			// "cs:<slug>" id with a bare "/" path. The parsed launcher
			// already knows the real name and listening port, so the
			// post-deploy access hint is built from that instead.
			if app.IsHelperScript {
				m = builtinAppMeta{App: app.Name, Path: helperAccessPath(app.Port, app.WebPath)}
			}
			meta = &m
			if strings.Contains(provisionScript, "{{WEBKVM_DB_PASS}}") {
				meta.DBPass = cloudinit.GeneratePassword(16)
				provisionScript = strings.ReplaceAll(provisionScript, "{{WEBKVM_DB_PASS}}", meta.DBPass)
			}
		}

		baseImg := app.BaseImage
		if baseImg == "" {
			baseImg = "images:ubuntu/26.04"
		}
		// Nesting and autostart stay on by default — several community
		// installers run containerised workloads, and an appliance the
		// operator deployed is expected back after a reboot — but an
		// explicit choice from the caller now wins over that default.
		nesting := true
		if req.Nesting != nil {
			nesting = *req.Nesting
		}
		autostart := true
		if req.Autostart != nil {
			autostart = *req.Autostart
		}
		privileged := false
		if req.Privileged != nil {
			privileged = *req.Privileged
		}
		ciReq := req.CloudInit
		if ciReq == nil {
			ciReq = &models.CloudInitRequest{
				User:     "ubuntu",
				Hostname: vmName,
			}
		}
		ciReq.ProvisionScript = provisionScript

		createReq := models.CreateVMRequest{
			Name:    vmName,
			Type:    "container",
			Image:   baseImg,
			Network: req.Network,
			// The pool the caller picked is finally honoured. It used to
			// be validated in full and then dropped, so the container
			// landed wherever the Incus profile pointed while the UI
			// claimed otherwise.
			StoragePool: poolName,
			VCPUs:       app.VCPUs,
			RAMMB:       app.RAMMB,
			DiskGB:      app.DiskGB,
			Nesting:     &nesting,
			Privileged:  &privileged,
			Autostart:   &autostart,
			Profiles:    req.Profiles,
			GPU:         req.GPU,
			CloudInit:   ciReq,
		}

		vm, err := h.compute.CreateDomain(createReq)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "failed to deploy container: "+err.Error())
			return
		}

		metaUpdate := models.VMMetaUpdate{
			OwnerID: &owner,
		}
		if meta != nil {
			if metaJSON, err := json.Marshal(meta); err == nil {
				appInfoStr := string(metaJSON)
				metaUpdate.AppInfo = &appInfoStr
			}
		}
		_, _ = h.compute.UpdateVMMeta(vm.ID, metaUpdate)

		if h.audit != nil {
			h.audit.Log(auditFor(r, "container.appliance_deploy", vm.ID, map[string]any{
				"appliance": app.ID,
				"name":      vmName,
				"type":      "container",
			}))
		}

		resp := map[string]any{
			"id":     vm.ID,
			"name":   vm.Name,
			"type":   "container",
			"status": "created",
		}
		if meta != nil {
			resp["app_info"] = meta
		}
		jsonResp(w, http.StatusCreated, resp)
		return
	}

	// Fail fast: validate the cloud-init payload BEFORE creating the job,
	// so a bad form returns 400 without leaving a queued job zombie (or an
	// audit entry for a deploy that never starts).
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

	jobID := fmt.Sprintf("app_%d", time.Now().UnixNano())
	job := &models.DownloadJob{
		ID:       jobID,
		Name:     vmName,
		URL:      app.URL,
		Owner:    jobOwner(r),
		Progress: 0,
		Status:   "queued",
		Pool:     poolName,
	}
	storeJob(job)

	if h.audit != nil {
		h.audit.Log(auditFor(r, "vm.appliance_deploy", id, map[string]any{
			"appliance": app.ID,
			"name":      vmName,
			"pool":      poolName,
			"size":      app.SizeBytes,
		}))
	}
	go h.deployApplianceJob(jobID, app, vmName, poolName, req.Network, req.CloudInit, owner, req.Autostart)

	jsonResp(w, http.StatusAccepted, map[string]string{"job_id": jobID, "status": "started"})
}

// networkExists reports whether a libvirt network with this name exists
// (active or inactive). Error is non-nil only when libvirt itself failed
// to answer.
func (h *Handler) networkExists(name string) (bool, error) {
	nets, err := h.compute.ListNetworks()
	if err != nil {
		return false, err
	}
	return networkInList(name, nets), nil
}

// poolExistsActive reports whether a libvirt storage pool with this name
// exists and is running (active). Error is non-nil only when libvirt
// itself failed to answer (V13-DATA-02 pre-flight).
func (h *Handler) poolExistsActive(name string) (exists, active bool, err error) {
	pools, lerr := h.compute.ListStoragePools()
	if lerr != nil {
		return false, false, lerr
	}
	for _, p := range pools {
		if p.Name == name {
			return true, poolStateUsable(p.State), nil
		}
	}
	return false, false, nil
}

// poolStateUsable reports whether a pool is in a state that can back a
// new instance.
//
// The two backends spell "healthy" differently: libvirt pools are
// "active", Incus pools are "created" (its other states are "pending",
// "errored" and "unknown"). Comparing against "active" alone therefore
// rejected every Incus pool that existed and worked perfectly — a
// container deploy naming its own pool would have been refused with
// "storage pool is not active".
func poolStateUsable(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "active", "created":
		return true
	default:
		return false
	}
}

// networkInList is the pure membership check used by networkExists,
// separated so it can be unit-tested without a live libvirt connection.
func networkInList(name string, nets []models.Network) bool {
	for _, n := range nets {
		if n.Name == name {
			return true
		}
	}
	return false
}

// deployDiskCandidates lists the pool filenames a deploy would write for
// the given VM name, covering both supported on-disk formats.
func deployDiskCandidates(vmName string) []string {
	return []string{vmName + ".qcow2", vmName + ".img"}
}

// fileInode returns the inode of path, or 0 if it cannot be stat'ed for
// any reason. Linux-only (syscall.Stat_t), matching the server target.
func fileInode(path string) uint64 {
	if strings.Contains(path, "..") {
		return 0
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return st.Ino
}

// deployPoolName resolves the storage pool an appliance deploy targets.
// An explicit pool wins. Empty keeps the historical default for VMs (the
// libvirt disk pool); for an admin's container deploy it stays empty so
// Incus inherits the pool its profile uses. A non-admin container deploy
// with no pool used to stay "" too, which skipped the pool ACL and
// charged disk quota to a phantom "" pool while Incus quietly put the
// root disk in its default pool: resolve the effective container pool up
// front (as CreateVM does for non-admins) so ACL, quota and the deploy
// all name the same pool. It may still be "" when none can be resolved;
// the caller fails closed on that.
func (h *Handler) deployPoolName(reqPool string, deployingContainer bool, role string) string {
	poolName := strings.TrimSpace(reqPool)
	if poolName != "" {
		return poolName
	}
	if !deployingContainer {
		return config.DiskPoolName
	}
	if role != models.RoleAdmin {
		return h.containerPoolName("")
	}
	return ""
}

// verifyDeployTargetFree is the authoritative pre-flight: nil when the
// domain name is free and neither candidate pool filename is taken.
// Checks BOTH libvirt's view (registered volumes) and the filesystem
// (a leftover image a failed deploy never registered). verifyErr is
// non-nil when libvirt itself failed to answer (caller must fail
// closed: a target we cannot verify is a target we must not overwrite).
func (h *Handler) verifyDeployTargetFree(poolName, vmName string) (exists bool, verifyErr error) {
	if strings.Contains(vmName, "/") || strings.Contains(vmName, "\\") || strings.Contains(vmName, "..") {
		return false, fmt.Errorf("invalid vm name %q", vmName)
	}
	if strings.Contains(poolName, "/") || strings.Contains(poolName, "\\") || strings.Contains(poolName, "..") {
		return false, fmt.Errorf("invalid pool name %q", poolName)
	}
	exists, err := h.compute.DomainExists(vmName)
	if err != nil {
		return false, fmt.Errorf("cannot verify deploy target: %w", err)
	}
	if exists {
		return true, nil
	}
	// Filesystem truth first: a dir pool only registers volumes on
	// refresh, so a raw leftover file may not appear as a volume yet.
	poolPath, ferr := h.compute.GetPoolPath(poolName)
	if ferr != nil {
		return false, fmt.Errorf("cannot resolve pool before deploy: %w", ferr)
	}
	if strings.Contains(poolPath, "..") {
		return false, fmt.Errorf("invalid pool path: traversal not allowed")
	}
	for _, cand := range deployDiskCandidates(vmName) {
		candBase := filepath.Base(cand)
		if candBase != cand || strings.Contains(cand, "..") || strings.Contains(cand, "/") || strings.Contains(cand, "\\") {
			continue
		}
		target := filepath.Join(poolPath, candBase)
		if strings.Contains(target, "..") {
			continue
		}
		if rel, err := filepath.Rel(poolPath, target); err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			continue
		}
		if _, err := os.Stat(target); err == nil {
			return true, nil
		}
		vexists, verr := h.compute.VolumeExists(poolName, cand)
		if verr != nil {
			return false, fmt.Errorf("cannot verify deploy target: %w", verr)
		}
		if vexists {
			return true, nil
		}
	}
	return false, nil
}

// deployApplianceJob runs the download → decompress → validate → create
// pipeline in the background, updating the shared job store so the UI
// can render a live progress bar.
func (h *Handler) deployApplianceJob(jobID string, app appliances.Appliance, vmName, poolName, network string, cloudInit *models.CloudInitRequest, owner string, autostart *bool) {
	// Serialize same-name deployments. The second job waits
	// here, then its pre-flight sees the first one's artifacts and stops
	// before touching any bytes on the pool.
	unlock := h.acquireDeployLock(vmName)
	defer unlock()

	poolPath, err := h.compute.GetPoolPath(poolName)
	if err != nil {
		updateJob(jobID, 0, "error", "resolve pool: "+err.Error())
		return
	}

	// Pre-flight re-check under the name lock (the handler check already
	// ran, but minutes of download could have elapsed since then, or the
	// request raced with another one). Fail-closed on verification errors.
	exists, verr := h.verifyDeployTargetFree(poolName, vmName)
	if verr != nil {
		updateJob(jobID, 0, "error", verr.Error())
		if h.audit != nil {
			h.audit.Log(audit.Entry{Time: time.Now().UTC().Format(time.RFC3339), User: owner,
				Action: "vm.appliance_deploy_blocked", Resource: vmName,
				Detail: map[string]any{"appliance": app.ID, "reason": "verify failed"}})
		}
		return
	}
	if exists {
		updateJob(jobID, 0, "error", fmt.Sprintf("deploy target taken: VM or volume named %q already exists", vmName))
		if h.audit != nil {
			h.audit.Log(audit.Entry{Time: time.Now().UTC().Format(time.RFC3339), User: owner,
				Action: "vm.appliance_deploy_blocked", Resource: vmName,
				Detail: map[string]any{"appliance": app.ID, "reason": "name collision"}})
		}
		return
	}

	// An "app" appliance installs software on top of a base image. Resolve
	// the effective download URL from the base image (following BaseImageID
	// chains), while keeping this appliance's own format/compression.
	effective := app
	if app.BaseImageID != "" {
		base, ok := h.appStore.Get(app.BaseImageID)
		if !ok {
			updateJob(jobID, 0, "error", "base image "+app.BaseImageID+" not found")
			return
		}
		effective.URL = base.URL
		effective.Format = base.Format
		effective.Compression = base.Compression
		effective.SizeBytes = base.SizeBytes
	}

	// Resolve the embedded provisioning script for this appliance (used by
	// app appliances to install the software on first boot).
	provisionScript, _ := h.appStore.GetProvision(app.ID)

	tmpDir := filepath.Join(h.cfg.DataDir, "appliances")
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		updateJob(jobID, 0, "error", "create temp dir: "+err.Error())
		return
	}
	rawPath := filepath.Join(tmpDir, vmName+".download")
	finalPath := rawPath
	var decompPath string
	// Clean up any files this deployment created. os.RemoveAll does not
	// expand globs, so track the exact paths we wrote and remove each.
	defer func() {
		_ = os.Remove(rawPath)
		if decompPath != "" && decompPath != rawPath {
			_ = os.Remove(decompPath)
		}
	}()

	// 1) Download with the DNS-rebind-safe transport.
	if err := downloadTo(jobID, effective.URL, rawPath); err != nil {
		updateJob(jobID, 0, "error", err.Error())
		return
	}

	// 2) Decompress. Supported: gz, xz, bz2.
	switch effective.Compression {
	case "gz", "xz", "bz2":
		updateJob(jobID, 99, "processing", "")
		decompPath = filepath.Join(tmpDir, vmName+".unpacked")
		var derr error
		switch effective.Compression {
		case "gz":
			derr = gunzipFile(rawPath, decompPath)
		case "xz":
			derr = xzFile(rawPath, decompPath)
		case "bz2":
			derr = bz2File(rawPath, decompPath)
		}
		if derr != nil {
			updateJob(jobID, 99, "error", "decompress: "+derr.Error())
			return
		}
		_ = os.Remove(rawPath)
		finalPath = decompPath
	case "none":
		// nothing to do
	default:
		updateJob(jobID, 99, "error", "unsupported compression "+effective.Compression)
		return
	}

	// 3) Validate the result before it becomes a VM disk.
	fi, err := os.Stat(finalPath)
	if err != nil {
		updateJob(jobID, 99, "error", "stat downloaded image: "+err.Error())
		return
	}
	if fi.Size() < 5<<20 { // minimum ~5 MiB
		updateJob(jobID, 99, "error", fmt.Sprintf("downloaded image too small (%d bytes)", fi.Size()))
		return
	}
	if effective.Format == "qcow2" && !isQCow2(finalPath) {
		updateJob(jobID, 99, "error", "downloaded file is not a valid qcow2 image")
		return
	}
	if effective.SizeBytes > 0 && fi.Size() > effective.SizeBytes*3 {
		updateJob(jobID, 99, "error", fmt.Sprintf("downloaded size (%d) far exceeds expected (%d)", fi.Size(), effective.SizeBytes))
		return
	}

	// 4) Place the image in the disk pool as a volume.
	ext := ".qcow2"
	if effective.Format == "raw" {
		ext = ".img"
	}
	poolFileName := vmName + ext
	poolDest := filepath.Join(poolPath, poolFileName)
	if err := moveFile(finalPath, poolDest); err != nil {
		updateJob(jobID, 99, "error", "move image to pool: "+err.Error())
		return
	}

	// Ownership guard: record the inode of the file we just
	// placed. The rollback helper below refuses to delete anything whose
	// inode no longer matches — a concurrent import/clone winning the
	// same path must never have its file destroyed by our cleanup, and
	// CreateDomain must not define a VM pointing at a foreign file.
	poolIno := fileInode(poolDest)
	assertStillOurs := func(stage string) bool {
		if poolIno == 0 {
			return true // no se pudo marcar; conservar comportamiento clásico
		}
		if cur := fileInode(poolDest); cur == poolIno {
			return true
		}
		updateJob(jobID, 99, "error", "deploy target was replaced concurrently ("+stage+"); conserving foreign file")
		return false
	}

	// From here on the image lives in the pool, so every
	// failure must roll it back — os.Rename already replaced anything on
	// poolDest, and an orphaned volume is invisible to quota accounting
	// and would block/overwrite the next deploy with the same name.
	removePoolImage := func(reason string) {
		if strings.Contains(poolDest, "..") {
			return
		}
		if _, serr := os.Stat(poolDest); serr != nil && !errors.Is(serr, os.ErrNotExist) {
			slog.Error("appliance_deploy_cleanup_stat_failed", "job", jobID, "file", poolDest, "err", serr)
		}
		if poolIno != 0 && fileInode(poolDest) != poolIno {
			// The file we placed is gone (someone replaced it); we
			// own nothing here. Do not delete what we don't own.
			slog.Warn("appliance_deploy_cleanup_skipped_foreign_file", "job", jobID, "file", poolDest)
			return
		}
		if rerr := os.Remove(poolDest); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			slog.Error("appliance_deploy_cleanup_failed", "job", jobID, "file", poolDest, "err", rerr)
		}
		if rerr := h.compute.RefreshPool(poolName); rerr != nil {
			slog.Warn("appliance_deploy_cleanup_refresh", "job", jobID, "pool", poolName, "err", rerr)
		}
		if h.audit != nil {
			h.audit.Log(audit.Entry{Time: time.Now().UTC().Format(time.RFC3339), User: owner,
				Action: "appliance.deploy_cleanup", Resource: poolFileName,
				Detail: map[string]any{"pool": poolName, "job": jobID, "reason": reason}})
		}
	}

	// Grow the image to the app's recommended disk size BEFORE the VM is
	// defined. Cloud images ship as tiny qcow2 files (a few GiB of virtual
	// disk); any provisioning that installs packages would otherwise run
	// out of space ("No space left on device") during first boot. On boot,
	// cloud-init's growpart+resizefs modules expand partition and root FS
	// into the new space automatically.
	if effective.DiskGB > 0 {
		target := effective.DiskGB * 1024 * 1024 * 1024
		if info, err := exec.Command("qemu-img", "info", "--output=json", poolDest).Output(); err == nil {
			var qi struct {
				VirtualSize int64 `json:"virtual-size"`
			}
			if json.Unmarshal(info, &qi) == nil && qi.VirtualSize > 0 && target > qi.VirtualSize {
				args := []string{"resize"}
				if effective.Format == "qcow2" || effective.Format == "" {
					args = append(args, "-f", "qcow2")
				}
				args = append(args, poolDest, fmt.Sprintf("%dG", effective.DiskGB))
				if out, err := exec.Command("qemu-img", args...).CombinedOutput(); err != nil {
					updateJob(jobID, 99, "error", fmt.Sprintf("resize disk to %dG: %v: %s", effective.DiskGB, err, strings.TrimSpace(string(out))))
					removePoolImage("qemu-img resize failed")
					return
				}
			}
		}
	}

	if err := h.compute.RefreshPool(poolName); err != nil {
		slog.Warn("appliance_deploy_refresh_failed", "job", jobID, "pool", poolName, "err", err)
	}
	if _, err := h.compute.GetStorageVolume(poolName, poolFileName); err != nil {
		removePoolImage("volume did not register after refresh")
		updateJob(jobID, 99, "error", "image did not register as volume: "+err.Error())
		return
	}
	if !assertStillOurs("after volume registration") {
		return
	}

	// V13-DATA-01 (anti-TOCTOU): the handler checked quota against the
	// appliance's *estimated* size minutes ago. Re-check against the REAL
	// on-disk size of the placed volume (after download + decompress +
	// grow) before the VM is defined. If the real footprint pushes the
	// owner over quota, abort the provisioning and destroy the volume
	// immediately — no orphan, no silent over-quota VM.
	if owner != "" {
		if rerr := h.recheckDiskQuota(owner, poolName, poolFileName); rerr != nil {
			removePoolImage("quota re-check failed after disk materialization")
			updateJob(jobID, 99, "error", "Cuota excedida tras la materialización del disco. Recursos limpiados por seguridad.")
			if h.audit != nil {
				h.audit.Log(audit.Entry{Time: time.Now().UTC().Format(time.RFC3339), User: owner,
					Action: "appliance.deploy_quota_rollback", Resource: poolFileName,
					Detail: map[string]any{"pool": poolName, "job": jobID, "error": rerr.Error()}})
			}
			return
		}
	}

	// 5) Create the VM from the existing disk with recommended resources.
	req := models.CreateVMRequest{
		Name:             vmName,
		VCPUs:            app.VCPUs,
		RAMMB:            app.RAMMB,
		Network:          network,
		ExistingDiskPool: poolName,
		ExistingDiskName: poolFileName,
		// libvirt's autostart flag maps 1:1 onto KVM, so unlike the
		// other container-only knobs it is honoured here (nil = off,
		// libvirt's default). The wizard's "Start at boot" switch.
		Autostart: autostart,
	}
	if cloudInit != nil && app.CloudInitSupported {
		req.CloudInit = cloudInit
	}
	if !assertStillOurs("before create VM") {
		return
	}
	vm, err := h.compute.CreateDomain(req)
	if err != nil {
		updateJob(jobID, 99, "error", "create VM: "+err.Error())
		removePoolImage("create VM failed")
		return
	}
	if owner != "" {
		_, _ = h.compute.UpdateVMMeta(vm.ID, models.VMMetaUpdate{OwnerID: &owner})
	}
	if req.CloudInit != nil {
		if req.CloudInit.User != "" {
			u := req.CloudInit.User
			_, _ = h.compute.UpdateVMMeta(vm.ID, models.VMMetaUpdate{CiUser: &u})
		}
		// Inject the app's provisioning script into the cloud-init seed so it
		// runs on first boot to install the software. Database credentials
		// are generated HERE (not inside the guest) so WebKVM can show them
		// in the UI pop-up while the guest uses exactly the same values.
		var meta *builtinAppMeta
		if provisionScript != "" {
			m := appMetaFor(app.ID)
			meta = &m
			if strings.Contains(provisionScript, "{{WEBKVM_DB_PASS}}") {
				meta.DBPass = cloudinit.GeneratePassword(16)
				provisionScript = strings.ReplaceAll(provisionScript, "{{WEBKVM_DB_PASS}}", meta.DBPass)
			}
			req.CloudInit.ProvisionScript = provisionScript
		}
		// If the NoCloud seed fails to build/attach, the job is
		// ERROR (not "completed with a warning") — the operator must not see
		// a green success for an app that never got its seed. AppInfo is NOT
		// persisted until the seed is attached, so the UI never shows
		// credentials for an unprovisioned app. The VM is kept (its disk is
		// valid) and the audit trail documents the partial state.
		if err := h.applyCloudInit(vm.ID, vm.Name, req.CloudInit); err != nil {
			updateJob(jobID, 99, "error", "cloud-init seed failed: "+err.Error())
			if h.audit != nil {
				h.audit.Log(audit.Entry{Time: time.Now().UTC().Format(time.RFC3339), User: owner,
					Action: "appliance.deploy_cloudinit_failed", Resource: vm.ID,
					Detail: map[string]any{"vm": vm.Name, "appliance": app.ID, "error": err.Error()}})
			}
			return
		}
		// Seed attached: NOW the credentials are real and safe to surface.
		if meta != nil {
			if b, merr := json.Marshal(meta); merr == nil {
				s := string(b)
				_, _ = h.compute.UpdateVMMeta(vm.ID, models.VMMetaUpdate{AppInfo: &s})
			}
		}
	}
	updateJob(jobID, 100, "completed", "VM "+vm.Name+" created")
}

// downloadTo downloads url to destPath with progress, delegating to the
// shared DNS-rebind-safe helper in storage.go.
func downloadTo(jobID, url, destPath string) error {
	const maxBytes int64 = 10 << 30
	_, err := secureDownloadWithProgress(jobID, url, destPath, 60*time.Minute, maxBytes)
	return err
}

func gunzipFile(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	// Some distros (e.g. OpenWrt) append trailing garbage after the
	// gzip stream. GNU gzip tolerates it; Go's reader with Multistream
	// enabled would try to parse it as another stream and fail with
	// "invalid header". Read only the first stream.
	zr.Multistream(false)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, zr); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

// xzFile decompresses an .xz archive (e.g. FreeBSD / Home Assistant
// cloud images) using the system's xz utility. We intentionally use the
// OS binary instead of a third-party Go module to avoid pulling in an
// external dependency; xz is preinstalled on Debian/Ubuntu hosts.
func xzFile(src, dst string) error {
	if _, err := exec.LookPath("xz"); err != nil {
		return fmt.Errorf("xz is required to decompress this image (install xz-utils)")
	}
	cmd := exec.Command("xz", "-dc", src)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	cmd.Stdout = out
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("xz failed: %v: %s", err, strings.TrimSpace(errBuf.String()))
	}
	return nil
}

// bz2File decompresses a .bz2 archive (e.g. OPNsense) using the standard
// library's compress/bzip2 (part of Go itself, not a third-party module).
func bz2File(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bzip2.NewReader(f)
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, br); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

// isQCow2 checks the QCOW2 magic: "QFI" 0xfb.
func isQCow2(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var b [4]byte
	if _, err := io.ReadFull(f, b[:]); err != nil {
		return false
	}
	return b[0] == 'Q' && b[1] == 'F' && b[2] == 'I' && b[3] == 0xfb
}

// moveFile moves a file from src to dst. It attempts an atomic rename first,
// falling back to a streaming copy followed by source deletion if src and dst
// reside on different filesystems or devices (e.g. EXDEV / cross-device link).
func moveFile(src, dst string) error {
	if strings.Contains(src, "..") || strings.Contains(dst, "..") {
		return fmt.Errorf("invalid path: traversal not allowed")
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	srcInfo, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, srcInfo.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	_ = in.Close()

	_ = os.Remove(src)
	return nil
}
