package libvirt

import (
	"testing"

	"libvirt.org/go/libvirt"
)

func TestRemoteSupportsKVM(t *testing.T) {
	caps := `<capabilities><guest><arch name="x86_64"><domain type="qemu"/><domain type="kvm"/></arch></guest></capabilities>`
	got, err := remoteSupportsKVM(caps)
	if err != nil || !got {
		t.Fatalf("remoteSupportsKVM() = %v, %v; want true", got, err)
	}

	noKVM, err := remoteSupportsKVM(`<capabilities><guest><arch name="x86_64"><domain type="qemu"/></arch></guest></capabilities>`)
	if err != nil || noKVM {
		t.Fatalf("remoteSupportsKVM() = %v, %v; want false", noKVM, err)
	}
	if _, err := remoteSupportsKVM("not XML"); err == nil {
		t.Fatal("malformed capabilities XML was accepted")
	}
}

func TestMigrateFlags(t *testing.T) {
	base := libvirt.MIGRATE_PERSIST_DEST | libvirt.MIGRATE_UNDEFINE_SOURCE

	live := migrateFlags(true, false)
	if live&base != base {
		t.Fatalf("live flags missing persist/undefine base: got %b", live)
	}
	if live&libvirt.MIGRATE_LIVE == 0 || live&libvirt.MIGRATE_PEER2PEER == 0 {
		t.Fatalf("live migration must set MIGRATE_LIVE and MIGRATE_PEER2PEER: got %b", live)
	}
	if live&libvirt.MIGRATE_NON_SHARED_DISK == 0 {
		t.Fatalf("live migration without shared storage must copy disks: got %b", live)
	}
	if live&libvirt.MIGRATE_OFFLINE != 0 {
		t.Fatalf("live migration must not set MIGRATE_OFFLINE: got %b", live)
	}

	shared := migrateFlags(true, true)
	if shared&libvirt.MIGRATE_NON_SHARED_DISK != 0 {
		t.Fatalf("shared-storage live migration must not copy disks: got %b", shared)
	}

	off := migrateFlags(false, true)
	if off&base != base {
		t.Fatalf("offline flags missing base: got %b", off)
	}
	if off&libvirt.MIGRATE_NON_SHARED_DISK != 0 {
		t.Fatalf("offline migration must not copy disk storage: got %b", off)
	}
	if off&libvirt.MIGRATE_OFFLINE == 0 {
		t.Fatalf("offline migration must set MIGRATE_OFFLINE: got %b", off)
	}
	if off&libvirt.MIGRATE_LIVE != 0 || off&libvirt.MIGRATE_PEER2PEER != 0 {
		t.Fatalf("offline migration must not set LIVE/PEER2PEER: got %b", off)
	}
}
