package hainstall

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Start launches the install in the background and returns immediately. The
// UI polls Snapshot() for the live log and per-step state.
func (m *Manager) Start(opts Options) error {
	opts = opts.Normalized()
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return ErrBusy
	}
	m.busy = true
	// Units a previous run left running are carried over: a re-run must not
	// launder them into "pre-existing" just because they are active now.
	carried := append([]string(nil), m.status.UnitsActive...)
	m.status = Status{
		State:   StateInstalling,
		Linstor: opts.Linstor,
		Steps: []Step{
			{ID: "preflight", State: StepPending},
			{ID: "snapshot_units", State: StepPending},
			{ID: "install_packages", State: StepPending},
			{ID: "restore_units", State: StepPending},
			{ID: "verify", State: StepPending},
		},
		Missing:      []string{},
		UnitsActive:  []string{},
		UnknownUnits: []string{},
	}
	m.logLocked("install requested (linstor=%v accept_experimental=%v fencing=%s)", opts.Linstor, opts.AcceptExperimental, strings.Join(opts.Fencing, ","))
	m.saveLocked()
	m.mu.Unlock()

	go m.runInstall(opts, carried)
	return nil
}

func (m *Manager) finish() {
	m.mu.Lock()
	m.busy = false
	m.saveLocked()
	m.mu.Unlock()
}

// fail records a terminal failure without purging anything: the operator
// decides what to do with the packages that did get installed.
func (m *Manager) fail(step, state, cause, output string, missing []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.State = state
	m.status.FailedStep = step
	m.status.Missing = missing
	m.status.Error = errorExcerpt(output)
	m.status.Cause = cause
	m.status.Commands = m.manualCommandsLocked(missing)
	m.logLocked("%s failed (%s)", step, cause)
	m.saveLocked()
}

func (m *Manager) runInstall(opts Options, carried []string) {
	defer m.finish()
	ctx := context.Background()
	linstor := opts.Linstor

	m.setStep("preflight", StepRunning, "")
	pf, packages := m.preflight(ctx, opts)
	if !pf.OK {
		m.setStep("preflight", StepFailed, failedChecks(pf))
		cause := "preflight"
		for _, check := range pf.Checks {
			if check.OK {
				continue
			}
			switch check.ID {
			case "distro":
				cause = "unsupported_distro"
			case "ha_repo":
				cause = "repo_missing"
			case "sync_db":
				cause = "sync_db_missing"
			case "experimental":
				cause = "experimental_not_accepted"
			}
		}
		missing := []string{}
		for _, pkg := range packages {
			if !pkg.Installed && pkg.Origin != OriginManual {
				missing = append(missing, pkg.Name)
			}
		}
		m.fail("preflight", StateNotInstalled, cause, failedChecks(pf), missing)
		// The same guided steps the plan shows: the operator can finish
		// by hand from the status alone.
		if plan := m.Plan(ctx, opts); len(plan.ManualCommands) > 0 {
			m.mu.Lock()
			m.status.Commands = plan.ManualCommands
			m.saveLocked()
			m.mu.Unlock()
		}
		return
	}
	m.setStep("preflight", StepDone, pf.Distro+" "+pf.Version)

	missing := installable(packages)
	if len(missing) == 0 {
		// Idempotent: nothing is left that WebKVM may install. Do not call
		// the package manager and do not touch any unit; only verify what a
		// previous run (or the operator) left behind.
		detail := "all packages already installed"
		if len(guidedMissing(packages)) > 0 {
			detail = "nothing WebKVM may install here; the rest is manual"
		}
		for _, id := range []string{"snapshot_units", "install_packages", "restore_units"} {
			m.setStep(id, StepSkipped, detail)
		}
		m.logf("%s; not touching the package manager or services", detail)
		m.applyVerify(ctx, linstor, nil, carried, true)
		return
	}
	pm := m.pmFor(pf.Family)

	// (a) Snapshot BEFORE anything can start a unit.
	m.setStep("snapshot_units", StepRunning, "")
	snaps := m.snapshotUnits(ctx, moduleUnits(linstor))
	unknown := []string{}
	for _, snap := range snaps {
		if !snap.known() {
			unknown = append(unknown, snap.unit)
		}
	}
	m.mu.Lock()
	m.status.UnknownUnits = unknown
	if len(unknown) > 0 {
		m.logLocked("unit state could not be read; treated as protected: %s", strings.Join(unknown, ", "))
	}
	m.saveLocked()
	m.mu.Unlock()
	m.setStep("snapshot_units", StepDone, describeSnapshot(snaps))

	// (b)+(c) Install; restore on every exit path, including a panic.
	installOK := m.installAndRestore(ctx, pm, opts, missing, snaps)

	// (d) Verify by re-reading the unit states.
	m.applyVerify(ctx, linstor, snaps, carried, installOK)
}

