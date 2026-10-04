package auth

import (
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestWebAuthnConfigValidation(t *testing.T) {
	cases := []struct {
		rpid   string
		origin string
	}{
		{"localhost", "https://localhost:8081"},
		{"localhost", "http://localhost:8080"},
		{"webkvm.local", "https://webkvm.local:8081"},
		{"kvm.myhome.org", "https://kvm.myhome.org"},
	}

	for _, tc := range cases {
		w, err := webauthn.New(&webauthn.Config{
			RPDisplayName: "WebKVM",
			RPID:          tc.rpid,
			RPOrigins:     []string{tc.origin},
		})
		if err != nil {
			t.Fatalf("webauthn.New failed for %s (%s): %v", tc.rpid, tc.origin, err)
		}
		if w == nil {
			t.Fatalf("expected webauthn instance, got nil")
		}
	}
}
