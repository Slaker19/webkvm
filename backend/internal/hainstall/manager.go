package hainstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"webkvm/internal/sysutil"
)

// ErrBusy is returned when an install or uninstall is already running.
var ErrBusy = errors.New("an install or uninstall is already running")

const (
	logLimit     = 300
	stepTimeout  = 20 * time.Minute
	probeTimeout = 60 * time.Second
)

// persistedState is the on-disk format. The file lives in the shared
// DATA_DIR (DRBD), but an install is per node: statuses are indexed by
// hostname so a failover cannot inherit another member's "installed".
type persistedState struct {
	Hosts map[string]Status `json:"hosts"`
}

// Manager owns the module state. One instance per process; the API polls it.
type Manager struct {
	mu       sync.Mutex
	dir      string
	path     string
	hostname string
	status   Status
	// others preserves the status of the other members that share DATA_DIR.
	others map[string]Status
	busy   bool
	// fam is the package-manager family seen by the last preflight.
	fam string
	// reconciledAt makes status reads cheap: the UI polls GET
	// /api/modules/ha, and each read used to run dpkg/systemctl and append
	// a log line. Install operations use the internal reconcile directly.
	reconciledAt time.Time

	runner sysutil.CommandRunner
	sleep  func(time.Duration)
	now    func() time.Time
}

// New loads the persisted status. An install interrupted by a restart is
// reported as a partial install instead of looking green forever.
func New(dataDir string, runner sysutil.CommandRunner) *Manager {
	if runner == nil {
		runner = sysutil.DefaultRunner
	}
	// An empty dataDir must NEVER resolve to a relative path (that would drop
	// ha-module.json into whatever the working directory is): behave like an
	// ephemeral manager that does not persist state.
	statePath := ""
	if dataDir != "" {
		statePath = filepath.Join(dataDir, "ha-module.json")
	}
	m := &Manager{
		dir:      dataDir,
		path:     statePath,
		hostname: localHostname(),
		runner:   runner,
		sleep:    time.Sleep,
		now:      time.Now,
		others:   map[string]Status{},
		status:   freshStatus(),
	}
	m.load()
	m.saveLocked()
	return m
}

// NewEphemeral is a Manager that never reads or writes the state file: for
// read-only commands (webkvm-cli ha deps check) on a node whose DATA_DIR
// must not be touched.
func NewEphemeral(runner sysutil.CommandRunner) *Manager {
	if runner == nil {
		runner = sysutil.DefaultRunner
	}
	return &Manager{
		hostname: localHostname(),
		runner:   runner,
		sleep:    time.Sleep,
		now:      time.Now,
		others:   map[string]Status{},
		status:   freshStatus(),
	}
}

func freshStatus() Status {
	return Status{State: StateNotInstalled, Steps: []Step{}, Missing: []string{}, GuidedMissing: []string{}, UnitsActive: []string{}, UnknownUnits: []string{}}
}

// legacyFields are keys written by the runtime-mask installer. They are not
// part of Status any more; they are only read to migrate an old partial.
type legacyFields struct {
	MasksLeft      []string `json:"masks_left"`
	ProtectedUnits []string `json:"protected_units"`
}

type legacyFile struct {
	Hosts map[string]legacyFields `json:"hosts"`
}

// migrateLegacy turns an old partial install into the new model: units that
// were masked (or, for an old services_active, every module unit that was
// not protected) become units_active, so Reconcile re-verifies them and
// stays partial while they are active or cannot be read.
func migrateLegacy(status Status, old legacyFields) Status {
	steps := make([]Step, 0, len(status.Steps))
	for _, step := range status.Steps {
		if step.ID != "systemd_mask" && step.ID != "mask_services" {
			steps = append(steps, step)
		}
	}
	status.Steps = steps
	if status.State != StatePartial || len(status.UnitsActive) > 0 {
		return status
	}
	switch {
	case len(old.MasksLeft) > 0:
		status.Cause = "services_active"
		status.UnitsActive = append([]string(nil), old.MasksLeft...)
	case status.Cause == "services_active":
		// Only the units the legacy installer knew about: the Debian extras
		// were never protected by it and must not turn up as "active".
		legacy := []string{"corosync.service", "pacemaker.service", "pcsd.service", "sbd.service"}
		if status.Linstor {
			legacy = append(legacy, "linstor-controller.service", "linstor-satellite.service")
		}
		for _, unit := range legacy {
			if !containsAny(old.ProtectedUnits, unit) {
				status.UnitsActive = append(status.UnitsActive, unit)
			}
		}
	default:
		return status
	}
	status.Commands = unitCommands(status.UnitsActive, nil)
	return status
}

