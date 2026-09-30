package api

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// GetVMMeta returns the WebKVM metadata (alias, notes, cover, groups) for a VM.
func (h *Handler) GetVMMeta(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	meta, err := h.compute.GetVMMeta(id)
	if err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	jsonResp(w, http.StatusOK, meta)
}

// UpdateVMMeta applies a partial update to the WebKVM metadata.
func (h *Handler) UpdateVMMeta(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var upd models.VMMetaUpdate
	if err := decodeBody(r, &upd); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Owner reassignment is admin-only (it drives quota accounting and
	// a user must not be able to dodge quotas by changing owners).
	if upd.OwnerID != nil {
		if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
			jsonErr(w, http.StatusForbidden, "only admins can change a VM's owner")
			return
		}
	}
	// Sharing a template with all users bypasses the owner/group/tag
	// ACLs for listing and instantiating it, so it is admin-only, and it
	// only applies to templates (unsharing is always accepted, so stale
	// flags can be cleared).
	if upd.Shared != nil {
		if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
			jsonErr(w, http.StatusForbidden, "only admins can share a template with all users")
			return
		}
		if *upd.Shared {
			cur, err := h.compute.GetVMMeta(id)
			if err != nil {
				h.vmActionErr(w, err, nil)
				return
			}
			isTemplate := cur.Template
			if upd.Template != nil {
				isTemplate = *upd.Template
			}
			if !isTemplate {
				jsonErr(w, http.StatusConflict, "only templates can be shared with all users")
				return
			}
		}
	}
	sharedReq := upd.Shared
	// Clearing the template flag through this generic path also clears
	// Shared, as UnsetVMTemplate does.
	if upd.Template != nil && !*upd.Template && upd.Shared == nil {
		f := false
		upd.Shared = &f
	}
	meta, err := h.compute.UpdateVMMeta(id, upd)
	if err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	if upd.Cover != nil {
		h.pruneVMCoverFiles(id, *upd.Cover)
	}
	h.audit.Log(auditFor(r, "vm.meta_update", id, nil))
	if sharedReq != nil {
		action := "vm.template_unshared"
		if *sharedReq {
			action = "vm.template_shared"
		}
		h.audit.Log(auditFor(r, action, id, nil))
	}
	jsonResp(w, http.StatusOK, meta)
}

// coverFilename extracts the on-disk filename from a stored cover URL
// (e.g. "/api/covers/<id>.<ext>?v=<cache-buster>" -> "<id>.<ext>").
// The query string must be stripped before calling filepath.Base — it
// isn't a path separator, so Base would otherwise return
// "<id>.<ext>?v=..." (no such file on disk).
func coverFilename(coverURL string) string {
	p, _, _ := strings.Cut(coverURL, "?")
	return filepath.Base(p)
}

// validCoverVMID reports whether id can safely name a per-VM cover file
// (<id>.<ext>) directly inside the covers dir: a single path segment,
// no separators, not "." / "..".
func validCoverVMID(id string) bool {
	return id != "" && id != "." && id != ".." &&
		filepath.Base(id) == id && !strings.ContainsAny(id, "/\\")
}

