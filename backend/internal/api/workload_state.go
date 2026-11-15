package api

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// workloadState is what WebKVM remembers about one workload, keyed by
// (node, hv, id). ha_enabled / ha_state are reserved for the HA wave.
type workloadState struct {
	Managed   bool   `json:"managed"`
	HAEnabled *bool  `json:"ha_enabled,omitempty"`
	HAState   string `json:"ha_state,omitempty"`
}

// workloadStore persists workload state in DATA_DIR/workload-state.json,
// written atomically. A workload with no record is managed (default true).
type workloadStore struct {
	path string
	mu   sync.Mutex
}

func newWorkloadStore(dataDir string) *workloadStore {
	return &workloadStore{path: filepath.Join(dataDir, "workload-state.json")}
}

func (h *Handler) workloadStore() *workloadStore {
	sharedInitMu.Lock()
	defer sharedInitMu.Unlock()
	if h.workloads == nil {
		dir := ""
		if h.cfg != nil {
			dir = h.cfg.DataDir
		}
		h.workloads = newWorkloadStore(dir)
	}
	return h.workloads
}

// workloadKey normalizes the (node, hv, id) triple. The local node is "".
func workloadKey(node, hv, id string) string {
	if isLocalNode(node) {
		node = "local"
	}
	if hv == "" {
		hv = "kvm"
	}
	return node + "|" + hv + "|" + id
}

func (s *workloadStore) load() (map[string]workloadState, error) {
	m := map[string]workloadState{}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return m, nil
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// Managed reports the managed flag of a workload (true when unrecorded).
func (s *workloadStore) Managed(node, hv, id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return true
	}
	st, ok := m[workloadKey(node, hv, id)]
	return !ok || st.Managed
}

// all returns a snapshot of every record (for listings).
func (s *workloadStore) all() map[string]workloadState {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return map[string]workloadState{}
	}
	return m
}

// SetManaged records the managed flag, keeping any other recorded fields.
func (s *workloadStore) SetManaged(node, hv, id string, managed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.load()
	if err != nil {
		return err
	}
	k := workloadKey(node, hv, id)
	st := m[k]
	st.Managed = managed
	m[k] = st
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".workload-state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.path)
}
