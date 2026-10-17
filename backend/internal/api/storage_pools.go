package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

var poolPathDenyList = []string{
	"/etc",
	"/proc",
	"/sys",
	"/boot",
	"/dev",
	"/var/lib/libvirt",
	"/var/log",
	"/root",
	"/home",
}

var poolPathAllowRE = regexp.MustCompile(`^/[a-zA-Z0-9/._ -]+$`)
var poolDirNameRE = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// defaultPoolDirName turns a pool name into the folder name used when
// the pool is created on the system disk without an explicit path.
func defaultPoolDirName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." ||
		name != filepath.Base(name) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("pool name %q cannot be used as a folder name; give an explicit path", name)
	}
	if !poolDirNameRE.MatchString(name) {
		return "", fmt.Errorf("pool name %q has characters that cannot be used as a folder name (allowed: letters, digits, dot, dash, underscore); give an explicit path", name)
	}
	return name, nil
}

func validatePoolPath(p string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("must be an absolute path")
	}
	if !poolPathAllowRE.MatchString(p) {
		return fmt.Errorf("path contains invalid characters; allowed: [a-zA-Z0-9/._ -]")
	}
	for _, deny := range poolPathDenyList {
		if p == deny || strings.HasPrefix(p, deny+"/") {
			return fmt.Errorf("path %q is reserved (denied: %s)", p, deny)
		}
	}
	return nil
}

func (h *Handler) ListPools(w http.ResponseWriter, r *http.Request) {
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// V13-DATA-02: scope the list to the caller's AllowedPools so the
	// deploy modal (and every other consumer) only ever sees eligible
	// pools. Admins and unrestricted users see everything.
	if user, role, _ := audit.FromRequest(r); role != models.RoleAdmin && user != "" {
		if u, uerr := h.userStore.Get(user); uerr == nil {
			if set, all := poolAllowSet(u); !all {
				filtered := pools[:0]
				for _, p := range pools {
					if set[p.Name] {
						filtered = append(filtered, p)
					}
				}
				pools = filtered
			}
		}
	}
	jsonResp(w, http.StatusOK, pools)
}

// PoolBreakdown is the per-pool storage split shown as a segmented bar
// in the UI.
type PoolBreakdown struct {
	Pool      string `json:"pool"`
	Path      string `json:"path"`
	Purpose   string `json:"purpose"`
	Capacity  int64  `json:"capacity"`
	Available int64  `json:"available"`
	// DeviceID lets the frontend dedupe pools that share one physical
	// filesystem: libvirt reports the WHOLE filesystem's capacity for
	// every dir pool on it, so summing them double-counts the disk.
	DeviceID uint64 `json:"device_id,omitempty"`
	// Bytes by category. Volumes are split by extension because that's
	// what actually distinguishes them on a dir pool: .iso is optical
	// media, everything else is a VM/container disk image.
	DiskBytes   int64 `json:"disk_bytes"`
	ISOBytes    int64 `json:"iso_bytes"`
	BackupBytes int64 `json:"backup_bytes"`
	OtherBytes  int64 `json:"other_bytes"`
	// FreeBytes is Available, surfaced here so the bar can render the
	// unused remainder without a second lookup.
	FreeBytes int64 `json:"free_bytes"`
}

// GetStorageBreakdown reports what is actually consuming each pool,
// so the Storage page can show a segmented bar (VM disks / ISOs /
// backups / free) instead of a single opaque "62% used".
func (h *Handler) GetStorageBreakdown(w http.ResponseWriter, r *http.Request) {
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Same AllowedPools scoping as ListPools — a restricted user must
	// not learn about pools they can't use.
	if user, role, _ := audit.FromRequest(r); role != models.RoleAdmin && user != "" {
		if u, uerr := h.userStore.Get(user); uerr == nil {
			if set, all := poolAllowSet(u); !all {
				filtered := pools[:0]
				for _, p := range pools {
					if set[p.Name] {
						filtered = append(filtered, p)
					}
				}
				pools = filtered
			}
		}
	}

	out := make([]PoolBreakdown, 0, len(pools))
	for _, p := range pools {
		b := PoolBreakdown{
			Pool: p.Name, Path: p.Path, Purpose: p.Purpose,
			Capacity: p.Capacity, Available: p.Available,
			DeviceID: p.DeviceID, FreeBytes: p.Available,
		}
		vols, verr := h.compute.ListStorageVolumes(p.Name)
		if verr != nil {
			out = append(out, b)
			continue
		}
		for _, v := range vols {
			if v.IsSnapshot {
				continue
			}
			size := v.Allocated
			if size <= 0 {
				size = v.Capacity
			}
			switch {
			case compute.HasPurpose(p.Purpose, compute.PoolPurposeISO),
				strings.HasSuffix(strings.ToLower(v.Name), ".iso"):
				b.ISOBytes += size
			case compute.HasPurpose(p.Purpose, compute.PoolPurposeBackup),
				isBackupVolumeName(v.Name):
				b.BackupBytes += size
			case v.Format != "":
				b.DiskBytes += size
			default:
				b.OtherBytes += size
			}
		}
		out = append(out, b)
	}
	jsonResp(w, http.StatusOK, out)
}

