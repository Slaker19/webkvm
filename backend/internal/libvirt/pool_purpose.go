package libvirt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	PoolPurposeDisk = "disk"
	PoolPurposeISO  = "iso"
)

// PoolPurposeStore persists the intended use (disk/iso) of libvirt storage pools.
type PoolPurposeStore struct {
	path string
	mu   sync.RWMutex
	data map[string]string
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

func (s *PoolPurposeStore) Save() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, b, 0644)
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

// InferPoolPurpose guesses the pool purpose from its name.
// No "general" category: anything not explicitly ISO is treated as disk.
func InferPoolPurpose(name string) string {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "iso") || strings.HasSuffix(lower, "-isos") {
		return PoolPurposeISO
	}
	return PoolPurposeDisk
}
