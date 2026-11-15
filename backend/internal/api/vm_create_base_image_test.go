package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

type baseImageCreateBackend struct {
	compute.Backend
	pools   []models.StoragePool
	paths   map[string]string
	created []models.CreateVMRequest
}

func (b *baseImageCreateBackend) ListStoragePools() ([]models.StoragePool, error) {
	return b.pools, nil
}

func (b *baseImageCreateBackend) DiskPoolName() string { return "webkvm-disks" }

func (b *baseImageCreateBackend) GetPoolPath(name string) (string, error) {
	return b.paths[name], nil
}

func (b *baseImageCreateBackend) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	b.created = append(b.created, req)
	return models.VM{ID: "vm-1", Name: req.Name, Hypervisor: "kvm"}, nil
}

func (b *baseImageCreateBackend) UpdateVMMeta(string, models.VMMetaUpdate) (models.VMMeta, error) {
	return models.VMMeta{}, nil
}

func TestCreateVMFromCachedBaseImageUsesServerResolvedBackingFile(t *testing.T) {
	poolDir := t.TempDir()
	basePath := filepath.Join(poolDir, "base-ubuntu-24.04.qcow2")
	image := make([]byte, 2<<20)
	copy(image, []byte{'Q', 'F', 'I', 0xfb})
	if err := os.WriteFile(basePath, image, 0o600); err != nil {
		t.Fatal(err)
	}

	be := &baseImageCreateBackend{
		pools: []models.StoragePool{
			{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk, State: "active"},
		},
		paths: map[string]string{"webkvm-disks": poolDir},
	}
	h := &Handler{
		compute: be,
		cfg:     &config.Config{DataDir: filepath.Join(t.TempDir(), "data")},
	}
	body, err := json.Marshal(map[string]any{
		"name": "ubuntu-test", "vcpus": 2, "ram_mb": 2048, "disk_gb": 30,
		"network": "br0", "storage_pool": "webkvm-disks", "base_image_id": "ubuntu-24.04",
		"base_image_path": "/etc/passwd",
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/vms", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", models.RoleAdmin)
	rec := httptest.NewRecorder()

	h.CreateVM(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if len(be.created) != 1 {
		t.Fatalf("CreateDomain calls = %d, want 1", len(be.created))
	}
	if got := be.created[0].BaseImagePath; got != basePath {
		t.Fatalf("resolved backing path = %q, want server-owned cached image %q", got, basePath)
	}

	missingBody := bytes.NewReader([]byte(`{"name":"missing-base","base_image_id":"not-cached"}`))
	missingReq := httptest.NewRequest(http.MethodPost, "/api/vms", missingBody)
	missingReq.Header.Set("Content-Type", "application/json")
	missingReq.Header.Set("X-User", "admin")
	missingReq.Header.Set("X-Role", models.RoleAdmin)
	missingRec := httptest.NewRecorder()
	h.CreateVM(missingRec, missingReq)
	if missingRec.Code != http.StatusNotFound {
		t.Fatalf("missing base status = %d, want 404: %s", missingRec.Code, missingRec.Body.String())
	}
	invalidBody := bytes.NewReader([]byte(`{"name":"invalid-base","base_image_id":"../outside"}`))
	invalidReq := httptest.NewRequest(http.MethodPost, "/api/vms", invalidBody)
	invalidReq.Header.Set("Content-Type", "application/json")
	invalidReq.Header.Set("X-User", "admin")
	invalidReq.Header.Set("X-Role", models.RoleAdmin)
	invalidRec := httptest.NewRecorder()
	h.CreateVM(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid base id status = %d, want 400: %s", invalidRec.Code, invalidRec.Body.String())
	}

	remoteBody := bytes.NewReader([]byte(`{"name":"remote-base","base_image_id":"ubuntu-24.04","node":"remote-kvm"}`))
	remoteReq := httptest.NewRequest(http.MethodPost, "/api/vms", remoteBody)
	remoteReq.Header.Set("Content-Type", "application/json")
	remoteReq.Header.Set("X-User", "admin")
	remoteReq.Header.Set("X-Role", models.RoleAdmin)
	remoteRec := httptest.NewRecorder()
	h.CreateVM(remoteRec, remoteReq)
	if remoteRec.Code != http.StatusNotImplemented {
		t.Fatalf("remote base status = %d, want 501: %s", remoteRec.Code, remoteRec.Body.String())
	}
	if len(be.created) != 1 {
		t.Fatalf("unsupported or missing base image reached the backend: calls=%d", len(be.created))
	}

	for _, tc := range []struct {
		name string
		body string
	}{
		{"container", `{"name":"c-base","type":"container","image":"ubuntu:24.04","base_image_id":"ubuntu-24.04"}`},
		{"too-big", `{"name":"big-base","disk_gb":2048,"base_image_id":"ubuntu-24.04"}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/vms", bytes.NewReader([]byte(tc.body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User", "admin")
		req.Header.Set("X-Role", models.RoleAdmin)
		h.CreateVM(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400: %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if len(be.created) != 1 {
		t.Fatalf("rejected requests reached the backend: calls=%d", len(be.created))
	}
}
