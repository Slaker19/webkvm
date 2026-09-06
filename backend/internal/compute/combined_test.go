package compute

import (
	"errors"
	"testing"

	"webkvm/internal/models"
)

type fakeBackend struct {
	Backend  // embedded interface provides the unimplemented methods
	vms      []models.VM
	err      error
	startErr error
}

func (f *fakeBackend) ListDomains() ([]models.VM, error) { return f.vms, f.err }
func (f *fakeBackend) GetDomain(id string) (models.VM, error) {
	for _, v := range f.vms {
		if v.ID == id {
			return v, nil
		}
	}
	return models.VM{}, ErrNotImplemented
}

// StartDomain returns startErr (nil = primary supports it; ErrNotImplemented
// = secondary not implemented, the LXD fail-safe).
func (f *fakeBackend) StartDomain(id string) error { return f.startErr }

var _ Backend = (*fakeBackend)(nil)

// TestCombinedMergesLists: the primary (KVM) list plus the secondary
// (LXD) list appear together — containers alongside VMs.
func TestCombinedMergesLists(t *testing.T) {
	kvm := &fakeBackend{vms: []models.VM{{ID: "vm-1", Name: "vm-1", Hypervisor: "kvm"}}}
	lxd := &fakeBackend{vms: []models.VM{{ID: "web", Name: "web", Hypervisor: "lxd"}}}
	c := NewCombined(kvm, lxd)
	vms, err := c.ListDomains()
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 2 {
		t.Fatalf("merged list = %d, want 2", len(vms))
	}
	if vms[0].Hypervisor != "kvm" || vms[1].Hypervisor != "lxd" {
		t.Fatalf("merge order wrong: %+v", vms)
	}
}

// TestCombinedSecondaryFailureIsNonFatal: a broken LXD daemon must NOT
// hide KVM VMs or break the list.
func TestCombinedSecondaryFailureIsNonFatal(t *testing.T) {
	kvm := &fakeBackend{vms: []models.VM{{ID: "vm-1", Hypervisor: "kvm"}}}
	lxd := &fakeBackend{err: ErrNotImplemented}
	c := NewCombined(kvm, lxd)
	vms, err := c.ListDomains()
	if err != nil {
		t.Fatalf("secondary failure must be non-fatal, got %v", err)
	}
	if len(vms) != 1 || vms[0].ID != "vm-1" {
		t.Fatalf("KVM list must survive: %+v", vms)
	}
}

// TestCombinedNoSecondary: KVM-only wiring (LXD disabled).
func TestCombinedNoSecondary(t *testing.T) {
	kvm := &fakeBackend{vms: []models.VM{{ID: "vm-1"}}}
	c := NewCombined(kvm, nil)
	if vms, err := c.ListDomains(); err != nil || len(vms) != 1 {
		t.Fatalf("kvm-only list broken: %+v, %v", vms, err)
	}
}

// TestCombinedGetDomain: primary first, then secondary.
func TestCombinedGetDomain(t *testing.T) {
	kvm := &fakeBackend{vms: []models.VM{{ID: "vm-1"}}}
	lxd := &fakeBackend{vms: []models.VM{{ID: "web"}}}
	c := NewCombined(kvm, lxd)
	got, err := c.GetDomain("web")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "web" {
		t.Fatalf("GetDomain should fall back to secondary, got %+v", got)
	}
	if _, err := c.GetDomain("missing"); err != ErrNotImplemented {
		t.Fatalf("missing id: err = %v, want ErrNotImplemented", err)
	}
}

// TestCombinedRoutesContainerOpsToSecondary: an operation on an LXD
// container must land on the LXD backend (ErrNotImplemented → 501), NOT
// leak a primary "domain not found". This is the fail-safe contract.
func TestCombinedRoutesContainerOpsToSecondary(t *testing.T) {
	kvm := &fakeBackend{vms: []models.VM{{ID: "vm-1"}}, startErr: nil}
	lxd := &fakeBackend{vms: []models.VM{{ID: "web"}}, startErr: ErrNotImplemented}
	c := NewCombined(kvm, lxd)
	// Seed ownership via GetDomain, as the handler does before the op.
	if _, err := c.GetDomain("web"); err != nil {
		t.Fatal(err)
	}
	err := c.StartDomain("web")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("container start must be ErrNotImplemented (501), got %v", err)
	}
	// KVM ops still route to the primary.
	if err := c.StartDomain("vm-1"); err != nil {
		t.Fatalf("kvm start must reach the primary, got %v", err)
	}
}