// installAndRestore runs the package install and, whatever happens, restores
// the units that were definitively off before. The deferred restore also
// covers a panic or an interrupted run.
func (m *Manager) installAndRestore(ctx context.Context, pm packageManager, opts Options, missing []string, snaps []unitSnap) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
			m.setStep("install_packages", StepFailed, "internal error")
			m.fail("install_packages", StatePartial, "internal_error", fmt.Sprint(r), missing)
		}
		m.restoreUnits(ctx, snaps)
	}()
	return m.installPackages(ctx, pm, opts.Linstor, missing)
}

func (m *Manager) installPackages(ctx context.Context, pm packageManager, linstor bool, missing []string) bool {
	m.setStep("install_packages", StepRunning, strings.Join(missing, ", "))
	res := pm.install(ctx, missing)
	if res.Err != nil {
		cause := classify(string(res.Out) + " " + res.Err.Error())
		if res.Phase == "refresh" {
			m.setStep("install_packages", StepFailed, "metadata refresh: "+cause)
			m.fail("install_packages", StateNotInstalled, cause, string(res.Out)+"\n"+res.Err.Error(), missing)
			return false
		}
		// Recompute what is actually missing for a precise manual command.
		_, left := m.verifyState(ctx, linstor)
		m.setStep("install_packages", StepFailed, cause)
		m.fail("install_packages", StatePartial, cause, string(res.Out)+"\n"+res.Err.Error(), left)
		if pm.family() == FamilyPacman && cause == "package_unavailable" {
			m.mu.Lock()
			m.status.Commands = append([]string{"# The sync database may be stale. Upgrade the whole system first (never a bare -Sy):", "sudo pacman -Syu"}, m.status.Commands...)
			m.saveLocked()
			m.mu.Unlock()
		}
		return false
	}
	m.setStep("install_packages", StepDone, strings.Join(missing, ", "))
	return true
}

// restoreUnits undoes what the package postinst scripts may have enabled or
// started, but only for units that were definitively disabled and inactive
// before the install. Anything else (enabled/active before, or unknown) is
// never touched.
func (m *Manager) restoreUnits(ctx context.Context, snaps []unitSnap) {
	targets := []string{}
	for _, snap := range snaps {
		if snap.restorable() {
			targets = append(targets, snap.unit)
		}
	}
	if len(targets) == 0 {
		m.setStep("restore_units", StepSkipped, "no unit needed restoring")
		return
	}
	m.setStep("restore_units", StepRunning, "")
	restored := []string{}
	for _, unit := range targets {
		if state, ok := m.systemctlState(ctx, "is-enabled", unit); ok && state == "not-found" {
			continue
		}
		restored = append(restored, unit)
		if out, err := m.run(ctx, probeTimeout, "systemctl", "disable", unit); err != nil {
			m.logf("warning: systemctl disable %s: %v %s", unit, err, strings.TrimSpace(string(out)))
		}
		if state, ok := m.systemctlState(ctx, "is-active", unit); !ok || unitRunning(state) {
			if out, err := m.run(ctx, probeTimeout, "systemctl", "stop", unit); err != nil {
				m.logf("warning: systemctl stop %s: %v %s", unit, err, strings.TrimSpace(string(out)))
			}
		}
	}
	if len(restored) == 0 {
		m.setStep("restore_units", StepSkipped, "no unit needed restoring")
		return
	}
	m.setStep("restore_units", StepDone, strings.Join(restored, ", "))
	m.logf("units restored to disabled/stopped: %s", strings.Join(restored, ", "))
}

