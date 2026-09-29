package libvirt

import (
	"testing"
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