func normalizeStatus(status Status) Status {
	if status.Steps == nil {
		status.Steps = []Step{}
	}
	if status.Missing == nil {
		status.Missing = []string{}
	}
	if status.UnitsActive == nil {
		status.UnitsActive = []string{}
	}
	if status.UnknownUnits == nil {
		status.UnknownUnits = []string{}
	}
	if status.GuidedMissing == nil {
		status.GuidedMissing = []string{}
	}
	if status.State == StateInstalling {
		status.State = StatePartial
		status.FailedStep = "interrupted"
		status.Error = "the installer was interrupted by a restart"
		status.Cause = "interrupted"
		status.Commands = manualCommandsFor(nil, "", status.Missing)
		status.Steps = append(status.Steps, Step{ID: "interrupted", State: StepFailed, Detail: "panel restarted during install"})
	}
	return status
}

// load must be called without the lock (constructor only).
func (m *Manager) load() {
	if m.path == "" {
		return
	}
	data, err := os.ReadFile(m.path)
	if err != nil {
		return
	}
	var file persistedState
	if json.Unmarshal(data, &file) == nil && file.Hosts != nil {
		var legacy legacyFile
		_ = json.Unmarshal(data, &legacy)
		for host, status := range file.Hosts {
			status = normalizeStatus(migrateLegacy(status, legacy.Hosts[host]))
			status.Hostname = host
			if host == m.hostname {
				m.status = status
			} else {
				m.others[host] = status
			}
		}
		return
	}
	// Legacy single-status file (previous patch). It has no hostname, so it
	// could belong to any member: never adopt it as the local truth.
	var legacy Status
	if json.Unmarshal(data, &legacy) == nil && legacy.State != "" {
		var old legacyFields
		_ = json.Unmarshal(data, &old)
		legacy = normalizeStatus(migrateLegacy(legacy, old))
		host := legacy.Hostname
		if host == "" {
			host = "legacy"
		}
		legacy.Hostname = host
		if host == m.hostname {
			m.status = legacy
		} else {
			m.others[host] = legacy
		}
	}
}

func localHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return "local"
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return "local"
	}
	return host
}

// Path is the persisted state file (useful for operators and tests).
func (m *Manager) Path() string { return m.path }

// Snapshot returns a deep copy of the current status.
func (m *Manager) Snapshot() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneStatus(m.status)
}

// Busy reports whether a background install is running.
func (m *Manager) Busy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy
}

// reconcileTTL is how long a status read may reuse the last probe. Short
// enough that an external package change shows up on the next screen.
const reconcileTTL = 5 * time.Second

// Reconcile replaces the persisted claim with what dpkg actually reports on
// this node. It is called before every status read and on Verify, so a
// state inherited from another member (or edited by hand) cannot survive.
// Results are reused for reconcileTTL so a polling UI does not shell out to
// the package manager on every request.
func (m *Manager) Reconcile(ctx context.Context) Status {
	m.mu.Lock()
	if !m.busy && !m.reconciledAt.IsZero() && m.now().Sub(m.reconciledAt) < reconcileTTL {
		status := cloneStatus(m.status)
		m.mu.Unlock()
		return status
	}
	m.mu.Unlock()
	return m.reconcile(ctx, nil)
}

// ReconcilePlan settles the status with a plan resolved moments ago, so a
// CLI run does not ask the package manager about every package twice (8.34
// defect 2). The package-manager reads are the expensive part on Arch.
func (m *Manager) ReconcilePlan(ctx context.Context, plan Plan) Status {
	return m.reconcile(ctx, plan.Packages)
}

