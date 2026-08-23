package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// TokenBlacklist is an in-memory denylist of revoked tokens.
// Entries auto-expire when the original token would have expired
// anyway, so the map never grows unbounded for a steady-state user
// count. Resets on backend restart, which is acceptable for a
// single-host install: revoked tokens come back into force only if
// the backend is restarted within their remaining lifetime.
type TokenBlacklist struct {
	mu      sync.RWMutex
	entries map[string]time.Time // jtiHash -> expiresAt
	stop    chan struct{}
}

func NewTokenBlacklist() *TokenBlacklist {
	bl := &TokenBlacklist{
		entries: make(map[string]time.Time),
		stop:    make(chan struct{}),
	}
	go bl.gc()
	return bl
}

// Revoke adds a token (by jti, falling back to a hash of the token
// string when no jti is set) to the denylist until `expiresAt`.
func (b *TokenBlacklist) Revoke(jti, tokenStr string, expiresAt time.Time) {
	key := jti
	if key == "" {
		key = hashToken(tokenStr)
	}
	b.mu.Lock()
	b.entries[key] = expiresAt
	b.mu.Unlock()
}

// IsRevoked returns true if the token (or jti) is in the denylist
// and the entry has not yet expired.
func (b *TokenBlacklist) IsRevoked(jti, tokenStr string) bool {
	key := jti
	if key == "" {
		key = hashToken(tokenStr)
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	exp, ok := b.entries[key]
	if !ok {
		return false
	}
	return time.Now().Before(exp)
}

// Close stops the GC goroutine. Idempotent.
func (b *TokenBlacklist) Close() {
	select {
	case <-b.stop:
	default:
		close(b.stop)
	}
}

func (b *TokenBlacklist) gc() {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case now := <-t.C:
			b.mu.Lock()
			for k, exp := range b.entries {
				if !now.Before(exp) {
					delete(b.entries, k)
				}
			}
			b.mu.Unlock()
		}
	}
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}
