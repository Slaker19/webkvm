package api

import (
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/models"
)

// ListHostPCIDevices returns the PCI devices on the host available for
// passthrough, grouped by IOMMU group. Admin only (wired under /api/host's
// admin group, same bar as USB — arguably higher, given the boot_vga risk).
func (h *Handler) ListHostPCIDevices(w http.ResponseWriter, r *http.Request) {
	groups, err := h.compute.ListHostPCIDevices()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, groups)
}

// AttachPCIDevice passes one or more host PCI devices through to a VM —
// normally every address in one IOMMU group at once. Admin only. The VM
// must already be shut off; the boot/console GPU is refused unconditionally
// — both enforced in libvirt.Connector.AttachPCIDevice.
func (h *Handler) AttachPCIDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req models.AttachPCIRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(req.Addresses) == 0 {
		jsonErr(w, http.StatusBadRequest, "addresses is required")
		return
	}
	if err := h.compute.AttachPCIDevice(id, req.Addresses); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.pci_attach", id, map[string]interface{}{"addresses": req.Addresses}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "attached"})
}

// DetachPCIDevice removes a previously passed-through PCI device from a
// VM. Admin only. The address travels percent-encoded in the URL (it
// contains ':' and '.'), same reasoning as the MAC path param on the
// network-interface detach route.
func (h *Handler) DetachPCIDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	address, err := url.PathUnescape(chi.URLParam(r, "address"))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid address parameter")
		return
	}
	if err := h.compute.DetachPCIDevice(id, address); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.pci_detach", id, map[string]interface{}{"address": address}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "detached"})
}
