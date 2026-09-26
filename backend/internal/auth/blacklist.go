package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TokenBlacklist is a denylist of revoked tokens, optionally persisted to
// DATA_DIR/revoked.json so a revoked token stays revoked across backend
// restarts (a logout between restarts must not resurrect a token).
//
// Persistence guarantees:
//   - Atomic writes: every mutation is written to `revoked.json.tmp`
//     (0600), fsync'ed, then renamed over the target. A crash or kernel
//     panic can therefore only ever leave either the previous valid file
//     or the new one; the JSON is never half-written.
//   - Fail-open with evidence: if the file is unreadable or corrupt, we
//     move it aside to `revoked.json.corrupt-<unixnano>` (never silently
//     destroyed) and continue with an empty in-memory set, exactly like
//     the users store treats corrupt users.json.
//   - The file only ever shrinks via the GC ticker; a fresh load maps
//     stored jti hashes back into the live denylist without touching
//     their original expiry.
//
// Entries auto-expire when the original token would have expired anyway,
// so the map never grows unbounded for a steady-state user count.
type TokenBlacklist struct {
	mu   sync.RWMutex
	path string
	// entries maps jtiHash -> absolute expiry time.
	entries map[string]time.Time
	stop    chan struct{}
	// dirty records whether in-memory state differs from the file.
	dirty bool
}

func NewTokenBlacklist() *TokenBlacklist {
	bl := &TokenBlacklist{
		path:    "",
		entries: make(map[string]time.Time),
		stop:    make(chan struct{}),
	}
	go bl.gc()
	return bl
}

// NewTokenBlacklistWithPath is NewTokenBlacklist plus persistence.
// A missing file is a normal first-boot case (empty blacklist). A corrupt
// file does NOT abort startup: it is moved to
// `revoked.json.corrupt-<ts>` (fail-open, documented audit trail) and the
// blacklist starts empty — worse case after a crash is that tokens
// revoked in the previous run become valid again, never that a healthy
// server refuses to start.
func NewTokenBlacklistWithPath(path string) *TokenBlacklist {
	bl := &TokenBlacklist{
		path:    path,
		entries: make(map[string]time.Time),
		stop:    make(chan struct{}),
	}
	if err := bl.loadLocked(); err != nil {
		// loadLocked swallows corrupt files itself (fail-open); an error
		// here means catastrophic IO. Surface it, but keep the server
		// up — a revoked-token file that cannot be read must not take
		// down the whole backend.
		slog.Error("revoked_json_load_failed", "path", path, "err", err)
	}
	go bl.gc()
	return bl
}

type revokedFile struct {
	Version int                  `json:"version"`
	Entries map[string]time.Time `json:"entries"`
	SavedAt time.Time            `json:"saved_at"`
}

const revokedFileVersion = 1

// load reads revoked.json into memory. Corrupt/invalid files are moved
// aside (fail-open) and logged; a missing file is the normal first-boot
// case. Caller must hold b.mu.
func (b *TokenBlacklist) loadLocked() error {
	if b.path == "" {
		return nil
	}
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		// Unreadable (permissions/IO): fail-open with log, keep going.
		slog.Error("revoked_json_read_failed", "path", b.path, "err", err)
		return nil
	}
	var rf revokedFile
	if err := json.Unmarshal(data, &rf); err != nil || rf.Version != revokedFileVersion {
		// Move the corrupt file aside rather than deleting it (forensics),
		// then start from an empty state.
		backup := fmt.Sprintf("%s.corrupt-%d", b.path, time.Now().Unix())
		if rerr := os.Rename(b.path, backup); rerr != nil {
			slog.Error("revoked_json_quarantine_failed", "path", b.path, "backup", backup, "err", rerr)
		} else {
			slog.Warn("revoked_json_corrupt_moved_aside", "path", b.path, "backup", backup)
		}
		return nil
	}
	now := time.Now()
	for k, exp := range rf.Entries {
		// Drop already-expired entries on load so the file can shrink.
		if now.Before(exp) {
			b.entries[k] = exp
		}
	}
	return nil
}

// saveLocked persists the in-memory state atomically:
// tmp(0600) -> fsync -> rename -> (best effort) fsync directory.
// The directory fsync seals the rename itself against a kernel panic,
// which is what "swap the file" means at the POSIX durability level.
func (b *TokenBlacklist) saveLocked() error {
	if b.path == "" {
		return nil
	}
	rf := revokedFile{Version: revokedFileVersion, SavedAt: time.Now().UTC(), Entries: make(map[string]time.Time, len(b.entries))}
	for k, exp := range b.entries {
		rf.Entries[k] = exp
	}
	data, err := json.MarshalIndent(rf, "", "  ")
	if err != nil {
		return err
	}
	tmp := b.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	// Durability steps, each best-effort on the failure path so one bad
	// syscall never wedges the mutex held by the caller.
	if err = f.Sync(); err != nil {
		slog.Warn("revoked_json_fsync_failed", "path", tmp, "err", err)
	}
	if err = f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, b.path); err != nil {
		os.Remove(tmp)
		return err
	}
	if dir, derr := os.Open(filepath.Dir(b.path)); derr == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

// Revoke adds a token (by jti, falling back to a hash of the token
// string when no jti is set) to the denylist until `expiresAt`. The
// on-disk copy is refreshed on every revocation: revocations are rare
// (logout / refresh / admin action) and losing one on a crash is what
// this feature exists to prevent.
func (b *TokenBlacklist) Revoke(jti, tokenStr string, expiresAt time.Time) {
	key := jti
	if key == "" {
		key = hashToken(tokenStr)
	}
	b.mu.Lock()
	b.entries[key] = expiresAt
	b.dirty = true
	serr := b.saveLocked()
	if serr != nil {
		b.dirty = true // retry on the next GC cycle
		slog.Warn("revoked_json_save_failed", "path", b.path, "err", serr)
	} else {
		b.dirty = false
	}
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

// Close stops the GC goroutine and flushes pending state to disk.
// Idempotent.
func (b *TokenBlacklist) Close() {
	select {
	case <-b.stop:
	default:
		close(b.stop)
	}
	b.mu.Lock()
	if b.dirty {
		if err := b.saveLocked(); err != nil {
			slog.Warn("revoked_json_final_save_failed", "path", b.path, "err", err)
		}
		b.dirty = false
	}
	b.mu.Unlock()
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
			before := len(b.entries)
			for k, exp := range b.entries {
				if !now.Before(exp) {
					delete(b.entries, k)
				}
			}
			if len(b.entries) != before || b.dirty {
				if err := b.saveLocked(); err != nil {
					slog.Warn("revoked_json_gcsave_failed", "path", b.path, "err", err)
					b.dirty = true
				} else {
					b.dirty = false
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
