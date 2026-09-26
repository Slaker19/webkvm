package libvirt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVMDiskPath_FirstImport(t *testing.T) {
	dir := t.TempDir()
	got, err := VMDiskPath(dir, "myvm", ".qcow2")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "myvm.qcow2")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVMDiskPath_PoolDirDoesNotExist(t *testing.T) {
	// A non-existent pool dir is the "fresh install" case — we
	// just return the canonical name. The caller will create
	// the directory on the first write.
	got, err := VMDiskPath("/tmp/does-not-exist-pool-xyz", "freshvm", ".qcow2")
	if err != nil {
		t.Fatal(err)
	}
	want := "/tmp/does-not-exist-pool-xyz/freshvm.qcow2"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVMDiskPath_Collision(t *testing.T) {
	dir := t.TempDir()
	// Simulate one previous import.
	if err := os.WriteFile(filepath.Join(dir, "myvm.qcow2"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := VMDiskPath(dir, "myvm", ".qcow2")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "myvm-2.qcow2")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVMDiskPath_MultipleCollisions(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"myvm.qcow2", "myvm-2.qcow2", "myvm-3.qcow2"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := VMDiskPath(dir, "myvm", ".qcow2")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "myvm-4.qcow2")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestVMDiskPath_IgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	// A .bak file and a different VM's disk should NOT count
	// toward the counter — only files matching <name>[-N]?<ext>.
	for _, n := range []string{"myvm.bak", "myvm.tar.gz", "othervm.qcow2", "readme.txt"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := VMDiskPath(dir, "myvm", ".qcow2")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "myvm.qcow2")
	if got != want {
		t.Fatalf("got %q, want %q (unrelated files should not bump counter)", got, want)
	}
}

func TestVMDiskPath_DifferentExtIsNotACollision(t *testing.T) {
	dir := t.TempDir()
	// myvm.raw exists; we're asking for a .qcow2 — that's a
	// different file, no counter bump.
	if err := os.WriteFile(filepath.Join(dir, "myvm.raw"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := VMDiskPath(dir, "myvm", ".qcow2")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "myvm.qcow2")
	if got != want {
		t.Fatalf("got %q, want %q (different ext should not collide)", got, want)
	}
}

func TestVMDiskPath_ValidatesInputs(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name   string
		vmName string
		ext    string
	}{
		{"empty name", "", ".qcow2"},
		{"empty ext", "myvm", ""},
		{"ext without dot", "myvm", "qcow2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := VMDiskPath(dir, c.vmName, c.ext); err == nil {
				t.Fatalf("expected error for vmName=%q ext=%q", c.vmName, c.ext)
			}
		})
	}
}

func TestVMDiskPath_PrefixMatch(t *testing.T) {
	// Defensive: a VM named "myvm" must not bump the counter
	// for "myvm2.qcow2" (which matches the regex naively if
	// not anchored). The regex uses ^<name>(-\d+)?<ext>$, so
	// myvm2.qcow2 should be treated as a separate VM.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "myvm2.qcow2"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := VMDiskPath(dir, "myvm", ".qcow2")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "myvm.qcow2")
	if got != want {
		t.Fatalf("got %q, want %q (myvm2 must not match myvm)", got, want)
	}
}
