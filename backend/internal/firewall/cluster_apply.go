package firewall

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ClusterManager owns the cluster-wide default-deny table and its
// Safe-Apply window. It lives on each node (the CLI runs it locally), so the
// same profile produces the same table on all three members.
type ClusterManager struct {
	mu sync.Mutex

	path             string
	profile          ClusterProfile
	version          int64
	profileHash      string
	confirmedRuleset string
	pending          *clusterPending

	logger        *slog.Logger
	rollbackAfter time.Duration

	// injectable for tests
	applyFn func(string) error
	flushFn func() error
	readFn  func() string
}

type clusterPending struct {
	prev     string
	next     string
	profile  ClusterProfile
	deadline time.Time
	timer    *time.Timer
}

// NewClusterManager loads the profile at path (default disabled) and wires
// the real nftables applier.
func NewClusterManager(path string, logger *slog.Logger) (*ClusterManager, error) {
	if logger == nil {
		logger = slog.Default()
	}
	file, err := LoadClusterProfileFile(path)
	if err != nil {
		return nil, err
	}
	m := &ClusterManager{
		path:          path,
		profile:       file.Profile,
		version:       file.Version,
		profileHash:   file.SHA256,
		logger:        logger,
		rollbackAfter: 30 * time.Second,
	}
	m.applyFn = applyClusterRuleset
	m.flushFn = flushClusterTable
	m.readFn = readClusterTable
	return m, nil
}

// SetSafeHooks replaces the nftables callbacks. Tests use it to exercise the
// Safe-Apply flow without touching the host firewall.
func (m *ClusterManager) SetSafeHooks(apply func(string) error, flush func() error, read func() string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if apply != nil {
		m.applyFn = apply
	}
	if flush != nil {
		m.flushFn = flush
	}
	if read != nil {
		m.readFn = read
	}
}

// Profile returns the persisted profile.
func (m *ClusterManager) Profile() ClusterProfile {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.profile
}

// SaveProfile persists a profile without applying it.
func (m *ClusterManager) SaveProfile(p ClusterProfile) error {
	file, err := SaveClusterProfile(m.path, p)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.profile = file.Profile
	m.version = file.Version
	m.profileHash = file.SHA256
	m.mu.Unlock()
	return nil
}

// ProfileFilePath is the canonical per-node profile path.
func (m *ClusterManager) ProfileFilePath() string { return m.path }

// FileMeta returns the published version and hash (0/empty when the admin
// has never confirmed a profile).
func (m *ClusterManager) FileMeta() (int64, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.version, m.profileHash
}

// Snapshot returns the canonical file bytes for distribution to peers.
func (m *ClusterManager) Snapshot() ([]byte, error) {
	return os.ReadFile(m.path)
}

// Preview renders the ruleset for the given profile without applying it.
func (m *ClusterManager) Preview(p ClusterProfile) (string, error) {
	return RenderClusterRuleset(p)
}

// Status reports the profile, the pending deadline (if any) and the hash of
// the last confirmed ruleset.
func (m *ClusterManager) Status() (ClusterProfile, time.Time, bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deadline := time.Time{}
	pending := m.pending != nil
	if pending {
		deadline = m.pending.deadline
	}
	return m.profile, deadline, pending, rulesetHash(m.confirmedRuleset)
}

// Stage validates and applies p, and schedules an automatic rollback after
// the window unless Confirm is called.
func (m *ClusterManager) Stage(p ClusterProfile) (time.Time, error) {
	if !p.Enabled {
		return time.Time{}, ErrClusterFirewallDisabled
	}
	ruleset, err := RenderClusterRuleset(p)
	if err != nil {
		return time.Time{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil {
		return time.Time{}, errors.New("a cluster firewall apply is already pending; confirm or roll it back first")
	}
	prev := m.confirmedRuleset
	if err := m.applyFn(ruleset); err != nil {
		return time.Time{}, err
	}
	deadline := time.Now().Add(m.rollbackAfter)
	m.pending = &clusterPending{
		prev:     prev,
		next:     ruleset,
		profile:  normalizeClusterProfile(p),
		deadline: deadline,
	}
	m.pending.timer = time.AfterFunc(m.rollbackAfter, func() {
		rolled, rerr := m.Rollback()
		if rerr != nil {
			m.logger.Error("cluster_firewall_auto_rollback_failed", "err", rerr)
			return
		}
		if rolled {
			m.logger.Warn("cluster_firewall_timeout_rolled_back")
		}
	})
	m.logger.Info("cluster_firewall_staged", "deadline", deadline)
	return deadline, nil
}

// Confirm persists the staged profile as the new baseline.
func (m *ClusterManager) Confirm() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		return ErrNoPendingClusterApply
	}
	if m.pending.timer != nil {
		m.pending.timer.Stop()
	}
	next := m.pending.next
	profile := m.pending.profile
	m.confirmedRuleset = next
	m.pending = nil
	file, err := SaveClusterProfile(m.path, profile)
	if err != nil {
		return err
	}
	m.profile = file.Profile
	m.version = file.Version
	m.profileHash = file.SHA256
	m.logger.Info("cluster_firewall_confirmed")
	return nil
}

