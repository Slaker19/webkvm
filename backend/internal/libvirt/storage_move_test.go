package libvirt

import (
	"os"
	"path/filepath"
	"testing"

	"webkvm/internal/models"
)

// TestDomainDisks pins the XML parsing that decides which files a move
// touches. Getting this wrong is not a cosmetic bug: a missed disk is
// left behind in the old pool while the VM is repointed away from it,
// and a wrongly matched one would have its source rewritten to a file
// that was never copied.
func TestDomainDisks(t *testing.T) {
	const xml = `<domain type='kvm'>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/pool/a.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/isos/install.iso'/>
      <target dev='sda' bus='sata'/>
    </disk>
    <disk type='file' device='disk'>
      <driver name='qemu' type='raw'/>
      <source file='/pool/b.img'/>
      <target dev='vdb' bus='virtio'/>
    </disk>
  </devices>
</domain>`

	got := domainDisks(xml)
	if len(got) != 2 {
		t.Fatalf("got %d disks, want 2 (the cdrom must not be moved)", len(got))
	}
	want := []movableDisk{
		{target: "vda", path: "/pool/a.qcow2", format: "qcow2"},
		{target: "vdb", path: "/pool/b.img", format: "raw"},
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("disk %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// A disk with no <driver type> is assumed qcow2, which is what every
// disk WebKVM creates actually is — guessing raw would make qemu-img
// write a flat image the size of the virtual disk.
func TestDomainDisksDefaultFormat(t *testing.T) {
	const xml = `<domain><devices>
    <disk type='file' device='disk'>
      <source file='/pool/x.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
  </devices></domain>`
	got := domainDisks(xml)
	if len(got) != 1 || got[0].format != "qcow2" {
		t.Fatalf("got %+v, want a single qcow2 disk", got)
	}
}

// A network or block-backed disk has no <source file>, so there is
// nothing on a filesystem to relocate; it must be skipped rather than
// half-parsed into an empty path that a later os.Stat would trip on.
func TestDomainDisksSkipsNonFile(t *testing.T) {
	const xml = `<domain><devices>
    <disk type='network' device='disk'>
      <driver name='qemu' type='raw'/>
      <source protocol='rbd' name='pool/img'/>
      <target dev='vda' bus='virtio'/>
    </disk>
  </devices></domain>`
	if got := domainDisks(xml); len(got) != 0 {
		t.Fatalf("got %+v, want no movable disks", got)
	}
}

// TestCopyFile checks the byte-for-byte copy a cross-filesystem move
// relies on, plus its refusal to clobber an existing destination: the
// source is deleted afterwards, so overwriting the wrong file would
// destroy both.
func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.bin")
	payload := []byte("webkvm move payload\x00\x01\x02")
	if err := os.WriteFile(src, payload, 0o640); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "dst.bin")
	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("content = %q, want %q", got, payload)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("mode = %v, want 0640 (permissions must survive the move)", fi.Mode().Perm())
	}

	if err := copyFile(src, dst); err == nil {
		t.Error("copyFile overwrote an existing destination; it must refuse")
	}
}

// sameFilesystem drives the choice between an instant rename and a full
// copy. An unknown device ID must read as "different": taking the
// rename path across devices fails outright.
func TestSameFilesystem(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if !sameFilesystem(dir, sub) {
		t.Error("two directories in one temp dir reported as different filesystems")
	}
	missing := filepath.Join(dir, "does-not-exist")
	if sameFilesystem(dir, missing) {
		t.Error("an unstattable path must not be treated as the same filesystem")
	}
	if sameFilesystem(missing, missing) {
		t.Error("two unstattable paths must not match each other")
	}
}

// freeBytes only has to be sane: a real filesystem reports something
// positive, and a path that does not exist reports an error instead of
// a zero that would read as "pool full".
func TestFreeBytes(t *testing.T) {
	n, err := freeBytes(t.TempDir())
	if err != nil {
		t.Fatalf("freeBytes: %v", err)
	}
	if n <= 0 {
		t.Errorf("freeBytes = %d, want a positive figure", n)
	}
	if _, err := freeBytes(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("freeBytes on a missing path must fail, not report 0 free")
	}
}

// collectDirs feeds the post-move pool refresh. Duplicates would cause
// the same pool to be rescanned once per disk.
func TestCollectDirs(t *testing.T) {
	got := collectDirs([]movedDisk{
		{from: "/pool-a/one.qcow2", to: "/pool-b/one.qcow2", target: "vda"},
		{from: "/pool-a/two.qcow2", to: "/pool-b/two.qcow2", target: "vdb"},
		{from: "/pool-c/three.qcow2", to: "/pool-b/three.qcow2", target: "vdc"},
	})
	want := []string{"/pool-a", "/pool-c"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dir %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOtherDomainAttachments(t *testing.T) {
	atts := []models.VolumeAttachment{
		{VMID: "uuid-self", VMName: "self"},
		{VMID: "self", VMName: "self"},
		{VMID: "uuid-other", VMName: "other"},
	}
	got := otherDomainAttachments(atts, "uuid-self", "self")
	if len(got) != 1 || got[0].VMName != "other" {
		t.Fatalf("got %+v, want only 'other'", got)
	}
	if len(otherDomainAttachments(atts[:2], "uuid-self", "self")) != 0 {
		t.Fatal("self-only attachments must not count as shared")
	}
}
