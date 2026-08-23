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
	"sync"

	"github.com/robfig/cron/v3"
)

// Schedule is one VM's power schedule.
type Schedule struct {
	StartCron string `json:"start_cron,omitempty"`
	StopCron  string `json:"stop_cron,omitempty"`
}

// Enabled reports whether the schedule has at least one rule.
func (s Schedule) Enabled() bool {
	return s.StartCron != "" || s.StopCron != ""
}

// Store persists schedules per VM id.
type Store struct {
	mu   sync.Mutex
	path string
	byVM map[string]Schedule
}

// NewStore creates the store (does not touch disk).
func NewStore(dataDir string) *Store {
	return &Store{
		path: filepath.Join(dataDir, "vm-schedules.json"),
		byVM: map[string]Schedule{},
	}
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
		Version  int                `json:"version"`
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

// Get returns the schedule for a VM (empty if none).
func (s *Store) Get(vmID string) Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byVM[vmID]
}

// Set validates and stores a schedule. Empty both clears it.
func (s *Store) Set(vmID string, sch Schedule) error {
	if sch.Enabled() {
		if err := Validate(sch); err != nil {
			return err
		}
	}
	s.mu.Lock()
	if sch.Enabled() {
		s.byVM[vmID] = sch
	} else {
		delete(s.byVM, vmID)
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
	for _, expr := range []string{sch.StartCron, sch.StopCron} {
		if expr == "" {
			continue
		}
		p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := p.Parse(expr); err != nil {
			return err
		}
	}
	if !sch.Enabled() {
		return errors.New("schedule needs at least a start or stop cron")
	}
	return nil
}

// PowerFunc starts or stops a VM. Implemented by the caller (libvirt).
type PowerFunc func(vmID, action string) error

// Scheduler owns a single cron and (re)registers entries from the
// store. It is safe to call Rebuild repeatedly.
type Scheduler struct {
	store  *Store
	power  PowerFunc
	logger *slog.Logger

	mu   sync.Mutex
	cron *cron.Cron
	// entryIDs keeps the cron entry IDs so Rebuild can remove them.
	entryIDs map[string]cron.EntryID // vmID -> last start entry; stop stored separately
}

// NewScheduler builds a scheduler. power is called with ("start"|"stop").
func NewScheduler(store *Store, power PowerFunc, logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{
		store:    store,
		power:    power,
		logger:   logger,
		cron:     cron.New(),
		entryIDs: map[string]cron.EntryID{},
	}
}

// Start boots the scheduler and registers all schedules.
func (s *Scheduler) Start() {
	s.Rebuild()
	s.cron.Start()
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.cron.Stop()
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
	for vmID, sch := range s.store.All() {
		s.addLocked(vmID, sch)
	}
	s.cron.Start()
}

func (s *Scheduler) addLocked(vmID string, sch Schedule) {
	if sch.StartCron != "" {
		if _, err := s.cron.AddFunc(sch.StartCron, s.fire(vmID, "start")); err != nil {
			s.logger.Warn("vmsched_start_cron_invalid", "vm", vmID, "err", err)
		}
	}
	if sch.StopCron != "" {
		if _, err := s.cron.AddFunc(sch.StopCron, s.fire(vmID, "stop")); err != nil {
			s.logger.Warn("vmsched_stop_cron_invalid", "vm", vmID, "err", err)
		}
	}
}

// fire returns a closure that re-reads the current schedule before
// acting (defence in depth against stale registrations) and runs the
// power action best-effort.
func (s *Scheduler) fire(vmID, action string) func() {
	return func() {
		cur := s.store.Get(vmID)
		if !cur.Enabled() {
			return // schedule was cleared after registration
		}
		if s.power == nil {
			return
		}
		if err := s.power(vmID, action); err != nil {
			s.logger.Warn("vmsched_power_failed", "vm", vmID, "action", action, "err", err)
		} else {
			s.logger.Info("vmsched_power", "vm", vmID, "action", action)
		}
	}
}
