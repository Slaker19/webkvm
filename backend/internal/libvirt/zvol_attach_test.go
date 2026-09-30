package libvirt

import (
	"testing"
	"webkvm/internal/models"
)

func TestParseDisksFiltered_BlockZVol(t *testing.T) {
	xmlDesc := `<domain type='kvm'>
  <name>test-vm</name>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/var/lib/libvirt/images/test-vm.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='block' device='disk'>
      <driver name='qemu' type='raw' cache='none' io='native'/>
      <source dev='/dev/zvol/tank/vms/test-vol1'/>
      <target dev='vdb' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/var/lib/libvirt/isos/ubuntu.iso'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
  </devices>
</domain>`

	c := &Connector{}
	disks := c.parseDisksFiltered(xmlDesc, false)

	if len(disks) != 3 {
		t.Fatalf("expected 3 disks, got %d", len(disks))
	}

	// Disk 1: file
	d0 := disks[0]
	if d0.Type != "file" || d0.Target != "vda" || d0.Source != "/var/lib/libvirt/images/test-vm.qcow2" || d0.BlockDev != "" {
		t.Errorf("unexpected d0: %+v", d0)
	}

	// Disk 2: block zvol
	d1 := disks[1]
	if d1.Type != "block" {
		t.Errorf("d1.Type = %q, want %q", d1.Type, "block")
	}
	if d1.Target != "vdb" {
		t.Errorf("d1.Target = %q, want %q", d1.Target, "vdb")
	}
	if d1.Source != "" {
		t.Errorf("d1.Source should be empty for block devices, got %q", d1.Source)
	}
	if d1.BlockDev != "/dev/zvol/tank/vms/test-vol1" {
		t.Errorf("d1.BlockDev = %q, want %q", d1.BlockDev, "/dev/zvol/tank/vms/test-vol1")
	}
	if d1.ZVol != "tank/vms/test-vol1" {
		t.Errorf("d1.ZVol = %q, want %q", d1.ZVol, "tank/vms/test-vol1")
	}
	if d1.Name != "test-vol1" {
		t.Errorf("d1.Name = %q, want %q", d1.Name, "test-vol1")
	}

	// Disk 3: cdrom
	d2 := disks[2]
	if d2.Device != "cdrom" || !d2.ReadOnly {
		t.Errorf("unexpected d2: %+v", d2)
	}
}

func TestAttachDisk_ZVolValidation(t *testing.T) {
	c := &Connector{}
	// Invalid: both source and zvol
	err := c.AttachDisk("dummy-id", models.AttachDiskRequest{
		Device: "disk",
		Source: "/path/to/img",
		ZVol:   "tank/vol1",
	})
	if err == nil {
		t.Errorf("expected error when both source and zvol specified, got nil")
	}

	// Invalid: zvol as cdrom
	err = c.AttachDisk("dummy-id", models.AttachDiskRequest{
		Device: "cdrom",
		ZVol:   "tank/vol1",
	})
	if err == nil {
		t.Errorf("expected error when zvol is attached as cdrom, got nil")
	}
}
