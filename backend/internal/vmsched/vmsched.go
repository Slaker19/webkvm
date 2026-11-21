// Package vmsched schedules automatic VM power-on/power-off via cron
// expressions. Schedules are persisted to {dataDir}/vm-schedules.json
// and re-registered on startup so they survive service restarts.
//
// Safety: a single shared cron is used; the schedule is re-read from
// the store before firing (no stale closures), and a failure to start
// a VM is logged, never fatal. Cron expressions are validated at write
// time so a bad entry is rejected by the API.
package vmsched

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/robfig/cron/v3"
)

// Schedule is one VM's power and snapshot schedule.
type Schedule struct {
	StartCron    string `json:"start_cron,omitempty"`
	StopCron     string `json:"stop_cron,omitempty"`
	SnapshotCron string `json:"snapshot_cron,omitempty"`
	SnapshotMax  int    `json:"snapshot_max,omitempty"`
}

// Enabled reports whether the schedule has at least one rule.
func (s Schedule) Enabled() bool {
	return s.StartCron != "" || s.StopCron != "" || s.SnapshotCron != ""
}

// Store persists schedules per VM id.
//
// Keys are node-scoped composites ("local/web", "n_ab12/web"): the
// federation clone shares this store across nodes, and two nodes can run
// domains with the same name — a bare key would make node B's schedule
// power node A's VM (and vice versa). Legacy bare keys from older
// versions are re-keyed to the local node by MigrateLegacy.
type Store struct {
	mu   sync.Mutex
	path string
	byVM map[string]Schedule
}

// ScopeKey builds the composite store key for one node's VM.
func ScopeKey(nodeID, vmID string) string { return nodeID + "/" + vmID }

