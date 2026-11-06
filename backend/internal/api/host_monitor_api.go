package api

import (
	"context"
	"encoding/hex"
	"net/http"
	"strings"
	"time"
)

// isValidDockerID checks if the given string is a valid Docker container/image ID.
// Docker IDs are hex strings: 12 chars (short) or 64 chars (full SHA256).
func isValidDockerID(id string) bool {
	// Strip optional "sha256:" prefix
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) != 12 && len(id) != 64 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// GetHostMonitor returns fast host monitoring data (CPU, memory, disk, network, system info).
// This endpoint is optimized for speed and returns within ~100ms.
func (h *Handler) GetHostMonitor(w http.ResponseWriter, r *http.Request) {
	if h.hostMonitor == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host monitor not initialized")
		return
	}

	resp, err := h.hostMonitor.CollectFast(r.Context())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, resp)
}

// GetHostMonitorExtended returns slow host monitoring data (Docker, GPU, NPU, users).
// This endpoint may take up to 3 seconds as it collects data from external commands.
// Clients should call this after GetHostMonitor to populate additional panels.
func (h *Handler) GetHostMonitorExtended(w http.ResponseWriter, r *http.Request) {
	if h.hostMonitor == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host monitor not initialized")
		return
	}

	resp, err := h.hostMonitor.CollectExtended(r.Context())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, resp)
}

// GetHostMonitorAll returns all host monitoring data with progressive loading.
// Fast data is returned immediately, slow data is collected in parallel with a timeout.
// Deprecated: Use GetHostMonitor + GetHostMonitorExtended for better perceived performance.
func (h *Handler) GetHostMonitorAll(w http.ResponseWriter, r *http.Request) {
	if h.hostMonitor == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host monitor not initialized")
		return
	}

	resp, err := h.hostMonitor.Collect(r.Context())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, resp)
}

// GetHostProcesses returns the list of running processes.
func (h *Handler) GetHostProcesses(w http.ResponseWriter, r *http.Request) {
	if h.hostMonitor == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host monitor not initialized")
		return
	}

	procs := h.hostMonitor.CollectProcesses(r.Context())
	jsonResp(w, http.StatusOK, map[string]any{
		"processes": procs,
	})
}

// DockerContainerAction performs an action on a Docker container (start/stop/restart).
func (h *Handler) DockerContainerAction(w http.ResponseWriter, r *http.Request) {
	containerID := r.PathValue("id")
	action := r.PathValue("action")

	if containerID == "" {
		jsonErr(w, http.StatusBadRequest, "container ID required")
		return
	}

	if !isValidDockerID(containerID) {
		jsonErr(w, http.StatusBadRequest, "invalid container ID format")
		return
	}

	if action != "start" && action != "stop" && action != "restart" {
		jsonErr(w, http.StatusBadRequest, "invalid action")
		return
	}

	if h.hostMonitor == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host monitor not initialized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var cmd string
	switch action {
	case "start":
		cmd = "start"
	case "stop":
		cmd = "stop"
	case "restart":
		cmd = "restart"
	}

	_, err := h.hostMonitor.Runner().Run(ctx, "docker", cmd, containerID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DeleteDockerContainer deletes a Docker container.
func (h *Handler) DeleteDockerContainer(w http.ResponseWriter, r *http.Request) {
	containerID := r.PathValue("id")
	if containerID == "" {
		jsonErr(w, http.StatusBadRequest, "container ID required")
		return
	}

	if !isValidDockerID(containerID) {
		jsonErr(w, http.StatusBadRequest, "invalid container ID format")
		return
	}

	if h.hostMonitor == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host monitor not initialized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	_, err := h.hostMonitor.Runner().Run(ctx, "docker", "rm", "-f", containerID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

// DeleteDockerImage deletes a Docker image.
func (h *Handler) DeleteDockerImage(w http.ResponseWriter, r *http.Request) {
	imageID := r.PathValue("id")
	if imageID == "" {
		jsonErr(w, http.StatusBadRequest, "image ID required")
		return
	}

	if !isValidDockerID(imageID) {
		jsonErr(w, http.StatusBadRequest, "invalid image ID format")
		return
	}

	if h.hostMonitor == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host monitor not initialized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	_, err := h.hostMonitor.Runner().Run(ctx, "docker", "rmi", "-f", imageID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, map[string]string{"status": "ok"})
}

// PruneDockerImages prunes unused Docker images.
func (h *Handler) PruneDockerImages(w http.ResponseWriter, r *http.Request) {
	if h.hostMonitor == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host monitor not initialized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	out, err := h.hostMonitor.Runner().Run(ctx, "docker", "image", "prune", "-f")
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	jsonResp(w, http.StatusOK, map[string]string{
		"status": "ok",
		"output": string(out),
	})
}
