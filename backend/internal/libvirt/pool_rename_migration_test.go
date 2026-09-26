package libvirt

import (
	"strings"
	"testing"

	"webkvm/internal/config"
)

// The migration is the only thing standing between an upgraded install
// and two ISO libraries, so the decision of WHICH pool it is allowed to
// touch is pinned here. The libvirt calls themselves need a live
// connection and are exercised on the real host; this covers the logic
// that decides whether they run at all.

func TestOnSystemDisk(t *testing.T) {
	const poolsDir = "/opt/webkvm/pools"

	tests := []struct {
		name     string
		poolPath string
		want     bool
	}{
		{"managed pool", "/opt/webkvm/pools/ISOS", true},
		{"managed pool, trailing slash", "/opt/webkvm/pools/ISOS/", true},
		{"managed pool, unclean path", "/opt/webkvm/pools/./ISOS", true},
		{"external disk", "/mnt/Lexar/Discos", false},
		{"external disk named like ours", "/mnt/Seagate/ISOS", false},
		{"nested below the pools dir", "/opt/webkvm/pools/a/b", false},
		{"the pools dir itself", "/opt/webkvm/pools", false},
		{"empty path", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := onSystemDisk(tc.poolPath, poolsDir); got != tc.want {
				t.Errorf("onSystemDisk(%q, %q) = %v, want %v",
					tc.poolPath, poolsDir, got, tc.want)
			}
		})
	}
}

func TestOnSystemDiskEmptyPoolsDir(t *testing.T) {
	// A Config with no DataDir must not make every pool look managed.
	if onSystemDisk("/opt/webkvm/pools/ISOS", "") {
		t.Fatal("an empty pools dir must never match")
	}
}

// The migration only has a reason to exist while the two names differ.
// If someone ever sets ISOPoolName back to "ISOS", migrateLegacyISOPool
// returns early — this pins the premise so the guard is not silently
// load-bearing on a value nobody checks.
func TestISOPoolNameIsNoLongerLegacy(t *testing.T) {
	if config.ISOPoolName == legacyISOPoolName {
		t.Fatalf("ISOPoolName is still %q: the rename migration is a no-op", legacyISOPoolName)
	}
	if !isISOPoolNameSuffixed(config.ISOPoolName) {
		t.Fatalf("ISOPoolName %q does not end in -isos: backup ISO detection "+
			"(backupstore.isISOPoolName) would stop recognizing the built-in library",
			config.ISOPoolName)
	}
}

// isISOPoolNameSuffixed mirrors backupstore.isISOPoolName, which this
// package cannot import without a cycle. The test above is the point:
// the two must agree on the built-in library's name.
func isISOPoolNameSuffixed(pool string) bool {
	const suffix = "-isos"
	return len(pool) > len(suffix) && pool[len(pool)-len(suffix):] == suffix
}

// The rename moves the ISO directory; a domain still naming the old
// path in its CD-ROM fails to start ("Cannot access storage file").
func TestRewriteLegacyPoolRefs(t *testing.T) {
	const oldDir = "/opt/webkvm/pools/ISOS"
	const newDir = "/opt/webkvm/pools/webkvm-isos"
	in := `<domain type='kvm'><name>vm1</name><devices>
<disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='/opt/webkvm/pools/webkvm-disks/vm1.qcow2'/><target dev='vda'/></disk>
<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='/opt/webkvm/pools/ISOS/rocky.iso'/><target dev='sda'/></disk>
<disk type='file' device='cdrom'><source file='/opt/webkvm/pools/ISOS2/other.iso'/><target dev='sdb'/></disk>
<disk type='volume' device='cdrom'><source pool='ISOS' volume='deb.iso'/><target dev='sdc'/></disk>
</devices></domain>`
	out, changed := rewriteLegacyPoolRefs(in, oldDir, newDir, legacyISOPoolName, config.ISOPoolName)
	if !changed {
		t.Fatal("expected a change")
	}
	for _, want := range []string{
		"file='/opt/webkvm/pools/webkvm-isos/rocky.iso'",
		"file='/opt/webkvm/pools/webkvm-disks/vm1.qcow2'",
		"file='/opt/webkvm/pools/ISOS2/other.iso'",
		"pool='" + config.ISOPoolName + "' volume='deb.iso'",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/pools/ISOS/") {
		t.Errorf("old path left behind:\n%s", out)
	}
	// Idempotent: a second pass changes nothing.
	if again, ch := rewriteLegacyPoolRefs(out, oldDir, newDir, legacyISOPoolName, config.ISOPoolName); ch || again != out {
		t.Error("second rewrite must be a no-op")
	}
}