// applyVerify sets the final state. It re-reads the unit states: a unit that
// was not running before and is now enabled or active (or whose state cannot
// be read) means the install is partial, never a clean "installed".
func (m *Manager) applyVerify(ctx context.Context, linstor bool, snaps []unitSnap, carried []string, installOK bool) {
	state, missing := m.verifyState(ctx, linstor)

	candidates := []string{}
	failClosed := map[string]bool{}
	for _, snap := range snaps {
		switch {
		case snap.restorable():
			candidates = append(candidates, snap.unit)
			failClosed[snap.unit] = true
		case !snap.known():
			candidates = append(candidates, snap.unit)
		}
	}
	for _, unit := range carried {
		if !containsAny(candidates, unit) {
			candidates = append(candidates, unit)
		}
		failClosed[unit] = true
	}
	active := m.unsettledUnits(ctx, candidates, failClosed)
	// A unit the operator had enabled but stopped is not ours to disable. If
	// a postinst starts it anyway, report it instead of claiming "installed":
	// a cluster service running unexpectedly is exactly the case this
	// installer must never hide.
	started := m.startedProtectedUnits(ctx, snaps)
	for _, unit := range started {
		if !containsAny(active, unit) {
			active = append(active, unit)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.Missing = missing
	m.status.GuidedMissing = guidedNames(m.fam, missing)
	m.status.UnitsActive = active
	if len(active) > 0 {
		detail := "enabled or active: " + strings.Join(active, ", ")
		m.status.Steps = ensureStep(m.status.Steps, "verify", StepFailed, detail)
		m.status.State = StatePartial
		m.status.Commands = append(m.status.Commands, unitCommandsFor(active, m.status.UnknownUnits, started)...)
		if installOK {
			m.status.FailedStep = "verify"
			m.status.Cause = "services_active"
			m.status.Error = detail
		} else if m.status.Error != "" {
			m.status.Error += "\n" + detail
		}
		m.logLocked("units enabled/active after install: %s", strings.Join(active, ", "))
		m.saveLocked()
		return
	}
	if !installOK {
		m.status.Steps = ensureStep(m.status.Steps, "verify", StepFailed, "install did not complete")
		m.saveLocked()
		return
	}
	switch state {
	case StatePartialGuided:
		m.status.State = StatePartialGuided
		m.status.FailedStep = ""
		m.status.Error = ""
		m.status.Cause = "guided_pending"
		m.status.Commands = m.manualCommandsLocked(missing)
		m.status.Steps = ensureStep(m.status.Steps, "verify", StepSkipped, "waiting for manual install: "+strings.Join(missing, ", "))
		m.logLocked("WebKVM part done; the operator installs: %s", strings.Join(missing, ", "))
	case StateInstalled:
		m.status.State = StateInstalled
		m.status.FailedStep = ""
		m.status.Error = ""
		m.status.Cause = ""
		m.status.Commands = nil
		m.status.Steps = ensureStep(m.status.Steps, "verify", StepDone, "all packages installed")
		m.logLocked("cluster/HA packages installed")
	default:
		m.status.State = state
		m.status.FailedStep = "verify"
		m.status.Cause = "package_unavailable"
		m.status.Commands = m.manualCommandsLocked(missing)
		m.status.Steps = ensureStep(m.status.Steps, "verify", StepFailed, "missing: "+strings.Join(missing, ", "))
	}
	m.saveLocked()
}

func failedChecks(pf Preflight) string {
	parts := []string{}
	for _, check := range pf.Checks {
		if !check.OK && check.Severity == "blocker" {
			detail := check.Detail
			if detail == "" {
				detail = check.Hint
			}
			parts = append(parts, check.ID+": "+detail)
		}
	}
	return strings.Join(parts, "; ")
}

// moduleUnits are the systemd units the module may install.
func moduleUnits(linstor bool) []string {
	units := make([]string, 0, len(Services)+2)
	for _, name := range Services {
		units = append(units, name+".service")
	}
	if linstor {
		units = append(units, "linstor-controller.service", "linstor-satellite.service")
	}
	return units
}

// unitSnap is the state of one unit read before the install. enabled/active
// are empty when the query could not give a definite answer.
type unitSnap struct {
	unit    string
	enabled string
	active  string
}

func (s unitSnap) known() bool { return s.enabled != "" && s.active != "" }

// restorable is fail-closed: only a unit that was definitely disabled (or
// not found) AND definitely not running (inactive, or failed: a dead unit
// cannot belong to a live cluster) may be disabled/stopped afterwards.
// enabled, static, indirect, alias, masked, active, activating, any other
// state, any error and any timeout leave the unit alone.
func (s unitSnap) restorable() bool {
	if !s.known() {
		return false
	}
	switch s.enabled {
	case "disabled", "not-found":
	default:
		return false
	}
	return s.active == "inactive" || s.active == "failed"
}

func describeSnapshot(snaps []unitSnap) string {
	parts := make([]string, 0, len(snaps))
	for _, snap := range snaps {
		state := "unknown"
		if snap.known() {
			state = snap.enabled + "/" + snap.active
		}
		parts = append(parts, snap.unit+"="+state)
	}
	return strings.Join(parts, " ")
}

func (m *Manager) snapshotUnits(ctx context.Context, units []string) []unitSnap {
	snaps := make([]unitSnap, 0, len(units))
	for _, unit := range units {
		snap := unitSnap{unit: unit}
		enabled, ok1 := m.systemctlState(ctx, "is-enabled", unit)
		active, ok2 := m.systemctlState(ctx, "is-active", unit)
		if ok1 && ok2 {
			snap.enabled, snap.active = enabled, active
		}
		snaps = append(snaps, snap)
	}
	return snaps
}

func unitRunning(state string) bool {
	return state == "active" || state == "activating" || state == "reloading"
}

func unitEnabled(state string) bool {
	return state == "enabled" || state == "enabled-runtime"
}

// unsettledUnits returns the units that are enabled or running now. When
// failClosed[unit] is set, a state that cannot be read counts as running.
func (m *Manager) unsettledUnits(ctx context.Context, units []string, failClosed map[string]bool) []string {
	out := []string{}
	for _, unit := range units {
		enabled, ok1 := m.systemctlState(ctx, "is-enabled", unit)
		active, ok2 := m.systemctlState(ctx, "is-active", unit)
		switch {
		case ok1 && unitEnabled(enabled), ok2 && unitRunning(active):
			out = append(out, unit)
		case (!ok1 || !ok2) && failClosed[unit]:
			out = append(out, unit)
		}
	}
	return out
}

// startedProtectedUnits lists units that were known to be stopped but not
// restorable (enabled/static/masked before the install) and are running now.
// They are reported, never stopped by WebKVM: an operator-enabled unit is
// theirs, and disabling it would silently change their cluster.
func (m *Manager) startedProtectedUnits(ctx context.Context, snaps []unitSnap) []string {
	out := []string{}
	for _, snap := range snaps {
		if !snap.known() || snap.restorable() || unitRunning(snap.active) {
			continue
		}
		if active, ok := m.systemctlState(ctx, "is-active", snap.unit); ok && unitRunning(active) {
			out = append(out, snap.unit)
		}
	}
	return out
}

// unitCommands gives the exact command for units that need operator action.
// Units whose pre-install state was unknown get a check-first hint instead
// of a blind stop: they may belong to a running cluster.
func unitCommands(units, unknown []string) []string {
	return unitCommandsFor(units, unknown, nil)
}

// unitCommandsFor is unitCommands with a stop-only list: units the operator
// had already enabled are stopped but never disabled (8.32 P1).
func unitCommandsFor(units, unknown, stopOnly []string) []string {
	stop, stopOnlyUnits, check := []string{}, []string{}, []string{}
	for _, unit := range units {
		switch {
		case containsAny(unknown, unit):
			check = append(check, unit)
		case containsAny(stopOnly, unit):
			stopOnlyUnits = append(stopOnlyUnits, unit)
		default:
			stop = append(stop, unit)
		}
	}
	commands := []string{}
	if len(stop) > 0 {
		commands = append(commands, "sudo systemctl disable --now "+strings.Join(stop, " "))
	}
	if len(stopOnlyUnits) > 0 {
		commands = append(commands, "sudo systemctl stop "+strings.Join(stopOnlyUnits, " "))
	}
	if len(check) > 0 {
		commands = append(commands,
			"# state before the install was unknown; stop only if not part of a running cluster:",
			"systemctl status "+strings.Join(check, " "),
			"sudo systemctl disable --now "+strings.Join(check, " "))
	}
	return commands
}

// systemctlState runs one query and returns a definite state. A timeout,
// exec failure, empty output or an error that is not a plain exit status is
// "unknown": the unit must then be treated as protected (fail-closed).
func (m *Manager) systemctlState(ctx context.Context, verb, unit string) (string, bool) {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := m.runner.Run(queryCtx, "systemctl", verb, unit)
	if queryCtx.Err() != nil {
		return "", false
	}
	state := strings.TrimSpace(string(out))
	if state == "" {
		return "", false
	}
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			return "", false
		}
	}
	// A unit that does not exist yet (the usual case before the first
	// install) answers on stderr: that is a definite "not found".
	if verb == "is-enabled" && strings.Contains(state, "No such file or directory") {
		return "not-found", true
	}
	return state, true
}

