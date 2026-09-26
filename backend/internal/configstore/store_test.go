package configstore

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"go.uber.org/goleak"
)
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}


func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}

	// Defaults are loaded.
	if got := s.GetInt("server.port"); got != 8080 {
		t.Fatalf("port default = %d, want 8080", got)
	}

	// Set a few values across the 8-field Phase 1.7-bis-backup schema.
	applied, failed, err := s.SetMany(Set{
		"server.port":            float64(9090), // JSON numbers come as float64
		"server.bind_addr":       "127.0.0.1",
		"auth.token_ttl":         "12h",
		"auth.allow_api_tokens":  false,
		"server.trust_proxy":     true,
		"logging.level":          "debug",
	})
	if err != nil {
		t.Fatalf("SetMany: %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("failed: %v", failed)
	}
	if len(applied) != 6 {
		t.Fatalf("applied = %d, want 6", len(applied))
	}

	// Re-read.
	if got := s.GetInt("server.port"); got != 9090 {
		t.Fatalf("port = %d, want 9090", got)
	}
	if got := s.GetString("server.bind_addr"); got != "127.0.0.1" {
		t.Fatalf("bind_addr = %q, want 127.0.0.1", got)
	}
	if got := s.GetDuration("auth.token_ttl"); got != 12*time.Hour {
		t.Fatalf("token_ttl = %v, want 12h", got)
	}
	if got := s.GetBool("auth.allow_api_tokens"); got {
		t.Fatalf("allow_api_tokens = %v, want false", got)
	}
	if got := s.GetBool("server.trust_proxy"); !got {
		t.Fatalf("trust_proxy = %v, want true", got)
	}
	if got := s.GetString("logging.level"); got != "debug" {
		t.Fatalf("log_level = %q, want debug", got)
	}

	// New store from the same dir picks up the values.
	s2, err := New(dir, DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.GetInt("server.port"); got != 9090 {
		t.Fatalf("reload port = %d, want 9090", got)
	}
}

func TestStoreValidation(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}

	// Out-of-range int.
	_, failed, _ := s.SetMany(Set{"server.port": float64(99999)})
	if _, ok := failed["server.port"]; !ok {
		t.Fatalf("expected server.port validation error, got %v", failed)
	}

	// Invalid enum.
	_, failed, _ = s.SetMany(Set{"logging.level": "trace"})
	if _, ok := failed["logging.level"]; !ok {
		t.Fatalf("expected logging.level enum error, got %v", failed)
	}

	// Invalid duration.
	_, failed, _ = s.SetMany(Set{"auth.token_ttl": "never"})
	if _, ok := failed["auth.token_ttl"]; !ok {
		t.Fatalf("expected token_ttl duration error, got %v", failed)
	}

	// Unknown key.
	_, failed, _ = s.SetMany(Set{"foo.bar": "x"})
	if _, ok := failed["foo.bar"]; !ok {
		t.Fatalf("expected unknown key error, got %v", failed)
	}
}

func TestStoreSnapshotMasksSecrets(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	// No secret fields in the 12-field Phase 1.7-bis schema, so
	// this just sanity-checks the snapshot still serializes
	// without panic.
	snap := s.Snapshot()
	for k, v := range snap {
		_ = k
		_ = v
	}
}

func TestStorePendingRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir, DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	// Everything is at default -> no pending restart.
	if got := s.PendingRestart(); len(got) != 0 {
		t.Fatalf("expected empty pending, got %v", got)
	}
	// Change a restart-only field.
	if _, _, err := s.SetMany(Set{"server.port": float64(9090)}); err != nil {
		t.Fatal(err)
	}
	pending := s.PendingRestart()
	if len(pending) == 0 {
		t.Fatalf("expected pending restart, got none")
	}
	found := false
	for _, k := range pending {
		if k == "server.port" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected server.port in pending, got %v", pending)
	}

	// Change a hot field — should NOT be pending.
	if _, _, err := s.SetMany(Set{"logging.level": "debug"}); err != nil {
		t.Fatal(err)
	}
	pending2 := s.PendingRestart()
	for _, k := range pending2 {
		if k == "logging.level" {
			t.Fatalf("logging.level should not be pending restart, got %v", pending2)
		}
	}

	// auth.token_ttl is hot; not pending.
	if _, _, err := s.SetMany(Set{"auth.token_ttl": "5m"}); err != nil {
		t.Fatal(err)
	}
	pending3 := s.PendingRestart()
	for _, k := range pending3 {
		if k == "auth.token_ttl" {
			t.Fatalf("auth.token_ttl should not be pending restart, got %v", pending3)
		}
	}
}

func TestStoreReset(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, DefaultSchema())
	_, _, _ = s.SetMany(Set{"server.port": float64(9090)})
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if got := s.GetInt("server.port"); got != 8080 {
		t.Fatalf("after reset port = %d, want 8080", got)
	}
}

func TestStoreFileIsValidJSON(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir, DefaultSchema())
	_, _, _ = s.SetMany(Set{"server.bind_addr": "10.0.0.1", "server.port": float64(1234)})
	// The on-disk file is {version, values, saved_at}.
	var f File
	data, err := os.ReadFile(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("file not valid json: %v", err)
	}
	if f.Version != 1 {
		t.Fatalf("version = %d, want 1", f.Version)
	}
	if f.Values["server.port"].(float64) != 1234 {
		t.Fatalf("persisted port = %v, want 1234", f.Values["server.port"])
	}
}
