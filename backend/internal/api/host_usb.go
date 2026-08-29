package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// ListHostUSBDevices returns the USB devices on the host available
// for passthrough. Admin only (wired under /api/host's admin group).
func (h *Handler) ListHostUSBDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := h.lv.ListHostUSBDevices()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, devs)
}

type usbDeviceRequest struct {
	VendorID  string `json:"vendor_id"`
	ProductID string `json:"product_id"`
}

// AttachUSBDevice passes a host USB device through to a VM. Admin only.
func (h *Handler) AttachUSBDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req usbDeviceRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.VendorID == "" || req.ProductID == "" {
		jsonErr(w, http.StatusBadRequest, "vendor_id and product_id are required")
		return
	}
	if err := h.lv.AttachUSBDevice(id, req.VendorID, req.ProductID); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.usb_attach", id, map[string]interface{}{"vendor_id": req.VendorID, "product_id": req.ProductID}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "attached"})
}

// DetachUSBDevice removes a passed-through USB device from a VM. Admin only.
func (h *Handler) DetachUSBDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vendorID := chi.URLParam(r, "vendorId")
	productID := chi.URLParam(r, "productId")
	if err := h.lv.DetachUSBDevice(id, vendorID, productID); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.usb_detach", id, map[string]interface{}{"vendor_id": vendorID, "product_id": productID}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "detached"})
}
