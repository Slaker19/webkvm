package libvirt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The five pool purposes. A pool has exactly ONE: a disk that serves
// several purposes gets one independent pool per purpose.
//
// These mirror compute.PoolPurpose* by value; this package can't
// import internal/compute (compute imports libvirt), so the values —
// not the identifiers — are the contract. They are the strings
// persisted in pool-purposes.json, so they must never change.
const (
	PoolPurposeDisk      = "disk"
	PoolPurposeISO       = "iso"
	PoolPurposeContainer = "container"
	PoolPurposeBackup    = "backup"
	PoolPurposeTemplate  = "template"
)

// PoolPurposeStore persists the intended use (disk/iso/container/
// backup/template) of libvirt storage pools.
type PoolPurposeStore struct {
	path string
	mu   sync.RWMutex
	data map[string]string
	// saveMu serializes the file replacement itself, so concurrent
	// Set/Delete calls cannot interleave their writes.
	saveMu sync.Mutex
}

func NewPoolPurposeStore(dataDir string) *PoolPurposeStore {
	return &PoolPurposeStore{
		path: filepath.Join(dataDir, "pool-purposes.json"),
		data: make(map[string]string),
	}
}

func (s *PoolPurposeStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(b, &s.data)
}

// Save persists the map atomically: tmp -> fsync -> rename. This was
// the ONLY store in the tree still doing a plain os.WriteFile onto the
// live path, which truncates to zero before writing — a crash or power
// cut mid-write left pool-purposes.json empty or half-written. On the
// next start every pool would fall back to InferPoolPurpose(name),
// which answers "disk" for anything it does not recognize: the ISO,
// container, backup and template pools would all silently turn into
// disk pools and vanish from their sections of the UI.
func (s *PoolPurposeStore) Save() error {
	s.mu.RLock()
	b, err := json.MarshalIndent(s.data, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}

	// Serialize writers: Set/Delete drop the lock before calling Save,
	// so two concurrent callers (the init-disk flow registers one pool
	// per folder, back to back) could otherwise reach os.WriteFile at
	// the same time on the same path.
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	if dir, derr := os.Open(filepath.Dir(s.path)); derr == nil {
		_ = dir.Sync()
		dir.Close()
	}
	return nil
}

func (s *PoolPurposeStore) Get(name string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.data[name]; ok {
		return p
	}
	return ""
}

func (s *PoolPurposeStore) Set(name, purpose string) error {
	s.mu.Lock()
	s.data[name] = purpose
	s.mu.Unlock()
	return s.Save()
}

// Delete removes the entry for name. It is a no-op if the entry
// doesn't exist, so callers don't need to check first. The file is
// rewritten only when an entry was actually removed (avoids bumping
// mtime on every save).
func (s *PoolPurposeStore) Delete(name string) error {
	s.mu.Lock()
	if _, ok := s.data[name]; !ok {
		s.mu.Unlock()
		return nil
	}
	delete(s.data, name)
	s.mu.Unlock()
	return s.Save()
}

// purposeSuffixes maps the per-purpose suffix the init-disk flow
// appends to a volume name onto the purpose it denotes. Order
// matters only for readability; lookups are exact-suffix matches.
var purposeSuffixes = []struct {
	suffix  string
	purpose string
}{
	{"-vdi", PoolPurposeDisk},
	{"-discos", PoolPurposeDisk},
	{"-isos", PoolPurposeISO},
	{"-containers", PoolPurposeContainer},
	{"-contenedores", PoolPurposeContainer},
	{"-backups", PoolPurposeBackup},
	{"-plantillas", PoolPurposeTemplate},
	{"-templates", PoolPurposeTemplate},
}

// InferPoolPurpose guesses the pool purpose from its name.
//
// The explicit per-purpose suffixes the init-disk flow generates
// (mydisk-vdi, mydisk-isos, mydisk-containers,
// mydisk-backups, mydisk-plantillas) are matched FIRST and win
// outright. Only then does the loose "contains iso" heuristic run,
// which exists for hand-made pools named "ISOS" or "iso-library".
//
// The order is not cosmetic: "iso" as a substring used to swallow
// every other suffix, so a pool legitimately named
// "isostorage-containers" was classified as an ISO library and its
// containers became invisible. Anything unmatched is a disk pool —
// there is no "general" category.
func InferPoolPurpose(name string) string {
	lower := strings.ToLower(name)
	for _, s := range purposeSuffixes {
		if strings.HasSuffix(lower, s.suffix) {
			return s.purpose
		}
	}
	if strings.Contains(lower, "iso") {
		return PoolPurposeISO
	}
	return PoolPurposeDisk
}
