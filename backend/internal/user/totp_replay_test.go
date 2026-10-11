package user

import (
	"testing"
	"time"

	"webkvm/internal/auth"
)

// A TOTP code may be used once: replaying it (or an older one) is refused.
func TestConsumeTOTP_RejectsReplay(t *testing.T) {
	s, _, _ := seedStore(t)
	mkUser(t, s, "carol")
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTOTP("carol", secret, nil); err != nil {
		t.Fatal(err)
	}
	// EnableTOTP burns the current step; the next step's code is fresh.
	next, _ := auth.TOTPCodeAt(secret, time.Now().Add(30*time.Second))
	ok, err := s.ConsumeTOTP("carol", next)
	if err != nil || !ok {
		t.Fatalf("first use: ok=%v err=%v", ok, err)
	}
	if ok, _ := s.ConsumeTOTP("carol", next); ok {
		t.Fatal("replay of the same code must be rejected")
	}
	older, _ := auth.TOTPCodeAt(secret, time.Now())
	if ok, _ := s.ConsumeTOTP("carol", older); ok {
		t.Fatal("an older step must be rejected after a newer one was used")
	}
	if ok, _ := s.ConsumeTOTP("carol", "000000"); ok {
		t.Fatal("wrong code accepted")
	}
}
