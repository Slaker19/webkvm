package libvirt

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// allocatedBytes reports what the file actually occupies on disk, as
// opposed to how large it claims to be.
func allocatedBytes(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("cannot read block allocation on this platform")
	}
	return st.Blocks * 512
}

// A disk image is mostly holes. Copying one by streaming its bytes
// turns a 4 GiB image holding 1 MiB of data into 4 GiB of zeroes on
// the destination, which fills a pool sized for the real usage.
func TestCopyFileKeepsHoles(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sparse.img")
	const size = 512 << 20

	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	// Data at the very start and the very end, holes in between: the
	// layout that catches an implementation which stops at the first
	// hole or forgets the tail.
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xab}, 4096), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt(bytes.Repeat([]byte{0xcd}, 4096), size-4096); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dst := filepath.Join(dir, "copy.img")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}

	srcData, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	dstData, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srcData, dstData) {
		t.Fatal("the copy does not match the source byte for byte")
	}

	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != size {
		t.Errorf("size = %d, want %d: a file ending in a hole must keep its length", fi.Size(), size)
	}

	srcAlloc, dstAlloc := allocatedBytes(t, src), allocatedBytes(t, dst)
	t.Logf("allocated: source=%d KiB copy=%d KiB (apparent size %d MiB)",
		srcAlloc/1024, dstAlloc/1024, size>>20)
	if dstAlloc > srcAlloc*4+(1<<20) {
		t.Errorf("the copy occupies %d KiB against the source's %d KiB: the holes were filled in",
			dstAlloc/1024, srcAlloc/1024)
	}
}

// A file with no holes at all must still be copied exactly.
func TestCopyFileDenseFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dense.bin")
	want := bytes.Repeat([]byte("webkvm"), 10000)
	if err := os.WriteFile(src, want, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "copy.bin")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the copy does not match the source")
	}
}

// An empty file is a legitimate edge: the loop must not run at all.
func TestCopyFileEmpty(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(src, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "copy.bin")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() != 0 {
		t.Errorf("size = %d, want 0", fi.Size())
	}
}

// copyFile must never overwrite an existing file: the move paths rely
// on that to avoid clobbering a volume that already uses the name.
func TestCopyFileRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.bin")
	dst := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(src, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyFile(src, dst); err == nil {
		t.Fatal("copying over an existing file was allowed")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "existing" {
		t.Error("the existing file was modified")
	}
}

// Skipping a hole by seeking past the current end of file reads, to
// XFS, as a file being extended, and it responds with speculative
// preallocation: tens of megabytes of real blocks behind a region that
// holds no data. Declaring the final size up front avoids it. The
// margin here is deliberately tight — the earlier bug showed up as
// 64 MiB of allocation behind 8 KiB of data.
func TestCopyFileNoSpeculativePreallocation(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "big.img")
	const size = 1 << 30

	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("head"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("tail"), size-4); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dst := filepath.Join(dir, "copy.img")
	if err := copyFile(src, dst); err != nil {
		t.Fatal(err)
	}

	srcAlloc, dstAlloc := allocatedBytes(t, src), allocatedBytes(t, dst)
	t.Logf("1 GiB image with 8 bytes of data: source=%d KiB copy=%d KiB",
		srcAlloc/1024, dstAlloc/1024)
	if dstAlloc > srcAlloc+(4<<20) {
		t.Errorf("the copy allocated %d KiB against the source's %d KiB",
			dstAlloc/1024, srcAlloc/1024)
	}
}
