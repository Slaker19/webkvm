package api

import (
	"os"
	"path/filepath"
	"testing"
)

// The picker must never walk the operator into a directory that pool
// creation would then refuse. Both share validatePoolPath, so this
// pins the cases that matter for browsing specifically: a denied
// directory named directly, and one named indirectly through "..".
func TestBrowseLocalPathRules(t *testing.T) {
	denied := []string{
		"/etc",
		"/etc/libvirt",
		"/home",
		"/root",
		"/proc",
		"/sys",
		"/boot",
		"/dev",
		"/var/log",
		"/var/lib/libvirt",
	}
	for _, p := range denied {
		if err := validatePoolPath(p); err == nil {
			t.Errorf("validatePoolPath(%q) allowed a reserved directory", p)
		}
	}

	// Cleaning happens before validation, so a denied directory spelled
	// through ".." must resolve to the denied one and be refused.
	if got := filepath.Clean("/mnt/../etc"); got != "/etc" {
		t.Fatalf("filepath.Clean(/mnt/../etc) = %q, want /etc", got)
	}
	if err := validatePoolPath(filepath.Clean("/mnt/../etc")); err == nil {
		t.Error("a traversal into /etc was allowed")
	}

	allowed := []string{"/mnt", "/mnt/Seagate", "/srv/data", "/opt/webkvm/pools"}
	for _, p := range allowed {
		if err := validatePoolPath(p); err != nil {
			t.Errorf("validatePoolPath(%q) refused a browsable directory: %v", p, err)
		}
	}
}

// Writability is probed by writing, not by reading the mode bits: the
// server runs as root, for whom the bits say yes on a read-only mount
// that will still refuse the write.
func TestDirWritable(t *testing.T) {
	dir := t.TempDir()
	if !dirWritable(dir) {
		t.Error("dirWritable said no on a fresh temp dir")
	}

	ro := filepath.Join(dir, "ro")
	if err := os.Mkdir(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	// Skipped when the test runs as root, for whom 0500 is not a
	// barrier — the very case the probe exists to handle on a real
	// read-only mount, which a temp dir cannot simulate.
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits are not enforced")
	}
	if dirWritable(ro) {
		t.Error("dirWritable said yes on a directory it cannot write to")
	}

	if dirWritable(filepath.Join(dir, "does-not-exist")) {
		t.Error("dirWritable said yes on a missing directory")
	}
}