// verifyState (profile.go) asks the node's package manager; Verify
// re-checks it and updates the persisted state.
// Verify re-checks the package manager and updates the persisted state.
func (m *Manager) Verify(ctx context.Context) Status {
	// Verify is the operator asking for the truth now: bypass the
	// status-read cache.
	m.mu.Lock()
	m.reconciledAt = time.Time{}
	m.mu.Unlock()
	return m.Reconcile(ctx)
}

func ensureStep(steps []Step, id, state, detail string) []Step {
	for i := range steps {
		if steps[i].ID == id {
			steps[i].State = state
			steps[i].Detail = detail
			return steps
		}
	}
	return append(steps, Step{ID: id, State: state, Detail: detail})
}

// StartUninstall removes the module packages in the background. Removing is
// the default; purge (which deletes configuration such as
// /etc/corosync/corosync.conf) requires an explicit additional opt-in.
func (m *Manager) StartUninstall(purge bool) error {
	m.mu.Lock()
	if m.busy {
		m.mu.Unlock()
		return ErrBusy
	}
	m.busy = true
	m.status = Status{
		State:        StateUninstalling,
		Steps:        []Step{{ID: "uninstall", State: StepPending}},
		Missing:      []string{},
		UnitsActive:  []string{},
		UnknownUnits: []string{},
	}
	m.logLocked("uninstall requested (purge=%v)", purge)
	m.saveLocked()
	m.mu.Unlock()

	go m.runUninstall(purge)
	return nil
}

