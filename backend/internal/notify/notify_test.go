package notify

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSecretsNotSerializedInStatus(t *testing.T) {
	dir := t.TempDir()
	n, err := New(dir, Config{Enabled: true, WebhookEnabled: true, WebhookURL: "https://example.com/hook"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Update(Config{Enabled: true, WebhookEnabled: true, WebhookURL: "https://example.com/hook"}, "super-secret-token", "smtp-user-value", "smtp-pass-value", false); err != nil {
		t.Fatal(err)
	}
	st := n.Status()
	// The Status struct has no secret fields; serialized output must not
	// contain the credential VALUES.
	raw, _ := jsonMarshal(st)
	for _, secret := range []string{"super-secret-token", "smtp-user-value", "smtp-pass-value"} {
		if contains(raw, secret) {
			t.Errorf("secret value leaked into status JSON: %s", secret)
		}
	}
	if !st.HasWebhookSecret || !st.HasSMTPUser || !st.HasSMTPPassword {
		t.Errorf("expected secrets present booleans to be true")
	}
}

func TestEmptySecretKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	n, _ := New(dir, Config{}, nil)
	_ = n.Update(Config{}, "tok", "user", "pass", false)
	// Re-save with empty secrets -> must NOT clear.
	if err := n.Update(Config{}, "", "", "", false); err != nil {
		t.Fatal(err)
	}
	st := n.Status()
	if !st.HasWebhookSecret || !st.HasSMTPUser || !st.HasSMTPPassword {
		t.Fatalf("empty secret fields cleared stored secrets")
	}
}

func TestClearSecretClears(t *testing.T) {
	dir := t.TempDir()
	n, _ := New(dir, Config{}, nil)
	_ = n.Update(Config{}, "tok", "user", "pass", false)
	if err := n.Update(Config{}, "", "", "", true); err != nil {
		t.Fatal(err)
	}
	st := n.Status()
	if st.HasWebhookSecret || st.HasSMTPUser || st.HasSMTPPassword {
		t.Fatalf("clear_secret did not clear secrets")
	}
}

func TestSecretsFilePerms(t *testing.T) {
	dir := t.TempDir()
	n, _ := New(dir, Config{}, nil)
	_ = n.Update(Config{}, "tok", "", "", false)
	fi, err := os.Stat(filepath.Join(dir, "notify-secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestValidateRejectsPlainHTTP(t *testing.T) {
	err := validateConfig(Config{WebhookEnabled: true, WebhookURL: "http://example.com/hook"})
	if err == nil {
		t.Fatal("expected http webhook URL to be rejected")
	}
}

func TestValidateRejectsSMTPWithoutTLS(t *testing.T) {
	err := validateConfig(Config{SMTPEnabled: true, SMTPHost: "smtp.example.com", SMTPPort: 25, SMTPFrom: "a@b.c", SMTPTo: "d@e.f"})
	if err == nil {
		t.Fatal("expected smtp without tls to be rejected")
	}
}

// small helpers to avoid importing encoding/json in tests is overkill.
func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func contains(b []byte, s string) bool {
	return bytes.Contains(b, []byte(s))
}