// pruneVMCoverFiles removes the per-VM uploaded cover files
// (<coversDir>/<vmID>.<ext>, any extension) except the one keepCoverURL
// points to. It is called after a VM's cover changed (upload of another
// format, a media-library cover, DELETE /cover) and after the VM itself
// is deleted (keepCoverURL ""), so replaced uploads no longer pile up in
// the covers dir forever.
//
// A clone inherits its source's <webkvm:meta>, cover URL included, so a
// file still referenced by any other VM is left alone. If the VM list
// can't be read, nothing is deleted: an orphaned file is harmless, a
// broken cover on another VM is not.
func (h *Handler) pruneVMCoverFiles(vmID, keepCoverURL string) {
	if h.cfg == nil || !validCoverVMID(vmID) {
		return
	}
	coversDir := filepath.Clean(h.cfg.CoversDir())
	entries, err := os.ReadDir(coversDir)
	if err != nil {
		return
	}
	keep := ""
	if p, _, _ := strings.Cut(keepCoverURL, "?"); strings.HasPrefix(p, "/api/covers/") {
		keep = coverFilename(p)
	}
	var victims []string
	for _, e := range entries {
		name := e.Name()
		ext := filepath.Ext(name)
		if e.IsDir() || name == keep || ext == "" || strings.TrimSuffix(name, ext) != vmID {
			continue
		}
		victims = append(victims, name)
	}
	if len(victims) == 0 {
		return
	}
	inUse := map[string]bool{}
	if h.compute != nil {
		vms, err := h.compute.ListDomains()
		if err != nil {
			h.logError("vm_cover_cleanup_skipped", err, vmID)
			return
		}
		for _, vm := range vms {
			if vm.ID == vmID {
				continue
			}
			if p, _, _ := strings.Cut(vm.Cover, "?"); strings.HasPrefix(p, "/api/covers/") {
				inUse[coverFilename(p)] = true
			}
		}
	}
	for _, name := range victims {
		if inUse[name] {
			continue
		}
		fp := filepath.Join(coversDir, name)
		if filepath.Dir(fp) != coversDir {
			continue
		}
		if err := os.Remove(fp); err != nil && !errors.Is(err, os.ErrNotExist) { // lgtm[go/path-injection] - fp is a direct child of coversDir
			h.logError("vm_cover_cleanup_failed", err, fp)
		}
	}
}

// UploadCover stores an image file as the VM's cover. The path is
// recorded in <webkvm:meta><cover>...</cover> so the frontend can
// resolve the file via /api/covers/{path}.
func (h *Handler) UploadCover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil { // 8MB cap
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			jsonErr(w, http.StatusRequestEntityTooLarge, "cover image exceeds the 8 MB limit")
			return
		}
		jsonErr(w, http.StatusBadRequest, "invalid multipart: "+err.Error())
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "file field required")
		return
	}
	defer file.Close()

	// Magic-byte sniff: PNG, JPEG, WebP. Reject anything else to keep
	// the static directory clean of arbitrary uploads.
	br := make([]byte, 16)
	n, err := io.ReadFull(file, br)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		jsonErr(w, http.StatusInternalServerError, "read file: "+err.Error())
		return
	}
	br = br[:n]
	_, err = file.Seek(0, io.SeekStart)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "seek file: "+err.Error())
		return
	}

	var format, ext string
	switch {
	case n >= 8 && string(br[:8]) == "\x89PNG\r\n\x1a\n":
		format, ext = "png", ".png"
	case n >= 3 && br[0] == 0xff && br[1] == 0xd8 && br[2] == 0xff:
		format, ext = "jpeg", ".jpg"
	case n >= 12 && string(br[:4]) == "RIFF" && string(br[8:12]) == "WEBP":
		format, ext = "webp", ".webp"
	default:
		jsonErr(w, http.StatusBadRequest, "cover must be a PNG, JPEG or WebP image")
		return
	}

	coversDir := h.cfg.CoversDir()
	if err := os.MkdirAll(coversDir, 0755); err != nil {
		jsonErr(w, http.StatusInternalServerError, "create covers dir: "+err.Error())
		return
	}

	// Filename is just the VM id + ext so it's easy to reason about.
	cleanID := filepath.Base(id)
	if cleanID == "" || cleanID == "." || cleanID == ".." || strings.Contains(cleanID, "/") || strings.Contains(cleanID, "\\") || strings.Contains(cleanID, "..") {
		jsonErr(w, http.StatusBadRequest, "invalid VM id")
		return
	}
	dst := filepath.Join(coversDir, cleanID+ext)
	if rel, rerr := filepath.Rel(coversDir, dst); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		jsonErr(w, http.StatusBadRequest, "invalid cover path")
		return
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "create cover file: "+err.Error())
		return
	}
	if _, err := io.Copy(out, file); err != nil {
		out.Close()
		os.Remove(dst) // lgtm[go/path-injection] - dst validated above
		jsonErr(w, http.StatusInternalServerError, "write cover: "+err.Error())
		return
	}
	if err := out.Close(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "close cover: "+err.Error())
		return
	}

	// Update metadata. The URL is cache-busted with the upload time:
	// the path is always /api/covers/{id}.{ext} regardless of content
	// (same VM, same format = same filename), so replacing a cover
	// with a same-format image reused the exact same URL the browser
	// had already cached — showing the deleted/old image after a
	// re-upload even though the new file was saved correctly.
	url := fmt.Sprintf("/api/covers/%s%s?v=%d", cleanID, ext, time.Now().UnixNano())
	if _, err := h.compute.UpdateVMMeta(id, models.VMMetaUpdate{Cover: &url}); err != nil {
		os.Remove(dst) // lgtm[go/path-injection]
		jsonErr(w, http.StatusInternalServerError, "save cover meta: "+err.Error())
		return
	}

	// A previous upload in another format (<id>.png -> <id>.jpg) is now
	// unreferenced.
	h.pruneVMCoverFiles(id, url)

	jsonResp(w, http.StatusOK, models.CoverUploadResponse{
		URL:    url,
		Format: format,
	})
	_ = hdr
}

