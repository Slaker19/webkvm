package libvirt

import "testing"

// TestInferPoolPurposeSuffixes: the explicit per-purpose suffixes the
// init-disk flow generates must win over the loose "contains iso"
// heuristic. Before the suffixes were checked first, any name with
// "iso" anywhere in it ("isostorage-containers", "mis-isos-backups")
// was misclassified as an ISO library.
func TestInferPoolPurposeSuffixes(t *testing.T) {
	cases := map[string]string{
		// Canonical names produced by createPoolsForSubfolders.
		"mydisk-vdi":        PoolPurposeDisk,
		"mydisk-isos":       PoolPurposeISO,
		"mydisk-containers": "container",
		"mydisk-backups":    "backup",
		"mydisk-plantillas": "template",
		// Legacy / alternative suffixes.
		"lexar-discos":       PoolPurposeDisk,
		"lexar-contenedores": "container",
		"lexar-templates":    "template",
		// Suffix beats the "iso" substring anywhere in the name.
		"isostorage-containers": "container",
		"mis-isos-backups":      "backup",
		"lab-iso-vdi":           PoolPurposeDisk,
		"iso-lab-plantillas":    "template",
		// Loose heuristic still covers hand-made ISO pools.
		"ISOS":        PoolPurposeISO,
		"iso-library": PoolPurposeISO,
		"my-isos":     PoolPurposeISO,
		// Everything else is a disk pool; there is no "general".
		"webkvm-disks": PoolPurposeDisk,
		"Seagate":      PoolPurposeDisk,
		"":             PoolPurposeDisk,
	}
	for name, want := range cases {
		if got := InferPoolPurpose(name); got != want {
			t.Errorf("InferPoolPurpose(%q) = %q, want %q", name, got, want)
		}
	}
}

// Case must never matter: libvirt pool names keep the operator's
// capitalization ("Lexar-Contenedores").
func TestInferPoolPurposeIsCaseInsensitive(t *testing.T) {
	for _, name := range []string{"Lexar-Contenedores", "LEXAR-CONTENEDORES", "lexar-contenedores"} {
		if got := InferPoolPurpose(name); got != "container" {
			t.Errorf("InferPoolPurpose(%q) = %q, want container", name, got)
		}
	}
}