func (m *Manager) reconcile(ctx context.Context, known []Package) Status {
	m.mu.Lock()
	if m.busy {
		status := cloneStatus(m.status)
		m.mu.Unlock()
		return status
	}
	m.busy = true
	linstor := m.status.Linstor
	m.mu.Unlock()
	defer m.finish()

	var state string
	var missing []string
	if known == nil {
		state, missing = m.verifyState(ctx, linstor)
	} else {
		state, missing = stateOfPackages(known)
	}

	// Units recorded as needing operator action are re-read on every
	// reconcile (fail-closed: an unreadable state keeps the unit listed).
	// dpkg being happy says nothing about a unit the operator still has to
	// stop, so installed is never promoted while any of them is active.
	m.mu.Lock()
	pending := append([]string(nil), m.status.UnitsActive...)
	unknown := append([]string(nil), m.status.UnknownUnits...)
	// Keep a failed install's diagnosis across status reads. Reconcile runs
	// on every GET /api/modules/ha; clearing it here made the UI show a bare
	// "not installed" after a real apt/dnf failure (8.37).
	lastStep, lastCause, lastError := m.status.FailedStep, m.status.Cause, m.status.Error
	installFailed := lastStep == "install_packages" || lastStep == "preflight"
	m.mu.Unlock()
	failClosed := map[string]bool{}
	for _, unit := range pending {
		failClosed[unit] = true
	}
	remaining := m.unsettledUnits(ctx, pending, failClosed)

	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Missing = missing
	m.status.GuidedMissing = guidedNames(m.fam, missing)
	m.status.UnitsActive = remaining
	unitCmds := unitCommands(remaining, unknown)
	switch {
	case state == StateInstalled && len(remaining) > 0:
		m.status.State = StatePartial
		m.status.FailedStep = "verify"
		m.status.Cause = "services_active"
		m.status.Error = "enabled or active: " + strings.Join(remaining, ", ")
		m.status.Commands = unitCmds
		m.status.Steps = ensureStep(m.status.Steps, "verify", StepFailed, "enabled or active: "+strings.Join(remaining, ", "))
		m.logLocked("reconcile: all packages installed, but module units are still enabled/active")
	case state == StateInstalled:
		m.status.State = StateInstalled
		m.status.FailedStep = ""
		m.status.Error = ""
		m.status.Cause = ""
		m.status.Commands = nil
		m.status.Steps = ensureStep(m.status.Steps, "verify", StepDone, "all packages installed")
		m.logLocked("reconcile: all packages installed")
	case state == StatePartialGuided:
		m.status.State = StatePartialGuided
		m.status.FailedStep = ""
		m.status.Error = ""
		m.status.Cause = "guided_pending"
		m.status.Commands = append(m.manualCommandsLocked(missing), unitCmds...)
		m.status.Steps = ensureStep(m.status.Steps, "verify", StepFailed, "waiting for manual install: "+strings.Join(missing, ", "))
		m.logLocked("reconcile: only operator-installed packages are missing: %s", strings.Join(missing, ", "))
	case state == StateNotInstalled:
		m.status.State = StateNotInstalled
		m.status.FailedStep = ""
		m.status.Error = ""
		m.status.Cause = ""
		if installFailed {
			m.status.FailedStep, m.status.Cause, m.status.Error = lastStep, lastCause, lastError
		}
		m.status.Commands = append(m.manualCommandsLocked(missing), unitCmds...)
		m.status.Steps = ensureStep(m.status.Steps, "verify", StepPending, "no packages installed")
		m.logLocked("reconcile: no packages installed")
	default:
		m.status.State = StatePartial
		if installFailed {
			m.status.FailedStep, m.status.Cause, m.status.Error = lastStep, lastCause, lastError
			m.status.Commands = append(m.manualCommandsLocked(missing), unitCmds...)
		} else {
			// Packages are simply absent on a host where HA was never
			// enabled: that is "not installed", not a failed install.
			// Only an actual attempt keeps the error diagnosis.
			m.status.FailedStep = ""
			m.status.Cause = ""
			m.status.Commands = append(m.manualCommandsLocked(missing), unitCmds...)
		}
		step := StepPending
		if installFailed {
			step = StepFailed
		}
		m.status.Steps = ensureStep(m.status.Steps, "verify", step, "missing: "+strings.Join(missing, ", "))
		m.logLocked("reconcile: missing %s", strings.Join(missing, ", "))
	}
	m.reconciledAt = m.now()
	m.saveLocked()
	return cloneStatus(m.status)
}

func cloneStatus(s Status) Status {
	out := s
	out.Steps = append([]Step{}, s.Steps...)
	out.Missing = append([]string{}, s.Missing...)
	out.GuidedMissing = append([]string{}, s.GuidedMissing...)
	out.UnitsActive = append([]string{}, s.UnitsActive...)
	out.UnknownUnits = append([]string{}, s.UnknownUnits...)
	out.Commands = append([]string(nil), s.Commands...)
	out.Log = append([]string(nil), s.Log...)
	return out
}

func (m *Manager) saveLocked() {
	if m.path == "" {
		return
	}
	m.status.Hostname = m.hostname
	m.status.UpdatedAt = m.now()
	file := persistedState{Hosts: map[string]Status{}}
	for host, status := range m.others {
		file.Hosts[host] = status
	}
	file.Hosts[m.hostname] = m.status
	if data, err := json.MarshalIndent(file, "", "  "); err == nil {
		_ = os.WriteFile(m.path, data, 0o600)
	}
}

