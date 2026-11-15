package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	computeincus "webkvm/internal/compute/incus"
	"webkvm/internal/incusremote"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

var errIncusCredentialsUnavailable = errors.New("stored Incus credentials are unavailable")

type incusNodeInventory struct {
	NodeID   string                  `json:"node_id"`
	Name     string                  `json:"name"`
	Project  string                  `json:"project"`
	HostArch string                  `json:"host_arch"`
	Pools    []models.StoragePool    `json:"pools"`
	Networks []models.Network        `json:"networks"`
	Profiles []string                `json:"profiles"`
	Images   []models.IncusImageItem `json:"images"`
}

type incusInventorySource interface {
	ServerArchitecture() (string, error)
	ListStoragePools() ([]models.StoragePool, error)
	ListNetworks() ([]models.Network, error)
	ListIncusProfiles() ([]string, error)
	ListIncusImages() ([]models.IncusImageItem, error)
}

func (h *Handler) incusCredentialsForNode(node nodes.Node) (incusremote.Credentials, error) {
	if h.nodes == nil || h.nodes.IncusCredentials() == nil {
		return incusremote.Credentials{}, errIncusCredentialsUnavailable
	}
	stored, err := h.nodes.IncusCredentials().Load(node.CredentialID)
	if err != nil {
		return incusremote.Credentials{}, errIncusCredentialsUnavailable
	}
	return incusremote.Credentials{
		ClientCertificate: stored.ClientCertificate,
		ClientKey:         stored.ClientKey,
		ServerCertificate: stored.ServerCertificate,
	}, nil
}

const (
	// remoteIncusListTimeout bounds a read-only listing round trip.
	remoteIncusListTimeout = 20 * time.Second
	// remoteIncusOperationTimeout bounds one instance operation. The Incus
	// backend waits for the server-side operation (image pull, graceful
	// shutdown), so this must be generous; the request context still cancels
	// it when the caller disconnects.
	remoteIncusOperationTimeout = 10 * time.Minute
)

func (h *Handler) connectIncusNode(ctx context.Context, node nodes.Node) (*computeincus.IncusBackend, error) {
	credentials, err := h.incusCredentialsForNode(node)
	if err != nil {
		return nil, err
	}
	return computeincus.NewRemoteIncusBackend(ctx, node.URI, credentials, node.Project)
}

// openIncusNode connects to a remote Incus node and returns the backend with
// a release that closes the client and cancels its context. The Incus client
// stores ctx and uses it for every later request, so the context must stay
// alive for the whole operation, not just for the connect; callers must not
// cancel it before release.
func (h *Handler) openIncusNode(parent context.Context, node nodes.Node, timeout time.Duration) (*computeincus.IncusBackend, func(), error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	backend, err := h.connectIncusNode(ctx, node)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return backend, func() {
		backend.Close()
		cancel()
	}, nil
}

func collectIncusNodeInventory(node nodes.Node, backend incusInventorySource) (incusNodeInventory, error) {
	arch, err := backend.ServerArchitecture()
	if err != nil {
		return incusNodeInventory{}, fmt.Errorf("read server architecture: %w", err)
	}
	pools, err := backend.ListStoragePools()
	if err != nil {
		return incusNodeInventory{}, fmt.Errorf("list storage pools: %w", err)
	}
	networks, err := backend.ListNetworks()
	if err != nil {
		return incusNodeInventory{}, fmt.Errorf("list networks: %w", err)
	}
	profiles, err := backend.ListIncusProfiles()
	if err != nil {
		return incusNodeInventory{}, fmt.Errorf("list profiles: %w", err)
	}
	images, err := backend.ListIncusImages()
	if err != nil {
		return incusNodeInventory{}, fmt.Errorf("list images: %w", err)
	}
	if pools == nil {
		pools = []models.StoragePool{}
	}
	if networks == nil {
		networks = []models.Network{}
	}
	if profiles == nil {
		profiles = []string{}
	}
	if images == nil {
		images = []models.IncusImageItem{}
	}
	return incusNodeInventory{
		NodeID: node.ID, Name: node.Name, Project: node.Project, HostArch: arch,
		Pools: pools, Networks: networks, Profiles: profiles, Images: images,
	}, nil
}

// GetNodeIncusInventory returns resources from the selected Incus endpoint.
// The nodes router keeps this administrator-only because Incus access is
// restricted to administrators in V1.
func (h *Handler) GetNodeIncusInventory(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	node, ok := h.nodes.Get(chiURLParam(r, "id"))
	if !ok {
		jsonErr(w, http.StatusNotFound, "node not found")
		return
	}
	if node.Driver() != nodes.BackendIncus {
		jsonErr(w, http.StatusBadRequest, "selected node is not an Incus endpoint")
		return
	}
	if !node.Enabled {
		jsonErr(w, http.StatusNotFound, "Incus node is disabled")
		return
	}
	credentials, err := h.incusCredentialsForNode(node)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	backend, err := computeincus.NewRemoteIncusBackend(ctx, node.URI, credentials, node.Project)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, fmt.Sprintf("connect to Incus node %q: %v", node.Name, err))
		return
	}
	defer backend.Close()

	inventory, err := collectIncusNodeInventory(node, backend)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, fmt.Sprintf("read inventory from Incus node %q: %v", node.Name, err))
		return
	}
	jsonResp(w, http.StatusOK, inventory)
}
