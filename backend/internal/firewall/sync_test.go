package firewall

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProfileFile(t *testing.T, path string, version int64, p ClusterProfile) string {
	t.Helper()
	hash, err := ProfileSHA256(p)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(ClusterProfileFile{Version: version, SHA256: hash, Profile: p}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return hash
}

func okHooks() (SyncHooks, *int, *int) {
	checks, applies := 0, 0
	return SyncHooks{
		Check:        func(string) error { checks++; return nil },
		Apply:        func(string) error { applies++; return nil },
		TablePresent: func() bool { return false },
	}, &checks, &applies
}

func TestSyncAppliesVersionAndRecordsState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-firewall.json")
	p := testClusterProfile()
	p.Enabled = true
	hash := writeProfileFile(t, path, 3, p)

	hooks, checks, applies := okHooks()
	state, err := SyncClusterProfile(path, false, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if *checks != 1 || *applies != 1 {
		t.Fatalf("checks=%d applies=%d, want 1/1", *checks, *applies)
	}
	if state.Version != 3 || state.SHA256 != hash || state.LastError != "" || state.AppliedAt.IsZero() {
		t.Fatalf("state = %+v", state)
	}
	loaded := LoadSyncState(SyncStatePath(path))
	if loaded.Version != 3 || loaded.SHA256 != hash {
		t.Fatalf("persisted state = %+v", loaded)
	}
}

func TestSyncDisabledProfileIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-firewall.json")
	p := testClusterProfile() // Enabled=false
	writeProfileFile(t, path, 1, p)
	hooks, checks, applies := okHooks()
	state, err := SyncClusterProfile(path, false, hooks)
	if err != nil {
		t.Fatal(err)
	}
	if *checks != 0 || *applies != 0 {
		t.Fatalf("disabled profile reached nft: checks=%d applies=%d", *checks, *applies)
	}
	if state.Version != 0 {
		t.Fatalf("disabled profile must not be recorded as applied: %+v", state)
	}
}

func TestSyncRejectsOlderVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-firewall.json")
	p := testClusterProfile()
	p.Enabled = true
	writeProfileFile(t, path, 4, p)
	hooks, _, applies := okHooks()
	if _, err := SyncClusterProfile(path, false, hooks); err != nil {
		t.Fatal(err)
	}
	writeProfileFile(t, path, 3, p)
	if _, err := SyncClusterProfile(path, false, hooks); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("expected an older-version error, got %v", err)
	}
	if *applies != 1 {
		t.Fatalf("apply calls = %d, want only the first", *applies)
	}
	// --force-rollback accepts it.
	if _, err := SyncClusterProfile(path, true, hooks); err != nil {
		t.Fatalf("force rollback failed: %v", err)
	}
	if *applies != 2 {
		t.Fatalf("apply calls = %d, want 2 after rollback", *applies)
	}
}

func TestSyncRejectsSameVersionDifferentHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-firewall.json")
	p := testClusterProfile()
	p.Enabled = true
	writeProfileFile(t, path, 2, p)
	hooks, _, _ := okHooks()
	if _, err := SyncClusterProfile(path, false, hooks); err != nil {
		t.Fatal(err)
	}
	p.WebPort = 8443
	writeProfileFile(t, path, 2, p)
	if _, err := SyncClusterProfile(path, false, hooks); err == nil {
		t.Fatal("same version with a different hash must be rejected")
	}
}

func TestSyncCheckFailureKeepsTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-firewall.json")
	p := testClusterProfile()
	p.Enabled = true
	writeProfileFile(t, path, 1, p)
	applies := 0
	hooks := SyncHooks{
		Check:        func(string) error { return errors.New("syntax error") },
		Apply:        func(string) error { applies++; return nil },
		TablePresent: func() bool { return false },
	}
	state, err := SyncClusterProfile(path, false, hooks)
	if err == nil {
		t.Fatal("expected the nft check failure")
	}
	if applies != 0 {
		t.Fatal("nft -f must not run when nft -c fails")
	}
	if state.Version != 0 || state.LastError == "" {
		t.Fatalf("failed attempt state = %+v", state)
	}
	persisted := LoadSyncState(SyncStatePath(path))
	if persisted.LastError == "" || persisted.Version != 0 {
		t.Fatalf("persisted failed state = %+v", persisted)
	}
}

func TestSyncIdempotentWhenTablePresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-firewall.json")
	p := testClusterProfile()
	p.Enabled = true
	writeProfileFile(t, path, 1, p)
	present := false
	applies := 0
	hooks := SyncHooks{
		Check:        func(string) error { return nil },
		Apply:        func(string) error { applies++; return nil },
		TablePresent: func() bool { return present },
	}
	if _, err := SyncClusterProfile(path, false, hooks); err != nil {
		t.Fatal(err)
	}
	present = true
	if _, err := SyncClusterProfile(path, false, hooks); err != nil {
		t.Fatal(err)
	}
	if applies != 1 {
		t.Fatalf("apply calls = %d, want 1 (boot resync must be a no-op)", applies)
	}
	present = false
	if _, err := SyncClusterProfile(path, false, hooks); err != nil {
		t.Fatal(err)
	}
	if applies != 2 {
		t.Fatalf("apply calls = %d, want 2 after the table disappeared", applies)
	}
}

func TestReceiveRejectsOversizedPayload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-firewall.json")
	payload := bytes.Repeat([]byte("x"), MaxClusterProfileBytes+1)
	if _, err := ReceiveClusterProfile(path, bytes.NewReader(payload), SyncHooks{}); err == nil {
		t.Fatal("oversized payload must be rejected")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the profile file must not be created for an oversized payload")
	}
}

func TestReceiveWritesPrevAndSyncs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-firewall.json")
	p := testClusterProfile()
	p.Enabled = true
	writeProfileFile(t, path, 1, p)
	hooks, _, applies := okHooks()
	if _, err := SyncClusterProfile(path, false, hooks); err != nil {
		t.Fatal(err)
	}
	p.ManagementCIDRs = append(p.ManagementCIDRs, "10.20.30.0/24")
	incomingDir := t.TempDir()
	writeProfileFile(t, filepath.Join(incomingDir, "incoming.json"), 2, p)
	incoming, err := os.ReadFile(filepath.Join(incomingDir, "incoming.json"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := ReceiveClusterProfile(path, bytes.NewReader(incoming), hooks)
	if err != nil {
		t.Fatal(err)
	}
	if state.Version != 2 || *applies != 2 {
		t.Fatalf("state=%+v applies=%d", state, *applies)
	}
	prev, err := os.ReadFile(path + ".prev")
	if err != nil {
		t.Fatalf("previous profile was not kept: %v", err)
	}
	if !bytes.Contains(prev, []byte(`"version": 1`)) {
		t.Fatalf(".prev is not version 1:\n%s", prev)
	}
}

func TestReceiveRejectsOlderVersionWithoutOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster-firewall.json")
	p := testClusterProfile()
	p.Enabled = true
	writeProfileFile(t, path, 5, p)
	hooks, _, _ := okHooks()
	if _, err := SyncClusterProfile(path, false, hooks); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	incomingDir := t.TempDir()
	writeProfileFile(t, incomingDir+"/old.json", 4, p)
	incoming, _ := os.ReadFile(incomingDir + "/old.json")
	if _, err := ReceiveClusterProfile(path, bytes.NewReader(incoming), hooks); err == nil {
		t.Fatal("older version must be rejected")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected profile overwrote the canonical file")
	}
}

func TestAssertManagementAccess(t *testing.T) {
	p := testClusterProfile()
	rs, err := RenderClusterRuleset(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := assertManagementAccess(p, rs); err != nil {
		t.Fatalf("valid ruleset rejected: %v", err)
	}
	if err := assertManagementAccess(p, strings.Replace(rs, "ip saddr @mgmt tcp dport", "ip saddr @nope tcp dport", 1)); err == nil {
		t.Fatal("a ruleset without the management accept must be rejected")
	}
	p.ManagementCIDRs = nil
	if err := assertManagementAccess(p, rs); err == nil {
		t.Fatal("empty management CIDRs must be rejected")
	}
}

func TestSyncRejectsUnsignedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster-firewall.json")
	p := testClusterProfile()
	p.Enabled = true
	data, err := json.Marshal(map[string]any{"version": 2, "profile": p})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncClusterProfile(path, false, SyncHooks{}); err == nil || !strings.Contains(err.Error(), "required sha256") {
		t.Fatalf("sync accepted an unsigned profile: %v", err)
	}
}

func TestReceiveRejectsUnsignedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster-firewall.json")
	p := testClusterProfile()
	p.Enabled = true
	data, err := json.Marshal(map[string]any{"version": 2, "profile": p})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReceiveClusterProfile(path, bytes.NewReader(data), SyncHooks{}); err == nil || !strings.Contains(err.Error(), "required sha256") {
		t.Fatalf("receive accepted an unsigned profile: %v", err)
	}
}
