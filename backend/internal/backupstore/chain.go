// Package backupstore — incremental backup chain tracking.
//
// A Chain records the sequence of libvirt checkpoints and their
// corresponding backup files for a single VM on a single target.
// The chain is the unit of retention: pruning a chain removes the
// base backup and all its increments atomically.
//
// Persistence: one JSON file per target at
// {dataDir}/backup/chains-{targetID}.json, using the same
// versioned-envelope + atomic-write pattern as the other store
// files (see store.go saveJSON).
package backupstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// CheckpointEntry is one point in a backup chain.
type CheckpointEntry struct {
	Name       string            `json:"name"`                 // libvirt checkpoint name (chk-<epoch>-<rand>)
	JobID      string            `json:"job_id"`               // backup job that produced this entry
	BackupFile string            `json:"backup_file"`          // primary filename on the target (webkvm-…-qcow2)
	DiskFiles  map[string]string `json:"disk_files,omitempty"` // device -> backup filename (for multi-disk)
	Parent     string            `json:"parent"`               // parent checkpoint name ("" = base)
	Mode       string            `json:"mode"`                 // "full" | "incremental"
	SizeBytes  int64             `json:"size_bytes"`
	CreatedAt  time.Time         `json:"created_at"`
}

// Chain is the full backup history for one VM on one target.
type Chain struct {
	VMID       string            `json:"vm_id"`
	VMName     string            `json:"vm_name,omitempty"`
	TargetID   string            `json:"target_id"`
	BaseFile   string            `json:"base_file"`            // first backup file in the chain
	Checkpoint string            `json:"checkpoint,omitempty"` // latest libvirt checkpoint name
	Entries    []CheckpointEntry `json:"entries"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// chainsFile is the on-disk envelope for all chains of one target.
type chainsFile struct {
	Version int      `json:"version"`
	Chains  []*Chain `json:"chains"`
}

// ChainStore manages chain persistence for a single target.
// Safe for concurrent use.
type ChainStore struct {
	path string
	mu   sync.Mutex
}

// NewChainStore opens (or creates) the chain file for the given target.
func NewChainStore(dataDir, targetID string) *ChainStore {
	dir := filepath.Join(dataDir, "backup")
	_ = os.MkdirAll(dir, 0o755)
	return &ChainStore{
		path: filepath.Join(dir, fmt.Sprintf("chains-%s.json", targetID)),
	}
}

// Load reads all chains from disk. A missing file is not an error.
func (cs *ChainStore) Load() ([]*Chain, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	data, err := os.ReadFile(cs.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var cf chainsFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parse %s: %w", cs.path, err)
	}

	// Defensive: ensure Entries is never nil so callers can range
	// without nil checks.
	for _, c := range cf.Chains {
		if c.Entries == nil {
			c.Entries = []CheckpointEntry{}
		}
	}

	return cf.Chains, nil
}

// Save writes all chains to disk atomically.
func (cs *ChainStore) Save(chains []*Chain) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(cs.path), 0o755); err != nil {
		return err
	}

	cf := chainsFile{Version: 1, Chains: chains}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}

	tmp := cs.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, cs.path)
}

// AppendEntry adds a checkpoint entry to the chain for vmID.
// If no chain exists yet, a new one is created.
// Returns the updated chain.
func (cs *ChainStore) AppendEntry(vmID, targetID, vmName string, entry CheckpointEntry) (*Chain, error) {
	chains, err := cs.Load()
	if err != nil {
		return nil, err
	}

	var chain *Chain
	for _, c := range chains {
		if c.VMID == vmID {
			chain = c
			break
		}
	}

	now := time.Now().UTC()
	if chain == nil {
		chain = &Chain{
			VMID:      vmID,
			VMName:    vmName,
			TargetID:  targetID,
			CreatedAt: now,
		}
		chains = append(chains, chain)
	}

	chain.Entries = append(chain.Entries, entry)
	chain.Checkpoint = entry.Name
	chain.UpdatedAt = now

	if chain.BaseFile == "" {
		chain.BaseFile = entry.BackupFile
	}

	if err := cs.Save(chains); err != nil {
		return nil, err
	}
	return chain, nil
}

// GetChain returns the chain for vmID, or nil if none exists.
func (cs *ChainStore) GetChain(vmID string) (*Chain, error) {
	chains, err := cs.Load()
	if err != nil {
		return nil, err
	}
	for _, c := range chains {
		if c.VMID == vmID {
			return c, nil
		}
	}
	return nil, nil
}

// LatestCheckpoint returns the most recent checkpoint name for vmID,
// or "" if the chain is empty.
func (cs *ChainStore) LatestCheckpoint(vmID string) (string, error) {
	chain, err := cs.GetChain(vmID)
	if err != nil {
		return "", err
	}
	if chain == nil || len(chain.Entries) == 0 {
		return "", nil
	}
	return chain.Checkpoint, nil
}

// DeleteChain removes the entire chain for vmID.
// Returns true if a chain was deleted.
func (cs *ChainStore) DeleteChain(vmID string) (bool, error) {
	chains, err := cs.Load()
	if err != nil {
		return false, err
	}

	filtered := make([]*Chain, 0, len(chains))
	deleted := false
	for _, c := range chains {
		if c.VMID == vmID {
			deleted = true
			continue
		}
		filtered = append(filtered, c)
	}

	if !deleted {
		return false, nil
	}

	return true, cs.Save(filtered)
}

// FlattenOrder returns the ordered list of backup files needed to
// restore the chain up to and including the given checkpoint.
// The order is base → increment-1 → … → target, ready for
// `qemu-img rebase` + `qemu-img convert`.
// If checkpoint is "", the latest entry is used.
// OrderedEntries returns the lineage of entries from base to target checkpoint.
func (c *Chain) OrderedEntries(checkpoint string) []CheckpointEntry {
	if len(c.Entries) == 0 {
		return nil
	}

	target := checkpoint
	if target == "" {
		target = c.Checkpoint
	}

	// Build a name→entry map for O(1) lookup.
	byName := make(map[string]CheckpointEntry, len(c.Entries))
	for _, e := range c.Entries {
		byName[e.Name] = e
	}

	// Walk backwards from target to base, then reverse.
	var ordered []CheckpointEntry
	current := target
	for current != "" {
		entry, ok := byName[current]
		if !ok {
			break
		}
		ordered = append(ordered, entry)
		current = entry.Parent
	}

	// Reverse to get base-first order.
	for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
		ordered[i], ordered[j] = ordered[j], ordered[i]
	}
	return ordered
}

func (c *Chain) FlattenOrder(checkpoint string) []string {
	ordered := c.OrderedEntries(checkpoint)
	files := make([]string, len(ordered))
	for i, e := range ordered {
		files[i] = e.BackupFile
	}
	return files
}

// AllFiles returns every backup file in the chain (base + increments).
func (c *Chain) AllFiles() []string {
	var files []string
	seen := make(map[string]bool)
	for _, e := range c.Entries {
		if e.BackupFile != "" && !seen[e.BackupFile] {
			seen[e.BackupFile] = true
			files = append(files, e.BackupFile)
		}
		for _, f := range e.DiskFiles {
			if f != "" && !seen[f] {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return files
}

// TotalSize returns the sum of all entry sizes.
func (c *Chain) TotalSize() int64 {
	var total int64
	for _, e := range c.Entries {
		total += e.SizeBytes
	}
	return total
}

// SortChainsByUpdatedAt sorts chains newest-first (helper for UI).
func SortChainsByUpdatedAt(chains []*Chain) {
	sort.Slice(chains, func(i, j int) bool {
		return chains[i].UpdatedAt.After(chains[j].UpdatedAt)
	})
}
