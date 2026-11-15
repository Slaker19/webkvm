package api

import (
	"strings"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

type poolOnlyBackend struct {
	compute.Backend
}

func (b *poolOnlyBackend) ListStoragePools() ([]models.StoragePool, error) {
	return []models.StoragePool{{Name: "webkvm-disks", Path: "/var/lib/libvirt/images", State: "active"}}, nil
}

// A source outside every pool cannot be checked for dual attachment; on a
// remote node that must fail closed unless the caller forces it.
func TestRemoteAttachUnknownSourceRequiresForce(t *testing.T) {
	h := &Handler{compute: &poolOnlyBackend{}, remote: &remoteTarget{}}

	err := h.checkSourceNotAttachedElsewhere("vm-a", "/mnt/other/disk.qcow2", false)
	if err == nil || !strings.Contains(err.Error(), "retry with force") {
		t.Fatalf("unresolved remote source must fail closed, got %v", err)
	}
	if err := h.checkSourceNotAttachedElsewhere("vm-a", "/mnt/other/disk.qcow2", true); err != nil {
		t.Fatalf("force must allow the attach: %v", err)
	}
}
