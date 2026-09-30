package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

// deleteTestBackend is a minimal compute.Backend stub: GetDomain returns a
// fixed VM (with one qcow2 disk), DeleteDomain/DeleteVMDiskFiles record
// that they ran, matching the DeleteVMDiskFiles contract (it "deletes"
// nothing itself — the seed ISO cleanup being tested lives entirely in
// DeleteVM, outside DeleteVMDiskFiles).
type deleteTestBackend struct {
	compute.Backend
	vm models.VM
}

func (b *deleteTestBackend) GetDomain(id string) (models.VM, error) { return b.vm, nil }
func (b *deleteTestBackend) DeleteDomain(id string) error           { return nil }
func (b *deleteTestBackend) DeleteVMDiskFiles(vmName string, exact ...string) ([]string, []string, error) {
	return []string{vmName + ".qcow2"}, nil, nil
}

// TestDeleteVM_RemovesCloudInitSeedISO is a regression test for a disk
// leak found during a live audit: cloud-init seed ISOs live outside
// every storage pool (cloudinitDir(), separate from vm.Disks/the pool
// sweep in DeleteVMDiskFiles), so deleting a VM with ?disks=true removed
// its qcow2 but left seed-<vm>.iso orphaned on disk forever. Confirmed
// live: deploying and deleting a real appliance VM left
// /opt/webkvm/cloudinit/seed-<name>.iso behind after "disks deleted".
func TestDeleteVM_RemovesCloudInitSeedISO(t *testing.T) {
	dataDir := t.TempDir()
	ciDir := filepath.Join(dataDir, "cloudinit")
	if err := os.MkdirAll(ciDir, 0o755); err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(ciDir, "seed-audit-poc.iso")
	if err := os.WriteFile(seedPath, []byte("fake iso"), 0o644); err != nil {
		t.Fatal(err)
	}

	be := &deleteTestBackend{vm: models.VM{
		ID:   "audit-poc",
		Name: "audit-poc",
		Disks: []models.DiskInfo{
			{Device: "disk", Name: "audit-poc.qcow2"},
			{Device: "cdrom", Name: "seed-audit-poc.iso", Source: seedPath},
		},
	}}
	h := &Handler{cfg: &config.Config{DataDir: dataDir}, compute: be}

	req := httptest.NewRequest(http.MethodDelete, "/api/vms/audit-poc?disks=true", nil)
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", string(models.RoleAdmin))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "audit-poc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	h.DeleteVM(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body=%s)", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(seedPath); !os.IsNotExist(err) {
		t.Errorf("expected cloud-init seed ISO %s to be removed, stat err = %v", seedPath, err)
	}
}
