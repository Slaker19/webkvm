// Package tokens implements long-lived API tokens (a la GitHub PATs).
//
// A token is a random 32-byte secret, base64url-encoded. The full token
// is shown to the user exactly once at creation time; we only persist
// the sha256 hash and a short prefix (first 8 chars, used to identify
// the token in the UI: "wvmb_AbCdEfGh…"). Revocation = delete the file
// entry.
//
// Storage: {DataDir}/api-tokens.json, atomic rename, 0600.
package tokens

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Token is a stored token entry. The Hash is the sha256 of the full
// token, hex-encoded. The Plain is never persisted — it's only in the
// response to the create endpoint.
type Token struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Prefix     string    `json:"prefix"`     // first 8 chars of the token, used to display
	Hash       string    `json:"hash"`       // sha256 of the full token
	Username   string    `json:"username"`   // owner
	Role       string    `json:"role"`       // role at creation time
	Scopes     []string  `json:"scopes"`     // future: capability list
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
	Revoked    bool      `json:"revoked,omitempty"`
}

// IsExpired reports whether the token is past its expiry.
func (t Token) IsExpired() bool {
	return time.Now().After(t.ExpiresAt)
}

// Store persists tokens. Safe for concurrent use.
type Store struct {
	mu   sync.RWMutex
	path string
	toks map[string]*Token // by ID
}

// New loads (or creates) the token store at {dataDir}/api-tokens.json.
func New(dataDir string) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		path: filepath.Join(dataDir, "api-tokens.json"),
		toks: map[string]*Token{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s.save()
	}
	if err != nil {
		return err
	}
	var list []*Token
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	for _, t := range list {
		s.toks[t.ID] = t
	}
	return nil
}

func (s *Store) save() error {
	list := make([]*Token, 0, len(s.toks))
	for _, t := range s.toks {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.After(list[j].CreatedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Create issues a new token. The plain text is returned ONCE; the
// returned Token struct only has the hash.
func (s *Store) Create(name, username, role string, scopes []string, ttl time.Duration) (Token, string, error) {
	if name == "" {
		return Token{}, "", errors.New("name is required")
	}
	if ttl <= 0 {
		ttl = 30 * 24 * time.Hour
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, "", err
	}
	full := "wvmb_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(full))
	hash := hex.EncodeToString(sum[:])
	prefix := full[:12] // "wvmb_" + 7 chars

	t := &Token{
		ID:        hash[:16],
		Name:      name,
		Prefix:    prefix,
		Hash:      hash,
		Username:  username,
		Role:      role,
		Scopes:    scopes,
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(ttl),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toks[t.ID] = t
	if err := s.save(); err != nil {
		delete(s.toks, t.ID)
		return Token{}, "", err
	}
	return *t, full, nil
}

// Validate checks the plain token against the store. Returns the token
// (without the hash) on success. Updates LastUsedAt.
func (s *Store) Validate(plain string) (*Token, error) {
	if !strings.HasPrefix(plain, "wvmb_") {
		return nil, errors.New("invalid token")
	}
	sum := sha256.Sum256([]byte(plain))
	hash := hex.EncodeToString(sum[:])
	id := hash[:16]
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.toks[id]
	if !ok || t.Hash != hash {
		return nil, errors.New("invalid token")
	}
	if t.Revoked {
		return nil, errors.New("token revoked")
	}
	if t.IsExpired() {
		return nil, errors.New("token expired")
	}
	t.LastUsedAt = time.Now().UTC()
	// Best-effort persist; we don't fail the request if the file
	// write fails — the in-memory state is correct and the next save
	// will catch it up.
	_ = s.save()
	out := *t
	return &out, nil
}

// List returns all tokens for a user (or all users if username == "").
// Hash is omitted from the returned copies.
func (s *Store) List(username string) []Token {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Token, 0, len(s.toks))
	for _, t := range s.toks {
		if username != "" && t.Username != username {
			continue
		}
		c := *t
		c.Hash = ""
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Revoke marks a token as revoked. Idempotent.
func (s *Store) Revoke(id, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.toks[id]
	if !ok {
		return errors.New("token not found")
	}
	if username != "" && t.Username != username && t.Role != "admin" {
		return errors.New("not the owner")
	}
	t.Revoked = true
	return s.save()
}

// Delete removes a token entirely. Idempotent.
func (s *Store) Delete(id, username string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.toks[id]
	if !ok {
		return nil
	}
	if username != "" && t.Username != username && t.Role != "admin" {
		return errors.New("not the owner")
	}
	delete(s.toks, id)
	return s.save()
}

// PurgeExpired removes expired tokens. Safe to call from a timer.
func (s *Store) PurgeExpired() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, t := range s.toks {
		if t.IsExpired() {
			delete(s.toks, id)
			n++
		}
	}
	if n == 0 {
		return 0, nil
	}
	return n, s.save()
}
