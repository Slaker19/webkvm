package api

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
)

// humanizeStartError takes the raw libvirt error returned by
// StartDomain and turns it into something an operator can act on.
// The default virError(Code=..., Domain=..., Message=...) blob is
// technically complete but hostile to read, especially the "Cannot
// access storage file" case which is the most common failure mode
// after a fresh import (a CDROM ISO is missing on the destination).
func humanizeStartError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Cannot access storage file"):
		// Pull the path out of the libvirt error blob so we can
		// show a clean "ISO is missing" / "disk is missing" hint.
		if i := strings.Index(msg, "'/"); i >= 0 {
			if j := strings.Index(msg[i+1:], "'"); j >= 0 {
				path := msg[i+1 : i+1+j]
				_, statErr := os.Stat(path)
				if os.IsNotExist(statErr) {
					return fmt.Sprintf("Cannot start VM: storage file does not exist on this host: %s. Upload the ISO via the Storage page and attach it, or remove the disk/CDROM device from the VM configuration.", path)
				}
				return fmt.Sprintf("Cannot start VM: storage file is not readable: %s. Check file permissions and that the libvirt process can access it.", path)
			}
		}
		return "Cannot start VM: one of the configured storage files is missing or unreadable. Check the VM's disk and CDROM configuration."
	case strings.Contains(msg, "machine type"):
		return fmt.Sprintf("Cannot start VM: %s. The destination hypervisor does not support the machine type in the VM definition. Re-import the VM from a host with a compatible qemu version.", msg)
	case strings.Contains(msg, "Cannot find 'efi' firmware"):
		return "Cannot start VM: OVMF/EFI firmware is not installed on this host. Install the ovmf package (e.g. apt install ovmf) and try again."
	}
	return msg
}

// vmActionErr writes the right HTTP status for an instance operation:
// compute.ErrNotImplemented → 501 (the hypervisor backend does not
// support this operation, e.g. an LXD container before Fase 2), anything
// else → 500. Keeps containers from surfacing confusing internal errors.
func (h *Handler) vmActionErr(w http.ResponseWriter, err error, humanize func(error) string) {
	if errors.Is(err, compute.ErrNotImplemented) {
		jsonErr(w, http.StatusNotImplemented, err.Error())
		return
	}
	// State conflicts (start while running, force-off while stopped,
	// resume while not paused, ...) are a 409, not a 500.
	if errors.Is(err, compute.ErrDomainNotRunning) ||
		errors.Is(err, compute.ErrDomainNotPaused) ||
		errors.Is(err, compute.ErrDomainAlreadyPaused) ||
		errors.Is(err, compute.ErrDomainAlreadyRunning) {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	if humanize != nil {
		jsonErr(w, http.StatusInternalServerError, humanize(err))
		return
	}
	jsonErr(w, http.StatusInternalServerError, err.Error())
}

func (h *Handler) StartVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.checkStartQuota(id); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	if err := h.compute.StartDomain(id); err != nil {
		h.vmActionErr(w, err, humanizeStartError)
		return
	}
	// Re-apply the firewall so port forwards for this VM take effect
	// once it boots and grabs its IP. Best-effort: a failure here is
	// logged, never fatal to the start.
	if h.fwMgr != nil {
		if _, ferr := h.fwMgr.Apply(); ferr != nil {
			h.logError("firewall_reapply_after_start_failed", ferr, id)
		}
	}
	h.audit.Log(auditFor(r, "vm.start", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "started"})
}

func (h *Handler) ShutdownVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.compute.ShutdownDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	h.audit.Log(auditFor(r, "vm.shutdown", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "shutdown"})
}

func (h *Handler) ForceOffVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.compute.ForceOffDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	h.audit.Log(auditFor(r, "vm.forceoff", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "force off"})
}

func (h *Handler) RebootVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.compute.RebootDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	if h.fwMgr != nil {
		if _, ferr := h.fwMgr.Apply(); ferr != nil {
			h.logError("firewall_reapply_after_reboot_failed", ferr, id)
		}
	}
	h.audit.Log(auditFor(r, "vm.reboot", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "reboot"})
}

func (h *Handler) SuspendVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.compute.SuspendDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	h.audit.Log(auditFor(r, "vm.suspend", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "suspended"})
}

func (h *Handler) ResumeVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if err := h.checkStartQuota(id); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	if err := h.compute.ResumeDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	h.audit.Log(auditFor(r, "vm.resume", id, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "resumed"})
}

func (h *Handler) GetBootDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	device, err := h.compute.GetBootDevice(id)
	if err != nil {
		if errors.Is(err, compute.ErrNotImplemented) {
			jsonResp(w, http.StatusOK, map[string]string{"boot_device": ""})
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"boot_device": device})
}

// GetVMLogs returns the trailing lines of the VM's hypervisor / guest log.
func (h *Handler) GetVMLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	lines := 200
	if l := r.URL.Query().Get("lines"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			lines = n
		}
	}
	logs, err := h.compute.GetDomainLog(id, lines)
	if err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"logs": logs})
}

func (h *Handler) SetBootDevice(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Device string `json:"device"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Device == "" {
		jsonErr(w, http.StatusBadRequest, "device is required")
		return
	}
	if req.Device != "disk" && req.Device != "hd" && req.Device != "cdrom" && req.Device != "network" {
		jsonErr(w, http.StatusBadRequest, "device must be one of: disk, hd, cdrom, network")
		return
	}
	if err := h.compute.SetBootDevice(id, req.Device); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.boot_set", id, map[string]interface{}{"device": req.Device}))
	jsonResp(w, http.StatusOK, map[string]string{"status": "boot device updated"})
}

// GetAutostart returns the libvirtd autostart flag for a VM.
func (h *Handler) GetAutostart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	enabled, err := h.compute.GetDomainAutostart(id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]bool{"autostart": enabled})
}

// SetAutostart toggles the libvirtd autostart flag for a VM.
func (h *Handler) SetAutostart(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.compute.SetDomainAutostart(id, req.Enabled); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.autostart_set", id, map[string]interface{}{"enabled": req.Enabled}))
	jsonResp(w, http.StatusOK, map[string]bool{"autostart": req.Enabled})
}
