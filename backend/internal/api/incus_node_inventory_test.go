package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

type incusInventoryBackendStub struct {
	compute.Backend
	pools    []models.StoragePool
	networks []models.Network
	profiles []string
	images   []models.IncusImageItem
	arch     string
	err      error
}

func (b incusInventoryBackendStub) ListStoragePools() ([]models.StoragePool, error) {
	return b.pools, b.err
}

func (b incusInventoryBackendStub) ListNetworks() ([]models.Network, error) {
	return b.networks, b.err
}

func (b incusInventoryBackendStub) ListIncusProfiles() ([]string, error) {
	return b.profiles, b.err
}

func (b incusInventoryBackendStub) ListIncusImages() ([]models.IncusImageItem, error) {
	return b.images, b.err
}

func (b incusInventoryBackendStub) ServerArchitecture() (string, error) { return b.arch, b.err }

func TestCollectIncusNodeInventoryKeepsRemoteResources(t *testing.T) {
	node := nodes.Node{ID: "n1", Name: "incus-west", Project: "containers"}
	got, err := collectIncusNodeInventory(node, incusInventoryBackendStub{
		pools:    []models.StoragePool{{Name: "fast", Purpose: "container"}},
		networks: []models.Network{{Name: "incusbr0", Bridge: "incusbr0"}},
		profiles: []string{"default", "web"},
		images:   []models.IncusImageItem{{Ref: "images:alpine/3.20"}},
		arch:     "x86_64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != node.ID || got.Name != node.Name || got.Project != node.Project {
		t.Fatalf("node metadata = %+v", got)
	}
	if got.HostArch != "x86_64" {
		t.Fatalf("host architecture = %q, want x86_64", got.HostArch)
	}
	if len(got.Pools) != 1 || got.Pools[0].Name != "fast" {
		t.Fatalf("pools = %+v", got.Pools)
	}
	if len(got.Networks) != 1 || got.Networks[0].Name != "incusbr0" {
		t.Fatalf("networks = %+v", got.Networks)
	}
	if len(got.Profiles) != 2 || got.Profiles[1] != "web" {
		t.Fatalf("profiles = %+v", got.Profiles)
	}
	if len(got.Images) != 1 || got.Images[0].Ref != "images:alpine/3.20" {
		t.Fatalf("images = %+v", got.Images)
	}
}

func TestCollectIncusNodeInventoryReturnsEmptyArrays(t *testing.T) {
	got, err := collectIncusNodeInventory(nodes.Node{}, incusInventoryBackendStub{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pools == nil || got.Networks == nil || got.Profiles == nil || got.Images == nil {
		t.Fatalf("empty inventories must be arrays, got %+v", got)
	}
}

func incusInventoryRequest(id string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/nodes/"+id+"/incus-inventory", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestGetNodeIncusInventoryRejectsLibvirtNode(t *testing.T) {
	registry, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := registry.Create("remote-kvm", "qemu+ssh://user@host.example/system")
	if err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	(&Handler{nodes: registry}).GetNodeIncusInventory(res, incusInventoryRequest(remote.ID))

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d: %s", res.Code, http.StatusBadRequest, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "Incus") {
		t.Fatalf("error should identify the expected backend: %s", res.Body.String())
	}
}

func TestGetNodeIncusInventoryRejectsDisabledNode(t *testing.T) {
	registry, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := registry.CreateTyped(
		"remote-incus",
		"https://incus.example:8443",
		nodes.BackendIncus,
		"default",
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	if _, err := registry.Update(remote.ID, "", "", &disabled); err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	(&Handler{nodes: registry}).GetNodeIncusInventory(res, incusInventoryRequest(remote.ID))

	if res.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d: %s", res.Code, http.StatusNotFound, res.Body.String())
	}
}

func TestGetNodeIncusInventoryRequiresStoredCredentials(t *testing.T) {
	registry, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	remote, err := registry.CreateTyped(
		"remote-incus",
		"https://incus.example:8443",
		nodes.BackendIncus,
		"containers",
		strings.Repeat("a", 64),
		strings.Repeat("b", 64),
	)
	if err != nil {
		t.Fatal(err)
	}

	res := httptest.NewRecorder()
	(&Handler{nodes: registry}).GetNodeIncusInventory(res, incusInventoryRequest(remote.ID))

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: %s", res.Code, http.StatusInternalServerError, res.Body.String())
	}
	if strings.Contains(res.Body.String(), remote.CredentialID) {
		t.Fatalf("error disclosed credential ID: %s", res.Body.String())
	}
}
