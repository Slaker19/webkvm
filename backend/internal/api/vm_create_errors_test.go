package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

type createErrorBackend struct {
	compute.Backend
	err error
}

func (b *createErrorBackend) CreateDomain(models.CreateVMRequest) (models.VM, error) {
	return models.VM{}, b.err
}

func TestCreateVMMapsClientErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"nat network", errors.New(`network "default" is not a host Linux bridge; WebKVM only uses real bridges (vmbr0, vmbr1, …)`)},
		{"unknown incus remote", errors.New(`unknown LXD image remote "nope" (known: images, ubuntu)`)},
		{"missing local image", errors.New(`no local image with fingerprint "abc"`)},
		{"too many vcpus", errors.New(`define domain: virError(Code=67, Domain=10, Message='unsupported configuration: Maximum CPUs greater than specified machine type limit 4096'`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{compute: &createErrorBackend{err: tc.err}}
			body := []byte(`{"name":"err-vm","vcpus":1,"ram_mb":1024,"disk_gb":2}`)
			req := httptest.NewRequest(http.MethodPost, "/api/vms", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User", "admin")
			req.Header.Set("X-Role", models.RoleAdmin)
			rec := httptest.NewRecorder()

			h.CreateVM(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

type countingCreateBackend struct {
	compute.Backend
	called bool
}

func (b *countingCreateBackend) CreateDomain(models.CreateVMRequest) (models.VM, error) {
	b.called = true
	return models.VM{ID: "x"}, nil
}

func TestCreateVMRejectsRAMAboveHostMemory(t *testing.T) {
	be := &countingCreateBackend{}
	h := &Handler{compute: be}
	body := []byte(`{"name":"huge-ram","vcpus":1,"ram_mb":99999999,"disk_gb":2}`)
	req := httptest.NewRequest(http.MethodPost, "/api/vms", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	rec := httptest.NewRecorder()

	h.CreateVM(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if be.called {
		t.Fatal("the oversized request reached the backend")
	}
}
