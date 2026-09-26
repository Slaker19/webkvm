package api

import (
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// Retagging a pool moves no data, which is exactly why a populated
// pool has to be checked first: the files stay where they are and
// simply stop being visible to the half of the storage layer that
// keys off the purpose.
func TestPoolRetagBlockers(t *testing.T) {
	disks := []models.StorageVolume{
		{Name: "vm1.qcow2"},
		{Name: "vm2.raw"},
	}
	isos := []models.StorageVolume{
		{Name: "debian-13.iso"},
		{Name: "ubuntu-24.04.ISO"}, // extension match is case-insensitive
	}
	mixed := append(append([]models.StorageVolume{}, disks...), isos...)

	tests := []struct {
		name    string
		vols    []models.StorageVolume
		purpose string
		want    int
	}{
		{"empty pool retags to anything", nil, compute.PoolPurposeISO, 0},
		{"disks stay disks", disks, compute.PoolPurposeDisk, 0},
		{"disks to template is the golden-image shelf", disks, compute.PoolPurposeTemplate, 0},
		{"disks block an iso retag", disks, compute.PoolPurposeISO, 2},
		{"isos stay isos", isos, compute.PoolPurposeISO, 0},
		{"isos block a disk retag", isos, compute.PoolPurposeDisk, 2},
		{"isos block a template retag", isos, compute.PoolPurposeTemplate, 2},
		{"mixed blocks either way", mixed, compute.PoolPurposeISO, 2},
		// backup pools hold tarballs and metadata, not a fixed
		// extension, so nothing blocks the retag.
		{"backup accepts anything", mixed, compute.PoolPurposeBackup, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := poolRetagBlockers(tc.vols, tc.purpose)
			if len(got) != tc.want {
				t.Fatalf("poolRetagBlockers(%q) = %v (%d blockers), want %d",
					tc.purpose, got, len(got), tc.want)
			}
		})
	}
}

// A pool created without a path lands in a folder named after it under
// the pools directory. The name comes from the client and becomes a
// path segment, so anything that could walk out of that directory has
// to be refused rather than escaped.
func TestDefaultPoolDirName(t *testing.T) {
	ok := []string{"nvme-kvm", "Seagate_isos", "pool.1", "a"}
	for _, n := range ok {
		got, err := defaultPoolDirName(n)
		if err != nil {
			t.Errorf("defaultPoolDirName(%q) errored: %v", n, err)
		}
		if got != n {
			t.Errorf("defaultPoolDirName(%q) = %q, want it unchanged", n, got)
		}
	}

	bad := []string{
		"",
		".",
		"..",
		"../../etc",
		"a/b",
		`a\b`,
		"/abs",
		"has space",    // a folder name we would rather not create silently
		"emoji-\u2728", // outside the allowed set
	}
	for _, n := range bad {
		if got, err := defaultPoolDirName(n); err == nil {
			t.Errorf("defaultPoolDirName(%q) = %q with no error, want refusal", n, got)
		}
	}
}

// A qcow2 snapshot view is not a file on disk, so it must never be
// the reason a retag is refused — the operator has no way to move it.
func TestPoolRetagBlockersIgnoresSnapshots(t *testing.T) {
	vols := []models.StorageVolume{
		{Name: "vm1.qcow2", IsSnapshot: true, SnapshotOfVMID: "abc"},
	}
	if got := poolRetagBlockers(vols, compute.PoolPurposeISO); len(got) != 0 {
		t.Fatalf("snapshot blocked the retag: %v", got)
	}
}
