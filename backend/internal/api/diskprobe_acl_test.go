package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/user"
)

type probeMockBackend struct {
	compute.Backend
	pools       []models.StoragePool
	vms         map[string]models.VM
	metas       map[string]models.VMMeta
	attachments map[string][]models.VolumeAttachment
}

func (b *probeMockBackend) ListStoragePools() ([]models.StoragePool, error) {
	return b.pools, nil
}

func (b *probeMockBackend) GetDomain(id string) (models.VM, error) {
	if vm, ok := b.vms[id]; ok {
		return vm, nil
	}
	return models.VM{}, fmt.Errorf("domain %q not found", id)
}

func (b *probeMockBackend) GetVMMeta(id string) (models.VMMeta, error) {
	if m, ok := b.metas[id]; ok {
		return m, nil
	}
	return models.VMMeta{}, fmt.Errorf("meta for %q not found", id)
}

func (b *probeMockBackend) FindVolumeAttachments(pool, vol string) ([]models.VolumeAttachment, error) {
	key := pool + "/" + vol
	return b.attachments[key], nil
}

func setupProbeTest(t *testing.T) (*Handler, string, string) {
	t.Helper()
	dir := t.TempDir()
	poolADir := filepath.Join(dir, "poolA")
	poolBDir := filepath.Join(dir, "poolB")
	if err := os.MkdirAll(poolADir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(poolBDir, 0755); err != nil {
		t.Fatal(err)
	}

	diskA := filepath.Join(poolADir, "alice.qcow2")
	diskB := filepath.Join(poolBDir, "bob.qcow2")
	if err := os.WriteFile(diskA, []byte("fake-qcow2-a"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diskB, []byte("fake-qcow2-b"), 0644); err != nil {
		t.Fatal(err)
	}

	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")

	// Alice only has access to poolA
	if _, err := us.Create(models.CreateUserRequest{
		Username:     "alice",
		Password:     "Str0ng-Pass#2026",
		Role:         models.RoleOperator,
		AllowedPools: []string{"poolA"},
	}); err != nil {
		t.Fatal(err)
	}

	// Bob has access to poolB
	if _, err := us.Create(models.CreateUserRequest{
		Username:     "bob",
		Password:     "Str0ng-Pass#2026",
		Role:         models.RoleOperator,
		AllowedPools: []string{"poolB"},
	}); err != nil {
		t.Fatal(err)
	}

	be := &probeMockBackend{
		pools: []models.StoragePool{
			{Name: "poolA", Path: poolADir},
			{Name: "poolB", Path: poolBDir},
		},
		vms: map[string]models.VM{
			"vm-alice": {
				ID:      "vm-alice",
				Name:    "vm-alice",
				OwnerID: "alice",
				Disks: []models.DiskInfo{
					{Target: "vda", Source: diskA, Device: "disk"},
				},
			},
			"vm-bob": {
				ID:      "vm-bob",
				Name:    "vm-bob",
				OwnerID: "bob",
				Disks: []models.DiskInfo{
					{Target: "vda", Source: diskB, Device: "disk"},
				},
			},
		},
		metas: map[string]models.VMMeta{
			"vm-alice": {OwnerID: "alice"},
			"vm-bob":   {OwnerID: "bob"},
		},
		attachments: map[string][]models.VolumeAttachment{
			"poolA/alice.qcow2": {{VMID: "vm-alice", VMName: "vm-alice"}},
			"poolB/bob.qcow2":   {{VMID: "vm-bob", VMName: "vm-bob"}},
		},
	}

	h := &Handler{
		userStore: us,
		compute:   be,
	}
	return h, diskA, diskB
}

// A-5: ProbeStorageDisk no debe permitir a un usuario inspeccionar discos de pools a los que no tiene acceso
func TestProbeStorageDisk_PoolRestringido_Bloqueado(t *testing.T) {
	h, _, diskB := setupProbeTest(t)

	// Alice intenta inspeccionar el disco de Bob en poolB (pool no permitido para Alice)
	body, _ := json.Marshal(map[string]any{"path": diskB, "deep": false})
	req := httptest.NewRequest(http.MethodPost, "/api/storage/probe-disk", bytes.NewReader(body))
	req.Header.Set("X-User", "alice")
	req.Header.Set("X-Role", models.RoleOperator)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ProbeStorageDisk(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("Alice no deberia poder sondear un disco en poolB; got status %d (body: %s)", rec.Code, rec.Body.String())
	}
}

// A-5: ProbeStorageDisk no debe permitir a un usuario sondear un disco en uso por una VM de otro usuario
func TestProbeStorageDisk_DiscoDeOtraVM_Bloqueado(t *testing.T) {
	h, diskA, _ := setupProbeTest(t)

	// Bob tiene permitido poolA en este caso temporal, pero el disco pertenece a vm-alice
	allowed := []string{"poolA", "poolB"}
	_, _ = h.userStore.Update("bob", models.UpdateUserRequest{AllowedPools: &allowed})

	body, _ := json.Marshal(map[string]any{"path": diskA, "deep": false})
	req := httptest.NewRequest(http.MethodPost, "/api/storage/probe-disk", bytes.NewReader(body))
	req.Header.Set("X-User", "bob")
	req.Header.Set("X-Role", models.RoleOperator)
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.ProbeStorageDisk(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("Bob no deberia poder sondear un disco adjunto a la VM de Alice; got status %d (body: %s)", rec.Code, rec.Body.String())
	}
}

// A-5: ProbeDisk en /api/vms/{id}/disks/{dev}/probe debe ignorar ?source= ajeno
func TestProbeDisk_IgnoraSourceAjeno(t *testing.T) {
	h, _, diskB := setupProbeTest(t)

	// Alice hace probe en SU VM (vm-alice), pero le inyecta ?source=diskB con un dev que no existe en vm-alice
	req := httptest.NewRequest(http.MethodGet, "/api/vms/vm-alice/disks/vdb/probe?source="+diskB, nil)
	req.Header.Set("X-User", "alice")
	req.Header.Set("X-Role", models.RoleOperator)

	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "vm-alice")
	rctx.URLParams.Add("dev", "vdb") // vdb no existe en vm-alice
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rec := httptest.NewRecorder()
	h.ProbeDisk(rec, req)

	// No debe usar diskB. Debe dar 404 Not Found porque vdb no existe en vm-alice.
	if rec.Code != http.StatusNotFound {
		t.Fatalf("ProbeDisk con dev inexistente no debe usar ?source= ajeno; got status %d (body: %s)", rec.Code, rec.Body.String())
	}
}
