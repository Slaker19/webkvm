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

type cloneGuardBackend struct {
	compute.Backend
	src   models.VM
	exist bool
}

func (b *cloneGuardBackend) GetDomain(string) (models.VM, error) { return b.src, nil }
func (b *cloneGuardBackend) DomainExists(string) (bool, error)   { return b.exist, nil }
func (b *cloneGuardBackend) CloneDomain(string, models.CloneVMRequest) (models.VM, error) {
	return models.VM{ID: "copy"}, nil
}

func contextWithRoute(req *http.Request, ctx *chi.Context) context.Context {
	return context.WithValue(req.Context(), chi.RouteCtxKey, ctx)
}

func TestCloneVMRefusesTakenTargetName(t *testing.T) {
	h := &Handler{compute: &cloneGuardBackend{exist: true}}
	req := httptest.NewRequest(http.MethodPost, "/api/vms/source/clone", strings.NewReader(`{"name":"taken"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", "source")
	req = req.WithContext(contextWithRoute(req, ctx))
	rec := httptest.NewRecorder()

	h.CloneVM(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestBatchCloneContainerRejectsLinked(t *testing.T) {
	be := &cloneGuardBackend{src: models.VM{ID: "c1", Type: "container", Hypervisor: "incus"}}
	h := &Handler{compute: be}
	req := httptest.NewRequest(http.MethodPost, "/api/vms/c1/batch-clone",
		strings.NewReader(`{"base_name":"copy","count":2,"linked":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", "c1")
	req = req.WithContext(contextWithRoute(req, ctx))
	rec := httptest.NewRecorder()

	h.BatchCloneVM(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

func TestBatchCloneRejectsTakenTargetBeforeCloning(t *testing.T) {
	be := &cloneGuardBackend{src: models.VM{ID: "src", Type: "vm"}, exist: true}
	h := &Handler{compute: be}
	req := httptest.NewRequest(http.MethodPost, "/api/vms/src/batch-clone",
		strings.NewReader(`{"base_name":"copy","count":2}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("id", "src")
	req = req.WithContext(contextWithRoute(req, ctx))
	rec := httptest.NewRecorder()

	h.BatchCloneVM(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}
