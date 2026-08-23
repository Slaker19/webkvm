package libvirt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildCIFSSecretXML(t *testing.T) {
	got := buildCIFSSecretXML("cifs-files.example.com/share", "alice")
	wants := []string{
		"ephemeral='no'",
		"private='yes'",
		"user=alice",
		"usage=cifs-files.example.com/share",
	}
	for _, w := range wants {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in: %s", w, got)
		}
	}
	// No <usage> block for CIFS — libvirt has no native cifs usage
	// type; the binding is done via the pool's <auth> reference.
	if strings.Contains(got, "<usage") {
		t.Errorf("CIFS secret should not have <usage> block: %s", got)
	}
}

func TestBuildCIFSSecretXML_EscapesUser(t *testing.T) {
	got := buildCIFSSecretXML("cifs-h/share", "x'onload='alert(1)")
	if !strings.Contains(got, "&apos;") {
		t.Fatalf("user not escaped: %s", got)
	}
}

func TestCIFSSecretsPath(t *testing.T) {
	got := cifsSecretsPath("/opt/webkvm")
	want := filepath.Join("/opt/webkvm", "cifs-secrets.json")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestPersistAndReadCIFSSecretRef(t *testing.T) {
	dir := t.TempDir()
	ref := &SecretRef{
		PoolName:   "pool-a",
		SecretUUID: "abc-123",
		CreatedAt:  1700000000,
	}
	if err := persistCIFSSecretRef(dir, ref); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cifsSecretsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != cifsSecretsFileMode {
		t.Fatalf("file mode = %o, want %o", info.Mode().Perm(), cifsSecretsFileMode)
	}
	got, err := readCIFSSecretRef(dir, "pool-a")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.SecretUUID != "abc-123" {
		t.Fatalf("got %+v, want SecretUUID=abc-123", got)
	}
}

func TestPersistCIFSSecretRef_OverwritesAndPreservesOthers(t *testing.T) {
	dir := t.TempDir()
	if err := persistCIFSSecretRef(dir, &SecretRef{PoolName: "a", SecretUUID: "u-a"}); err != nil {
		t.Fatal(err)
	}
	if err := persistCIFSSecretRef(dir, &SecretRef{PoolName: "b", SecretUUID: "u-b"}); err != nil {
		t.Fatal(err)
	}
	if err := persistCIFSSecretRef(dir, &SecretRef{PoolName: "a", SecretUUID: "u-a2"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(cifsSecretsPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]SecretRef
	if err := json.Unmarshal(b, &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(all))
	}
	if all["a"].SecretUUID != "u-a2" {
		t.Fatalf("a.SecretUUID = %q, want u-a2", all["a"].SecretUUID)
	}
	if all["b"].SecretUUID != "u-b" {
		t.Fatalf("b.SecretUUID = %q, want u-b", all["b"].SecretUUID)
	}
}

func TestRemoveCIFSSecretRef_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// Remove from non-existent file: no error.
	if err := removeCIFSSecretRef(dir, "nope"); err != nil {
		t.Fatalf("first remove on missing file: %v", err)
	}
	if err := persistCIFSSecretRef(dir, &SecretRef{PoolName: "p", SecretUUID: "u"}); err != nil {
		t.Fatal(err)
	}
	if err := removeCIFSSecretRef(dir, "p"); err != nil {
		t.Fatal(err)
	}
	// Second remove: pool already gone, no error.
	if err := removeCIFSSecretRef(dir, "p"); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	b, _ := os.ReadFile(cifsSecretsPath(dir))
	var all map[string]SecretRef
	_ = json.Unmarshal(b, &all)
	if _, ok := all["p"]; ok {
		t.Fatal("expected p to be removed")
	}
}

func TestReadCIFSSecretRef_NotFoundReturnsNilNil(t *testing.T) {
	dir := t.TempDir()
	got, err := readCIFSSecretRef(dir, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestLoadCIFSSecretsFromDisk(t *testing.T) {
	dir := t.TempDir()
	if err := persistCIFSSecretRef(dir, &SecretRef{PoolName: "p1", SecretUUID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := persistCIFSSecretRef(dir, &SecretRef{PoolName: "p2", SecretUUID: "u2"}); err != nil {
		t.Fatal(err)
	}

	// Reset in-memory state for the test (other tests may have populated it).
	cifsSecretsMu.Lock()
	cifsSecrets = map[string]SecretRef{}
	cifsSecretsMu.Unlock()

	if err := loadCIFSSecretsFromDisk(dir); err != nil {
		t.Fatal(err)
	}
	cifsSecretsMu.RLock()
	defer cifsSecretsMu.RUnlock()
	if len(cifsSecrets) != 2 {
		t.Fatalf("expected 2 loaded, got %d", len(cifsSecrets))
	}
	if cifsSecrets["p1"].SecretUUID != "u1" {
		t.Fatalf("p1 UUID = %q, want u1", cifsSecrets["p1"].SecretUUID)
	}
}

func TestLoadCIFSSecretsFromDisk_MissingFileIsOK(t *testing.T) {
	dir := t.TempDir()
	if err := loadCIFSSecretsFromDisk(dir); err != nil {
		t.Fatalf("missing file should be no-op, got %v", err)
	}
}

func TestLoadCIFSSecretsFromDisk_CorruptFileIsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(cifsSecretsPath(dir), []byte("not json"), cifsSecretsFileMode); err != nil {
		t.Fatal(err)
	}
	if err := loadCIFSSecretsFromDisk(dir); err == nil {
		t.Fatal("expected error for corrupt file")
	}
}

func TestLookupCIFSSecretRef_FallbackToDisk(t *testing.T) {
	dir := t.TempDir()
	if err := persistCIFSSecretRef(dir, &SecretRef{PoolName: "cold", SecretUUID: "u-cold"}); err != nil {
		t.Fatal(err)
	}
	cifsSecretsMu.Lock()
	cifsSecrets = map[string]SecretRef{}
	cifsSecretsMu.Unlock()

	got, err := lookupCIFSSecretRef(dir, "cold")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.SecretUUID != "u-cold" {
		t.Fatalf("got %+v, want u-cold", got)
	}
}

func TestUnsetCIFSSecret_NoConnectorSkipsDisk(t *testing.T) {
	dir := t.TempDir()
	if err := persistCIFSSecretRef(dir, &SecretRef{PoolName: "p", SecretUUID: "u"}); err != nil {
		t.Fatal(err)
	}
	// Seed the in-memory map so we can verify it's cleared.
	cifsSecretsMu.Lock()
	cifsSecrets = map[string]SecretRef{"p": {PoolName: "p", SecretUUID: "u"}}
	cifsSecretsMu.Unlock()

	// nil connector: unsetCIFSSecret should clear the in-memory
	// entry but cannot reach the disk file (no DataDir).
	if err := unsetCIFSSecret(nil, nil, "p"); err != nil {
		t.Fatal(err)
	}
	cifsSecretsMu.RLock()
	_, stillThere := cifsSecrets["p"]
	cifsSecretsMu.RUnlock()
	if stillThere {
		t.Fatal("expected in-memory p to be cleared")
	}
	// Disk file still has p (connector-less unset cannot reach it).
	b, _ := os.ReadFile(cifsSecretsPath(dir))
	if !strings.Contains(string(b), `"p"`) {
		t.Fatalf("expected disk file to retain p without a connector, got: %s", b)
	}
}
