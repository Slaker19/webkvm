package firewall

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// MaxClusterProfileBytes caps what the SSH receiver accepts. A profile is a
// handful of CIDRs; anything larger is a mistake or an attack.
const MaxClusterProfileBytes = 256 << 10

// SyncState is the per-node record of what was last applied. It lives next
// to the profile so the UI and the HA readiness check can compare nodes
// without contacting them.
type SyncState struct {
	Version       int64     `json:"version"`
	SHA256        string    `json:"sha256"`
	AppliedAt     time.Time `json:"applied_at,omitempty"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
}

// SyncHooks let tests exercise the Safe-Apply path without touching nft.
type SyncHooks struct {
	Check        func(path string) error
	Apply        func(path string) error
	TablePresent func() bool
}

// DefaultSyncHooks validates with `nft -c -f` and applies with `nft -f`.
func DefaultSyncHooks() SyncHooks {
	return SyncHooks{
		Check: func(path string) error { return runNft("-c", "-f", path) },
		Apply: func(path string) error { return runNft("-f", path) },
		TablePresent: func() bool {
			return exec.Command("nft", "list", "table", "inet", "webkvm_cluster").Run() == nil
		},
	}
}

func runNft(args ...string) error {
	out, err := exec.Command("nft", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// SyncStatePath returns the state file that belongs to a profile path.
func SyncStatePath(profilePath string) string {
	return strings.TrimSuffix(profilePath, filepath.Ext(profilePath)) + ".state.json"
}

// LoadSyncState reads the state file. Missing or corrupt state means "nothing
// applied yet": the next sync will apply the file.
func LoadSyncState(path string) SyncState {
	var state SyncState
	data, err := os.ReadFile(path)
	if err != nil {
		return SyncState{}
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return SyncState{}
	}
	return state
}

func saveSyncState(path string, state SyncState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o600)
}

// assertManagementAccess is a belt-and-braces check on the rendered ruleset:
// the management CIDRs must be non-empty and SSH/web must still be accepted
// from them. A future renderer regression must fail closed here instead of
// locking the operator out of the host.
func assertManagementAccess(p ClusterProfile, ruleset string) error {
	if len(p.ManagementCIDRs) == 0 {
		return errors.New("management_cidrs is empty: refusing to apply")
	}
	want := fmt.Sprintf("ip saddr @mgmt tcp dport { %d, %d } accept", p.SSHPort, p.WebPort)
	if !strings.Contains(ruleset, want) {
		return fmt.Errorf("rendered ruleset does not preserve management SSH/web access (%q missing)", want)
	}
	return nil
}

// SyncClusterProfile validates and applies the canonical profile. The
// version must be newer than the last applied one unless forceRollback is
// set. `nft -c` runs first; a failure leaves the live table untouched and
// records LastError for the UI.
func SyncClusterProfile(profilePath string, forceRollback bool, hooks SyncHooks) (SyncState, error) {
	file, err := LoadClusterProfileFileStrict(profilePath)
	if err != nil {
		return SyncState{}, err
	}
	statePath := SyncStatePath(profilePath)
	state := LoadSyncState(statePath)
	now := time.Now().UTC()

	if !file.Profile.Enabled {
		// Disabled is the default. The clone must not fail (and take the
		// cluster degraded) just because the admin has not enabled the
		// firewall yet: do nothing and leave the live table untouched.
		state.LastAttemptAt = now
		_ = saveSyncState(statePath, state)
		return state, nil
	}

	same := file.Version == state.Version && file.SHA256 == state.SHA256
	if !forceRollback {
		if file.Version < state.Version {
			return state, fmt.Errorf("profile version %d is older than the applied version %d", file.Version, state.Version)
		}
		if file.Version == state.Version && !same {
			return state, fmt.Errorf("profile version %d is already applied with a different hash", file.Version)
		}
		if same && state.Version > 0 {
			// Boot-time resync: the file did not change, but the kernel
			// table may have been lost by a reboot.
			if hooks.TablePresent != nil && hooks.TablePresent() {
				return state, nil
			}
		}
	}

	ruleset, err := RenderClusterRuleset(file.Profile)
	if err != nil {
		return failSync(statePath, state, now, err)
	}
	if err := assertManagementAccess(file.Profile, ruleset); err != nil {
		return failSync(statePath, state, now, err)
	}

	tmp, err := os.CreateTemp("", "webkvm-cluster-sync-*.nft")
	if err != nil {
		return failSync(statePath, state, now, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(ruleset); err != nil {
		tmp.Close()
		return failSync(statePath, state, now, err)
	}
	if err := tmp.Close(); err != nil {
		return failSync(statePath, state, now, err)
	}

	if err := hooks.Check(tmpPath); err != nil {
		return failSync(statePath, state, now, fmt.Errorf("nft check failed, live table untouched: %w", err))
	}
	if err := hooks.Apply(tmpPath); err != nil {
		return failSync(statePath, state, now, err)
	}

	state.Version = file.Version
	state.SHA256 = file.SHA256
	state.AppliedAt = now
	state.LastAttemptAt = now
	state.LastError = ""
	if err := saveSyncState(statePath, state); err != nil {
		return state, err
	}
	return state, nil
}

func failSync(statePath string, state SyncState, now time.Time, cause error) (SyncState, error) {
	state.LastAttemptAt = now
	state.LastError = cause.Error()
	_ = saveSyncState(statePath, state)
	return state, cause
}

// ReceiveClusterProfile reads a profile from stdin (the SSH forced command),
// rejects oversized or older payloads before touching the file, keeps the
// previous profile as .prev and syncs locally.
func ReceiveClusterProfile(profilePath string, r io.Reader, hooks SyncHooks) (SyncState, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxClusterProfileBytes+1))
	if err != nil {
		return SyncState{}, err
	}
	if len(data) > MaxClusterProfileBytes {
		return SyncState{}, fmt.Errorf("profile exceeds the %d-byte limit", MaxClusterProfileBytes)
	}
	file, err := parseClusterProfileFileStrict(data)
	if err != nil {
		return SyncState{}, err
	}
	if file.Version < 1 {
		return SyncState{}, errors.New("received profile has no version")
	}

	statePath := SyncStatePath(profilePath)
	state := LoadSyncState(statePath)
	if file.Version < state.Version {
		return state, fmt.Errorf("received version %d is older than the applied version %d", file.Version, state.Version)
	}
	if file.Version == state.Version && file.SHA256 != state.SHA256 {
		return state, fmt.Errorf("received version %d conflicts with the applied hash", file.Version)
	}
	if file.Version > state.Version {
		if current, rerr := os.ReadFile(profilePath); rerr == nil {
			if werr := writeFileAtomic(profilePath+".prev", current, 0o600); werr != nil {
				return state, werr
			}
		}
		if err := writeFileAtomic(profilePath, data, 0o600); err != nil {
			return state, err
		}
	}
	return SyncClusterProfile(profilePath, false, hooks)
}
