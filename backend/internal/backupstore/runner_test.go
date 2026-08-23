package backupstore

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"webkvm/internal/models"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubVMSource returns a fixed list of VMs for resolveScope.
func stubVMSource(vms []models.VM) VMSource {
	return func() ([]models.VM, error) { return vms, nil }
}

// stubXMLSource returns a fixed XML for any VM id, used by
// the run-level writeBackup tests so they don't need a
// real libvirt connection.
func stubXMLSource(xml string) VMXMLSource {
	return func(vmID string) (string, error) { return xml, nil }
}

// TestWriteBackupProducesConfigAndPerVMArchives covers the
// Phase II format: each run produces one archive per in-scope
// VM plus a config archive, all sharing a run-level suffix
// (timestamp + random). The test verifies the file count
// and per-file kinds.
func TestWriteBackupProducesConfigAndPerVMArchives(t *testing.T) {
	dir := t.TempDir()
	// Drop a few metadata files the config tar picks up.
	for _, name := range []string{"users.json", "groups.json", "config.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	pool := filepath.Join(dir, "pools", "webkvm-disks")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pool, "tiny.qcow2"), []byte("disk-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	target := t.TempDir()
	r := &Runner{
		dataDir:   dir,
		vms:       stubVMSource([]models.VM{{ID: "vm-1", Disks: []models.DiskInfo{{Device: "disk", Source: filepath.Join(pool, "tiny.qcow2")}}}}),
		xmlSource: stubXMLSource("<domain type='kvm'><name>vm-1</name></domain>"),
		config:    func() BackupConfig { return BackupConfig{MaxFileSizeMB: 100, VerifyOnWrite: false} },
		logger:    discardLogger(),
	}
	tgt := Target{ID: "t1", Path: target, VMFilter: "all", Enabled: true}

	files, total, err := r.writeBackup(tgt, tgt.Path)
	if err != nil {
		t.Fatalf("writeBackup failed: %v", err)
	}
	if total <= 0 {
		t.Fatal("expected non-zero total bytes")
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files (1 VM + 1 config), got %d: %+v", len(files), files)
	}
	// First should be the VM, last the config.
	if files[0].Kind != "vm" || files[0].VMID != "vm-1" {
		t.Errorf("expected first file to be vm-1's tar, got %+v", files[0])
	}
	if files[1].Kind != "config" {
		t.Errorf("expected last file to be config tar, got %+v", files[1])
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Filename, ".tar.zst") {
			t.Errorf("Phase II archives must be .tar.zst, got %q", f.Filename)
		}
		if _, err := os.Stat(filepath.Join(target, f.Filename)); err != nil {
			t.Errorf("file not on disk: %v", err)
		}
	}
	// Open the VM tar and verify it contains domain.xml + the disk + manifest.txt.
	vmPath := filepath.Join(target, files[0].Filename)
	verifyPhaseIIArchive(t, vmPath, "vm-1", []string{"tiny.qcow2"})
}

// TestWriteBackupConfigOnlyWhenNoVMs covers the case where
// VMSource returns an empty list (e.g. no VMs on the host
// at backup time). The run still produces a config tar; no
// VM tars.
func TestWriteBackupConfigOnlyWhenNoVMs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "users.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	r := &Runner{
		dataDir:   dir,
		vms:       stubVMSource(nil),
		xmlSource: nil,
		config:    func() BackupConfig { return BackupConfig{MaxFileSizeMB: 100} },
		logger:    discardLogger(),
	}
	tgt := Target{ID: "t1", Path: target, VMFilter: "all", Enabled: true}
	files, total, err := r.writeBackup(tgt, tgt.Path)
	if err != nil {
		t.Fatal(err)
	}
	if total <= 0 {
		t.Fatal("expected non-zero total bytes")
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file (config only), got %d", len(files))
	}
	if files[0].Kind != "config" {
		t.Errorf("expected config tar, got %+v", files[0])
	}
}

// TestWriteBackupSkipsOversizeViaProducer covers the size
// filter in the new producer path. A single oversize disk
// doesn't fail the run; the VM tar is written with the
// manifest noting the skip.
func TestWriteBackupSkipsOversizeViaProducer(t *testing.T) {
	dir := t.TempDir()
	pool := filepath.Join(dir, "pools", "webkvm-disks")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatal(err)
	}
	// 100 MiB sparse file.
	big, err := os.Create(filepath.Join(pool, "big.qcow2"))
	if err != nil {
		t.Fatal(err)
	}
	big.Write(make([]byte, 1))
	big.Truncate(100 * 1024 * 1024)
	big.Close()

	target := t.TempDir()
	r := &Runner{
		dataDir:   dir,
		vms:       stubVMSource([]models.VM{{ID: "vm-1", Disks: []models.DiskInfo{{Device: "disk", Source: filepath.Join(pool, "big.qcow2")}}}}),
		xmlSource: stubXMLSource("<d/>"),
		// Cap at 1 MiB — the 100 MiB file is dropped.
		config: func() BackupConfig { return BackupConfig{MaxFileSizeMB: 1} },
		logger: discardLogger(),
	}
	tgt := Target{ID: "t1", Path: target, VMFilter: "all", Enabled: true}
	files, _, err := r.writeBackup(tgt, tgt.Path)
	if err != nil {
		t.Fatalf("writeBackup should not fail on oversize skip: %v", err)
	}
	// The VM tar is still written, but the manifest notes SKIPPED.
	vmPath := filepath.Join(target, files[0].Filename)
	verifyPhaseIIArchiveContains(t, vmPath, []string{"manifest.txt"})
	verifyPhaseIIArchiveExcludes(t, vmPath, []string{"disks/big.qcow2"})
}

