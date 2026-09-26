package backupstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// V13-BCK-04: RecordVerification persists the outcome and
// AttachVerified copies it (last_verified + sha256 on success,
// last_verify_error on failure) onto list responses, surviving a
// store reload from disk.
func TestVerifiedRecordAndAttach(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	key1 := "webkvm-h-20260101T120000.123456789Z-abc123-config.tar.zst"
	if err := s.RecordVerification("t_1", key1, "abc123deadbeef", true, ""); err != nil {
		t.Fatal(err)
	}
	key2 := "webkvm-h-20260102T120000.123456789Z-def456-config.tar.zst"
	if err := s.RecordVerification("t_1", key2, "", false, "checksum mismatch"); err != nil {
		t.Fatal(err)
	}

	// Attach maps the record onto BackupFile list entries.
	files := []BackupFile{
		{TargetID: "t_1", Filename: key1},
		{TargetID: "t_1", Filename: key2},
		{TargetID: "t_1", Filename: "unverified.tar.zst"},
	}
	attached := s.AttachVerified(files)

	if attached[0].Sha256 != "abc123deadbeef" {
		t.Errorf("verified file sha256 = %q", attached[0].Sha256)
	}
	if attached[0].LastVerified.IsZero() {
		t.Error("verified file should carry LastVerified")
	}
	if attached[0].LastVerifyError != "" {
		t.Errorf("verified file should not carry an error: %q", attached[0].LastVerifyError)
	}
	if attached[1].LastVerified.IsZero() {
		t.Error("failed file still records when it was attempted (LastVerified)")
	}
	if !strings.Contains(attached[1].LastVerifyError, "checksum mismatch") {
		t.Errorf("failed file should carry last_verify_error: %q", attached[1].LastVerifyError)
	}
	if attached[1].Sha256 != "" {
		t.Errorf("failed verification must not surface a stale hash: %q", attached[1].Sha256)
	}
	if !attached[2].LastVerified.IsZero() || attached[2].Sha256 != "" {
		t.Error("never-verified file should stay blank")
	}

	// Survives a reload from verified.json.
	reloaded, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	again := reloaded.AttachVerified(files)
	if again[0].LastVerified.IsZero() || again[0].Sha256 != "abc123deadbeef" {
		t.Errorf("record did not survive reload: %+v", again[0])
	}
	if again[1].LastVerifyError == "" {
		t.Error("failure record did not survive reload")
	}

	// The file is JSON 0600 and holds no secret material.
	fi, err := os.Stat(filepath.Join(dir, "backup", "verified.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("verified.json mode = %v, want 0600", fi.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(dir, "backup", "verified.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Verified map[string]struct {
			At     time.Time `json:"at"`
			Sha256 string    `json:"sha256"`
			Ok     bool      `json:"ok"`
			Error  string    `json:"error"`
		} `json:"verified"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Verified) != 2 {
		t.Errorf("verified.json holds %d records, want 2", len(parsed.Verified))
	}
}
