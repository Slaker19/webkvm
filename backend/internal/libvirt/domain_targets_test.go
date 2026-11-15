package libvirt

import "testing"

func TestDiskTargetsDoNotCollideWithOpticalMedia(t *testing.T) {
	cases := []struct {
		bus, disk, iso, virtio string
	}{
		{"virtio", "vda", "sda", "sdb"},
		{"sata", "sda", "sdb", "sdc"},
		{"scsi", "sda", "sdb", "sdc"},
		{"ide", "sda", "sdb", "sdc"},
		{"", "vda", "sda", "sdb"},
	}
	for _, tc := range cases {
		disk, iso, virtio := diskTargets(tc.bus)
		if disk != tc.disk || iso != tc.iso || virtio != tc.virtio {
			t.Errorf("diskTargets(%q) = %s/%s/%s, want %s/%s/%s", tc.bus, disk, iso, virtio, tc.disk, tc.iso, tc.virtio)
		}
		if disk == iso || iso == virtio || disk == virtio {
			t.Errorf("diskTargets(%q) reuses a unit: %s/%s/%s", tc.bus, disk, iso, virtio)
		}
	}
}
