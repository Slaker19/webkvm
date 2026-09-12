package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/models"
)

// AttachSharedFolder adds a 9p shared folder to a VM. Admin only (the
// route group in router.go enforces this) — a shared folder exposes an
// entire host directory tree recursively, a materially larger blast
// radius than a single-file disk source, so it doesn't get the
// assertPoolAllowed non-admin path disk attachment has. The VM must
// already be shut off; enforced in libvirt.Connector.AttachSharedFolder.
func (h *Handler) AttachSharedFolder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req models.AttachSharedFolderRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Tag == "" {
		jsonErr(w, http.StatusBadRequest, "tag is required")
		return
	}
	if err := h.validateSharedFolderPath(req.HostPath); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	if err := h.compute.AttachSharedFolder(id, req.HostPath, req.Tag, req.ReadOnly); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.shared_folder_attach", id, map[string]interface{}{"host_path": req.HostPath, "tag": req.Tag}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "attached"})
}

// DetachSharedFolder removes a previously attached 9p shared folder,
// identified by its guest-visible tag. Admin only.
func (h *Handler) DetachSharedFolder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tag := chi.URLParam(r, "tag")
	if err := h.compute.DetachSharedFolder(id, tag); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.shared_folder_detach", id, map[string]interface{}{"tag": tag}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "detached"})
}
