package backupstore

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newRunFile writes a syntactically-valid archive filename under dir
// with the given content and returns the absolute path.
func newRunFile(t *testing.T, dir, content string) string {
	t.Helper()
	const name = "webkvm-gatehost-20260101T120000.123456789Z-abc123-config.tar.zst"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, name)
}

// V13-BCK-04 simulated-corruption unit test: a local "remote" copy
// whose bytes differ from the staging source must be detected by
// verifyWrittenPrimary (streamed sha256 comparison) — the job-level
// caller turns that into a failed job — and the corrupt remote copy
// must be purged so it can never be restored.
func TestVerifyWrittenPrimary_PurgesCorruptRemoteCopy(t *testing.T) {
	staging := t.TempDir()
	remoteDir := t.TempDir()
	newRunFile(t, staging, "good archive bytes")
	// Simulated corruption: flip a byte in the uploaded copy.
	remote := newRunFile(t, remoteDir, "good archive bytZ")

	tgt := Target{
		ID:   "t_corrupt",
		Type: TargetLocal,
		Path: remoteDir,
	}
	err := verifyWrittenPrimary(nil, tgt, staging, filepath.Base(remote))
	if err == nil {
		t.Fatal("expected checksum mismatch to be detected")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should mention the checksum mismatch: %v", err)
	}
	if !strings.Contains(err.Error(), "purged") {
		t.Errorf("error should say the corrupt copy was purged: %v", err)
	}
	if _, statErr := os.Stat(remote); !os.IsNotExist(statErr) {
		t.Errorf("corrupt remote copy should have been purged (stat err = %v)", statErr)
	}
}

// V13-BCK-04: an intact remote copy passes verification and is NOT
// touched.
func TestVerifyWrittenPrimary_IntactPasses(t *testing.T) {
	staging := t.TempDir()
	remoteDir := t.TempDir()
	newRunFile(t, staging, "intact bytes")
	remote := newRunFile(t, remoteDir, "intact bytes")

	tgt := Target{ID: "t_ok", Type: TargetLocal, Path: remoteDir}
	if err := verifyWrittenPrimary(nil, tgt, staging, filepath.Base(remote)); err != nil {
		t.Fatalf("expected intact copy to pass: %v", err)
	}
	if _, statErr := os.Stat(remote); statErr != nil {
		t.Fatalf("intact copy should remain in place: %v", statErr)
	}
}

// V13-BCK-04: sha256LocalFile streams (io.Copy) and matches a known
// digest — proving the hashing path used as the expected value.
func TestSha256LocalFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob")
	content := "hello webkvm sha256"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := sha256LocalFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("sha256LocalFile = %s, want %s", got, want)
	}
}