func isBackupVolumeName(name string) bool {
	n := strings.ToLower(name)
	return strings.HasSuffix(n, ".tar") || strings.HasSuffix(n, ".tar.gz") ||
		strings.HasSuffix(n, ".tar.zst") || strings.HasSuffix(n, ".zst") ||
		strings.HasSuffix(n, ".ova")
}

func (h *Handler) CreatePool(w http.ResponseWriter, r *http.Request) {
	var req models.CreatePoolRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	isContainerPool := req.Purpose == compute.PoolPurposeContainer || req.Purpose == "lxc" || strings.HasPrefix(req.Type, "incus-") || req.Type == "container"
	if req.Type == "iscsi" {
		if req.Path == "" {
			req.Path = "/dev/disk/by-path"
		}
		if req.SourceHost == "" {
			jsonErr(w, http.StatusBadRequest, "source_host is required for iscsi pool")
			return
		}
		targetIQN := req.SourceDevice
		if targetIQN == "" {
			targetIQN = req.SourceIQN
		}
		if targetIQN == "" {
			jsonErr(w, http.StatusBadRequest, "source_device (target IQN) is required for iscsi pool")
			return
		}
		req.SourceDevice = targetIQN
		if (req.SourceUsername != "") != (req.SourcePassword != "") {
			jsonErr(w, http.StatusBadRequest,
				"iscsi chap auth requires both source_username and source_password")
			return
		}
	}

	if req.Path == "" && !isContainerPool && (req.Type == "dir" || req.Type == "") {
		safe, err := defaultPoolDirName(req.Name)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		req.Path = filepath.Join(h.cfg.PoolsDir(), safe)
	}
	if req.Path == "" && !isContainerPool {
		jsonErr(w, http.StatusBadRequest, "path is required")
		return
	}
	if strings.EqualFold(req.SourceFormat, "cifs") {
		if (req.SourceUsername != "") != (req.SourcePassword != "") {
			jsonErr(w, http.StatusBadRequest,
				"cifs auth requires both source_username and source_password")
			return
		}
	}
	if req.Purpose != "" {
		one, ok := parsePoolPurpose(req.Purpose)
		if !ok {
			jsonErr(w, http.StatusBadRequest,
				"purpose must be a single value: 'disk', 'iso', 'container', 'backup' or 'template'")
			return
		}
		req.Purpose = one
	}
	if req.Path != "" && req.Type != "iscsi" {
		if err := validatePoolPath(req.Path); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid path: "+err.Error())
			return
		}
	} else if req.Path != "" && req.Type == "iscsi" {
		if !filepath.IsAbs(req.Path) || !poolPathAllowRE.MatchString(req.Path) {
			jsonErr(w, http.StatusBadRequest, "invalid iscsi path")
			return
		}
	}
	pool, err := h.compute.CreateStoragePool(r.Context(), req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "storage.pool.create", pool.Name, map[string]any{
		"type":    req.Type,
		"purpose": req.Purpose,
		"format":  req.SourceFormat,
		"auth":    req.SourceUsername != "",
	}))
	jsonResp(w, http.StatusCreated, pool)
}

func poolRetagBlockers(vols []models.StorageVolume, newPurpose string) []string {
	var bad []string
	for _, v := range vols {
		if v.IsSnapshot {
			continue
		}
		isISO := strings.EqualFold(filepath.Ext(v.Name), ".iso")
		switch newPurpose {
		case compute.PoolPurposeISO:
			if !isISO {
				bad = append(bad, v.Name)
			}
		case compute.PoolPurposeDisk, compute.PoolPurposeTemplate:
			if isISO {
				bad = append(bad, v.Name)
			}
		}
	}
	return bad
}