// DeleteCover removes the cover image (both file and metadata).
func (h *Handler) DeleteCover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	empty := ""
	if _, err := h.compute.UpdateVMMeta(id, models.VMMetaUpdate{Cover: &empty}); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Only this VM's own uploads are removed: a clone's inherited cover
	// URL points at its source's file, which must survive.
	h.pruneVMCoverFiles(id, "")
	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ServeCover streams a cover image from the covers directory. No auth
// (UUID-in-URL is the access control, like the VNC console pattern).
func (h *Handler) ServeCover(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "path")
	clean := filepath.Base(name)
	if clean != name || strings.Contains(clean, "/") || strings.Contains(clean, "\\") || strings.Contains(clean, "..") {
		http.NotFound(w, r)
		return
	}
	fp := filepath.Join(h.cfg.CoversDir(), clean)
	if rel, rerr := filepath.Rel(h.cfg.CoversDir(), fp); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(fp); err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, fp)
}

// UpdateNetIface changes MAC / network / VLAN of an existing interface.
func (h *Handler) UpdateNetIface(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	mac := chi.URLParam(r, "mac")
	var req models.UpdateNetIfaceRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.VLANTag != nil && (*req.VLANTag < 0 || *req.VLANTag > 4094) {
		jsonErr(w, http.StatusBadRequest, "vlan_tag must be between 1 and 4094 (or 0 to remove)")
		return
	}
	if req.MAC != nil {
		normalized, err := normalizeMAC(*req.MAC)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		req.MAC = &normalized
	}
	if req.Network != nil && *req.Network != "" {
		if owner, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
			u, uerr := h.userStore.Get(owner)
			if uerr != nil {
				jsonErr(w, http.StatusUnauthorized, "user not found")
				return
			}
			if err := assertNetworkAllowed(u, *req.Network); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}
	if err := h.compute.UpdateNetworkIface(id, mac, req); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "updated"})
}

// CheckVLANSupport reports VLAN support for a network.
func (h *Handler) CheckVLANSupport(w http.ResponseWriter, r *http.Request) {
	network := r.URL.Query().Get("network")
	if network == "" {
		jsonErr(w, http.StatusBadRequest, "network query parameter required")
		return
	}
	v, err := h.compute.CheckVLANSupport(network)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, v)
}

func normalizeMAC(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", fmt.Errorf("MAC is required")
	}
	// Accept colon or dash separators, normalize to lower-case colon form.
	hexPart := strings.NewReplacer(":", "", "-", "", ".", "").Replace(s)
	b, err := hex.DecodeString(hexPart)
	if err != nil || len(b) != 6 {
		return "", fmt.Errorf("invalid MAC address %q (expect XX:XX:XX:XX:XX:XX)", s)
	}
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", b[0], b[1], b[2], b[3], b[4], b[5]), nil
}

