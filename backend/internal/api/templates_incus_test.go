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
)

type incusTemplateBackend struct {
	compute.Backend
	cloneRequest models.CloneVMRequest
	state        models.VMState
	exists       bool
}

func (b *incusTemplateBackend) GetVMMeta(string) (models.VMMeta, error) {
	return models.VMMeta{Template: true, Shared: true, OwnerID: "admin"}, nil
}

func (b *incusTemplateBackend) GetDomain(id string) (models.VM, error) {
	state := b.state
	if state == "" {
		state = models.VMStateShutoff
	}
	return models.VM{
		ID: id, Name: id, Type: "container", Hypervisor: "incus",
		State: state,
		Disks: []models.DiskInfo{{Target: "/", Pool: "incus-pool", SizeGB: 8}},
	}, nil
}

func (b *incusTemplateBackend) DomainExists(string) (bool, error) { return b.exists, nil }

func (b *incusTemplateBackend) ListStoragePools() ([]models.StoragePool, error) {
	return []models.StoragePool{{Name: "incus-pool", Purpose: compute.PoolPurposeContainer, State: "created"}}, nil
}

func (b *incusTemplateBackend) CloneDomain(_ string, req models.CloneVMRequest) (models.VM, error) {
	b.cloneRequest = req
	return models.VM{ID: "clone1", Name: req.Name, Type: "container", Hypervisor: "incus"}, nil
}

func (b *incusTemplateBackend) UpdateVMMeta(string, models.VMMetaUpdate) (models.VMMeta, error) {
	return models.VMMeta{}, nil
}

func postIncusTemplate(t *testing.T, h *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/templates/template/instantiate", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", "template")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, ctx))
	rec := httptest.NewRecorder()
	h.InstantiateTemplate(rec, req)
	return rec
}

func TestInstantiateIncusTemplateUsesContainerPool(t *testing.T) {
	be := &incusTemplateBackend{}
	h := &Handler{compute: be}

	rec := postIncusTemplate(t, h, `{"name":"clone1","pool":"incus-pool"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if be.cloneRequest.Name != "clone1" || be.cloneRequest.Pool != "incus-pool" {
		t.Fatalf("clone request = %+v", be.cloneRequest)
	}
}

func TestInstantiateIncusTemplateRejectsKVMOnlyOptions(t *testing.T) {
	be := &incusTemplateBackend{}
	h := &Handler{compute: be}

	rec := postIncusTemplate(t, h, `{"name":"clone1","linked":true}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("linked clone status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	rec = postIncusTemplate(t, h, `{"name":"clone1","cloud_init":{"user":"demo"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cloud-init status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestVMRootPoolFindsIncusRootDevice(t *testing.T) {
	vm := models.VM{
		Disks: []models.DiskInfo{
			{Target: "data", Pool: "data-pool"},
			{Target: "root", Pool: "container-pool"},
		},
	}
	if got := vmRootPool(vm); got != "container-pool" {
		t.Fatalf("root pool = %q, want container-pool", got)
	}
}

func TestInstantiateTemplateRejectsRunningSource(t *testing.T) {
	be := &incusTemplateBackend{state: models.VMStateRunning}
	h := &Handler{compute: be}

	rec := postIncusTemplate(t, h, `{"name":"clone1"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if be.cloneRequest.Name != "" {
		t.Fatalf("running template reached the clone: %+v", be.cloneRequest)
	}
}

func TestInstantiateTemplateRejectsTakenTarget(t *testing.T) {
	be := &incusTemplateBackend{exists: true}
	h := &Handler{compute: be}

	rec := postIncusTemplate(t, h, `{"name":"taken"}`)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if be.cloneRequest.Name != "" {
		t.Fatalf("clone ran despite the taken target: %+v", be.cloneRequest)
	}
}
