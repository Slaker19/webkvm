package api

import (
	"net/http"

	"webkvm/internal/hostcaps"
)

// GetCapabilities reports which device models and CPU options the local
// QEMU/libvirt actually accepts, so the create/edit pickers can disable
// (rather than silently offer) unsupported choices.
//
// Reported as plain string lists: the frontend keeps its curated
// ordering and labels, and only filters by the supported set. That way a
// new model added to the UI does not need a matching server change to
// appear, and an old UI against a new backend keeps working.
func (h *Handler) GetCapabilities(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, capabilityPayload(hostcaps.Get()))
}

// RefreshCapabilities drops the cached probe, so a UI "re-detect" button
// (or an operator who just installed qemu-system-modules-spice) can pick
// up newly available models without restarting the service.
func (h *Handler) RefreshCapabilities(w http.ResponseWriter, r *http.Request) {
	hostcaps.Invalidate()
	jsonResp(w, http.StatusOK, capabilityPayload(hostcaps.Get()))
}

func capabilityPayload(c *hostcaps.Capabilities) map[string]any {
	return map[string]any{
		"video_models":    c.VideoModels,
		"graphics_types":  c.GraphicsTypes,
		"disk_buses":      c.DiskBuses,
		"disk_devices":    c.DiskDevices,
		"sound_models":    c.SoundModels,
		"network_models":  c.NetworkModels,
		"cpu_models":      c.CPUModels,
		"cpu_modes":       c.CPUModes,
		"cpu_flags":       c.CPUFlags,
		"spice_supported": c.SPICESupported,
		"guestfs":         c.GuestFS,
		"guestfs_bin":     c.GuestFSBin,
		"qemu_version":    c.QEMUVersion,
		// Lets the UI offer container GPU sharing only where it can
		// actually work, instead of showing a checkbox that silently
		// does nothing on a host with no render node.
		"has_gpu":          c.HasGPU,
		"gpu_render_nodes": c.GPURenderNodes,
		// parsed=false means the probe failed and the lists above are
		// conservative curated defaults, not measured host truth. The UI
		// surfaces this as a soft note instead of hiding options.
		"parsed": c.Parsed,
	}
}
