package api

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"webkvm/internal/audit"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// GetVMMeta returns the WebKVM metadata (alias, notes, cover, groups) for a VM.
func (h *Handler) GetVMMeta(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	meta, err := h.lv.GetVMMeta(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
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
	meta, err := h.lv.UpdateVMMeta(id, upd)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.meta_update", id, nil))
	jsonResp(w, http.StatusOK, meta)
}

// UploadCover stores an image file as the VM's cover. The path is
// recorded in <webkvm:meta><cover>...</cover> so the frontend can
// resolve the file via /api/covers/{path}.
func (h *Handler) UploadCover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := r.ParseMultipartForm(8 << 20); err != nil { // 8MB cap
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
	n, _ := io.ReadFull(file, br)
	br = br[:n]
	_, _ = file.Seek(0, io.SeekStart)

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
	if cleanID == "" || cleanID == "." || cleanID == ".." {
		jsonErr(w, http.StatusBadRequest, "invalid VM id")
		return
	}
	dst := filepath.Join(coversDir, cleanID+ext)
	if !strings.HasPrefix(filepath.Clean(dst), filepath.Clean(coversDir)+string(os.PathSeparator)) {
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

	// Update metadata.
	url := "/api/covers/" + cleanID + ext
	if _, err := h.lv.UpdateVMMeta(id, models.VMMetaUpdate{Cover: &url}); err != nil {
		os.Remove(dst) // lgtm[go/path-injection]
		jsonErr(w, http.StatusInternalServerError, "save cover meta: "+err.Error())
		return
	}

	jsonResp(w, http.StatusOK, models.CoverUploadResponse{
		URL:    url,
		Path:   dst,
		Format: format,
	})
	_ = hdr
}

// DeleteCover removes the cover image (both file and metadata).
func (h *Handler) DeleteCover(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	meta, err := h.lv.GetVMMeta(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if meta.Cover != "" {
		// cover is a URL like /api/covers/<id>.<ext>
		base := filepath.Base(meta.Cover)
		if base != "" && base != "." && base != ".." {
			fp := filepath.Join(h.cfg.CoversDir(), base)
			if !strings.HasPrefix(filepath.Clean(fp), filepath.Clean(h.cfg.CoversDir())+string(os.PathSeparator)) {
				jsonErr(w, http.StatusBadRequest, "invalid cover path")
				return
			}
			os.Remove(fp)
		}
	}
	empty := ""
	if _, err := h.lv.UpdateVMMeta(id, models.VMMetaUpdate{Cover: &empty}); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ServeCover streams a cover image from the covers directory. No auth
// (UUID-in-URL is the access control, like the VNC console pattern).
func (h *Handler) ServeCover(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "path")
	clean := filepath.Base(name)
	if clean != name || strings.ContainsAny(clean, "/\\") {
		http.NotFound(w, r)
		return
	}
	fp := filepath.Join(h.cfg.CoversDir(), clean)
	if !strings.HasPrefix(filepath.Clean(fp), filepath.Clean(h.cfg.CoversDir())+string(os.PathSeparator)) {
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
	if req.MAC != nil {
		normalized, err := normalizeMAC(*req.MAC)
		if err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		req.MAC = &normalized
	}
	if err := h.lv.UpdateNetworkIface(id, mac, req); err != nil {
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
	v, err := h.lv.CheckVLANSupport(network)
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

// GetVMMetrics returns the in-memory metric series for a VM. The series
// is empty if the collector hasn't sampled the VM yet.
func (h *Handler) GetVMMetrics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
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
