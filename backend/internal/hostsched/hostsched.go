// Package hostsched schedules automatic host package upgrades via a single
// cron expression. The config is persisted to {dataDir}/host-schedule.json
// and re-registered on startup so it survives service restarts.
//
// A scheduled upgrade is best-effort: the run callback is invoked on a
// separate goroutine, and any failure is logged, never fatal.
package hostsched

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/robfig/cron/v3"
)

// Config is the persisted host-update schedule.
type Config struct {
	Enabled     bool   `json:"enabled"`
	UpgradeCron string `json:"upgrade_cron,omitempty"` // e.g. "0 3 * * 0" (Sun 03:00)
	RebootAfter bool   `json:"reboot_after,omitempty"` // reboot the host after a successful upgrade
}

// Validate rejects a malformed cron expression or an enabled schedule with
// no expression.
func Validate(cfg Config) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.UpgradeCron == "" {
		return errors.New("schedule needs an upgrade_cron expression when enabled")
	}
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	if _, err := p.Parse(cfg.UpgradeCron); err != nil {
		return err
	}
	return nil
}

// Store persists the schedule config.
type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// NewStore creates the store (does not touch disk).
func NewStore(dataDir string) *Store {
	return &Store{path: filepath.Join(dataDir, "host-schedule.json")}
}

// Load reads the schedule file. A missing file is fine.
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
	return json.Unmarshal(data, &s.cfg)
}

// Save persists atomically.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(s.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the current config.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Set validates and persists the config.
func (s *Store) Set(cfg Config) error {
	if err := Validate(cfg); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return s.Save()
}

// Scheduler owns a single cron that fires the upgrade callback on schedule.
type Scheduler struct {
	store  *Store
	run    func() // performs the host upgrade (already guarded by the caller)
	logger *slog.Logger

	mu   sync.Mutex
	cron *cron.Cron
}

// NewScheduler builds a scheduler. run is invoked when the schedule fires.
func NewScheduler(store *Store, run func(), logger *slog.Logger) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{store: store, run: run, logger: logger, cron: cron.New()}
}

// Start boots the scheduler and registers the schedule if enabled.
func (s *Scheduler) Start() { s.Rebuild() }

// SetRun replaces the run callback and re-registers. Needed because the
// callback (h.runScheduledUpgrade) is only available after the Handler is
// constructed, while the scheduler is created earlier at startup.
func (s *Scheduler) SetRun(fn func()) {
	s.mu.Lock()
	s.run = fn
	s.mu.Unlock()
	s.Rebuild()
}

// Stop halts the scheduler.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	c := s.cron
	s.mu.Unlock()
	if c != nil {
		c.Stop()
	}
}

// Rebuild drops the existing entry and re-registers from the store. Call
// after any config change.
func (s *Scheduler) Rebuild() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron != nil {
		s.cron.Stop()
	}
	s.cron = cron.New()
	cfg := s.store.Get()
	if cfg.Enabled && cfg.UpgradeCron != "" && s.run != nil {
		if _, err := s.cron.AddFunc(cfg.UpgradeCron, s.run); err != nil {
			s.logger.Warn("hostsched_cron_invalid", "err", err)
		}
	}
	s.cron.Start()
}
