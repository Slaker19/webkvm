package auth

import (
	"sync"
	"testing"
	"time"
)

func TestBlacklist_RevokeAndIsRevokedByJTI(t *testing.T) {
	bl := NewTokenBlacklist()
	defer bl.Close()

	future := time.Now().Add(1 * time.Hour)
	bl.Revoke("jti-1", "token-string", future)
	if !bl.IsRevoked("jti-1", "token-string") {
		t.Error("expected jti-1 to be revoked")
	}
}

func TestBlacklist_RevokeByTokenHash(t *testing.T) {
	bl := NewTokenBlacklist()
	defer bl.Close()
	tok := "raw-token-without-jti"
	future := time.Now().Add(1 * time.Hour)

	bl.Revoke("", tok, future)
	if !bl.IsRevoked("", tok) {
		t.Error("expected token to be revoked by hash")
	}
	// And the hash should be deterministic.
	if !bl.IsRevoked("", "raw-token-without-jti") {
		t.Error("expected token to be revoked (second call)")
	}
}

func TestBlacklist_ExpiredEntryNotRevoked(t *testing.T) {
	bl := NewTokenBlacklist()
	defer bl.Close()
	past := time.Now().Add(-1 * time.Hour)
	bl.Revoke("jti-old", "tok-old", past)
	if bl.IsRevoked("jti-old", "tok-old") {
		t.Error("expired entry should not be reported as revoked")
	}
}

func TestBlacklist_UnrelatedTokenNotRevoked(t *testing.T) {
	bl := NewTokenBlacklist()
	defer bl.Close()
	future := time.Now().Add(1 * time.Hour)
	bl.Revoke("jti-1", "tok-1", future)
	if bl.IsRevoked("jti-2", "tok-2") {
		t.Error("unrelated token should not be revoked")
	}
}

func TestBlacklist_CloseIdempotent(t *testing.T) {
	bl := NewTokenBlacklist()
	bl.Close()
	bl.Close() // must not panic or block
}

func TestBlacklist_ConcurrentRevoke(t *testing.T) {
	bl := NewTokenBlacklist()
	defer bl.Close()
	future := time.Now().Add(1 * time.Hour)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			jti := "jti"
			tok := "tok"
			bl.Revoke(jti, tok, future)
			_ = bl.IsRevoked(jti, tok)
		}(i)
	}
	wg.Wait()
	if !bl.IsRevoked("jti", "tok") {
		t.Error("after concurrent revokes, jti should be revoked")
	}
}

func TestHashToken_Deterministic(t *testing.T) {
	a := hashToken("hello")
	b := hashToken("hello")
	if a != b {
		t.Errorf("hashes differ: %s vs %s", a, b)
	}
	c := hashToken("world")
	if a == c {
		t.Error("hashes of different inputs should differ")
	}
	// Should be 64 hex chars (sha256 = 32 bytes).
	if len(a) != 64 {
		t.Errorf("hash length = %d, want 64", len(a))
	}
}