// SplitKey splits a composite key. Returns ok=false for legacy bare keys.
func SplitKey(key string) (nodeID, vmID string, ok bool) {
	i := strings.IndexByte(key, '/')
	if i <= 0 || i == len(key)-1 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// NewStore creates the store (does not touch disk).
func NewStore(dataDir string) *Store {
	return &Store{
		path: filepath.Join(dataDir, "vm-schedules.json"),
		byVM: map[string]Schedule{},
	}
}

// MigrateLegacy re-keys schedules persisted by versions before node
// scoping: a bare "<vm>" key belongs to the local node. Call once after
// Load with the local node's ID; persists the migrated file.
func (s *Store) MigrateLegacy(localNodeID string) {
	if localNodeID == "" {
		return
	}
	s.mu.Lock()
	changed := false
	for key, sch := range s.byVM {
		if _, _, ok := SplitKey(key); ok {
			continue
		}
		delete(s.byVM, key)
		s.byVM[ScopeKey(localNodeID, key)] = sch
		changed = true
	}
	if changed {
		_ = s.saveLocked() // best-effort migration persist; next Save() writes it anyway
	}
	s.mu.Unlock()
}

// Load reads the schedules file. A missing file is fine.
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var file struct {
		Version   int                 `json:"version"`
		Schedules map[string]Schedule `json:"schedules"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	if file.Schedules != nil {
		s.byVM = file.Schedules
	}
	return nil
}

// Save persists atomically.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked writes the file; the caller holds s.mu.
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(map[string]any{
		"version":   1,
		"schedules": s.byVM,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the schedule for one node's VM (empty if none).
func (s *Store) Get(nodeID, vmID string) Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byVM[ScopeKey(nodeID, vmID)]
}

// Set validates and stores a schedule. Empty both clears it.
func (s *Store) Set(nodeID, vmID string, sch Schedule) error {
	if sch.Enabled() {
		if err := Validate(sch); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if sch.Enabled() {
		s.byVM[ScopeKey(nodeID, vmID)] = sch
	} else {
		delete(s.byVM, ScopeKey(nodeID, vmID))
	}
	s.mu.Unlock()
	return s.Save()
}

// All returns every schedule.
func (s *Store) All() map[string]Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Schedule, len(s.byVM))
	for k, v := range s.byVM {
		out[k] = v
	}
	return out
}

// Validate rejects malformed cron expressions.
func Validate(sch Schedule) error {
	for _, expr := range []string{sch.StartCron, sch.StopCron, sch.SnapshotCron} {
		if expr == "" {
			continue
		}
		p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := p.Parse(expr); err != nil {
			return err
		}
	}
	if !sch.Enabled() {
		return errors.New("schedule needs at least a start, stop, or snapshot cron")
	}
	return nil
}

// PowerFunc starts or stops a VM on the node the schedule belongs to.
// Implemented by the caller (libvirt/compute seam).
type PowerFunc func(nodeID, vmID, action string) error

// SnapshotFunc creates an automated snapshot on the owning node and
// prunes older ones.
type SnapshotFunc func(nodeID, vmID string, maxKeep int) error

// Scheduler owns a single cron and (re)registers entries from the
// store. It is safe to call Rebuild repeatedly.
type Scheduler struct {
	store    *Store
	power    PowerFunc
	snapshot SnapshotFunc
	logger   *slog.Logger

	mu   sync.Mutex
	cron *cron.Cron
	// entryIDs keeps the cron entry IDs so Rebuild can remove them.
	entryIDs map[string]cron.EntryID // vmID -> last start entry; stop stored separately
}

// NewScheduler builds a scheduler. power is called with ("start"|"stop").
func NewScheduler(store *Store, power PowerFunc, snapshot SnapshotFunc, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		store:    store,
		power:    power,
		snapshot: snapshot,
		logger:   logger,
		cron:     cron.New(),
		entryIDs: map[string]cron.EntryID{},
	}
}

// Start boots the scheduler and registers all schedules.
//
// Rebuild already starts the cron it builds, so this only needs to
// register. Reading s.cron here without the mutex raced with the
// assignment inside Rebuild.
func (s *Scheduler) Start() {
	s.Rebuild()
}

// Stop halts the scheduler.
//
// s.cron is REPLACED by Rebuild (a schedule edit from the API), so
// reading the field without the mutex was a genuine data race, caught
// by `go test -race`: Stop could read a half-assigned pointer and stop
// a cron that was already being discarded, leaving the live one
// running. In practice that meant "disable the scheduler" appeared to
// work while VMs kept being powered on and snapshotted.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	c := s.cron
	s.mu.Unlock()
	if c != nil {
		c.Stop()
	}
}

// Rebuild drops all entries and re-registers from the store. Call after
// any schedule change.
func (s *Scheduler) Rebuild() {
	s.mu.Lock()
	defer s.mu.Unlock()
	stopped := s.cron.Stop()
	if stopped != nil {
		<-stopped.Done()
	}
	s.cron = cron.New()
	for key, sch := range s.store.All() {
		s.addLocked(key, sch)
	}
	s.cron.Start()
}

func (s *Scheduler) addLocked(key string, sch Schedule) {
	nodeID, vmID, ok := SplitKey(key)
	if !ok {
		// Un-scoped entry: MigrateLegacy re-keys these; firing a bare key
		// would target an ambiguous VM, so it is skipped and logged.
		s.logger.Warn("vmsched_unscoped_key_skipped", "key", key)
		return
	}
	if sch.StartCron != "" {
		if _, err := s.cron.AddFunc(sch.StartCron, s.fire(nodeID, vmID, "start")); err != nil {
			s.logger.Warn("vmsched_start_cron_invalid", "vm", vmID, "err", err)
		}
	}
	if sch.StopCron != "" {
		if _, err := s.cron.AddFunc(sch.StopCron, s.fire(nodeID, vmID, "stop")); err != nil {
			s.logger.Warn("vmsched_stop_cron_invalid", "vm", vmID, "err", err)
		}
	}
	if sch.SnapshotCron != "" {
		maxKeep := sch.SnapshotMax
		if maxKeep <= 0 {
			maxKeep = 3
		}
		if _, err := s.cron.AddFunc(sch.SnapshotCron, s.fireSnapshot(nodeID, vmID, maxKeep)); err != nil {
			s.logger.Warn("vmsched_snapshot_cron_invalid", "vm", vmID, "err", err)
		}
	}
}

// fire returns a closure that re-reads the current schedule before
// acting (defence in depth against stale registrations) and runs the
// power action best-effort.
func (s *Scheduler) fire(nodeID, vmID, action string) func() {
	return func() {
		cur := s.store.Get(nodeID, vmID)
		if !cur.Enabled() {
			return // schedule was cleared after registration
		}
		if s.power == nil {
			return
		}
		if err := s.power(nodeID, vmID, action); err != nil {
			s.logger.Warn("vmsched_power_failed", "node", nodeID, "vm", vmID, "action", action, "err", err)
		} else {
			s.logger.Info("vmsched_power", "node", nodeID, "vm", vmID, "action", action)
		}
	}
}

func (s *Scheduler) fireSnapshot(nodeID, vmID string, maxKeep int) func() {
	return func() {
		cur := s.store.Get(nodeID, vmID)
		if !cur.Enabled() || cur.SnapshotCron == "" {
			return
		}
		if s.snapshot == nil {
			return
		}
		if err := s.snapshot(nodeID, vmID, maxKeep); err != nil {
			s.logger.Warn("vmsched_snapshot_failed", "node", nodeID, "vm", vmID, "err", err)
		} else {
			s.logger.Info("vmsched_snapshot_success", "node", nodeID, "vm", vmID)
		}
	}
}
