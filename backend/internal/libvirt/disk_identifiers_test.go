package libvirt

import (
	"testing"

	"webkvm/internal/models"
)

func TestValidateDiskIdentifiers(t *testing.T) {
	// Valid cases
	if err := validateDiskIdentifiers("", "", ""); err != nil {
		t.Errorf("Empty identifiers should be valid, got %v", err)
	}
	if err := validateDiskIdentifiers("50014ee000000001", "DRIVE-SERIAL-123", "ua-backup-disk"); err != nil {
		t.Errorf("Valid identifiers returned error: %v", err)
	}

	// Invalid WWN (not 16 hex chars)
	if err := validateDiskIdentifiers("invalid-wwn", "", ""); err == nil {
		t.Error("Expected error for non-hex WWN, got nil")
	}
	if err := validateDiskIdentifiers("12345", "", ""); err == nil {
		t.Error("Expected error for short WWN, got nil")
	}

	// Invalid Serial
	if err := validateDiskIdentifiers("", "invalid serial with spaces!", ""); err == nil {
		t.Error("Expected error for serial with spaces, got nil")
	}

	// Invalid Alias (must start with ua-)
	if err := validateDiskIdentifiers("", "", "my-alias"); err == nil {
		t.Error("Expected error for alias without ua- prefix, got nil")
	}
}

func TestParseDisks_ExtractsIdentifiers(t *testing.T) {
	xml := `<domain>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/var/lib/libvirt/images/vm1.qcow2'/>
      <target dev='vda' bus='virtio'/>
      <wwn>50014ee000000001</wwn>
      <serial>QA-SERIAL-999</serial>
      <alias name='ua-fast-storage'/>
    </disk>
  </devices>
</domain>`

	c := NewConnector("test:///default", nil)
	disks := c.parseDisks(xml)
	if len(disks) != 1 {
		t.Fatalf("Expected 1 disk, got %d", len(disks))
	}
	d := disks[0]
	if d.WWN != "50014ee000000001" {
		t.Errorf("Expected WWN 50014ee000000001, got %q", d.WWN)
	}
	if d.Serial != "QA-SERIAL-999" {
		t.Errorf("Expected Serial QA-SERIAL-999, got %q", d.Serial)
	}
	if d.Alias != "ua-fast-storage" {
		t.Errorf("Expected Alias ua-fast-storage, got %q", d.Alias)
	}
}

func TestParseDisks_ZVolBlockDisk(t *testing.T) {
	xml := `<domain>
  <devices>
    <disk type='block' device='disk'>
      <driver name='qemu' type='raw' cache='none' io='native'/>
      <source dev='/dev/zvol/tank/vms/web01'/>
      <target dev='vdb' bus='virtio'/>
    </disk>
  </devices>
</domain>`

	c := NewConnector("test:///default", nil)
	disks := c.parseDisks(xml)
	if len(disks) != 1 {
		t.Fatalf("Expected 1 disk, got %d", len(disks))
	}
	d := disks[0]
	if d.Source != "" {
		t.Errorf("block disk must keep Source empty (file-only paths key off it), got %q", d.Source)
	}
	if d.BlockDev != "/dev/zvol/tank/vms/web01" || d.ZVol != "tank/vms/web01" {
		t.Errorf("unexpected BlockDev/ZVol %q / %q", d.BlockDev, d.ZVol)
	}
	if d.Type != "block" || d.Target != "vdb" || d.Name != "web01" || d.Pool != "" {
		t.Errorf("unexpected disk %+v", d)
	}
}

func TestZVolDiskXML(t *testing.T) {
	typ, src, drv := zvolDiskXML(models.AttachDiskRequest{ZVol: "tank/vms/web01"})
	if typ != "block" || src != "<source dev='/dev/zvol/tank/vms/web01'/>" {
		t.Errorf("got %q %q", typ, src)
	}
	if drv != "<driver name='qemu' type='raw' cache='none' io='native'/>" {
		t.Errorf("default driver = %q", drv)
	}
	off, on := false, true
	_, _, drv = zvolDiskXML(models.AttachDiskRequest{ZVol: "tank/a", DiskCacheIO: &off, DiskDiscard: &on})
	if drv != "<driver name='qemu' type='raw' discard='unmap'/>" {
		t.Errorf("explicit driver = %q", drv)
	}
}

func TestAttachDisk_ZVolRejectsBadInput(t *testing.T) {
	c := NewConnector("test:///default", nil)
	if err := c.Open(); err != nil {
		t.Skipf("libvirt test driver unavailable: %v", err)
	}
	defer c.Close()
	cases := []models.AttachDiskRequest{
		{ZVol: "tank/../../dev/sda"},
		{ZVol: "tank/vm1", Device: "cdrom"},
		{ZVol: "tank/vm1", Source: "/var/lib/libvirt/images/x.qcow2"},
		{ZVol: "tank/vm1", SizeGB: 10},
	}
	for _, req := range cases {
		if err := c.AttachDisk("test", req); err == nil {
			t.Errorf("AttachDisk(%+v) = nil, want error", req)
		}
	}
}
