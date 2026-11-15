package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

func (h *Handler) createRemoteIncusVM(w http.ResponseWriter, r *http.Request, req models.CreateVMRequest, node nodes.Node) {
	if req.Type == "" && req.Image != "" {
		req.Type = "container"
	}
	if req.Type != "container" || strings.TrimSpace(req.Image) == "" {
		jsonErr(w, http.StatusBadRequest, "remote Incus creation requires type=container and an image")
		return
	}
	if req.Network == "" {
		jsonErr(w, http.StatusBadRequest, "select a bridge network from the target Incus node")
		return
	}
	if req.ExistingDiskPool != "" || req.ExistingDiskName != "" {
		jsonErr(w, http.StatusBadRequest, "existing libvirt disks cannot be attached to an Incus container")
		return
	}
	if req.VLANTag != nil && (*req.VLANTag < 1 || *req.VLANTag > 4094) {
		jsonErr(w, http.StatusBadRequest, "vlan_tag must be between 1 and 4094")
		return
	}
	if req.CloudInit != nil {
		h.scrubCloudInitSnippets(r, req.CloudInit)
		if err := (cloudinit.Config{
			User: req.CloudInit.User, Password: req.CloudInit.Password,
			SSHKey: req.CloudInit.SSHKey, Hostname: req.CloudInit.Hostname,
			CustomUserData: req.CloudInit.CustomUserData, SnippetID: req.CloudInit.SnippetID,
		}).Validate(); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// The client context must outlive the whole creation (image pull included);
	// release cancels it once the handler returns.
	backend, release, err := h.openIncusNode(r.Context(), node, remoteIncusOperationTimeout)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, errIncusCredentialsUnavailable) {
			status = http.StatusInternalServerError
		}
		jsonErr(w, status, fmt.Sprintf("cannot connect to Incus node %q: %v", node.Name, err))
		return
	}
	defer release()

	if req.StoragePool != "" {
		pools, err := backend.ListStoragePools()
		if err != nil {
			jsonErr(w, http.StatusBadGateway, fmt.Sprintf("cannot list pools on Incus node %q: %v", node.Name, err))
			return
		}
		found := false
		for _, pool := range pools {
			if pool.Name == req.StoragePool && (pool.State == "created" || pool.State == "active") {
				found = true
				break
			}
		}
		if !found {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("storage pool %q is not active on Incus node %q", req.StoragePool, node.Name))
			return
		}
	}
	networks, err := backend.ListNetworks()
	if err != nil {
		jsonErr(w, http.StatusBadGateway, fmt.Sprintf("cannot list networks on Incus node %q: %v", node.Name, err))
		return
	}
	networkFound := false
	for _, network := range networks {
		if network.Name == req.Network {
			networkFound = true
			break
		}
	}
	if !networkFound {
		jsonErr(w, http.StatusBadRequest, fmt.Sprintf("network %q is not available on Incus node %q", req.Network, node.Name))
		return
	}
	profiles, err := backend.ListIncusProfiles()
	if err != nil {
		jsonErr(w, http.StatusBadGateway, fmt.Sprintf("cannot list profiles on Incus node %q: %v", node.Name, err))
		return
	}
	if len(req.Profiles) == 0 {
		req.Profiles = []string{"default"}
	}
	for _, selected := range req.Profiles {
		found := false
		for _, profile := range profiles {
			if selected == profile {
				found = true
				break
			}
		}
		if !found {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("profile %q is not available on Incus node %q", selected, node.Name))
			return
		}
	}

	req.Node = ""
	req.Scope = h.mediaScope(r)
	vm, err := backend.CreateDomain(req)
	if err != nil {
		if errors.Is(err, compute.ErrForbidden) {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		jsonErr(w, http.StatusBadGateway, fmt.Sprintf("create Incus container on node %q: %v", node.Name, err))
		return
	}
	vm.Node = node.ID
	if h.audit != nil {
		h.audit.Log(auditFor(r, "vm.create", vm.ID, map[string]any{"node": node.ID, "backend": nodes.BackendIncus, "name": vm.Name}))
	}
	resp := map[string]any{"id": vm.ID, "name": vm.Name, "node": node.ID, "backend": nodes.BackendIncus}
	if req.CloudInit != nil && req.CloudInit.Password != "" {
		resp["password"] = req.CloudInit.Password
		resp["password_warning"] = "Save this password! It won't be shown again."
	}
	jsonResp(w, http.StatusCreated, resp)
}
