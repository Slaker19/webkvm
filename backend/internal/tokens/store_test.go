package tokens

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}


func TestStoreCreateAndValidate(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	tok, plain, err := s.Create("ci-deploy", "alice", "operator", []string{"vms.read"}, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if plain == "" {
		t.Fatal("plain token empty")
	}
	if tok.Name != "ci-deploy" {
		t.Fatalf("name = %q", tok.Name)
	}
	if !strings.HasPrefix(plain, "wvmb_") {
		t.Fatalf("plain missing prefix: %q", plain)
	}
	// Validate the plain.
	got, err := s.Validate(plain)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got.Username != "alice" {
		t.Fatalf("username = %q", got.Username)
	}
}

func TestStoreValidateWrongToken(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, err := s.Validate("wvmb_bogus")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStoreRevoke(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, plain, _ := s.Create("t1", "bob", "viewer", nil, time.Hour)
	id := lookupID(t, s, plain)
	if err := s.Revoke(id, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(plain); err == nil {
		t.Fatal("expected revoked token to fail")
	}
}

func TestStoreListByUser(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, _, _ = s.Create("a", "alice", "operator", nil, time.Hour)
	_, _, _ = s.Create("b", "bob", "viewer", nil, time.Hour)
	_, _, _ = s.Create("c", "alice", "operator", nil, time.Hour)
	got := s.List("alice")
	if len(got) != 2 {
		t.Fatalf("alice list len = %d, want 2", len(got))
	}
}

func TestStorePurgeExpired(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, _, _ = s.Create("old", "alice", "viewer", nil, time.Nanosecond)
	time.Sleep(10 * time.Millisecond)
	n, err := s.PurgeExpired()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("purged = %d, want 1", n)
	}
}

func TestStoreFileExists(t *testing.T) {
	dir := t.TempDir()
	_, _ = New(dir)
	if _, err := os.Stat(filepath.Join(dir, "api-tokens.json")); err != nil {
		t.Fatal(err)
	}
}

func lookupID(t *testing.T, s *Store, plain string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(sum[:])
	id := hash[:16]
	for _, tok := range s.toks {
		if tok.ID == id {
			return tok.ID
		}
	}
	t.Fatal("token not found")
	return ""
}