func (m *Manager) runUninstall(purge bool) {
	defer m.finish()
	ctx := context.Background()
	m.setStep("uninstall", StepRunning, "")

	pm := m.localPM(ctx)
	// Only real, installed names: never ask the package manager to remove a
	// logical name (fence-agents) that does not exist on this distro. Only
	// what WebKVM could have installed is removed; operator-built (AUR)
	// packages are listed as a manual command.
	remove, manual := []string{}, []string{}
	for _, spec := range profile(pm.family(), m.Snapshot().Linstor) {
		for _, alt := range spec.alternatives() {
			if spec.Informational || !pm.installed(ctx, alt) {
				continue
			}
			if spec.Origin == OriginAUR {
				manual = append(manual, alt)
			} else {
				remove = append(remove, alt)
			}
		}
	}
	if len(remove) == 0 && len(manual) == 0 {
		m.setStep("uninstall", StepDone, "nothing installed")
		m.resetNotInstalled("uninstall: nothing installed")
		return
	}
	action := "remove"
	if purge {
		action = "purge"
	}
	if len(remove) > 0 {
		out, err := pm.remove(ctx, remove, purge)
		if err != nil {
			cause := classify(string(out) + " " + err.Error())
			m.fail("uninstall", StatePartial, cause, string(out)+"\n"+err.Error(), remove)
			m.mu.Lock()
			m.status.Commands = []string{pm.removeCommand(remove, purge)}
			m.saveLocked()
			m.mu.Unlock()
			return
		}
	}
	if len(manual) > 0 {
		m.fail("uninstall", StatePartial, "guided_pending", "operator-installed packages remain: "+strings.Join(manual, ", "), manual)
		m.mu.Lock()
		m.status.Commands = []string{"# Packages WebKVM did not install itself:", pm.removeCommand(manual, purge)}
		m.saveLocked()
		m.mu.Unlock()
		return
	}
	m.setStep("uninstall", StepDone, action+": "+strings.Join(remove, ", "))
	m.resetNotInstalled(action + ": " + strings.Join(remove, ", "))
}

func (m *Manager) resetNotInstalled(line string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status.State = StateNotInstalled
	m.status.Missing = []string{}
	m.status.GuidedMissing = []string{}
	m.status.Steps = []Step{}
	m.status.FailedStep = ""
	m.status.Cause = ""
	m.status.Error = ""
	m.status.Commands = nil
	m.status.UnitsActive = []string{}
	m.status.UnknownUnits = []string{}
	m.status.Linstor = false
	m.logLocked("%s", line)
	m.saveLocked()
}
