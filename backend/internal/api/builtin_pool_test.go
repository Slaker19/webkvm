package api

import (
	"testing"

	"webkvm/internal/config"
)

// DeletePool used to rely entirely on Storage.svelte hiding the delete
// button for WebKVM's own pools — the API accepted the call. Deleting
// one does not even do what the operator asked: the server recreates
// the pool empty on the next start, having simply detached libvirt from
// a directory that still holds their disks and ISOs.
//
// The set is mirrored in frontend/src/lib/purpose.js (BUILTIN_POOLS);
// purpose.test.js pins the same names on that side.

func TestIsBuiltinPool(t *testing.T) {
	protected := []string{
		config.DiskPoolName,
		config.ISOPoolName,
		config.IncusPoolName,
		"ISOS", // pre-v2.5 ISO library, on installs not yet migrated
	}
	for _, name := range protected {
		if !isBuiltinPool(name) {
			t.Errorf("isBuiltinPool(%q) = false, want true", name)
		}
	}

	deletable := []string{
		"Lexar-Discos", "Seagate", "Lexar-Contenedores", "default", "",
		"WEBKVM-DISKS",    // case must not be normalized away
		"webkvm-isos-old", // a leftover the operator renamed by hand
		"my-webkvm-disks",
	}
	for _, name := range deletable {
		if isBuiltinPool(name) {
			t.Errorf("isBuiltinPool(%q) = true, want false", name)
		}
	}
}
