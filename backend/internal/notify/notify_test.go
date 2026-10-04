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
	if err := n.Update(Config{
		Enabled:         true,
		WebhookEnabled:  true,
		WebhookURL:      "https://example.com/hook",
		TelegramEnabled: true,
		TelegramChatID:  "123456",
		NtfyEnabled:     true,
		NtfyTopic:       "webkvm-alerts",
		GotifyEnabled:   true,
		GotifyServerURL: "https://gotify.example.com",
	}, "super-secret-token", "bot123456:secret-token", "smtp-user-value", "smtp-pass-value", "ntfy-secret-token", "gotify-app-token", false); err != nil {
		t.Fatal(err)
	}
	st := n.Status()
	// The Status struct has no secret fields; serialized output must not
	// contain the credential VALUES.
	raw, _ := jsonMarshal(st)
	for _, secret := range []string{"super-secret-token", "bot123456:secret-token", "smtp-user-value", "smtp-pass-value", "ntfy-secret-token", "gotify-app-token"} {
		if contains(raw, secret) {
			t.Errorf("secret value leaked into status JSON: %s", secret)
		}
	}
	if !st.HasWebhookSecret || !st.HasTelegramToken || !st.HasSMTPUser || !st.HasSMTPPassword || !st.HasNtfyToken || !st.HasGotifyToken {
		t.Errorf("expected secrets present booleans to be true: %+v", st)
	}
}

func TestEmptySecretKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	n, _ := New(dir, Config{}, nil)
	_ = n.Update(Config{TelegramChatID: "123"}, "tok", "tg-tok", "user", "pass", "ntfy-tok", "gotify-tok", false)
	// Re-save with empty secrets -> must NOT clear.
	if err := n.Update(Config{TelegramChatID: "123"}, "", "", "", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	st := n.Status()
	if !st.HasWebhookSecret || !st.HasTelegramToken || !st.HasSMTPUser || !st.HasSMTPPassword || !st.HasNtfyToken || !st.HasGotifyToken {
		t.Fatalf("empty secret fields cleared stored secrets")
	}
}

func TestClearSecretClears(t *testing.T) {
	dir := t.TempDir()
	n, _ := New(dir, Config{}, nil)
	_ = n.Update(Config{TelegramChatID: "123"}, "tok", "tg-tok", "user", "pass", "ntfy-tok", "gotify-tok", false)
	if err := n.Update(Config{}, "", "", "", "", "", "", true); err != nil {
		t.Fatal(err)
	}
	st := n.Status()
	if st.HasWebhookSecret || st.HasTelegramToken || st.HasSMTPUser || st.HasSMTPPassword || st.HasNtfyToken || st.HasGotifyToken {
		t.Fatalf("clear_secret did not clear secrets")
	}
}

func TestSecretsFilePerms(t *testing.T) {
	dir := t.TempDir()
	n, _ := New(dir, Config{}, nil)
	_ = n.Update(Config{}, "tok", "", "", "", "", "", false)
	fi, err := os.Stat(filepath.Join(dir, "notify-secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestValidateRejectsPlainHTTP(t *testing.T) {
	err := validateConfig(Config{WebhookEnabled: true, WebhookURL: "http://example.com/hook"}, "", "", "", "")
	if err == nil {
		t.Fatal("expected http webhook URL to be rejected")
	}

	err = validateConfig(Config{DiscordEnabled: true, DiscordWebhookURL: "http://discord.com/api/webhooks/123"}, "", "", "", "")
	if err == nil {
		t.Fatal("expected http discord URL to be rejected")
	}

	err = validateConfig(Config{SlackEnabled: true, SlackWebhookURL: "http://hooks.slack.com/services/123"}, "", "", "", "")
	if err == nil {
		t.Fatal("expected http slack URL to be rejected")
	}
}

func TestValidateTelegramRequiresChatIDAndToken(t *testing.T) {
	err := validateConfig(Config{TelegramEnabled: true}, "", "", "", "")
	if err == nil {
		t.Fatal("expected telegram without chat_id to be rejected")
	}

	err = validateConfig(Config{TelegramEnabled: true, TelegramChatID: "123"}, "", "", "", "")
	if err == nil {
		t.Fatal("expected telegram without token to be rejected")
	}

	err = validateConfig(Config{TelegramEnabled: true, TelegramChatID: "123"}, "bot123:abc", "", "", "")
	if err != nil {
		t.Fatalf("expected telegram with chat_id and token to pass, got: %v", err)
	}
}

func TestValidateNtfyAndGotify(t *testing.T) {
	// Ntfy requires topic
	err := validateConfig(Config{NtfyEnabled: true}, "", "", "", "")
	if err == nil {
		t.Fatal("expected ntfy without topic to be rejected")
	}
	// Ntfy rejects non-http/https URL
	err = validateConfig(Config{NtfyEnabled: true, NtfyTopic: "topic", NtfyServerURL: "ftp://example.com"}, "", "", "", "")
	if err == nil {
		t.Fatal("expected ntfy with invalid protocol to be rejected")
	}
	// Valid ntfy
	err = validateConfig(Config{NtfyEnabled: true, NtfyTopic: "topic", NtfyServerURL: "https://ntfy.sh"}, "", "", "", "")
	if err != nil {
		t.Fatalf("expected valid ntfy to pass, got %v", err)
	}

	// Gotify requires server URL
	err = validateConfig(Config{GotifyEnabled: true}, "", "", "", "")
	if err == nil {
		t.Fatal("expected gotify without server URL to be rejected")
	}
	// Gotify requires token
	err = validateConfig(Config{GotifyEnabled: true, GotifyServerURL: "https://gotify.net"}, "", "", "", "")
	if err == nil {
		t.Fatal("expected gotify without token to be rejected")
	}
	// Valid gotify
	err = validateConfig(Config{GotifyEnabled: true, GotifyServerURL: "https://gotify.net"}, "", "", "app-token-123", "")
	if err != nil {
		t.Fatalf("expected valid gotify to pass, got %v", err)
	}
}

func TestValidateRejectsSMTPWithoutTLS(t *testing.T) {
	err := validateConfig(Config{SMTPEnabled: true, SMTPHost: "smtp.example.com", SMTPPort: 25, SMTPFrom: "a@b.c", SMTPTo: "d@e.f"}, "", "", "", "")
	if err == nil {
		t.Fatal("expected smtp without tls to be rejected")
	}
}

func TestSendTest(t *testing.T) {
	dir := t.TempDir()
	n, err := New(dir, Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SendTest(); err == nil {
		t.Fatal("expected SendTest with no channel enabled to fail")
	}
	// Enable ntfy
	if err := n.Update(Config{NtfyEnabled: true, NtfyTopic: "alerts"}, "", "", "", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	if err := n.SendTest(); err != nil {
		t.Fatalf("expected SendTest to succeed with Ntfy enabled, got: %v", err)
	}
}

func jsonMarshal(v interface{}) ([]byte, error) {
	return json.Marshal(v)
}

func contains(b []byte, s string) bool {
	return bytes.Contains(b, []byte(s))
}
