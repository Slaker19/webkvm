package api

import (
	"fmt"
	"net/http"

	"webkvm/internal/libvirt"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

func (h *Handler) ListNetworks(w http.ResponseWriter, r *http.Request) {
	nets, err := h.lv.ListNetworks()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, nets)
}

func (h *Handler) CreateNetwork(w http.ResponseWriter, r *http.Request) {
	var req models.CreateNetworkRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	net, err := h.lv.CreateNetwork(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.create", req.Name, nil))
	jsonResp(w, http.StatusCreated, net)
}

func (h *Handler) UpdateNetwork(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "id")
	if name == "" {
		jsonErr(w, http.StatusBadRequest, "name is required")
		return
	}
	var req models.UpdateNetworkRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	net, err := h.lv.UpdateNetwork(name, req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.update", name, nil))
	jsonResp(w, http.StatusOK, net)
}

func (h *Handler) DeleteNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Refuse early (before the libvirt round-trip) so the response
	// is a clean 403, not a 500 with a libvirt error. The libvirt
	// layer also enforces this as defense in depth — see
	// libvirt.IsManagedNetwork and the check inside
	// Connector.DeleteNetwork.
	if libvirt.IsManagedNetwork(id) {
		jsonErr(w, http.StatusForbidden, fmt.Sprintf("network %q is managed by webVM and cannot be deleted via the API; remove the underlying Linux bridge manually (or re-run setup-bridge.sh) if you really want it gone", id))
		return
	}
	if err := h.lv.DeleteNetwork(id); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.delete", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (h *Handler) StartNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	net, err := h.lv.StartNetwork(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.start", id, nil))
	jsonResp(w, http.StatusOK, net)
}

func (h *Handler) StopNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	net, err := h.lv.StopNetwork(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "network.stop", id, nil))
	jsonResp(w, http.StatusOK, net)
}