func (h *Handler) UpdatePool(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		jsonErr(w, http.StatusBadRequest, "pool name required")
		return
	}
	var req models.UpdatePoolRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	hasUser := req.SourceUsername != nil
	hasPass := req.SourcePassword != nil
	if hasUser != hasPass {
		jsonErr(w, http.StatusBadRequest,
			"auth requires both source_username and source_password")
		return
	}
	if req.Purpose != nil {
		one, ok := parsePoolPurpose(*req.Purpose)
		if !ok {
			jsonErr(w, http.StatusBadRequest,
				"purpose must be a single value: 'disk', 'iso', 'container', 'backup' or 'template'")
			return
		}
		current, exists := h.lookupPoolPurpose(name)
		if !exists {
			jsonErr(w, http.StatusNotFound, fmt.Sprintf("storage pool %q does not exist", name))
			return
		}
		if (current == compute.PoolPurposeContainer) != (one == compute.PoolPurposeContainer) {
			jsonErr(w, http.StatusBadRequest,
				"a container pool lives in Incus and the others in libvirt; retagging across the two is not possible — create the pool you need instead")
			return
		}
		if one == current {
			req.Purpose = nil
		} else {
			if vols, verr := h.compute.ListStorageVolumes(name); verr == nil {
				if bad := poolRetagBlockers(vols, one); len(bad) > 0 {
					shown := bad
					if len(shown) > 5 {
						shown = shown[:5]
					}
					jsonErr(w, http.StatusConflict, fmt.Sprintf(
						"pool %q holds %d file(s) that do not belong in a %q pool (%s) — move them first",
						name, len(bad), one, strings.Join(shown, ", ")))
					return
				}
			}
			req.Purpose = &one
		}
		if req.Purpose == nil && !req.CifsNeedsReauth && !req.ChapNeedsReauth && !hasUser {
			pools, _ := h.compute.ListStoragePools()
			for _, p := range pools {
				if p.Name == name {
					jsonResp(w, http.StatusOK, p)
					return
				}
			}
			jsonErr(w, http.StatusNotFound, fmt.Sprintf("storage pool %q does not exist", name))
			return
		}
	}
	pool, err := h.compute.UpdateStoragePool(r.Context(), name, req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	details := map[string]any{"reauth": req.CifsNeedsReauth || req.ChapNeedsReauth}
	if req.Purpose != nil {
		details["purpose"] = *req.Purpose
	}
	h.audit.Log(auditFor(r, "storage.pool.update", name, details))
	jsonResp(w, http.StatusOK, pool)
}

func volumeInUse(w http.ResponseWriter, what string, atts []models.VolumeAttachment) bool {
	if len(atts) == 0 {
		return false
	}
	names := make([]string, 0, len(atts))
	for _, a := range atts {
		names = append(names, fmt.Sprintf("%s (%s)", a.VMName, a.State))
	}
	jsonResp(w, http.StatusConflict, map[string]any{
		"error":       fmt.Sprintf("%s is in use by: %s — detach it first", what, strings.Join(names, ", ")),
		"attachments": atts,
	})
	return true
}

func isBuiltinPool(name string) bool {
	switch name {
	case config.DiskPoolName, config.ISOPoolName, config.IncusPoolName, "ISOS":
		return true
	}
	return false
}

func (h *Handler) DeletePool(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		jsonErr(w, http.StatusBadRequest, "pool name required")
		return
	}
	if isBuiltinPool(name) {
		jsonErr(w, http.StatusConflict, fmt.Sprintf(
			"pool %q is a built-in WebKVM pool and cannot be deleted", name))
		return
	}
	vols, err := h.compute.ListStorageVolumes(name)
	if err != nil && !strings.Contains(err.Error(), "not found") && !strings.Contains(err.Error(), "not active") {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var allAtts []models.VolumeAttachment
	for _, v := range vols {
		atts, aerr := h.compute.FindVolumeAttachments(name, v.Name)
		if aerr != nil {
			continue
		}
		allAtts = append(allAtts, atts...)
	}
	if len(allAtts) > 0 {
		names := make([]string, 0, len(allAtts))
		for _, a := range allAtts {
			names = append(names, fmt.Sprintf("%s (%s)", a.VMName, a.State))
		}
		jsonErr(w, http.StatusConflict, fmt.Sprintf(
			"cannot delete pool %q: %d disk(s) still attached to VMs (%s) — detach them first",
			name, len(allAtts), strings.Join(names, ", ")))
		return
	}
	if err := h.compute.DeletePool(name); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "pool deleted"})
}

func (h *Handler) poolPurposeFor(name string) string {
	purpose, _ := h.lookupPoolPurpose(name)
	return purpose
}

func (h *Handler) lookupPoolPurpose(name string) (string, bool) {
	if h.compute == nil {
		return "", false
	}
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		return "", false
	}
	for _, p := range pools {
		if p.Name == name {
			return p.Purpose, true
		}
	}
	return "", false
}

func (h *Handler) requireISOPoolForCaller(w http.ResponseWriter, r *http.Request, name string) bool {
	if !h.requireISOPool(w, name) {
		return false
	}
	if !h.poolVisibleTo(r, name) {
		jsonErr(w, http.StatusForbidden, fmt.Sprintf("pool %q is not available to this user", name))
		return false
	}
	return true
}

func (h *Handler) requireISOPool(w http.ResponseWriter, name string) bool {
	purpose, ok := h.lookupPoolPurpose(name)
	if !ok || !compute.HasPurpose(purpose, compute.PoolPurposeISO) {
		jsonErr(w, http.StatusBadRequest,
			fmt.Sprintf("pool %q is not designated for ISO images", name))
		return false
	}
	return true
}

func (h *Handler) requireDiskPool(w http.ResponseWriter, name string) bool {
	purpose, ok := h.lookupPoolPurpose(name)
	if !ok || !compute.HasPurpose(purpose, compute.PoolPurposeDisk) {
		jsonErr(w, http.StatusBadRequest,
			fmt.Sprintf("pool %q is not designated for disk images", name))
		return false
	}
	return true
}
