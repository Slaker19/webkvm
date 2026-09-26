package backupstore

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestStreamRepackedDiskWritesRealBytes covers the `?compress=1`
// export path. It used to declare a tar header with Size: 0 and pipe
// `qemu-img convert -O qcow2` to stdout, but qcow2 output needs a
// seekable destination: qemu-img wrote nothing and still exited 0. The
// disk became a 0-byte tar entry while the manifest listed it and
// DisksIncluded counted it — a backup that looked complete and
// restored an empty disk.
func TestStreamRepackedDiskWritesRealBytes(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not available")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "vm.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", src, "8M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v: %s", err, out)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := streamRepackedDisk(context.Background(), tw, src, "disks/vm.qcow2"); err != nil {
		t.Fatalf("streamRepackedDisk: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	tr := tar.NewReader(&buf)
	hdr, err := tr.Next()
	if err != nil {
		t.Fatalf("reading tar: %v", err)
	}
	if hdr.Name != "disks/vm.qcow2" {
		t.Errorf("entry name = %q", hdr.Name)
	}
	if hdr.Size == 0 {
		t.Fatal("tar entry declares Size 0: the disk is missing from the archive")
	}

	extracted := filepath.Join(dir, "restored.qcow2")
	out, err := os.Create(extracted)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(out, tr)
	out.Close()
	if err != nil {
		t.Fatalf("extracting: %v", err)
	}
	if n != hdr.Size {
		t.Fatalf("extracted %d bytes, header declared %d", n, hdr.Size)
	}

	// The bytes must be a real qcow2 image, not a truncated prefix.
	if out, err := exec.Command("qemu-img", "check", extracted).CombinedOutput(); err != nil {
		t.Fatalf("extracted image fails qemu-img check: %v: %s", err, out)
	}

	// No temp file may survive a successful repack.
	leftovers, _ := filepath.Glob(filepath.Join(os.TempDir(), "webkvm-repack-*.qcow2"))
	for _, p := range leftovers {
		if fi, err := os.Stat(p); err == nil && fi.Size() > 0 {
			t.Errorf("leftover repack temp file: %s", p)
		}
	}
}
