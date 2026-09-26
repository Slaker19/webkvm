package libvirt

import (
	"os"
	"testing"

	"webkvm/internal/models"
)

func TestEvaluatePCIDevice(t *testing.T) {
	restrictions := map[string]pciRestriction{
		"0000:05:00.1": {reason: "host_root_disk", hostCritical: true},
		"0000:01:00.0": {reason: "host_uplink", hostCritical: true},
		"0000:03:00.0": {reason: "mounted_storage", hostCritical: false},
	}

	tests := []struct {
		name         string
		dev          models.PCIDevice
		wantAssign   bool
		wantReason   string
		wantCritical bool
	}{
		{
			name: "InUse takes top priority",
			dev: models.PCIDevice{
				Address: "0000:02:00.0",
				InUse:   true,
			},
			wantAssign:   false,
			wantReason:   "in_use",
			wantCritical: false,
		},
		{
			name: "BootVGA is critical and blocked",
			dev: models.PCIDevice{
				Address: "0000:04:00.0",
				BootVGA: true,
			},
			wantAssign:   false,
			wantReason:   "boot_vga",
			wantCritical: true,
		},
		{
			name: "Host root disk is critical and blocked",
			dev: models.PCIDevice{
				Address: "0000:05:00.1",
			},
			wantAssign:   false,
			wantReason:   "host_root_disk",
			wantCritical: true,
		},
		{
			name: "Host uplink is critical and blocked",
			dev: models.PCIDevice{
				Address: "0000:01:00.0",
			},
			wantAssign:   false,
			wantReason:   "host_uplink",
			wantCritical: true,
		},
		{
			name: "Mounted storage is blocked but not critical",
			dev: models.PCIDevice{
				Address: "0000:03:00.0",
			},
			wantAssign:   false,
			wantReason:   "mounted_storage",
			wantCritical: false,
		},
		{
			name: "Clean device is assignable",
			dev: models.PCIDevice{
				Address: "0000:02:00.0",
			},
			wantAssign:   true,
			wantReason:   "",
			wantCritical: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.dev
			evaluatePCIDevice(&d, restrictions)
			if d.Assignable != tt.wantAssign {
				t.Errorf("Assignable = %v, want %v", d.Assignable, tt.wantAssign)
			}
			if d.BlockReason != tt.wantReason {
				t.Errorf("BlockReason = %q, want %q", d.BlockReason, tt.wantReason)
			}
			if d.HostCritical != tt.wantCritical {
				t.Errorf("HostCritical = %v, want %v", d.HostCritical, tt.wantCritical)
			}
		})
	}
}

func TestEvaluatePCIGroup(t *testing.T) {
	t.Run("Clean single device group", func(t *testing.T) {
		grp := models.PCIIOMMUGroup{
			Group: 9,
			Devices: []models.PCIDevice{
				{Address: "0000:02:00.0", Assignable: true},
			},
		}
		evaluatePCIGroup(&grp)
		if !grp.Assignable || grp.BlockReason != "" || grp.HostCritical {
			t.Errorf("unexpected group evaluation: %+v", grp)
		}
	})

	t.Run("Group containing critical device", func(t *testing.T) {
		grp := models.PCIIOMMUGroup{
			Group: 5,
			Devices: []models.PCIDevice{
				{Address: "0000:04:00.0", Assignable: false, BlockReason: "boot_vga", HostCritical: true},
				{Address: "0000:04:00.1", Assignable: true},
			},
		}
		evaluatePCIGroup(&grp)
		if grp.Assignable {
			t.Errorf("expected group to be unassignable")
		}
		if !grp.HostCritical {
			t.Errorf("expected group to be host critical")
		}
		if grp.BlockReason != "boot_vga" {
			t.Errorf("expected block reason boot_vga, got %q", grp.BlockReason)
		}
	})

	t.Run("Group containing bridges", func(t *testing.T) {
		grp := models.PCIIOMMUGroup{
			Group: 0,
			Devices: []models.PCIDevice{
				{Address: "0000:00:01.0", Assignable: false, BlockReason: "pci_bridge"},
			},
		}
		evaluatePCIGroup(&grp)
		if grp.Assignable {
			t.Errorf("expected bridge group to be unassignable")
		}
		if grp.BlockReason != "pci_bridge" {
			t.Errorf("expected block reason pci_bridge, got %q", grp.BlockReason)
		}
	})

	t.Run("Group with in-use device", func(t *testing.T) {
		grp := models.PCIIOMMUGroup{
			Group: 10,
			Devices: []models.PCIDevice{
				{Address: "0000:03:00.0", InUse: true, Assignable: false, BlockReason: "in_use"},
			},
		}
		evaluatePCIGroup(&grp)
		if grp.Assignable {
			t.Errorf("expected in-use group to be unassignable")
		}
		if grp.BlockReason != "in_use" {
			t.Errorf("expected block reason in_use, got %q", grp.BlockReason)
		}
	})
}

func TestHostRestrictionsLive(t *testing.T) {
	if _, err := os.Stat("/sys/bus/pci/devices/0000:05:00.1"); os.IsNotExist(err) {
		t.Skip("host-specific PCI devices not present on this machine; skipping live test")
	}

	restr := pciHostRestrictions()
	t.Logf("Live host restrictions detected: %d entries", len(restr))
	for addr, r := range restr {
		t.Logf("  %s -> reason: %s, critical: %v", addr, r.reason, r.hostCritical)
	}

	// Verify that the root SATA controller (0000:05:00.1 or parent 0000:00:08.2) is detected as host_root_disk
	if r, found := restr["0000:05:00.1"]; !found || r.reason != "host_root_disk" || !r.hostCritical {
		t.Errorf("expected 0000:05:00.1 to be critical host_root_disk, got %+v", r)
	}

	// Verify that management uplink 0000:01:00.0 is detected as host_uplink
	if r, found := restr["0000:01:00.0"]; !found || r.reason != "host_uplink" || !r.hostCritical {
		t.Errorf("expected 0000:01:00.0 to be critical host_uplink, got %+v", r)
	}

	// Verify that WiFi 0000:02:00.0 is NOT in restrictions (clean)
	if r, found := restr["0000:02:00.0"]; found {
		t.Errorf("expected 0000:02:00.0 to have no restrictions, got %+v", r)
	}
}