// ListAllTags (V13-D-01) returns every distinct tag in use across all
// VMs, so the admin UI can offer them for policy assignment (AllowedTags,
// backup-by-tag). Admin/operator scoped.
func (h *Handler) ListAllTags(w http.ResponseWriter, r *http.Request) {
	if h.lv == nil {
		jsonResp(w, http.StatusOK, map[string]any{"tags": []string{}})
		return
	}
	vms, err := h.compute.ListDomains()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// The tag vocabulary is built from the VMs the caller may actually
	// see. Without this a viewer restricted to one group still got the
	// full tag list of the fleet — a map of every project name on the
	// host, and the filter chips it produced returned nothing.
	vms = h.filterVMsByACL(r, vms)
	set := map[string]bool{}
	for _, vm := range vms {
		for _, tag := range vm.Tags {
			if tag != "" {
				set[tag] = true
			}
		}
	}
	tags := make([]string, 0, len(set))
	for tag := range set {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	jsonResp(w, http.StatusOK, map[string]any{"tags": tags})
}

// GetVMMetrics returns the in-memory metric series for a VM. The series
// is empty if the collector hasn't sampled the VM yet.
func (h *Handler) GetVMMetrics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// v1.4 Fase 4.1: containers are sampled by the LXD collector; route
	// by hypervisor so LXC charts work exactly like KVM.
	if h.incusMetrics != nil {
		if vm, err := h.compute.GetDomain(id); err == nil && vm.Hypervisor == "incus" {
			m, _ := h.incusMetrics.Get(id)
			jsonResp(w, http.StatusOK, m)
			return
		}
	}
	if h.metrics == nil {
		jsonResp(w, http.StatusOK, models.VMMetrics{VMID: id})
		return
	}
	m, err := h.metrics.Get(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, m)
}

// GetGuestInfo returns QEMU guest-agent telemetry for a VM: the OS it
// runs, the filesystems the GUEST sees (with real free space, unlike
// the host-side qcow2 allocation shown elsewhere) and every address
// bound to its NICs (the host only knows about DHCP leases, so static
// guests are otherwise invisible).
//
// A missing or silent agent is a normal state for many guests, so it
// is reported as available=false with a hint rather than an HTTP
// error — the UI turns that into "install qemu-guest-agent", not a
// red failure banner.
func (h *Handler) GetGuestInfo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	// Containers have no QEMU guest agent, and the host can already
	// see their filesystems and addresses directly.
	if vm.Hypervisor == "incus" {
		jsonResp(w, http.StatusOK, compute.GuestInfo{
			Available: false,
			Error:     "guest agent telemetry is not applicable to containers",
		})
		return
	}
	if vm.State != models.VMStateRunning {
		jsonResp(w, http.StatusOK, compute.GuestInfo{
			Available: false,
			Error:     "the VM is not running",
		})
		return
	}
	info, err := h.compute.GetGuestInfo(id)
	if err != nil {
		// Reaching the domain failed (not merely "no agent").
		jsonResp(w, http.StatusOK, compute.GuestInfo{Available: false, Error: err.Error()})
		return
	}
	jsonResp(w, http.StatusOK, info)
}

// VMGuestFSTrim triggers filesystem discard/trim inside a running guest
// via the QEMU guest agent, reclaiming unused storage blocks.
func (h *Handler) VMGuestFSTrim(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	if vm.Hypervisor == "incus" {
		jsonErr(w, http.StatusBadRequest, "guest fstrim is not applicable to containers")
		return
	}
	if vm.State != models.VMStateRunning {
		jsonErr(w, http.StatusConflict, "the VM must be running to execute guest fstrim")
		return
	}
	res, err := h.compute.FSTrim(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.guest_fstrim", id, map[string]interface{}{
		"paths_count": len(res.Paths),
	}))
	jsonResp(w, http.StatusOK, res)
}

// GetVMMetricsHistory (V13-C-03) returns downsampled history for a VM.
// window is "24h", "168h" or "720h" (default 24h). <=24h uses per-minute
// resolution from the bucketed in-memory window; longer windows use the
// hourly rollup. Data lives in independent JSONL files under
// {dataDir}/metrics — never in the main store.
func (h *Handler) GetVMMetricsHistory(w http.ResponseWriter, r *http.Request) {
	if h.metricHist == nil {
		jsonErr(w, http.StatusServiceUnavailable, "metrics history not initialized")
		return
	}
	id := chi.URLParam(r, "id")
	window := time.Duration(0)
	switch r.URL.Query().Get("window") {
	case "", "24h":
		window = 24 * time.Hour
	case "168h":
		window = 7 * 24 * time.Hour
	case "720h":
		window = 30 * 24 * time.Hour
	default:
		jsonErr(w, http.StatusBadRequest, "window must be 24h, 168h or 720h")
		return
	}
	m, err := h.metricHist.History(id, window)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, m)
}
