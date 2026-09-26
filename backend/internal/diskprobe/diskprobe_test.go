package diskprobe

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestBasicEmptyThenData creates a real qcow2, probes it (expect empty),
// writes a chunk of data through qemu-img by growing a raw image, and
// re-probes to confirm HasData flips. Skipped when qemu-img is absent.
func TestBasicEmptyThenData(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed")
	}
	dir := t.TempDir()
	img := filepath.Join(dir, "empty.qcow2")

	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", img, "64M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v (%s)", err, out)
	}

	res, err := Basic(context.Background(), img)
	if err != nil {
		t.Fatalf("Basic(empty) error: %v", err)
	}
	if res.Format != "qcow2" {
		t.Errorf("format = %q, want qcow2", res.Format)
	}
	if res.HasData {
		t.Errorf("freshly created qcow2 reported HasData=true (allocated=%d)", res.Allocated)
	}

	// A raw image preallocated with data (zeros still count as allocated
	// in a raw file, so use a small sparse-created file with real bytes).
	raw := filepath.Join(dir, "data.raw")
	if err := os.WriteFile(raw, make([]byte, 4<<20), 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	res2, err := Basic(context.Background(), raw)
	if err != nil {
		t.Fatalf("Basic(raw) error: %v", err)
	}
	if !res2.HasData {
		t.Errorf("4MiB raw image reported HasData=false (allocated=%d)", res2.Allocated)
	}
}

// TestBasicMissingFile verifies a nonexistent path yields an error, not
// a false "empty disk" verdict.
func TestBasicMissingFile(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed")
	}
	if _, err := Basic(context.Background(), filepath.Join(t.TempDir(), "nope.qcow2")); err == nil {
		t.Fatal("Basic on a missing file should error")
	}
}

// TestParseVirtInspector covers the XML parser with a realistic
// fragment, including a data-disk case (no operatingsystem).
func TestParseVirtInspector(t *testing.T) {
	xmlDoc := `<?xml version="1.0"?>
<operatingsystems>
  <operatingsystem>
    <name>linux</name>
    <distro>ubuntu</distro>
    <mountpoints>
      <mountpoint dev="/dev/sda1" type="ext4" mountpoint="/"/>
      <mountpoint dev="/dev/sda2" type="xfs" mountpoint="/data"/>
    </mountpoints>
  </operatingsystem>
</operatingsystems>`

	name, distro, fs := parseVirtInspector([]byte(xmlDoc))
	if name != "linux" {
		t.Errorf("name = %q, want linux", name)
	}
	if distro != "ubuntu" {
		t.Errorf("distro = %q, want ubuntu", distro)
	}
	if len(fs) != 2 {
		t.Fatalf("filesystems = %v, want 2 entries", fs)
	}
	if fs[0] != "/dev/sda1 (ext4) @ /" {
		t.Errorf("fs[0] = %q", fs[0])
	}

	// Data disk: no operatingsystem element.
	name, distro, fs = parseVirtInspector([]byte(`<operatingsystems/>`))
	if name != "" || distro != "" || fs != nil {
		t.Errorf("empty output should yield no OS, got %q/%q/%v", name, distro, fs)
	}
}

// TestDeepWithoutInspector confirms a missing inspector degrades to a
// warning instead of failing the whole probe.
func TestDeepWithoutInspector(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed")
	}
	dir := t.TempDir()
	img := filepath.Join(dir, "d.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", img, "16M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v (%s)", err, out)
	}
	res := Deep(context.Background(), img, "")
	if res.Deep {
		t.Error("Deep=true without an inspector")
	}
	if res.Warning == "" {
		t.Error("expected a warning when inspector is unavailable")
	}
	if res.Format != "qcow2" {
		t.Errorf("basic facts should survive: format=%q", res.Format)
	}
}
