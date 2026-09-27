package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
	"webkvm/internal/user"
)

type mediaFailBackend struct {
	compute.Backend
	errToReturn error
}

func (b *mediaFailBackend) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	if b.errToReturn != nil {
		return models.VM{}, b.errToReturn
	}
	return models.VM{ID: "vm-" + req.Name, Name: req.Name}, nil
}

func (b *mediaFailBackend) ListDomains() ([]models.VM, error) {
	return nil, nil
}

func (b *mediaFailBackend) DiskPoolName() string {
	return "webkvm-disks"
}

func (b *mediaFailBackend) ListStoragePools() ([]models.StoragePool, error) {
	return []models.StoragePool{
		{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk, State: "active"},
		{Name: "webkvm-isos", Purpose: compute.PoolPurposeISO, State: "active"},
	}, nil
}

func newMediaValidationHarness(t *testing.T, backendErr error) *Handler {
	t.Helper()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")
	us, err := user.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return &Handler{
		cfg:       &config.Config{DataDir: t.TempDir()},
		userStore: us,
		compute:   &mediaFailBackend{errToReturn: backendErr},
	}
}

func TestCreateVM_InvalidISOReturns400(t *testing.T) {
	// 1. Media not found
	notFoundErr := fmt.Errorf("resolve ISO: media file \"nonexistent.iso\" not found in any storage pool")
	h1 := newMediaValidationHarness(t, notFoundErr)

	body, _ := json.Marshal(models.CreateVMRequest{
		Name:   "test-vm",
		RAMMB:  1024,
		VCPUs:  1,
		DiskGB: 10,
		ISO:    "nonexistent.iso",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/vms", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", string(models.RoleAdmin))
	w := httptest.NewRecorder()

	h1.CreateVM(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 Bad Request, got %d: %s", w.Code, w.Body.String())
	}

	// 2. Ambiguous media
	ambiguousErr := fmt.Errorf("resolve ISO: ambiguous media name \"alpine.iso\" found in multiple storage pools (pool-a, pool-b); specify the full path")
	h2 := newMediaValidationHarness(t, ambiguousErr)

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/vms", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-User", "admin")
	req2.Header.Set("X-Role", string(models.RoleAdmin))

	h2.CreateVM(w2, req2)

	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400 Bad Request on ambiguous media, got %d: %s", w2.Code, w2.Body.String())
	}
}