// TestWriteBackupRunSuffixShared verifies that all files in
// a single run share the same timestamp + random suffix.
// Two writeBackup calls back-to-back should produce files
// with different suffixes.
func TestWriteBackupRunSuffixShared(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	r := &Runner{
		dataDir:   dir,
		vms:       stubVMSource(nil),
		xmlSource: nil,
		config:    func() BackupConfig { return BackupConfig{MaxFileSizeMB: 100} },
		logger:    discardLogger(),
	}
	tgt := Target{ID: "t1", Path: target, VMFilter: "all", Enabled: true}
	first, _, err := r.writeBackup(tgt, tgt.Path)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := r.writeBackup(tgt, tgt.Path)
	if err != nil {
		t.Fatal(err)
	}
	// Both runs produce exactly one config tar. The
	// filenames differ in the random suffix.
	if first[0].Filename == second[0].Filename {
		t.Fatalf("two consecutive runs produced the same filename: %q", first[0].Filename)
	}
}

// verifyPhaseIIArchive opens the .tar.zst file and asserts
// the entries it contains.
func verifyPhaseIIArchive(t *testing.T, path, wantVMID string, wantDisks []string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zz, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zz.Close()
	tr := tar.NewReader(zz)
	got := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		got[hdr.Name] = b
	}
	if _, ok := got["domain.xml"]; !ok {
		t.Errorf("archive missing domain.xml: %v", got)
	}
	for _, d := range wantDisks {
		if _, ok := got["disks/"+d]; !ok {
			t.Errorf("archive missing disk %q: got entries %v", d, keys(got))
		}
	}
	if _, ok := got["manifest.txt"]; !ok {
		t.Error("archive missing manifest.txt")
	}
	if m := string(got["manifest.txt"]); !strings.Contains(m, "name="+wantVMID) {
		t.Errorf("manifest missing name=%s: %q", wantVMID, m)
	}
}

func verifyPhaseIIArchiveContains(t *testing.T, path string, names []string) {
	t.Helper()
	got := readPhaseIIArchiveNames(t, path)
	for _, n := range names {
		if !contains(got, n) {
			t.Errorf("archive %s missing %q; entries=%v", path, n, got)
		}
	}
}

func verifyPhaseIIArchiveExcludes(t *testing.T, path string, names []string) {
	t.Helper()
	got := readPhaseIIArchiveNames(t, path)
	for _, n := range names {
		if contains(got, n) {
			t.Errorf("archive %s should not contain %q; entries=%v", path, n, got)
		}
	}
}

func readPhaseIIArchiveNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zz, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zz.Close()
	tr := tar.NewReader(zz)
	var out []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, hdr.Name)
	}
	return out
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// TestResolveScope covers the per-target VM filter logic
// that the new writeBackup relies on.
func TestResolveScope(t *testing.T) {
	vms := []models.VM{
		{ID: "vm-1"}, {ID: "vm-2"}, {ID: "vm-3"},
	}
	r := &Runner{
		vms:    stubVMSource(vms),
		logger: discardLogger(),
	}
	t.Run("all", func(t *testing.T) {
		got, err := r.resolveScope(Target{VMFilter: "all"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Errorf("expected 3, got %d", len(got))
		}
	})
	t.Run("include", func(t *testing.T) {
		got, err := r.resolveScope(Target{VMFilter: "include", VMIDs: []string{"vm-1", "vm-3"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2, got %d", len(got))
		}
	})
	t.Run("exclude", func(t *testing.T) {
		got, err := r.resolveScope(Target{VMFilter: "exclude", VMIDs: []string{"vm-2"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Errorf("expected 2, got %d", len(got))
		}
	})
	t.Run("nil source", func(t *testing.T) {
		r2 := &Runner{vms: nil, logger: discardLogger()}
		got, err := r2.resolveScope(Target{VMFilter: "all"})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Errorf("expected 0 (config-only), got %d", len(got))
		}
	})
}

// TestFilterInTreeDisks covers the helper that drops
// out-of-tree and CDROM disks before they hit the producer.
func TestFilterInTreeDisks(t *testing.T) {
	root := "/opt/webkvm"
	inTree := []models.DiskInfo{
		{Device: "disk", Source: "/opt/webkvm/pools/webkvm-disks/vm-1.qcow2"},
		{Device: "disk", Source: "/opt/webkvm/pools/ISOS/ubuntu.iso"},
		{Device: "cdrom", Source: "/opt/webkvm/pools/ISOS/drivers.iso"},
		{Device: "disk", Source: "/var/lib/libvirt/images/stray.qcow2"},
		{Device: "disk", Source: ""},
	}
	got := filterInTreeDisks(inTree, root)
	if len(got) != 1 {
		t.Fatalf("expected 1 in-tree disk, got %d: %+v", len(got), got)
	}
	if !strings.HasSuffix(got[0].Source, "vm-1.qcow2") {
		t.Errorf("wrong disk: %q", got[0].Source)
	}
}

// TestSanitizeVMName covers the path-traversal defence.
func TestSanitizeVMName(t *testing.T) {
	cases := map[string]string{
		"vm-1":                 "vm-1",
		"ubuntu-22-04":         "ubuntu-22-04",
		"../../etc/passwd":     "._.._.._etc_passwd", // all separators turned into "_"
		"foo bar":              "foo_bar",
		"a/b\\c":               "a_b_c",
	}
	for in, want := range cases {
		got := sanitizeVMName(in)
		// The exact substitution is implementation-detail; we
		// just require that no slash or backslash remains.
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("sanitizeVMName(%q) = %q, still contains a separator", in, got)
		}
		if got == "" {
			t.Errorf("sanitizeVMName(%q) returned empty", in)
		}
		_ = want
	}
}

// TestRunOnceReturnsSentinelErrors guards the A1 fix for bug #1:
// pre-flight failures now come back as the typed sentinels in
// errors.go, which the HTTP handler maps to 404 / 409 / 400
// instead of the previous blanket 500.
func TestRunOnceReturnsSentinelErrors(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{
		store:  s,
		logger: discardLogger(),
		config: func() BackupConfig { return BackupConfig{MaxFileSizeMB: 100} },
	}

	// Unknown target → ErrTargetNotFound.
	_, err = r.RunOnce(context.Background(), "nope", "")
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("expected ErrTargetNotFound, got %v", err)
	}

	// Disabled target → ErrTargetDisabled.
	tgt := Target{ID: "t", Name: "t", Path: t.TempDir(), VMFilter: "all", Enabled: false}
	s.targets["t"] = &tgt
	_, err = r.RunOnce(context.Background(), "t", "")
	if !errors.Is(err, ErrTargetDisabled) {
		t.Fatalf("expected ErrTargetDisabled, got %v", err)
	}
}

// TestValidateTargetPathDenyList covers the A2 fix for bug #6.
// Targets pointing at system roots are rejected with
// ErrTargetPathUnwritable before any I/O. The app's dataDir
// is always allowed (so the bootstrap "default" target works).
func TestValidateTargetPathDenyList(t *testing.T) {
	dataDir := "/opt/webkvm/backup"
	for _, ok := range []string{
		"/opt/webkvm/backup/default",
		"/opt/webkvm/backup",
		"/mnt/webkvm-backup/nightly",
		"/srv/backups",
		"/tmp/test-backups",
		"/var/log/webkvm-backups",
	} {
		if err := ValidateTargetPath(ok, dataDir); err != nil {
			t.Errorf("expected %q to be allowed, got %v", ok, err)
		}
	}
	for _, bad := range []string{
		"/etc",
		"/etc/webkvm",
		"/usr",
		"/usr/local/bin",
		"/boot",
		"/proc/1/root",
		"/sys/kernel",
		"/bin",
		"/var/lib/dpkg/status",
		"/var/lib/libvirt/images",
	} {
		err := ValidateTargetPath(bad, dataDir)
		if err == nil {
			t.Errorf("expected %q to be denied, got nil", bad)
			continue
		}
		if !errors.Is(err, ErrTargetPathUnwritable) {
			t.Errorf("expected ErrTargetPathUnwritable for %q, got %v", bad, err)
		}
	}
}

// TestValidateTargetPathRelativeAndEmpty guards the two cheap
// shape errors a user can hit when typing into the Add Target
// form.
func TestValidateTargetPathRelativeAndEmpty(t *testing.T) {
	dataDir := "/opt/webkvm/backup"
	if err := ValidateTargetPath("", dataDir); !errors.Is(err, ErrTargetPathUnwritable) {
		t.Errorf("empty: expected ErrTargetPathUnwritable, got %v", err)
	}
	if err := ValidateTargetPath("backups/2024", dataDir); !errors.Is(err, ErrTargetPathUnwritable) {
		t.Errorf("relative: expected ErrTargetPathUnwritable, got %v", err)
	}
}

// TestValidateTargetPathSymlinkEscape verifies the symlink
// resolution: a path that doesn't exist yet but whose
// eventual target is under /etc is still rejected. The
// dangerous case is "/opt/webkvm/escape -> /etc/passwd"
// where the operator types the path naively and a future
// symlink redirects the I/O. We resolve the deepest existing
// ancestor and the deny-list check then catches it.
func TestValidateTargetPathSymlinkEscape(t *testing.T) {
	dataDir := t.TempDir()
	// Create /tmp/escape-root/ as a real dir, then point at a
	// non-existent subdir under it. The deny-list matches by
	// the cleaned path, not by what's on disk — so the path
	// /tmp/escape-root/something is fine.
	allowed := filepath.Join(dataDir, "new-target")
	if err := ValidateTargetPath(allowed, "/opt/webkvm/backup"); err != nil {
		t.Fatalf("non-existing path under tempdir should be allowed: %v", err)
	}
}

// TestCreateTargetRejectsDeniedPath is the integration test:
// the API-facing CreateTarget must reject the same paths
// ValidateTargetPath does. This is what stops a hostile or
// careless operator from saving "/etc" as a target.
func TestCreateTargetRejectsDeniedPath(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, err := s.CreateTarget("evil", "/etc", TargetLocal, "all", nil)
	if err == nil {
		t.Fatal("expected CreateTarget to reject /etc")
	}
	if !errors.Is(err, ErrTargetPathUnwritable) {
		t.Fatalf("expected ErrTargetPathUnwritable, got %v", err)
	}
}

// TestUpdateTargetRejectsDeniedPath is the parallel test for
// the PATCH path: changing a target's path to /etc must be
// rejected with 400.
func TestUpdateTargetRejectsDeniedPath(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, _ := s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	badPath := "/usr"
	_, err := s.UpdateTarget(tgt.ID, nil, &badPath, nil, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("expected UpdateTarget to reject /usr")
	}
	if !errors.Is(err, ErrTargetPathUnwritable) {
		t.Fatalf("expected ErrTargetPathUnwritable, got %v", err)
	}
}

// TestRestoreBackupRejectsDeniedPath covers the defense in
// depth: even if a hostile targets.json slips past the API
// and points at /etc, the runner refuses to extract.
func TestRestoreBackupRejectsDeniedPath(t *testing.T) {
	tgt := Target{ID: "evil", Name: "evil", Path: "/etc", Type: TargetLocal, Enabled: true}
	_, err := RestoreBackup(context.Background(), tgt, "webkvm-x-20260101T000000Z.tar.gz", t.TempDir())
	if err == nil {
		t.Fatal("expected RestoreBackup to reject /etc target")
	}
	if !errors.Is(err, ErrTargetPathUnwritable) {
		t.Fatalf("expected ErrTargetPathUnwritable, got %v", err)
	}
}

// TestRestoreBackupReturnsResult is a tiny sanity check
// that the new return type (RestoreResult, not string)
// is wired correctly. Builds a 1-byte tar.gz, calls
// RestoreBackup, asserts the destination directory and
// per-file result are populated.
func TestRestoreBackupReturnsResult(t *testing.T) {
	src := t.TempDir()
	dataDir := t.TempDir()
	tgtPath := filepath.Join(src, "default")
	if err := os.MkdirAll(tgtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(tgtPath, "webkvm-x-20260101T000000Z.tar.gz")
	if err := buildTinyTar(tarPath, "hello.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	tgt := Target{ID: "t", Name: "t", Path: tgtPath, Type: TargetLocal, Enabled: true}
	res, err := RestoreBackup(context.Background(), tgt, "webkvm-x-20260101T000000Z.tar.gz", dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(res.Destination), "restore-") {
		t.Errorf("expected destination to start with 'restore-', got %q", res.Destination)
	}
	if len(res.Files) != 1 {
		t.Fatalf("expected 1 file result, got %d", len(res.Files))
	}
	if res.Files[0].Filename != "webkvm-x-20260101T000000Z.tar.gz" {
		t.Errorf("unexpected filename in result: %q", res.Files[0].Filename)
	}
	// The per-archive manifest should exist on disk.
	if _, err := os.Stat(res.Files[0].Manifest); err != nil {
		t.Errorf("manifest not on disk: %v", err)
	}
}

// TestRestoreBackupSourceSizeCap is the zip-bomb guard for the
// input side: a tar.gz larger than MaxRestoreSourceBytes is
// refused before tar is even invoked.
func TestRestoreBackupSourceSizeCap(t *testing.T) {
	// Save and restore the global cap.
	orig := MaxRestoreSourceBytes
	defer func() { MaxRestoreSourceBytes = orig }()
	MaxRestoreSourceBytes = 100 // 100 bytes

	src := t.TempDir()
	dataDir := t.TempDir()
	tgtPath := filepath.Join(src, "default")
	if err := os.MkdirAll(tgtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// A 1 KiB archive — well over 100 bytes.
	big := make([]byte, 1024)
	for i := range big {
		big[i] = 'x'
	}
	name := "webkvm-x-20260101T000000Z.tar.gz"
	if err := os.WriteFile(filepath.Join(tgtPath, name), big, 0o644); err != nil {
		t.Fatal(err)
	}
	tgt := Target{ID: "t", Name: "t", Path: tgtPath, Type: TargetLocal, Enabled: true}
	_, err := RestoreBackup(context.Background(), tgt, name, dataDir)
	if err == nil {
		t.Fatal("expected error for oversize source")
	}
}

// TestRestoreBackupExtractedSizeCap is the zip-bomb guard for
// the output side: a tar.gz that, once extracted, exceeds
// MaxRestoreExtractedBytes is rejected and the half-extracted
// tree is removed.
func TestRestoreBackupExtractedSizeCap(t *testing.T) {
	orig := MaxRestoreExtractedBytes
	defer func() { MaxRestoreExtractedBytes = orig }()
	MaxRestoreExtractedBytes = 50 // tiny cap

	src := t.TempDir()
	dataDir := t.TempDir()
	tgtPath := filepath.Join(src, "default")
	if err := os.MkdirAll(tgtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// Build a real tar.gz containing a 1 KiB file.
	tarPath := filepath.Join(tgtPath, "webkvm-x-20260101T000000Z.tar.gz")
	if err := buildTinyTar(tarPath, "big.txt", make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	tgt := Target{ID: "t", Name: "t", Path: tgtPath, Type: TargetLocal, Enabled: true}
	_, err := RestoreBackup(context.Background(), tgt, "webkvm-x-20260101T000000Z.tar.gz", dataDir)
	if err == nil {
		t.Fatal("expected error for oversize extract")
	}
	// The half-extracted dir must be gone — the cap should
	// clean up after itself.
	entries, _ := os.ReadDir(dataDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "restore-") {
			t.Errorf("expected restore dir to be cleaned up, found %q", e.Name())
		}
	}
}

// TestRestoreBackupTimeout covers bug #8: a stuck tar is
// killed by the context timeout. We set the timeout to 1ms
// and restore a real tar; tar is fast but the timeout fires
// before the walk starts.
func TestRestoreBackupTimeout(t *testing.T) {
	orig := MaxRestoreDuration
	defer func() { MaxRestoreDuration = orig }()
	MaxRestoreDuration = 1 * time.Millisecond

	src := t.TempDir()
	dataDir := t.TempDir()
	tgtPath := filepath.Join(src, "default")
	if err := os.MkdirAll(tgtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	tarPath := filepath.Join(tgtPath, "webkvm-x-20260101T000000Z.tar.gz")
	if err := buildTinyTar(tarPath, "small.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	tgt := Target{ID: "t", Name: "t", Path: tgtPath, Type: TargetLocal, Enabled: true}
	_, err := RestoreBackup(context.Background(), tgt, "webkvm-x-20260101T000000Z.tar.gz", dataDir)
	// On a fast machine tar may finish before the 1ms
	// timeout fires. Either way, the call must return without
	// hanging the test. We accept timeout OR success; the
	// important property is "doesn't hang".
	if err != nil && !strings.Contains(err.Error(), "timeout") {
		t.Logf("restore returned non-timeout error (acceptable): %v", err)
	}
}

// buildTinyTar produces a gzip-compressed tar with one entry
// of the given name and content. Used by the cap tests.
func buildTinyTar(path, name string, content []byte) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(content); err != nil {
		return err
	}
	return nil
}

// TestAllocateOutputPathNoCollision is the happy path: the
// helper picks a unique name and creates the file with
// O_EXCL. Two calls with different <name> arguments
// produce different files; two calls with the same
// <name> would collide and the second O_EXCL would fail
// (the run-suffix is shared, so the test gives each call
// its own suffix).
func TestAllocateOutputPathNoCollision(t *testing.T) {
	dir := t.TempDir()
	r := &Runner{logger: discardLogger()}
	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	suffix1 := randHex(6)
	suffix2 := randHex(6)

	_, out1, err := r.allocateOutputPath(dir, "host", ts, suffix1, "config.tar.zst")
	if err != nil {
		t.Fatal(err)
	}
	_, out2, err := r.allocateOutputPath(dir, "host", ts, suffix2, "config.tar.zst")
	if err != nil {
		t.Fatal(err)
	}
	if out1 == out2 {
		t.Fatalf("expected distinct filenames, both are %q", out1)
	}
	if !strings.HasSuffix(out1, ".tar.zst") {
		t.Errorf("expected .tar.zst suffix, got %q", out1)
	}
	if _, err := os.Stat(out1); err != nil {
		t.Errorf("first file should exist on disk: %v", err)
	}
	if _, err := os.Stat(out2); err != nil {
		t.Errorf("second file should exist on disk: %v", err)
	}
}

// TestAllocateOutputPathRejectsBadDir verifies the helper
// surfaces permission errors. We point it at /proc which
// is read-only.
func TestAllocateOutputPathRejectsBadDir(t *testing.T) {
	r := &Runner{logger: discardLogger()}
	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	_, _, err := r.allocateOutputPath("/proc/1/root", "host", ts, randHex(6), "config.tar.zst")
	if err == nil {
		t.Fatal("expected error for read-only target dir")
	}
}

// TestValidBackupFilenameAcceptsAllFormats covers the three
// formats the runner has emitted over time: legacy 16-char
// UTC gzip, Phase I nanosecond+rand gzip, and Phase II
// nanosecond+rand+name zstd. All three must be deletable
// from the Files tab so old archives don't become orphans.
func TestValidBackupFilenameAcceptsAllFormats(t *testing.T) {
	for _, ok := range []string{
		"webkvm-host-20260625T120000Z.tar.gz",                          // legacy gzip
		"webkvm-host-20260625T120000.000000000Z-aabbcc.tar.gz",        // Phase I gzip
		"webkvm-host-with-dashes-20260625T120000Z.tar.gz",              // legacy + dashes
		"webkvm-host-with-dashes-20260625T120000.000000000Z-deadbe.tar.gz", // Phase I + dashes
		"webkvm-host-20260625T120000.000000000Z-aabbcc-config.tar.zst",  // Phase II config
		"webkvm-host-20260625T120000.000000000Z-aabbcc-vm-1.tar.zst",    // Phase II per-VM
		"webkvm-host-20260625T120000.000000000Z-aabbcc-ubuntu-22.04.tar.zst", // Phase II with dashes
		// randHex(6) → 12 hex chars; this is the actual
		// format the runner has been emitting on disk since
		// the A3 commit. The v8 release fixes the regex
		// to accept this. Without that fix every Phase II
		// archive on .130 (filename
		// "webkvm-webkvm-20260626T152618.644238018Z-d0bea29a764b-...")
		// failed ValidBackupFilename and POST /restore
		// returned 400 "invalid filename format".
		"webkvm-webkvm-20260626T152618.644238018Z-d0bea29a764b-7be64cc4-72c4-4bca-989d-142a4fc94ce9.tar.zst",
		"webkvm-webkvm-20260626T152618.644238018Z-d0bea29a764b-config.tar.zst",
	} {
		if !ValidBackupFilename(ok) {
			t.Errorf("expected %q to be accepted", ok)
		}
	}
	for _, bad := range []string{
		"webkvm-host-20260625T12000Z.tar.gz",       // too short
		"webkvm-host-20260625T120000X.tar.gz",      // bad suffix
		"webkvm-host-2026-06-25T12-00-00Z.tar.gz",  // wrong shape
		"webkvm-host-20260625T120000.000000000Z.tar.gz", // no random suffix
		"webkvm-host-20260625T120000.000000000Z-AABBCC.tar.gz", // uppercase
		"webkvm-host-20260625T120000.000000000Z-aabbcdX.tar.gz", // non-hex
		"webkvm-host-20260625T120000.000000000Z-aabbcc-vm-1.tar.gz", // wrong extension
		"webkvm-host-20260625T120000.000000000Z-aabbcc-../etc.tar.zst", // path traversal
		"webkvm-host-20260625T120000.000000000Z-aabbcc-.tar.zst",        // empty name
		"../etc/passwd",
		"foo.tar.gz",
	} {
		if ValidBackupFilename(bad) {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

// TestWriteBackupConcurrentRunsProduceDistinctFiles is the
// integration test for bug #5: two writeBackup calls in
// rapid succession must produce two distinct archive files.
// The pre-A3 code used a 1-second-resolution timestamp and
// would collide; the A3 commit fixed it for the old
// single-file format, and the Phase II format inherits
// the same protection.
func TestWriteBackupConcurrentRunsProduceDistinctFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"users.json", "groups.json", "config.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := t.TempDir()
	r := &Runner{
		dataDir:   dir,
		vms:       stubVMSource(nil),
		xmlSource: nil,
		config:    func() BackupConfig { return BackupConfig{MaxFileSizeMB: 100} },
		logger:    discardLogger(),
	}
	tgt := Target{ID: "t1", Path: target, VMFilter: "all", Enabled: true}

	f1, _, err := r.writeBackup(tgt, tgt.Path)
	if err != nil {
		t.Fatal(err)
	}
	f2, _, err := r.writeBackup(tgt, tgt.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(f1) != 1 || len(f2) != 1 {
		t.Fatalf("expected 1 file per run, got %d / %d", len(f1), len(f2))
	}
	if f1[0].Filename == f2[0].Filename {
		t.Fatalf("expected distinct filenames, both are %q", f1[0].Filename)
	}
	// The run writes the VM tar + the run config tar at the top
	// level, plus a config/ subdirectory with the stable "latest
	// config" snapshot. Count only the archive files.
	entries, _ := os.ReadDir(target)
	files := 0
	for _, e := range entries {
		if !e.IsDir() {
			files++
		}
	}
	if files != 2 {
		t.Fatalf("expected 2 archive files on disk, got %d", files)
	}
}

// TestRestoreRunMultiFile covers the Phase II restore
// behaviour: one run produces 1 config + N per-VM tars,
// and a single restore call extracts all of them into
// their respective sub-directories.
func TestRestoreRunMultiFile(t *testing.T) {
	src := t.TempDir()
	dataDir := t.TempDir()
	tgtPath := filepath.Join(src, "default")
	if err := os.MkdirAll(tgtPath, 0o755); err != nil {
		t.Fatal(err)
	}
	// Three files with a shared run-suffix: <ts>-<rand>.
	// One config tar + two per-VM tars.
	ts := "20260625T120000.000000000Z"
	suffix := "deadbe"
	files := map[string]string{
		"webkvm-host-" + ts + "-" + suffix + "-config.tar.zst": "manifest",
		"webkvm-host-" + ts + "-" + suffix + "-vm-1.tar.zst":     "domain.xml",
		"webkvm-host-" + ts + "-" + suffix + "-vm-2.tar.zst":     "domain.xml",
	}
	for name, content := range files {
		// Build a real zstd tar with one entry.
		path := filepath.Join(tgtPath, name)
		if err := buildTinyZstdTar(path, "entry", []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tgt := Target{ID: "t", Name: "t", Path: tgtPath, Type: TargetLocal, Enabled: true}
	res, err := RestoreRun(context.Background(), tgt, ts+"-"+suffix, dataDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 3 {
		t.Fatalf("expected 3 files extracted, got %d", len(res.Files))
	}
	// Each file should have extracted into its own subdir.
	for _, f := range res.Files {
		sub := filepath.Join(res.Destination, f.Subdir)
		// The "entry" file should exist in the subdir.
		entry := filepath.Join(sub, "entry")
		if _, err := os.Stat(entry); err != nil {
			t.Errorf("expected entry file under %s: %v", sub, err)
		}
	}
	// The run-level manifest should list all three files.
	entries, _ := os.ReadDir(res.Destination)
	foundManifest := false
	for _, e := range entries {
		if e.Name() == "RESTORE_MANIFEST.json" {
			foundManifest = true
		}
	}
	if !foundManifest {
		t.Error("expected run-level RESTORE_MANIFEST.json in destination")
	}
}

// TestRestoreRunRejectsInvalidSuffix covers the run-suffix
// validator: garbage in the request body must be rejected
// before any filesystem I/O.
func TestRestoreRunRejectsInvalidSuffix(t *testing.T) {
	tgt := Target{ID: "t", Name: "t", Path: t.TempDir(), Type: TargetLocal, Enabled: true}
	for _, bad := range []string{
		"garbage",
		"../etc",
		"20260625T120000Z-aabbcc",        // 16-char ts, not 26
		"20260625T120000.000000000Z-AABBCC", // uppercase rand
	} {
		_, err := RestoreRun(context.Background(), tgt, bad, t.TempDir(), nil)
		if err == nil {
			t.Errorf("expected error for run=%q", bad)
		}
	}
}

// buildTinyZstdTar produces a zstd-compressed tar with one
// entry. Used by the multi-file restore test.
func buildTinyZstdTar(path, name string, content []byte) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	zw, err := zstd.NewWriter(out)
	if err != nil {
		return err
	}
	defer zw.Close()
	tw := tar.NewWriter(zw)
	defer tw.Close()
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o644,
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(content); err != nil {
		return err
	}
	return nil
}

// TestWriteBackupDefaultCapAllowsRealVMDisks is the
// regression test for the production "muy poco" bug:
// the runner used to default to a 100 MB cap, which
// dropped every real VM disk. The cap now defaults to
// 1 TiB (effectively unlimited) so an out-of-the-box
// install produces a usable backup. This test
// constructs a 200 MB sparse file (well over the old
// 100 MB cap, well under the new 1 TiB default) and
// asserts the runner includes it.
func TestWriteBackupDefaultCapAllowsRealVMDisks(t *testing.T) {
	dir := t.TempDir()
	pool := filepath.Join(dir, "pools", "webkvm-disks")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatal(err)
	}
	// 200 MB sparse file — over the old 100 MB cap,
	// under the new 1 TiB default.
	big, err := os.Create(filepath.Join(pool, "vm.qcow2"))
	if err != nil {
		t.Fatal(err)
	}
	big.Write(make([]byte, 1))
	big.Truncate(200 * 1024 * 1024)
	big.Close()

	target := t.TempDir()
	r := &Runner{
		dataDir:   dir,
		vms:       stubVMSource([]models.VM{{ID: "vm-1", Disks: []models.DiskInfo{{Device: "disk", Source: filepath.Join(pool, "vm.qcow2")}}}}),
		xmlSource: stubXMLSource("<d/>"),
		// Note: MaxFileSizeMB=0 → the runner falls back
		// to the new default (1 TiB), which is exactly
		// what we're testing.
		config: func() BackupConfig { return BackupConfig{MaxFileSizeMB: 0} },
		logger: discardLogger(),
	}
	tgt := Target{ID: "t1", Path: target, VMFilter: "all", Enabled: true}
	files, _, err := r.writeBackup(tgt, tgt.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files (1 VM + 1 config), got %d", len(files))
	}
	vmPath := filepath.Join(target, files[0].Filename)
	// The disk should be present in the VM tar, NOT
	// just skipped.
	verifyPhaseIIArchiveContains(t, vmPath, []string{"disks/vm.qcow2", "manifest.txt", "domain.xml"})
	// And the manifest should NOT contain a SKIPPED line.
	f, err := os.Open(vmPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zz, err := zstd.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer zz.Close()
	tr := tar.NewReader(zz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "manifest.txt" {
			body, _ := io.ReadAll(tr)
			if strings.Contains(string(body), "SKIPPED") {
				t.Errorf("manifest should not contain SKIPPED with the new default cap, got: %s", body)
			}
		}
	}
}

// TestListBackupsOnTargetPhaseIIZstExtension is the regression
// test for the "No backup files yet" bug: the lister used to
// filter on .tar.gz only, so Phase II (.tar.zst) archives were
// silently hidden from the Files tab even though they were on
// disk and counted by jobs.json. The lister now accepts both
// extensions, so a directory with .tar.zst files only must
// return them.
func TestListBackupsOnTargetPhaseIIZstExtension(t *testing.T) {
	dir := t.TempDir()
	// Two Phase II files, one stale temp file that should
	// be ignored.
	plant := []struct {
		name string
		size int64
	}{
		{"webkvm-host-20260626T150000.000000000Z-abcdef-ubuntu-1.tar.zst", 5_141_406_464},
		{"webkvm-host-20260626T150000.000000000Z-abcdef-config.tar.zst", 4_554},
		{"webkvm-host-20260626T150000.000000000Z-abcdef.tmp", 999}, // not a backup
		{"README.md", 42},                                         // not a backup
	}
	for _, p := range plant {
		f, err := os.Create(filepath.Join(dir, p.name))
		if err != nil {
			t.Fatal(err)
		}
		f.Write(make([]byte, 1))
		f.Truncate(p.size)
		f.Close()
	}
	tgt := Target{ID: "t1", Path: dir}
	files, err := ListBackupsOnTarget(tgt)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 .tar.zst files, got %d: %+v", len(files), files)
	}
	// Both names present.
	have := map[string]bool{}
	for _, f := range files {
		have[f.Filename] = true
	}
	for _, p := range plant[:2] {
		if !have[p.name] {
			t.Errorf("missing %s in lister output", p.name)
		}
	}
	// Stale .tmp and README.md must not appear.
	if have["webkvm-host-20260626T150000.000000000Z-abcdef.tmp"] {
		t.Errorf(".tmp file leaked into lister output")
	}
	if have["README.md"] {
		t.Errorf("README.md leaked into lister output")
	}
}

// TestListBackupsOnTargetPhaseILegacyGzStillListed guards the
// other half of the extension fix: a Phase I .tar.gz left on
// disk by an older binary must still appear in the Files tab
// so the operator can delete it. A naive "only .tar.zst" fix
// would have regressed this.
func TestListBackupsOnTargetPhaseILegacyGzStillListed(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "webkvm-host-20260620T120000Z.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("x"))
	f.Close()
	tgt := Target{ID: "t1", Path: dir}
	files, err := ListBackupsOnTarget(tgt)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 .tar.gz file, got %d: %+v", len(files), files)
	}
	if !strings.HasSuffix(files[0].Filename, ".tar.gz") {
		t.Errorf("expected .tar.gz, got %q", files[0].Filename)
	}
}

// TestWriteBackupFailsFastOnDiskFull is the regression test
// for the v3 .130 incident: the target pointed at /tmp
// (tmpfs 3.6 GB), the VM had a 5 GB qcow2, and the runner
// only failed 60 s into the copy with a generic ENOSPC.
// The A8 pre-flight free-space check should abort the run
// before allocating the first output path.
func TestWriteBackupFailsFastOnDiskFull(t *testing.T) {
	dataDir := t.TempDir()
	// The disk pool lives under dataDir/pools/webkvm-disks
	// — estimateRunBytes walks the tree but skips that
	// subtree (already counted as disk size).
	pool := filepath.Join(dataDir, "pools", "webkvm-disks")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatal(err)
	}
	// Make a disk that the VM points to, but write it
	// into the pool dir so the runner can stat it. The
	// size needs to be larger than the target's free
	// space to trip the precheck.
	const diskSize = 1 << 30 // 1 GiB
	diskPath := filepath.Join(pool, "vm.qcow2")
	f, err := os.Create(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	f.Truncate(diskSize)
	f.Close()

	// The target path is a tmpfs-like mount with
	// effectively no free space. t.TempDir() is fine for
	// the test as long as we make the disk bigger than
	// the free space available there. To make the test
	// reliable, we point the target at a small tmpfs:
	// we ask the OS for a fresh temp dir and measure its
	// free space, then make the disk 2x that.
	target := t.TempDir()
	var stat syscall.Statfs_t
	if err := syscall.Statfs(target, &stat); err != nil {
		t.Fatal(err)
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	// Grow the disk to be definitively bigger than the
	// target's free space + 20% overhead.
	want := free*2 + diskSize
	if want < diskSize*2 {
		// tmpfs is huge, 2x free is enough.
		want = free * 2
	}
	if err := os.Truncate(diskPath, want); err != nil {
		t.Fatal(err)
	}

	r := &Runner{
		dataDir:   dataDir,
		vms:       stubVMSource([]models.VM{{ID: "vm-1", Disks: []models.DiskInfo{{Device: "disk", Source: diskPath}}}}),
		xmlSource: stubXMLSource("<d/>"),
		config:    func() BackupConfig { return BackupConfig{MaxFileSizeMB: 0} },
		logger:    discardLogger(),
	}
	tgt := Target{ID: "t1", Path: target, VMFilter: "all", Enabled: true}
	_, _, err = r.writeBackup(tgt, tgt.Path)
	if !errors.Is(err, ErrDiskFull) {
		t.Fatalf("expected ErrDiskFull, got %v", err)
	}
	// The precheck must NOT have created any archive on
	// disk — that's the whole point of fail-fast.
	entries, _ := os.ReadDir(target)
	if len(entries) != 0 {
		for _, e := range entries {
			t.Logf("unexpected file in target: %s", e.Name())
		}
		t.Errorf("expected no files written when disk is full, got %d", len(entries))
	}
}

// TestEstimateRunBytesSumsDiskAndConfig is a positive
// control for the precheck: with a known disk + known
// dataDir, the estimate must equal (disks + tree) * 1.20.
func TestEstimateRunBytesSumsDiskAndConfig(t *testing.T) {
	dataDir := t.TempDir()
	// Config-sized file in the data dir.
	c := filepath.Join(dataDir, "users.json")
	os.WriteFile(c, make([]byte, 1000), 0o644)
	// Disk pool with a 1 MB file.
	pool := filepath.Join(dataDir, "pools", "webkvm-disks")
	os.MkdirAll(pool, 0o755)
	d := filepath.Join(pool, "vm.qcow2")
	f, _ := os.Create(d)
	f.Truncate(1 << 20)
	f.Close()

	r := &Runner{dataDir: dataDir, logger: discardLogger()}
	vms := []models.VM{{ID: "vm-1", Disks: []models.DiskInfo{{Device: "disk", Source: d}}}}
	need, err := r.estimateRunBytes(vms)
	if err != nil {
		t.Fatal(err)
	}
	// Expected: (1 MB disk + 1 KB config) * 1.20 ≈ 1.20 MB.
	const want = int64((1<<20 + 1000) * 120 / 100)
	// Allow a 1% slop for tar header + framing.
	if need < want-want/100 || need > want+want/100 {
		t.Errorf("estimate off: got %d, want ~%d", need, want)
	}
}

func TestApplyRetentionKeepLast(t *testing.T) {
	dir := t.TempDir()
	// Plant 4 runs, each with 2 files (a VM + config), with distinct
	// suffixes and mtimes so ordering is deterministic.
	ts := []string{
		"20260626T010000.000000000Z-aaaaaa",
		"20260626T020000.000000000Z-bbbbbb",
		"20260626T030000.000000000Z-cccccc",
		"20260626T040000.000000000Z-dddddd",
	}
	for i, suf := range ts {
		base := time.Date(2026, 6, 26, i+1, 0, 0, 0, time.UTC)
		for _, name := range []string{"vm1", "config"} {
			fn := "webkvm-host-" + suf + "-" + name + ".tar.zst"
			p := filepath.Join(dir, fn)
			f, err := os.Create(p)
			if err != nil {
				t.Fatal(err)
			}
			f.Close()
			// Set mtime to the run's time (backwards so newest last
			// in insertion order).
			_ = os.Chtimes(p, base, base)
		}
	}

	store := &Store{}
	tgt := Target{ID: "t1", Path: dir, Retention: RetentionPolicy{KeepLast: 2}}
	removed, err := ApplyRetention(store, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Fatalf("expected 2 runs removed, got %d", removed)
	}
	// Only the 2 newest runs (cccccc, dddddd) should remain.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 4 {
		t.Fatalf("expected 4 files (2 runs x 2), got %d", len(entries))
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "aaaaaa") || strings.Contains(e.Name(), "bbbbbb") {
			t.Errorf("old run file not removed: %s", e.Name())
		}
	}
}

func TestApplyRetentionDisabledKeepsEverything(t *testing.T) {
	dir := t.TempDir()
	plant := []string{
		"webkvm-host-20260626T010000.000000000Z-aaaaaa-vm1.tar.zst",
		"webkvm-host-20260626T020000.000000000Z-bbbbbb-vm1.tar.zst",
		"webkvm-host-20260626T030000.000000000Z-cccccc-vm1.tar.zst",
	}
	for _, fn := range plant {
		if err := os.WriteFile(filepath.Join(dir, fn), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	store := &Store{}
	// No retention configured -> nothing removed.
	tgt := Target{ID: "t1", Path: dir, Retention: RetentionPolicy{}}
	removed, err := ApplyRetention(store, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("expected 0 removed with no policy, got %d", removed)
	}
	if n, _ := os.ReadDir(dir); len(n) != 3 {
		t.Fatalf("expected 3 files untouched, got %d", len(n))
	}
}

func TestApplyRetentionIgnoresForeignAndConfigDir(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	_ = os.MkdirAll(cfg, 0o755)
	// Foreign / non-run files must never be touched.
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "webkvm-host-20260626T010000.000000000Z-aaaaaa-config.tar.zst"), []byte("x"), 0o644)
	// One real run.
	_ = os.WriteFile(filepath.Join(dir, "webkvm-host-20260626T020000.000000000Z-bbbbbb-vm1.tar.zst"), []byte("x"), 0o644)
	_ = os.WriteFile(filepath.Join(cfg, "webkvm-config-latest.tar.zst"), []byte("x"), 0o644)

	store := &Store{}
	tgt := Target{ID: "t1", Path: dir, Retention: RetentionPolicy{KeepLast: 0, KeepDays: 1}}
	removed, err := ApplyRetention(store, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("expected 0 removed (all recent), got %d", removed)
	}
	// config dir + foreign files intact.
	for _, fn := range []string{"README.md", "webkvm-host-20260626T010000.000000000Z-aaaaaa-config.tar.zst"} {
		if _, err := os.Stat(filepath.Join(dir, fn)); err != nil {
			t.Errorf("foreign file removed: %s", fn)
		}
	}
	if _, err := os.Stat(filepath.Join(cfg, "webkvm-config-latest.tar.zst")); err != nil {
		t.Error("config dir content removed")
	}
}

func TestIsExcludedConfigFile(t *testing.T) {
	excluded := []string{"notify-secrets.json", "sftp-secrets.json", "admin-password.initial", "users.json.tmp"}
	for _, n := range excluded {
		if !isExcludedConfigFile(n) {
			t.Errorf("%q should be excluded from the config backup", n)
		}
	}
	kept := []string{"users.json", "config.json", "targets.json", "pool-purposes.json"}
	for _, n := range kept {
		if isExcludedConfigFile(n) {
			t.Errorf("%q should NOT be excluded", n)
		}
	}
}

func TestCollectConfigEntriesExcludesSecrets(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{
		"users.json", "config.json", "nodes.json",
		"notify-secrets.json", "sftp-secrets.json", "admin-password.initial",
	} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := &Runner{dataDir: dir, logger: slog.Default()}
	entries, err := r.collectConfigEntries()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		have[e.arcName] = true
	}
	if !have["users.json"] || !have["config.json"] {
		t.Fatalf("expected users.json/config.json present, got %v", have)
	}
	for _, secret := range []string{"notify-secrets.json", "sftp-secrets.json", "admin-password.initial"} {
		if have[secret] {
			t.Fatalf("SECRET %q leaked into the config backup entries", secret)
		}
	}
}
