package auth

import (
	"testing"

	"github.com/go-webauthn/webauthn/webauthn"

	"webkvm/internal/models"
)

func TestCredentialAfterLogin_CarriesLibrarySignCount(t *testing.T) {
	stored := &models.WebAuthnCredential{ID: "abc", SignCount: 5}
	lib := &webauthn.Credential{Authenticator: webauthn.Authenticator{SignCount: 9}}
	got, err := credentialAfterLogin(stored, lib)
	if err != nil {
		t.Fatal(err)
	}
	if got.SignCount != 9 {
		t.Fatalf("SignCount = %d, want 9 (from the library result)", got.SignCount)
	}
	if stored.SignCount != 5 {
		t.Fatal("stored credential must not be mutated")
	}
}

func TestCredentialAfterLogin_RejectsCloneWarning(t *testing.T) {
	stored := &models.WebAuthnCredential{ID: "abc", SignCount: 5}
	lib := &webauthn.Credential{Authenticator: webauthn.Authenticator{SignCount: 5, CloneWarning: true}}
	if _, err := credentialAfterLogin(stored, lib); err == nil {
		t.Fatal("clone warning must fail the login")
	}
}
