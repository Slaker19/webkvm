package webpush

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewPersistsVAPIDAndSubscriptions(t *testing.T) {
	dir := t.TempDir()
	m, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.PublicKey() == "" {
		t.Fatal("empty VAPID public key")
	}
	info, err := os.Stat(filepath.Join(dir, "webpush.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("webpush.json mode = %o, want 600", info.Mode().Perm())
	}
	sub := Subscription{
		Endpoint: "https://push.example.invalid/abc",
		Keys:     Keys{P256dh: "p256dh-key", Auth: "auth-key"},
		Locale:   "es",
	}
	if err := m.Subscribe("admin", sub); err != nil {
		t.Fatal(err)
	}
	if err := m.Subscribe("admin", Subscription{Endpoint: "http://insecure.example/x"}); err == nil {
		t.Fatal("http endpoint should be rejected")
	}

	reloaded, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.PublicKey() != m.PublicKey() {
		t.Fatal("VAPID public key changed after reload")
	}
	if len(reloaded.subs) != 1 || reloaded.subs[0].Owner != "admin" || reloaded.subs[0].Locale != "es" {
		t.Fatalf("subscriptions = %+v", reloaded.subs)
	}
	if err := reloaded.Unsubscribe("admin", sub.Endpoint); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.subs) != 0 {
		t.Fatalf("subscription was not removed: %+v", reloaded.subs)
	}
}