// Rollback restores the previously confirmed ruleset (or deletes the table
// when there was none). Returns whether there was a pending apply.
func (m *ClusterManager) Rollback() (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending == nil {
		return false, nil
	}
	if m.pending.timer != nil {
		m.pending.timer.Stop()
	}
	prev := m.pending.prev
	m.pending = nil
	if err := m.restore(prev); err != nil {
		return true, err
	}
	m.logger.Info("cluster_firewall_rolled_back")
	return true, nil
}

func (m *ClusterManager) restore(ruleset string) error {
	if strings.TrimSpace(ruleset) == "" {
		return m.flushFn()
	}
	return m.applyFn(ruleset)
}

// ApplyInteractive is the CLI flow: apply, then wait up to timeout for the
// operator to run `confirm` (confirmPath) or `rollback` (rollbackPath). If
// neither appears, the previous ruleset is restored. It blocks the CLI on
// purpose so the 30s window survives across separate command invocations.
func (m *ClusterManager) ApplyInteractive(p ClusterProfile, confirmPath, rollbackPath string, timeout time.Duration) error {
	m.RefreshBaseline()
	deadline, err := m.Stage(p)
	if err != nil {
		return err
	}
	_ = os.Remove(confirmPath)
	_ = os.Remove(rollbackPath)
	if timeout <= 0 {
		timeout = time.Until(deadline)
	}
	end := time.Now().Add(timeout)
	for time.Now().Before(end) {
		if _, err := os.Stat(confirmPath); err == nil {
			if err := m.Confirm(); err != nil {
				return err
			}
			_ = os.Remove(confirmPath)
			return nil
		}
		if _, err := os.Stat(rollbackPath); err == nil {
			_, rerr := m.Rollback()
			_ = os.Remove(rollbackPath)
			if rerr != nil {
				return fmt.Errorf("manual rollback failed: %w", rerr)
			}
			return errors.New("rollback requested")
		}
		time.Sleep(250 * time.Millisecond)
	}
	_, rerr := m.Rollback()
	if rerr != nil {
		return fmt.Errorf("not confirmed and rollback failed: %w", rerr)
	}
	return errors.New("not confirmed within the window; previous ruleset restored")
}

// TableApplied reports whether the cluster table is currently loaded.
func (m *ClusterManager) TableApplied() bool {
	return exec.Command("nft", "list", "table", "inet", "webkvm_cluster").Run() == nil
}

// RefreshBaseline reads the live table so an unconfirmed re-apply rolls back
// to what is actually running instead of deleting the table.
func (m *ClusterManager) RefreshBaseline() {
	if m.readFn == nil {
		return
	}
	live := m.readFn()
	m.mu.Lock()
	m.confirmedRuleset = live
	m.mu.Unlock()
}

func readClusterTable() string {
	out, err := exec.Command("nft", "list", "table", "inet", "webkvm_cluster").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func rulesetHash(ruleset string) string {
	if ruleset == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ruleset))
	return hex.EncodeToString(sum[:8])
}

func applyClusterRuleset(ruleset string) error {
	f, err := os.CreateTemp("", "webkvm-cluster-*.nft")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString(ruleset); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if out, err := exec.Command("nft", "-c", "-f", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("nft check failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("nft", "-f", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("nft apply failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func flushClusterTable() error {
	out, err := exec.Command("nft", "delete", "table", "inet", "webkvm_cluster").CombinedOutput()
	if err != nil {
		msg := strings.ToLower(string(out))
		if strings.Contains(msg, "no such file") || strings.Contains(msg, "does not exist") {
			return nil
		}
		return fmt.Errorf("nft flush failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
