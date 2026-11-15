package backupstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The managed cluster SSH private key (DATA_DIR/cluster-ssh/**) must never
// travel in a configuration backup, which is uploaded to remote targets. The
// config tar is built from an ALLOW-list of top-level files, so this fails as
// soon as someone allow-lists the directory or any file inside it.
func TestClusterSSHDirNeverInConfigBackup(t *testing.T) {
	dir := t.TempDir()
	sshDir := filepath.Join(dir, "cluster-ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"id_ed25519", "id_ed25519.pub", "id_ed25519.pending", "id_ed25519.pending.pub", "known_hosts", "meta.json", "last_test.json"} {
		if err := os.WriteFile(filepath.Join(sshDir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// A legitimate allow-listed file proves the walk itself works.
	if err := os.WriteFile(filepath.Join(dir, "groups.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	for name := range configAllowList {
		if strings.Contains(name, "cluster-ssh") || strings.HasPrefix(name, "id_") || name == "known_hosts" {
			t.Errorf("configAllowList must not contain %q", name)
		}
	}
	for _, name := range []string{"cluster-ssh", "id_ed25519", "id_ed25519.pub", "known_hosts", "meta.json", "last_test.json", "cluster-ssh/id_ed25519", "cluster-ssh/last_test.json"} {
		if !isExcludedConfigFile(name) {
			t.Errorf("%q is not excluded from config backups", name)
		}
	}

	r := &Runner{dataDir: dir, logger: discardLogger()}
	entries, err := r.collectConfigEntries()
	if err != nil {
		t.Fatal(err)
	}
	var sawGroups bool
	for _, e := range entries {
		if e.arcName == "groups.json" {
			sawGroups = true
		}
		if strings.Contains(e.arcName, "cluster-ssh") || strings.Contains(e.srcPath, "cluster-ssh") ||
			strings.HasPrefix(e.arcName, "id_ed25519") || strings.HasSuffix(e.arcName, "known_hosts") || strings.HasSuffix(e.arcName, "last_test.json") {
			t.Fatalf("config backup would include %q (%s)", e.arcName, e.srcPath)
		}
	}
	if !sawGroups {
		t.Fatal("sanity: groups.json should be collected")
	}
}

// The fencing evidence ledger is machine-local state, never part of the
// allow-listed config backup (same as ha-fencing.json).
func TestFencingStateFilesNotInConfigBackup(t *testing.T) {
	for _, name := range []string{"ha-fencing.json", "ha-fencing-events.json"} {
		if configAllowList[name] {
			t.Errorf("%s must not be allow-listed", name)
		}
		if !isExcludedConfigFile(name) {
			t.Errorf("%s must be excluded from config backups", name)
		}
	}
}
