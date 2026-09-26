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
	pools    []models.StoragePool
	paths    map[string]string
	deleted  []string
	// pathErrs forces GetPoolPath to fail for a pool that still
	// exists (libvirt lvm/zfs pools have no <target><path>).
	pathErrs map[string]error
	// deleteErrs forces DeletePool to fail for a pool this backend
	// does hold (e.g. still active).
	deleteErrs map[string]error
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
	lxd := &fakeBackend{vms: []models.VM{{ID: "web", Name: "web", Hypervisor: "incus"}}}
	c := NewCombined(kvm, lxd)
	vms, err := c.ListDomains()
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 2 {
		t.Fatalf("merged list = %d, want 2", len(vms))
	}
	if vms[0].Hypervisor != "kvm" || vms[1].Hypervisor != "incus" {
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

func (f *fakeBackend) ListIncusProfiles() ([]string, error) {
	return []string{"default", "custom-profile"}, nil
}

func (f *fakeBackend) ListIncusImages() ([]models.IncusImageItem, error) {
	return []models.IncusImageItem{
		{Ref: "ubuntu:24.04", Label: "Ubuntu 24.04 LTS", Category: "popular", Distro: "ubuntu"},
	}, nil
}

func TestCombinedListIncusProfiles(t *testing.T) {
	kvm := &fakeBackend{}
	lxd := &fakeBackend{}
	c := NewCombined(kvm, lxd)
	profiles, err := c.ListIncusProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0] != "default" {
		t.Fatalf("unexpected profiles: %v", profiles)
	}

	cKVMOnly := NewCombined(kvm, nil)
	profilesKVM, err := cKVMOnly.ListIncusProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(profilesKVM) != 0 {
		t.Fatalf("expected empty profiles for KVM-only, got %v", profilesKVM)
	}
}

func TestCombinedListIncusImages(t *testing.T) {
	kvm := &fakeBackend{}
	lxd := &fakeBackend{}
	c := NewCombined(kvm, lxd)
	images, err := c.ListIncusImages()
	if err != nil {
		t.Fatal(err)
	}
	if len(images) != 1 || images[0].Ref != "ubuntu:24.04" {
		t.Fatalf("unexpected images: %v", images)
	}

	cKVMOnly := NewCombined(kvm, nil)
	imagesKVM, err := cKVMOnly.ListIncusImages()
	if err != nil {
		t.Fatal(err)
	}
	if len(imagesKVM) != 0 {
		t.Fatalf("expected empty images for KVM-only, got %v", imagesKVM)
	}
}

func (f *fakeBackend) ListStoragePools() ([]models.StoragePool, error) {
	if f.pools != nil {
		return f.pools, nil
	}
	return []models.StoragePool{
		{Name: "default", Type: "dir", Purpose: "container"},
	}, nil
}

func (f *fakeBackend) GetPoolPath(name string) (string, error) {
	if err, ok := f.pathErrs[name]; ok {
		return "", err
	}
	if p, ok := f.paths[name]; ok {
		return p, nil
	}
	return "", errors.New("storage pool not found: " + name)
}

// DeletePool records the attempt and fails for a pool this backend
// doesn't hold — a real backend does the same, which is what makes
// the blind-delete fallback in Combined.DeletePool meaningful.
func (f *fakeBackend) DeletePool(name string) error {
	f.deleted = append(f.deleted, name)
	if err, ok := f.deleteErrs[name]; ok {
		return err
	}
	if _, ok := f.paths[name]; !ok {
		return errors.New("storage pool not found: " + name)
	}
	return nil
}

func (f *fakeBackend) ListStorageVolumes(pool string) ([]models.StorageVolume, error) {
	if pool == "default" {
		return []models.StorageVolume{
			{Name: "ct1", Pool: "default", Format: "filesystem", Capacity: 10737418240},
		}, nil
	}
	return nil, errors.New("pool not found")
}

func (f *fakeBackend) DeleteIncusImage(fp string) error {
	if fp == "testfp" {
		return nil
	}
	return errors.New("not found")
}

func TestCombinedStorageAndImages(t *testing.T) {
	kvm := &fakeBackend{}
	lxd := &fakeBackend{}
	c := NewCombined(kvm, lxd)

	pools, err := c.ListStoragePools()
	if err != nil {
		t.Fatal(err)
	}
	// Both fakes return a pool named "default": the shared name is
	// deduplicated, so one row survives.
	if len(pools) != 1 {
		t.Fatalf("expected 1 pool (shared name deduped), got %d", len(pools))
	}

	vols, err := c.ListStorageVolumes("default")
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 1 || vols[0].Name != "ct1" {
		t.Fatalf("unexpected volumes: %+v", vols)
	}

	if err := c.DeleteIncusImage("testfp"); err != nil {
		t.Fatalf("expected nil on delete image, got %v", err)
	}
}