func (m *Manager) logLocked(format string, args ...any) {
	line := m.now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	m.status.Log = append(m.status.Log, line)
	if len(m.status.Log) > logLimit {
		m.status.Log = append([]string(nil), m.status.Log[len(m.status.Log)-logLimit:]...)
	}
}

func (m *Manager) setStep(id, state, detail string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for i := range m.status.Steps {
		if m.status.Steps[i].ID == id {
			m.status.Steps[i].State = state
			if detail != "" {
				m.status.Steps[i].Detail = detail
			}
			found = true
		}
	}
	if !found {
		m.status.Steps = append(m.status.Steps, Step{ID: id, State: state, Detail: detail})
	}
	m.saveLocked()
}

func (m *Manager) logf(format string, args ...any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logLocked(format, args...)
	m.saveLocked()
}

func (m *Manager) run(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	if timeout > 0 {
		return m.runner.RunWithTimeout(name, timeout, args...)
	}
	return m.runner.Run(ctx, name, args...)
}

// classify turns raw package-manager output into a language the UI can
// explain instead of showing apt's wall of text.
func classify(output string) string {
	lower := strings.ToLower(output)
	contains := func(needles ...string) bool {
		for _, needle := range needles {
			if strings.Contains(lower, strings.ToLower(needle)) {
				return true
			}
		}
		return false
	}
	switch {
	case contains("could not resolve", "temporary failure resolving", "network is unreachable", "failed to fetch", "connection failed", "could not connect", "connection timed out", "failed to download", "cannot download", "curl error", "failed retrieving file", "failed to synchronize"):
		return "network"
	case contains("could not get lock", "unable to acquire the dpkg frontend lock", "is another process using it", "waiting for process with pid", "another copy is running", "unable to lock database", "failed to init transaction", "cannot open lock"):
		return "lock"
	case contains("no space left on device", "not enough free space"):
		return "disk_full"
	case contains("read-only file system", "readonly file system"):
		return "sandbox_readonly"
	case contains("unable to locate package", "has no installation candidate", "doesn't have a candidate", "no match for argument", "unable to find a match", "target not found"):
		return "package_unavailable"
	case contains("unmet dependencies", "held broken packages", "broken packages", "unresolvable dependencies", "conflicting files", "problem: package"):
		return "broken_dependencies"
	case contains("permission denied", "are you root", "must be root"):
		return "not_root"
	}
	return "unknown"
}

func errorExcerpt(output string) string {
	const max = 1500
	text := strings.TrimSpace(output)
	if len(text) > max {
		return "[earlier output truncated]\n" + text[len(text)-max:]
	}
	return text
}

// manualCommandsFor gives the operator the exact commands to finish by hand
// on a node of the given family (apt when the family is unknown).
func manualCommandsFor(pm packageManager, family string, missing []string) []string {
	commands := []string{}
	if pm == nil {
		pm = aptManager{}
		family = FamilyApt
	}
	official, aur := []string{}, []string{}
	for _, name := range missing {
		if spec, ok := specFor(family, name); ok && spec.Origin == OriginAUR {
			aur = append(aur, name)
		} else if ok && spec.Origin == OriginManual {
			continue
		} else {
			official = append(official, name)
		}
	}
	commands = append(commands, pm.installCommands(official)...)
	if len(aur) > 0 {
		commands = append(commands, aurSteps(aur).Commands...)
	}
	if family == FamilyApt || family == FamilyDnf {
		commands = append(commands, manualDisableCommands(family)...)
	}
	return commands
}

// setFamily remembers the package-manager family of this node.
func (m *Manager) setFamily(family string) {
	m.mu.Lock()
	m.fam = family
	m.mu.Unlock()
}

// manualCommandsLocked is manualCommands for callers already holding m.mu.
func (m *Manager) manualCommandsLocked(missing []string) []string {
	return manualCommandsFor(m.pmFor(m.fam), m.fam, missing)
}

// guidedNames are the missing packages that are the operator's to install.
func guidedNames(family string, missing []string) []string {
	out := []string{}
	for _, name := range missing {
		if spec, ok := specFor(family, name); ok && spec.Origin == OriginAUR {
			out = append(out, name)
		}
	}
	return out
}

// manualDisableCommands keeps the module's units off after a manual install.
// Debian also ships pcsd-ruby, logd and sbd_remote; systemctl reports the
// ones a distro does not have and carries on with the rest.
func manualDisableCommands(family string) []string {
	commands := []string{"sudo systemctl disable corosync pacemaker pcsd sbd"}
	if family == FamilyApt {
		commands = append(commands, "sudo systemctl disable pcsd-ruby logd sbd_remote  # Debian only; ignore 'does not exist'")
	}
	return commands
}
