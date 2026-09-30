package main

import "testing"

// This tool renames disk files to the canonical <vmName>.qcow2 format.
// Letting it loose on an ISO library would rename the operator's
// install media as if it were VM disks, so the skip list is pinned
// here — especially the pre-v2.5 "ISOS" name, which stopped matching
// config.ISOPoolName when the built-in library was renamed.
func TestIsISOPool(t *testing.T) {
	iso := []string{
		"webkvm-isos", // current built-in library
		"ISOS",        // pre-v2.5 built-in library
		"isos",
		"Lexar-isos",  // per-purpose pool from the init-disk flow
		"mydisk-ISOS", // same, upper-cased by hand
		"Seagate-contenedores-isos",
	}
	for _, name := range iso {
		if !isISOPool(name) {
			t.Errorf("isISOPool(%q) = false, want true", name)
		}
	}

	disks := []string{
		"webkvm-disks", "Lexar-Discos", "Seagate", "default", "",
		"isos-archive", // -isos is a prefix here, not a suffix
		"my-isos-pool",
	}
	for _, name := range disks {
		if isISOPool(name) {
			t.Errorf("isISOPool(%q) = true, want false", name)
		}
	}
}
