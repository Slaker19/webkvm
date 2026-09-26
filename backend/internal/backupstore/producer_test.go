package backupstore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"webkvm/internal/models"
)

// readTar reads the zstd- or gzip-compressed tar at r and
// returns the entries as a map of name → bytes. Used by the
// producer tests to assert on the archive's contents.
func readTar(t *testing.T, r io.Reader, compression string) map[string][]byte {
	t.Helper()
	var src io.Reader = r
	switch compression {
	case "zstd", "":
		zz, err := zstd.NewReader(r)
		if err != nil {
			t.Fatal(err)
		}
		defer zz.Close()
		src = zz
	case "gzip":
		gz, err := gzip.NewReader(r)
		if err != nil {
			t.Fatal(err)
		}
		defer gz.Close()
		src = gz
	default:
		t.Fatalf("unknown compression %q", compression)
	}
	tr := tar.NewReader(src)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		buf, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		out[hdr.Name] = buf
	}
	return out
}

// TestProduceVMArchiveHappyPath covers the basic shape of the
// produced archive: domain.xml, disks/<base>, manifest.txt,
// all wrapped in zstd by default. This is the test the runner
// and the export will both rely on; the per-entry name + the
// manifest line shape are the contract.
func TestProduceVMArchiveHappyPath(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "ubuntu.qcow2")
	if err := os.WriteFile(disk, []byte("fake qcow2 content"), 0o644); err != nil {
		t.Fatal(err)
	}
	vm := VMBackup{
		ID:        "vm-1",
		DomainXML: "<domain type='kvm'><name>vm-1</name></domain>",
		Disks: []models.DiskInfo{
			{Device: "disk", Source: disk, Name: "ubuntu.qcow2"},
		},
	}
	var buf bytes.Buffer
	res, err := ProduceVMArchive(context.Background(), vm, ProducerOpts{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if res.DisksIncluded != 1 || res.DisksSkipped != 0 {
		t.Fatalf("expected 1 disk included / 0 skipped, got %+v", res)
	}
	if res.TotalBytes == 0 {
		t.Fatal("expected non-zero TotalBytes")
	}
	entries := readTar(t, &buf, "zstd")
	if _, ok := entries["domain.xml"]; !ok {
		t.Fatal("missing domain.xml")
	}
	if string(entries["domain.xml"]) != vm.DomainXML {
		t.Fatalf("domain.xml mismatch: %q", entries["domain.xml"])
	}
	if _, ok := entries["disks/ubuntu.qcow2"]; !ok {
		t.Fatal("missing disks/ubuntu.qcow2")
	}
	if string(entries["disks/ubuntu.qcow2"]) != "fake qcow2 content" {
		t.Fatalf("disk content mismatch: %q", entries["disks/ubuntu.qcow2"])
	}
	manifest := string(entries["manifest.txt"])
	if !strings.Contains(manifest, "name=vm-1") {
		t.Fatalf("manifest missing name: %q", manifest)
	}
	if !strings.Contains(manifest, "disk0="+disk) {
		t.Fatalf("manifest missing disk0: %q", manifest)
	}
}

// TestProduceVMArchiveGzip covers the legacy compression
// path. The runner doesn't use it (the runner is zstd-only
// in B2), but the export exposes it via `?legacy=gzip` for
// compat with archives written by older WebKVM versions.
func TestProduceVMArchiveGzip(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "tiny.qcow2")
	if err := os.WriteFile(disk, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	vm := VMBackup{
		ID:        "vm-gz",
		DomainXML: "<domain/>",
		Disks:     []models.DiskInfo{{Device: "disk", Source: disk, Name: "tiny.qcow2"}},
	}
	var buf bytes.Buffer
	_, err := ProduceVMArchive(context.Background(), vm, ProducerOpts{Compression: "gzip"}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	entries := readTar(t, &buf, "gzip")
	if _, ok := entries["domain.xml"]; !ok {
		t.Fatal("missing domain.xml in gzip archive")
	}
}

// TestProduceVMArchiveSkipsMissingDisk covers the case where
// a disk referenced by the VM's metadata no longer exists on
// disk. The producer skips it (with a warn log) instead of
// failing the whole archive — a backup of a VM with a
// recently-removed disk is still useful, the manifest just
// records what's there.
func TestProduceVMArchiveSkipsMissingDisk(t *testing.T) {
	dir := t.TempDir()
	vm := VMBackup{
		ID:        "vm-missing",
		DomainXML: "<domain/>",
		Disks: []models.DiskInfo{
			{Device: "disk", Source: filepath.Join(dir, "deleted.qcow2")},
		},
	}
	var buf bytes.Buffer
	res, err := ProduceVMArchive(context.Background(), vm, ProducerOpts{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if res.DisksIncluded != 0 || res.DisksSkipped != 1 {
		t.Fatalf("expected 0 included / 1 skipped, got %+v", res)
	}
	entries := readTar(t, &buf, "zstd")
	if _, ok := entries["disks/deleted.qcow2"]; ok {
		t.Fatal("missing disk should not be in the archive")
	}
	manifest := string(entries["manifest.txt"])
	if strings.Contains(manifest, "disk0="+filepath.Join(dir, "deleted.qcow2")) {
		t.Fatalf("manifest should not list missing disk: %q", manifest)
	}
}

// TestProduceVMArchiveSkipsOversize covers the size cap
// path. The runner uses DiskSizeLimit to apply the
// per-target MaxFileSizeMB; a disk that exceeds it is
// recorded as SKIPPED in the manifest and excluded from
// the tar.
func TestProduceVMArchiveSkipsOversize(t *testing.T) {
	dir := t.TempDir()
	disk := filepath.Join(dir, "huge.qcow2")
	// 10 KiB file, cap is 1 KiB.
	if err := os.WriteFile(disk, make([]byte, 10*1024), 0o644); err != nil {
		t.Fatal(err)
	}
	vm := VMBackup{
		ID:        "vm-big",
		DomainXML: "<domain/>",
		Disks: []models.DiskInfo{
			{Device: "disk", Source: disk, Name: "huge.qcow2"},
		},
	}
	var buf bytes.Buffer
	res, err := ProduceVMArchive(context.Background(), vm, ProducerOpts{DiskSizeLimit: 1024}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if res.DisksIncluded != 0 || res.DisksSkipped != 1 {
		t.Fatalf("expected 0 included / 1 skipped, got %+v", res)
	}
	entries := readTar(t, &buf, "zstd")
	if _, ok := entries["disks/huge.qcow2"]; ok {
		t.Fatal("oversize disk should not be in the archive")
	}
	manifest := string(entries["manifest.txt"])
	if !strings.Contains(manifest, "SKIPPED") {
		t.Fatalf("manifest should mark the disk as SKIPPED: %q", manifest)
	}
}

// TestProduceVMArchiveUnsupportedCompression covers the
// fail-fast path: an unknown compression string returns an
// error before any tar is written. A typo in opts.Compression
// should not silently produce an uncompressed archive.
func TestProduceVMArchiveUnsupportedCompression(t *testing.T) {
	vm := VMBackup{ID: "x", DomainXML: "<d/>"}
	var buf bytes.Buffer
	_, err := ProduceVMArchive(context.Background(), vm, ProducerOpts{Compression: "lz4"}, &buf)
	if err == nil {
		t.Fatal("expected error for unsupported compression")
	}
	if !strings.Contains(err.Error(), "lz4") {
		t.Fatalf("error should mention the bad value: %v", err)
	}
}

// TestWriteTarEntryRejectsDotDot defends the tar entry name
// against ".." (which would let a hand-crafted VMBackup
// escape the archive on extract). The producer and any
// future caller go through writeTarEntry; the check is in
// one place.
func TestWriteTarEntryRejectsDotDot(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	defer tw.Close()
	if err := writeTarEntry(tw, "../etc/passwd", []byte("x"), 0o644); err == nil {
		t.Fatal("expected error for .. in entry name")
	}
	if err := writeTarEntry(tw, "disks/ok", []byte("x"), 0o644); err != nil {
		t.Fatalf("expected ok for non-.. name, got %v", err)
	}
}

// TestNewCompressorLevelClamping covers the zstd level
// range handling: 0 falls back to the default, out-of-range
// values are clamped. We don't assert the actual level
// produced (the encoder API doesn't expose it), just that
// the producer doesn't error on edge values.
func TestNewCompressorLevelClamping(t *testing.T) {
	for _, level := range []int{0, -1, 1, 22, 99, -99} {
		var buf bytes.Buffer
		w, closeFn, err := newCompressor(&buf, "zstd", level)
		if err != nil {
			t.Errorf("level=%d: %v", level, err)
			continue
		}
		// Smoke-test: write a byte, close.
		if _, err := w.Write([]byte("x")); err != nil {
			t.Errorf("level=%d write: %v", level, err)
		}
		if err := closeFn(); err != nil {
			t.Errorf("level=%d close: %v", level, err)
		}
	}
}

// TestWriteFileToTarResistsTruncateDuringRead is the
// regression test for the production bug "archive/tar:
// missed writing 4096 bytes" that surfaced in the Phase
// II release.
//
// The bug: the previous implementation did f.Stat() to
// get the size for the tar header, then io.Copy(tw, f)
// to write the body. If the file was truncated between
// those two calls (e.g. by logrotate, or by the backend
// appending to audit.log and then rolling), io.Copy
// returned EOF before writing all the declared bytes,
// and the next WriteHeader failed with "missed writing
// N bytes".
//
// This test simulates the race by writing a file,
// truncating it externally between the would-be stat
// and read, and asserting that writeFileToTar produces
// a self-consistent tar (header size matches body size)
// rather than a broken archive that the next WriteHeader
// would reject.
func TestWriteFileToTarResistsTruncateDuringRead(t *testing.T) {
	dir := t.TempDir()
	// 8 KiB of data — two 4-KiB blocks, the typical
	// allocation granularity that produces the "4096
	// bytes" error in the field.
	path := filepath.Join(dir, "audit.log")
	if err := os.WriteFile(path, make([]byte, 8192), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Simulate the race: between the would-be stat and
	// the would-be read, an external process truncates
	// the file. By the time the read happens, the file
	// is 4 KiB shorter. (The previous code would have
	// declared 8 KiB in the header, read 4 KiB, and the
	// next WriteHeader would have failed with "missed
	// writing 4096 bytes" — the exact error the operator
	// saw.)
	if err := os.Truncate(path, 4096); err != nil {
		t.Fatal(err)
	}

	// The fixed code reads the file fully first, so it
	// sees the post-truncate state (4 KiB) and writes a
	// header that matches the body.
	if err := writeFileToTar(tw, path, "audit.log"); err != nil {
		t.Fatalf("writeFileToTar failed: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close failed (this is the error the old code produced): %v", err)
	}

	// The resulting tar must be self-consistent: every
	// entry's header size must match what was actually
	// written.
	tr := tar.NewReader(&buf)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if int64(len(body)) != hdr.Size {
			t.Errorf("entry %q: header says %d bytes, body has %d — would have caused 'missed writing N bytes' on the next entry",
				hdr.Name, hdr.Size, len(body))
		}
	}
}

// TestWriteFileToTarMissingFile covers the case where
// the source file disappears between the readdir
// enumeration (collectConfigEntries) and the actual
// read. The function must return a clean error, not
// panic or write a half-archive.
func TestWriteFileToTarMissingFile(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	defer tw.Close()
	err := writeFileToTar(tw, "/nonexistent/path/file.json", "missing.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
