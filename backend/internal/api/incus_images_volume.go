package api

import (
	"errors"
	"net/http"
	"strings"

	"webkvm/internal/compute"
)

type incusImagesVolumeResponse struct {
	// Volume is "pool/volume", or empty for the daemon default.
	Volume string `json:"volume"`
	Pool   string `json:"pool"`
	// Default is true when Incus is caching images in its own
	// directory on the system disk.
	Default bool `json:"default"`
	// DefaultPath is where that is, so the UI can say it plainly
	// instead of leaving "default" to be guessed at.
	DefaultPath string `json:"default_path"`
}

const incusDefaultImagesPath = "/var/lib/incus/images"

// GetIncusImagesVolume handles GET /api/images/incus-volume.
//
// Incus caches every downloaded container image in its own directory on
// the system disk unless told otherwise. On a host whose storage was
// deliberately put on other disks, that is a cache quietly growing on
// the one disk the operator was trying to keep free — and until now
// nothing in WebKVM reported it, let alone offered to move it.
func (h *Handler) GetIncusImagesVolume(w http.ResponseWriter, r *http.Request) {
	vol, err := h.compute.GetIncusImagesVolume()
	if err != nil {
		if errors.Is(err, compute.ErrNotImplemented) {
			jsonErr(w, http.StatusNotImplemented, "Incus is not available on this host")
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := incusImagesVolumeResponse{
		Volume:      vol,
		Default:     vol == "",
		DefaultPath: incusDefaultImagesPath,
	}
	if pool, _, ok := strings.Cut(vol, "/"); ok {
		resp.Pool = pool
	}
	jsonResp(w, http.StatusOK, resp)
}

// SetIncusImagesVolume handles PUT /api/images/incus-volume.
//
// Admin-only (see router.go): this rewrites a daemon-wide setting that
// every container on the host depends on.
func (h *Handler) SetIncusImagesVolume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Pool is the Incus storage pool to cache images in. Empty
		// restores the daemon default.
		Pool string `json:"pool"`
		// Volume names the custom volume inside that pool. Optional:
		// "images" is used when it is left out.
		Volume string `json:"volume"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	pool := strings.TrimSpace(req.Pool)
	volume := strings.TrimSpace(req.Volume)

	// The pool has to be one WebKVM knows as a container pool. Incus
	// would accept any pool it owns, but a pool WebKVM files under a
	// different purpose is one the operator has designated for
	// something else, and silently filling it with the image cache is
	// exactly the kind of mixing the purposes exist to prevent.
	if pool != "" {
		purpose, ok := h.lookupPoolPurpose(pool)
		if !ok {
			jsonErr(w, http.StatusBadRequest, "storage pool "+pool+" does not exist")
			return
		}
		if !compute.HasPurpose(purpose, compute.PoolPurposeContainer) {
			jsonErr(w, http.StatusBadRequest,
				"pool "+pool+" is not an Incus container pool; the image cache can only live in one")
			return
		}
	}

	if err := h.compute.SetIncusImagesVolume(pool, volume); err != nil {
		if errors.Is(err, compute.ErrNotImplemented) {
			jsonErr(w, http.StatusNotImplemented, "Incus is not available on this host")
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "image.incus.volume", pool, map[string]any{
		"pool": pool, "volume": volume,
	}))
	h.GetIncusImagesVolume(w, r)
}
