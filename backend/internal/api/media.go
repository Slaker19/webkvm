package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"webkvm/internal/api/mediaassets"
	"webkvm/internal/audit"
	"webkvm/internal/configstore"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

var allowedMediaExts = map[string]string{
	".png":  "image/png",
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".ico":  "image/x-icon",
	".gif":  "image/gif",
}

// EnsureMediaDirs creates the media directories and seeds default system assets.
func (h *Handler) EnsureMediaDirs() {
	systemDir := h.cfg.MediaSystemDir()
	customDir := h.cfg.MediaCustomDir()

	_ = os.MkdirAll(systemDir, 0755)
	_ = os.MkdirAll(customDir, 0755)

	// Seed system assets if missing
	seedSystemAssets(systemDir)
}

func seedSystemAssets(dir string) {
	assets := map[string]string{
		"webkvm-logo.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 400 100" width="400" height="100">
  <defs>
    <linearGradient id="grad" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#6366f1" />
      <stop offset="100%" stop-color="#a855f7" />
    </linearGradient>
  </defs>
  <rect x="10" y="10" width="80" height="80" rx="18" fill="url(#grad)" />
  <path d="M30 35 L45 65 L60 45 L70 65" stroke="#ffffff" stroke-width="6" stroke-linecap="round" stroke-linejoin="round" fill="none" />
  <text x="110" y="62" font-family="system-ui, -apple-system, sans-serif" font-size="44" font-weight="800" fill="#ffffff" letter-spacing="-1">Web<tspan fill="#818cf8">KVM</tspan></text>
</svg>`,
		"webkvm-icon.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="100" height="100">
  <defs>
    <linearGradient id="icon-grad" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#6366f1" />
      <stop offset="100%" stop-color="#a855f7" />
    </linearGradient>
  </defs>
  <rect width="100" height="100" rx="22" fill="url(#icon-grad)" />
  <path d="M25 35 L42 65 L58 45 L75 65" stroke="#ffffff" stroke-width="7" stroke-linecap="round" stroke-linejoin="round" fill="none" />
</svg>`,
		"avatar-default.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100" width="100" height="100">
  <rect width="100" height="100" rx="50" fill="#3b82f6" />
  <circle cx="50" cy="38" r="18" fill="#ffffff" />
  <path d="M22 84 C22 66 35 58 50 58 C65 58 78 66 78 84 Z" fill="#ffffff" />
</svg>`,
		"cover-datacenter.svg": `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 600 340" width="600" height="340">
  <defs>
    <linearGradient id="dc-bg" x1="0%" y1="0%" x2="100%" y2="100%">
      <stop offset="0%" stop-color="#0f172a" />
      <stop offset="100%" stop-color="#1e1b4b" />
    </linearGradient>
  </defs>
  <rect width="600" height="340" fill="url(#dc-bg)" />
  <rect x="80" y="60" width="440" height="60" rx="10" fill="#1e293b" stroke="#334155" stroke-width="2" />
  <rect x="80" y="140" width="440" height="60" rx="10" fill="#1e293b" stroke="#334155" stroke-width="2" />
  <rect x="80" y="220" width="440" height="60" rx="10" fill="#1e293b" stroke="#334155" stroke-width="2" />
  <circle cx="120" cy="90" r="6" fill="#10b981" />
  <circle cx="120" cy="170" r="6" fill="#10b981" />
  <circle cx="120" cy="250" r="6" fill="#10b981" />
  <circle cx="145" cy="90" r="6" fill="#6366f1" />
  <circle cx="145" cy="170" r="6" fill="#6366f1" />
  <circle cx="145" cy="250" r="6" fill="#6366f1" />
</svg>`,
	}

	for name, content := range assets {
		target := filepath.Join(dir, name)
		if _, err := os.Stat(target); os.IsNotExist(err) {
			_ = os.WriteFile(target, []byte(content), 0644)
		}
	}

	// Bundled OS/distribution logos are embedded in the binary so the
	// installer never depends on a separate asset directory. Always keep
	// them in sync with the embedded copy (they are the canonical
	// originals of the WebKVM icon set).
	for _, name := range mediaassets.Names() {
		data, err := mediaassets.Read(name)
		if err != nil {
			continue
		}
		target := filepath.Join(dir, name)
		_ = os.WriteFile(target, data, 0644)
	}
}

// ListMedia returns all media files from both system (protected) and custom pools.
func (h *Handler) ListMedia(w http.ResponseWriter, r *http.Request) {
	h.EnsureMediaDirs()

	var items []models.MediaItem

	scanDir := func(dir string, isSystem bool, category string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			ext := strings.ToLower(filepath.Ext(name))
			mimeType, ok := allowedMediaExts[ext]
			if !ok {
				mimeType = "application/octet-stream"
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			size := info.Size()

			id := "custom:" + name
			if isSystem {
				id = "system:" + name
			}

			items = append(items, models.MediaItem{
				ID:        id,
				Name:      name,
				Category:  category,
				Path:      filepath.Join(dir, name),
				URL:       "/api/media/" + id + "/raw",
				Size:      size,
				SizeHuman: formatBytesHuman(size),
				MimeType:  mimeType,
				IsSystem:  isSystem,
				CreatedAt: info.ModTime(),
			})
		}
	}

	scanDir(h.cfg.MediaSystemDir(), true, "system")
	scanDir(h.cfg.MediaCustomDir(), false, "custom")

	// Sort newest first for custom, system at the end or alphabetical
	sort.Slice(items, func(i, j int) bool {
		if items[i].IsSystem != items[j].IsSystem {
			return !items[i].IsSystem // custom first
		}
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})

	jsonResp(w, http.StatusOK, map[string]any{
		"items": items,
		"count": len(items),
	})
}

// UploadMedia saves a new custom image to the media pool.
func (h *Handler) UploadMedia(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "media"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	h.EnsureMediaDirs()

	// Max 25 MB
	const maxUploadSize = 25 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	if err := r.ParseMultipartForm(maxUploadSize); err != nil {
		jsonErr(w, http.StatusBadRequest, "file too large (max 25MB) or invalid multipart form")
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "missing 'file' form field")
		return
	}
	defer file.Close()

	origName := header.Filename
	ext := strings.ToLower(filepath.Ext(origName))
	mimeType, ok := allowedMediaExts[ext]
	if !ok {
		jsonErr(w, http.StatusBadRequest, "unsupported image format. Allowed: .png, .jpg, .jpeg, .webp, .svg, .ico, .gif")
		return
	}

	// Generate safe filename: <timestamp>-<randHex>-<cleanBase><ext>
	var randBytes [4]byte
	_, _ = rand.Read(randBytes[:])
	randHex := hex.EncodeToString(randBytes[:])

	cleanBase := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, strings.TrimSuffix(filepath.Base(origName), ext))
	if len(cleanBase) > 30 {
		cleanBase = cleanBase[:30]
	}
	if cleanBase == "" {
		cleanBase = "upload"
	}

	targetFilename := fmt.Sprintf("%d-%s-%s%s", time.Now().Unix(), randHex, cleanBase, ext)
	targetPath := filepath.Join(h.cfg.MediaCustomDir(), targetFilename)

	dst, err := os.Create(targetPath)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to create destination file: "+err.Error())
		return
	}
	defer dst.Close()

	written, err := io.Copy(dst, file)
	if err != nil {
		_ = os.Remove(targetPath)
		jsonErr(w, http.StatusInternalServerError, "failed to write file: "+err.Error())
		return
	}

	id := "custom:" + targetFilename
	item := models.MediaItem{
		ID:        id,
		Name:      targetFilename,
		Category:  "custom",
		Path:      targetPath,
		URL:       "/api/media/" + id + "/raw",
		Size:      written,
		SizeHuman: formatBytesHuman(written),
		MimeType:  mimeType,
		IsSystem:  false,
		CreatedAt: time.Now(),
	}

	h.audit.Log(auditFor(r, "media.upload", id, map[string]any{
		"filename":  targetFilename,
		"size":      written,
		"mime_type": mimeType,
	}))

	jsonResp(w, http.StatusCreated, item)
}

// DeleteMedia deletes a custom media item. System media is protected and returns 403.
func (h *Handler) DeleteMedia(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "media"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	if chi.URLParam(r, "id") == "" {
		jsonErr(w, http.StatusBadRequest, "media id is required")
		return
	}
	id, ok := mediaIDParam(r)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "invalid filename")
		return
	}

	if strings.HasPrefix(id, "system:") {
		jsonErr(w, http.StatusForbidden, "Los recursos del sistema están protegidos y no pueden eliminarse por funcionamiento correcto de WebKVM.")
		return
	}
	// A bare filename has always been accepted as a custom asset;
	// normalise it so the in-use lookup below sees the same id the
	// references were stored with.
	if !strings.HasPrefix(id, "custom:") {
		id = "custom:" + id
	}

	dir, filename, ok := h.mediaPathFor(id)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "invalid filename")
		return
	}

	targetPath := filepath.Join(dir, filename)
	if _, err := os.Stat(targetPath); os.IsNotExist(err) {
		jsonErr(w, http.StatusNotFound, "media not found")
		return
	}

	// Refuse to delete an image something still points at, unless the
	// caller explicitly confirms. Deleting silently leaves a VM cover or
	// an avatar rendering as a broken image, with nothing to explain why.
	if r.URL.Query().Get("force") != "true" {
		if used := h.mediaUsage(id); len(used) > 0 {
			jsonResp(w, http.StatusConflict, map[string]any{
				"error":    "media_in_use",
				"message":  "This image is still in use. Retry with ?force=true to delete it anyway.",
				"usages":   used,
				"media_id": id,
			})
			return
		}
	}

	if err := os.Remove(targetPath); err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to delete media: "+err.Error())
		return
	}

	h.audit.Log(auditFor(r, "media.delete", id, map[string]any{
		"filename": filename,
	}))

	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted", "id": id})
}

// GetMediaRaw serves the raw image bytes with caching headers.
func (h *Handler) GetMediaRaw(w http.ResponseWriter, r *http.Request) {
	id, ok := mediaIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}

	var targetPath string
	if strings.HasPrefix(id, "system:") {
		filename := filepath.Base(strings.TrimPrefix(id, "system:"))
		targetPath = filepath.Join(h.cfg.MediaSystemDir(), filename)
	} else if strings.HasPrefix(id, "custom:") {
		filename := filepath.Base(strings.TrimPrefix(id, "custom:"))
		targetPath = filepath.Join(h.cfg.MediaCustomDir(), filename)
	} else {
		filename := filepath.Base(id)
		// Check custom first, then system
		targetPath = filepath.Join(h.cfg.MediaCustomDir(), filename)
		if _, err := os.Stat(targetPath); os.IsNotExist(err) {
			targetPath = filepath.Join(h.cfg.MediaSystemDir(), filename)
		}
	}

	stat, err := os.Stat(targetPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	ext := strings.ToLower(filepath.Ext(targetPath))
	mimeType := allowedMediaExts[ext]
	if mimeType == "" {
		mimeType = mime.TypeByExtension(ext)
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// SVG is executable markup: served as a top-level document it can
	// run scripts in WebKVM's origin (stored-XSS via an uploaded asset).
	// Forcing download keeps <img src> rendering intact while a direct
	// navigation to the URL no longer executes the SVG.
	if mimeType == "image/svg+xml" {
		w.Header().Set("Content-Disposition", "attachment; filename=\""+filepath.Base(targetPath)+"\"")
	}
	http.ServeFile(w, r, targetPath)
	_ = stat
}

// mediaIDParam returns the {id} route parameter percent-decoded. chi
// matches on the escaped request path, so a client that encodes the
// colon in "custom:x" (encodeURIComponent → "custom%3Ax") used to get
// the id back still escaped — and a 404 "media not found" for a file
// that plainly exists. Decoding here makes both spellings equivalent.
// Media filenames never contain '%' (UploadMedia maps everything outside
// [A-Za-z0-9_-] to '_'), so decoding an already-decoded id is a no-op.
// A malformed escape, an empty id, or anything that decodes to a path
// separator, NUL or a dot-segment is rejected outright rather than being
// passed on to the filesystem.
func mediaIDParam(r *http.Request) (string, bool) {
	id, err := url.PathUnescape(chi.URLParam(r, "id"))
	if err != nil || id == "" || strings.ContainsAny(id, "/\\\x00") {
		return "", false
	}
	name := id
	if i := strings.IndexByte(id, ':'); i >= 0 {
		name = id[i+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "", false
	}
	return id, true
}

// mediaExists reports whether a media id resolves to a real file. It is
// the guard that keeps dangling references out of persisted state: a
// reference stored for a file that was never there can only be diagnosed
// later, as a broken image with no trace of where it came from.
func (h *Handler) mediaExists(id string) bool {
	dir, name, ok := h.mediaPathFor(id)
	if !ok {
		return false
	}
	st, err := os.Stat(filepath.Join(dir, name))
	return err == nil && !st.IsDir()
}

// mediaPathFor maps a media id to its directory and filename, rejecting
// anything that is not a well-formed, single-segment id. Centralising this
// keeps the traversal defence in one place instead of repeating a
// filepath.Base sprinkle at every call site.
func (h *Handler) mediaPathFor(id string) (dir, name string, ok bool) {
	switch {
	case strings.HasPrefix(id, "system:"):
		dir = h.cfg.MediaSystemDir()
		name = strings.TrimPrefix(id, "system:")
	case strings.HasPrefix(id, "custom:"):
		dir = h.cfg.MediaCustomDir()
		name = strings.TrimPrefix(id, "custom:")
	default:
		return "", "", false
	}
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return "", "", false
	}
	return dir, name, true
}

// GetBranding returns the site logo and favicon. It is unauthenticated on
// purpose: both are rendered on the login screen, before a session exists.
// Only values that survived validation are returned, so a malformed entry
// written by some other path can never reach an <img src>.
func (h *Handler) GetBranding(w http.ResponseWriter, r *http.Request) {
	out := map[string]string{"logo": "", "favicon": ""}
	if h.settings != nil {
		for _, k := range []string{"logo", "favicon"} {
			v := h.settings.GetString("branding." + k)
			if v != "" && models.IsMediaRawURL(v) {
				out[k] = v
			}
		}
	}
	jsonResp(w, http.StatusOK, out)
}

// mediaUsageRef describes one place a media item is referenced from.
type mediaUsageRef struct {
	Kind string `json:"kind"`           // "avatar" | "logo" | "favicon" | "vm_cover"
	Name string `json:"name,omitempty"` // username or VM name
	ID   string `json:"id,omitempty"`   // VM id, when applicable
}

// mediaUsage reports everywhere a media item is currently referenced.
// Failures to consult a store are treated as "no usage found" rather than
// as an error: this feeds a safety prompt, and a store being briefly
// unavailable must not block an operator from tidying up their library.
func (h *Handler) mediaUsage(id string) []mediaUsageRef {
	raw := models.MediaRawURL(id)
	var out []mediaUsageRef

	if h.userStore != nil {
		for _, u := range h.userStore.List() {
			if u.Avatar == raw {
				out = append(out, mediaUsageRef{Kind: "avatar", Name: u.Username})
			}
		}
	}
	if h.settings != nil {
		if h.settings.GetString("branding.logo") == raw {
			out = append(out, mediaUsageRef{Kind: "logo"})
		}
		if h.settings.GetString("branding.favicon") == raw {
			out = append(out, mediaUsageRef{Kind: "favicon"})
		}
	}
	if h.compute != nil {
		vms, err := h.compute.ListDomains()
		if err == nil {
			for i := range vms {
				// Compare by media id. Every media-library reference ends
				// in "/raw", so comparing filepath.Base (the old approach)
				// matched every library cover against every item and made
				// unrelated deletes fail with media_in_use. Legacy per-VM
				// uploads (/api/covers/...) live outside the library and
				// can never reference a media item.
				cover, _, _ := strings.Cut(vms[i].Cover, "?")
				if coverID, ok := models.MediaIDFromRawURL(cover); ok && coverID == id {
					out = append(out, mediaUsageRef{Kind: "vm_cover", Name: vms[i].Name, ID: vms[i].ID})
				}
			}
		}
	}
	return out
}

// ApplyMediaUsage sets a media asset as avatar, logo, favicon, or VM cover.
func (h *Handler) ApplyMediaUsage(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "media"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	var req models.ApplyMediaUsageRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.MediaID == "" || req.Usage == "" {
		jsonErr(w, http.StatusBadRequest, "media_id and usage are required")
		return
	}

	// The media item must exist before anything records a reference to
	// it. Without this check a typo would be stored happily and only
	// surface later as a broken image, with no clue where it came from.
	if !h.mediaExists(req.MediaID) {
		jsonErr(w, http.StatusNotFound, "media not found: "+req.MediaID)
		return
	}
	rawURL := models.MediaRawURL(req.MediaID)

	switch req.Usage {
	case "avatar":
		user, _, _ := audit.FromRequest(r)
		if user == "" {
			jsonErr(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if h.userStore == nil {
			jsonErr(w, http.StatusServiceUnavailable, "user store unavailable")
			return
		}
		// Persist against the caller's own account only. Taking the
		// username from the session rather than the request body is what
		// stops one user from setting another user's avatar.
		if _, err := h.userStore.SetAvatar(user, rawURL); err != nil {
			if errors.Is(err, models.ErrUserNotFound) {
				jsonErr(w, http.StatusNotFound, "user not found")
				return
			}
			jsonErr(w, http.StatusInternalServerError, "save avatar: "+err.Error())
			return
		}
		h.audit.Log(auditFor(r, "media.set_avatar", req.MediaID, map[string]any{"user": user, "url": rawURL}))
		jsonResp(w, http.StatusOK, map[string]any{"status": "applied", "usage": "avatar", "url": rawURL})

	case "logo", "favicon":
		// Branding is global, so it is admin-only: the "media" permission
		// is enough to manage your own avatar, not to restyle the product
		// for every other user.
		if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
			jsonErr(w, http.StatusForbidden, "forbidden: branding is admin-only")
			return
		}
		if h.settings == nil {
			jsonErr(w, http.StatusServiceUnavailable, "settings store unavailable")
			return
		}
		key := "branding." + req.Usage
		_, failed, err := h.settings.SetMany(configstore.Set{key: rawURL})
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "save "+req.Usage+": "+err.Error())
			return
		}
		if reason, bad := failed[key]; bad {
			jsonErr(w, http.StatusBadRequest, reason)
			return
		}
		h.audit.Log(auditFor(r, "media.set_"+req.Usage, req.MediaID, map[string]any{"url": rawURL}))
		jsonResp(w, http.StatusOK, map[string]any{"status": "applied", "usage": req.Usage, "url": rawURL})

	case "vm_cover":
		if req.VMID == "" {
			jsonErr(w, http.StatusBadRequest, "vm_id is required for vm_cover usage")
			return
		}
		// IDOR guard: setting a cover writes VM metadata, so the caller
		// must actually have access to that VM (owner/group/tag/admin).
		if err := h.requireVMAccess(r, req.VMID); err != nil {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		coverURL := rawURL
		if _, err := h.compute.UpdateVMMeta(req.VMID, models.VMMetaUpdate{Cover: &coverURL}); err != nil {
			h.vmActionErr(w, err, nil)
			return
		}
		// A previous per-VM upload is no longer referenced.
		h.pruneVMCoverFiles(req.VMID, coverURL)
		h.audit.Log(auditFor(r, "media.set_vm_cover", req.MediaID, map[string]any{"vm_id": req.VMID, "url": rawURL}))
		jsonResp(w, http.StatusOK, map[string]any{"status": "applied", "usage": "vm_cover", "vm_id": req.VMID, "url": rawURL})

	default:
		jsonErr(w, http.StatusBadRequest, "unsupported usage type: "+req.Usage)
	}
}