// TestCombinedListStoragePoolsDedupesSharedName: same-name rows from
// both backends (e.g. a pool recreated on the other side, or a legacy
// dual registration) show as a single row, keeping the primary entry;
// pools unique to one side still appear. Current init-disk pools use
// distinct names per purpose (mydisk-vdi, mydisk-containers);
// this dedupe is the safety net underneath.
func TestCombinedListStoragePoolsDedupesSharedName(t *testing.T) {
	kvm := &fakeBackend{pools: []models.StoragePool{
		{Name: "storage-sda1", Type: "dir", Purpose: "disk,iso,container"},
	}}
	lxd := &fakeBackend{pools: []models.StoragePool{
		{Name: "storage-sda1", Type: "dir", Purpose: "container"},
		{Name: "webkvm-incus", Type: "dir", Purpose: "container"},
	}}
	c := NewCombined(kvm, lxd)

	pools, err := c.ListStoragePools()
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 2 {
		t.Fatalf("got %d rows, want 2 (shared name deduped, Incus-only kept): %+v", len(pools), pools)
	}
	if pools[0].Name != "storage-sda1" || pools[0].Purpose != "disk,iso,container" {
		t.Fatalf("primary entry must win the dedupe: %+v", pools[0])
	}
	if pools[1].Name != "webkvm-incus" {
		t.Fatalf("Incus-only pool must be kept: %+v", pools[1])
	}
}

// TestCombinedDeletePoolProbesBothBackends: deleting a pool removes
// it from whichever backend actually holds it (a name could exist on
// both sides after a manual dual registration); a pool living in only
// one side is removed from exactly that side, and an unknown pool errors.
func TestCombinedDeletePoolProbesBothBackends(t *testing.T) {
	kvm := &fakeBackend{paths: map[string]string{"storage-sda1": "/mnt/sda1"}}
	lxd := &fakeBackend{paths: map[string]string{
		"storage-sda1": "/mnt/sda1/contenedores",
		"webkvm-incus": "/var/lib/incus",
	}}
	c := NewCombined(kvm, lxd)

	if err := c.DeletePool("storage-sda1"); err != nil {
		t.Fatalf("dual delete: %v", err)
	}
	if len(kvm.deleted) != 1 || kvm.deleted[0] != "storage-sda1" {
		t.Fatalf("libvirt side not deleted: %v", kvm.deleted)
	}
	if len(lxd.deleted) != 1 || lxd.deleted[0] != "storage-sda1" {
		t.Fatalf("Incus side not deleted: %v", lxd.deleted)
	}

	// Incus-only pool: the libvirt side must be skipped entirely.
	kvm.deleted, lxd.deleted = nil, nil
	if err := c.DeletePool("webkvm-incus"); err != nil {
		t.Fatalf("incus-only delete: %v", err)
	}
	if len(kvm.deleted) != 0 {
		t.Fatalf("libvirt side must be skipped, got %v", kvm.deleted)
	}
	if len(lxd.deleted) != 1 {
		t.Fatalf("Incus side must delete the pool, got %v", lxd.deleted)
	}

	// Unknown pool: no probe matches, so the blind-delete fallback
	// tries both sides (a pool can exist while being invisible to
	// the probe) and surfaces the backend's error.
	kvm.deleted, lxd.deleted = nil, nil
	if err := c.DeletePool("nope"); err == nil {
		t.Fatal("expected error for unknown pool")
	}
	if len(kvm.deleted) != 1 || len(lxd.deleted) != 1 {
		t.Fatalf("unknown pool must fall back to a blind delete on both sides: kvm=%v lxd=%v", kvm.deleted, lxd.deleted)
	}
}

// TestCombinedDeletePoolWithoutFilesystemPath: libvirt answers
// GetPoolPath with a real "pool has no filesystem path" error for
// lvm/zfs pools — the pool EXISTS, it just has no <target><path>.
// Treating that as "not in this backend" would make such a pool
// undeletable, so the probe must accept it.
func TestCombinedDeletePoolWithoutFilesystemPath(t *testing.T) {
	kvm := &fakeBackend{
		paths:    map[string]string{"lvm-pool": ""},
		pathErrs: map[string]error{"lvm-pool": errors.New(`pool "lvm-pool" has no filesystem path`)},
	}
	lxd := &fakeBackend{}
	c := NewCombined(kvm, lxd)

	if err := c.DeletePool("lvm-pool"); err != nil {
		t.Fatalf("path-less libvirt pool must still be deletable: %v", err)
	}
	if len(kvm.deleted) != 1 || kvm.deleted[0] != "lvm-pool" {
		t.Fatalf("libvirt side not deleted: %v", kvm.deleted)
	}
	if len(lxd.deleted) != 0 {
		t.Fatalf("Incus side must be skipped, got %v", lxd.deleted)
	}
}

// A backend that fails the delete must surface its error instead of
// being masked by the other side's silent miss.
func TestCombinedDeletePoolReportsBackendError(t *testing.T) {
	kvm := &fakeBackend{
		paths:      map[string]string{"busy": "/mnt/busy"},
		deleteErrs: map[string]error{"busy": errors.New("pool is still active")},
	}
	c := NewCombined(kvm, &fakeBackend{})
	err := c.DeletePool("busy")
	if err == nil || err.Error() != "pool is still active" {
		t.Fatalf("delete error must reach the caller, got %v", err)
	}
}
