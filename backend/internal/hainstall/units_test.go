package hainstall

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startAndWait(t *testing.T, fake *fakeNode, opts Options) (*Manager, Status) {
	t.Helper()
	m := New(t.TempDir(), fake.runner())
	m.sleep = func(time.Duration) {}
	if err := m.Start(opts); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	return m, m.Snapshot()
}

func assertNoMask(t *testing.T, fake *fakeNode) {
	t.Helper()
	if fake.seen("mask") {
		t.Fatalf("a command mentions mask: %v", fake.commands)
	}
}

// Every pre-state of corosync.service crossed with a postinst that enables
// and starts it. Only a definitely-off unit may be restored.
func TestRestoreUnitsMatrix(t *testing.T) {
	const unit = "corosync.service"
	cases := []struct {
		name        string
		enabled     bool
		active      bool
		unknown     bool
		stays       bool
		wantTouched bool
		wantState   string
		wantActive  []string
		wantUnknown []string
	}{
		{name: "disabled-inactive", wantTouched: true, wantState: StateInstalled},
		{name: "enabled-active", enabled: true, active: true, wantState: StateInstalled},
		// The postinst starts it: not ours to disable, but never "installed".
		{name: "enabled-inactive", enabled: true, wantState: StatePartial, wantActive: []string{unit}},
		{name: "active-disabled", active: true, wantState: StateInstalled},
		{name: "unknown-unreadable-after", unknown: true, stays: true, wantState: StateInstalled, wantUnknown: []string{unit}},
		{name: "unknown", unknown: true, wantState: StatePartial, wantActive: []string{unit}, wantUnknown: []string{unit}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeNode()
			fake.enabledServices[unit] = tc.enabled
			fake.activeServices[unit] = tc.active
			fake.unknownUnits[unit] = tc.unknown
			fake.staysUnknown = tc.stays
			fake.enableAfterInstall = []string{unit}
			fake.activateAfterInstall = []string{unit}
			_, status := startAndWait(t, fake, Options{})
			assertNoMask(t, fake)
			if status.State != tc.wantState {
				t.Fatalf("state = %q, want %q (%+v)", status.State, tc.wantState, status)
			}
			if got := fake.seen("systemctl disable " + unit); got != tc.wantTouched {
				t.Fatalf("disable touched = %v, want %v: %v", got, tc.wantTouched, fake.commands)
			}
			if got := fake.seen("systemctl stop " + unit); got != tc.wantTouched {
				t.Fatalf("stop touched = %v, want %v", got, tc.wantTouched)
			}
			if strings.Join(status.UnitsActive, ",") != strings.Join(tc.wantActive, ",") {
				t.Fatalf("units_active = %v, want %v", status.UnitsActive, tc.wantActive)
			}
			if strings.Join(status.UnknownUnits, ",") != strings.Join(tc.wantUnknown, ",") {
				t.Fatalf("unknown_units = %v, want %v", status.UnknownUnits, tc.wantUnknown)
			}
			if tc.wantState == StatePartial && status.Cause != "services_active" {
				t.Fatalf("cause = %q", status.Cause)
			}
			// The other units were off before and are restored.
			if !fake.seen("systemctl disable pacemaker.service") {
				t.Fatal("pacemaker was not restored")
			}
		})
	}
}

func TestSnapshotHappensBeforeApt(t *testing.T) {
	fake := newFakeNode()
	startAndWait(t, fake, Options{})
	firstQuery, firstApt := -1, -1
	for i, command := range fake.commands {
		if firstQuery < 0 && strings.HasPrefix(command, "systemctl is-enabled") {
			firstQuery = i
		}
		if firstApt < 0 && strings.Contains(command, "apt-get") {
			firstApt = i
		}
	}
	if firstQuery < 0 || firstApt < 0 || firstQuery > firstApt {
		t.Fatalf("unit snapshot must precede apt (query=%d apt=%d)", firstQuery, firstApt)
	}
}

func TestAptFailureStillRestoresUnits(t *testing.T) {
	fake := newFakeNode()
	fake.aptFailText = "E: Unable to locate package resource-agents"
	fake.effectsOnFail = true
	fake.enableAfterInstall = []string{"corosync.service"}
	fake.activateAfterInstall = []string{"corosync.service"}
	_, status := startAndWait(t, fake, Options{})
	assertNoMask(t, fake)
	if status.State != StatePartial || status.Cause != "package_unavailable" {
		t.Fatalf("status = %+v", status)
	}
	if !fake.seen("systemctl disable corosync.service") || !fake.seen("systemctl stop corosync.service") {
		t.Fatalf("restore did not run after apt failure: %v", fake.commands)
	}
	if len(status.UnitsActive) != 0 {
		t.Fatalf("units_active = %v after a successful restore", status.UnitsActive)
	}
}

func TestPanicDuringInstallStillRestores(t *testing.T) {
	fake := newFakeNode()
	fake.panicOnInstall = true
	_, status := startAndWait(t, fake, Options{})
	if status.State != StatePartial || status.Cause != "internal_error" {
		t.Fatalf("status = %+v", status)
	}
	if !fake.seen("systemctl disable corosync.service") {
		t.Fatalf("restore did not run after a panic: %v", fake.commands)
	}
}

func TestRerunOnExistingClusterNodeTouchesNothing(t *testing.T) {
	for _, installed := range []bool{true, false} {
		fake := newFakeNode()
		if installed {
			fake.markAllInstalled(Packages)
		} else {
			fake.markAllInstalled([]string{"pacemaker", "corosync", "pcs", "sbd"})
		}
		for _, unit := range moduleUnits(false) {
			fake.enabledServices[unit] = true
			fake.activeServices[unit] = true
		}
		_, status := startAndWait(t, fake, Options{})
		assertNoMask(t, fake)
		if status.State != StateInstalled {
			t.Fatalf("installed=%v: state = %q (%+v)", installed, status.State, status)
		}
		if fake.seen("systemctl disable") || fake.seen("systemctl stop") || fake.seen("systemctl start") || fake.seen("systemctl enable") {
			t.Fatalf("installed=%v: touched a unit: %v", installed, fake.commands)
		}
	}
}

func TestVerifyDetectsStillActiveUnit(t *testing.T) {
	fake := newFakeNode()
	fake.enableAfterInstall = []string{"corosync.service"}
	fake.activateAfterInstall = []string{"corosync.service"}
	fake.stickyUnits["corosync.service"] = true // disable/stop do not take effect
	_, status := startAndWait(t, fake, Options{})
	if status.State != StatePartial || status.Cause != "services_active" {
		t.Fatalf("status = %+v", status)
	}
	if strings.Join(status.UnitsActive, ",") != "corosync.service" {
		t.Fatalf("units_active = %v", status.UnitsActive)
	}
	if !strings.Contains(strings.Join(status.Commands, "\n"), "sudo systemctl disable --now corosync.service") {
		t.Fatalf("manual command missing: %v", status.Commands)
	}
}

func TestLinstorOptInRestoresItsUnits(t *testing.T) {
	fake := newFakeNode()
	_, status := startAndWait(t, fake, Options{Linstor: true})
	if status.State != StateInstalled {
		t.Fatalf("status = %+v", status)
	}
	if !fake.seen("systemctl disable linstor-controller.service") || !fake.seen("systemctl disable linstor-satellite.service") {
		t.Fatalf("LINSTOR units not restored: %v", fake.commands)
	}
}

// A re-run must not launder a unit the previous run left running into a
// "pre-existing" one.
func TestRerunKeepsCarriedActiveUnit(t *testing.T) {
	fake := newFakeNode()
	fake.markAllInstalled(Packages)
	fake.activeServices["corosync.service"] = true
	dir := t.TempDir()
	writePersisted(t, dir, Status{State: StatePartial, Cause: "services_active", UnitsActive: []string{"corosync.service"}})
	m := New(dir, fake.runner())
	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	status := m.Snapshot()
	if status.State != StatePartial || strings.Join(status.UnitsActive, ",") != "corosync.service" {
		t.Fatalf("status = %+v", status)
	}
	if fake.seen("systemctl stop") || fake.seen("systemctl disable") {
		t.Fatalf("a carried unit must never be touched implicitly: %v", fake.commands)
	}
}

func TestReconcileKeepsUnitsActiveUntilSettled(t *testing.T) {
	stop := "sudo systemctl disable --now corosync.service"
	cases := []struct {
		name      string
		active    bool
		enabled   bool
		unknown   bool
		wantState string
	}{
		{name: "still active", active: true, wantState: StatePartial},
		{name: "still enabled", enabled: true, wantState: StatePartial},
		{name: "query fails", unknown: true, wantState: StatePartial},
		{name: "settled", wantState: StateInstalled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writePersisted(t, dir, Status{State: StatePartial, FailedStep: "verify", Cause: "services_active",
				UnitsActive: []string{"corosync.service"}, Commands: []string{stop}})
			fake := newFakeNode()
			fake.markAllInstalled(Packages)
			fake.activeServices["corosync.service"] = tc.active
			fake.enabledServices["corosync.service"] = tc.enabled
			fake.unknownUnits["corosync.service"] = tc.unknown
			status := New(dir, fake.runner()).Reconcile(context.Background())
			if status.State != tc.wantState {
				t.Fatalf("state = %q, want %q (%+v)", status.State, tc.wantState, status)
			}
			has := strings.Contains(strings.Join(status.Commands, "\n"), stop)
			if tc.wantState == StatePartial && (!has || status.Cause != "services_active") {
				t.Fatalf("partial must keep cause and command: %+v", status)
			}
			if tc.wantState == StateInstalled && (has || len(status.UnitsActive) != 0) {
				t.Fatalf("stale units_active/command kept: %+v", status)
			}
			assertNoMask(t, fake)
		})
	}
}

const legacyJSON = `{"hosts":{"%s":{
  "state":"partial","hostname":"%s","failed_step":"apt_update","cause":"network",
  "masks_left":["corosync.service","pacemaker.service","pcsd.service","sbd.service"],
  "protected_units":["sbd.service"],
  "steps":[{"id":"systemd_mask","state":"done"},{"id":"mask_services","state":"done"}],
  "missing":[],"commands":["sudo systemctl unmask --runtime corosync.service"],
  "updated_at":"2026-11-10T10:00:00Z"}}}`

func TestLegacyPersistedStateLoadsAndReverifies(t *testing.T) {
	host := localHostname()
	write := func(dir string) {
		data := strings.ReplaceAll(legacyJSON, "%s", host)
		if err := os.WriteFile(filepath.Join(dir, "ha-module.json"), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	write(dir)
	fake := newFakeNode()
	fake.markAllInstalled(Packages)
	fake.activeServices["pcsd.service"] = true
	m := New(dir, fake.runner())
	status := m.Snapshot()
	if status.State != StatePartial || status.Cause != "services_active" || len(status.UnitsActive) != 4 {
		t.Fatalf("legacy status = %+v", status)
	}
	got := m.Reconcile(context.Background())
	if got.State != StatePartial || strings.Join(got.UnitsActive, ",") != "pcsd.service" {
		t.Fatalf("reconcile with an active unit = %+v", got)
	}
	if fake.seen("mask") || strings.Contains(strings.Join(got.Commands, "\n"), "mask") {
		t.Fatalf("legacy mask command survived: %v", got.Commands)
	}
	raw, _ := os.ReadFile(m.Path())
	for _, key := range []string{"masks_left", "protected_units", "mask_services"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("persisted file still carries %s", key)
		}
	}

	// Once the unit is stopped the same legacy file reconciles to installed.
	dir2 := t.TempDir()
	write(dir2)
	fake2 := newFakeNode()
	fake2.markAllInstalled(Packages)
	if got := New(dir2, fake2.runner()).Reconcile(context.Background()); got.State != StateInstalled {
		t.Fatalf("settled legacy partial = %+v", got)
	}
}

func TestLegacyServicesActiveWithoutMasksIsReverified(t *testing.T) {
	dir := t.TempDir()
	host := localHostname()
	raw := `{"hosts":{"` + host + `":{"state":"partial","cause":"services_active","protected_units":["corosync.service","pacemaker.service","sbd.service"],"steps":[],"missing":[]}}}`
	if err := os.WriteFile(filepath.Join(dir, "ha-module.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(dir, newFakeNode().runner())
	if got := strings.Join(m.Snapshot().UnitsActive, ","); got != "pcsd.service" {
		t.Fatalf("units_active = %q, want the unprotected pcsd.service", got)
	}
}

func TestStatusJSONNeverHasNullArrays(t *testing.T) {
	check := func(label string, status Status) {
		data, err := json.Marshal(status)
		if err != nil {
			t.Fatal(err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"units_active", "unknown_units", "steps", "missing"} {
			if v, ok := obj[key]; !ok || string(v) == "null" {
				t.Fatalf("%s: %s = %s", label, key, v)
			}
		}
		for _, gone := range []string{"masks_left", "protected_units"} {
			if _, ok := obj[gone]; ok {
				t.Fatalf("%s: %s must not be emitted", label, gone)
			}
		}
	}
	fake := newFakeNode()
	m := New(t.TempDir(), fake.runner())
	check("fresh", m.Snapshot())
	check("reconciled", m.Reconcile(context.Background()))
	_, status := startAndWait(t, newFakeNode(), Options{})
	check("installed", status)
	check("zero value clone", cloneStatus(Status{}))
}

func TestInstallStepsAreTheNewOnes(t *testing.T) {
	_, status := startAndWait(t, newFakeNode(), Options{})
	ids := []string{}
	for _, step := range status.Steps {
		ids = append(ids, step.ID)
	}
	if got := strings.Join(ids, ","); got != "preflight,snapshot_units,install_packages,restore_units,verify" {
		t.Fatalf("steps = %s", got)
	}
}

func TestExperimentalAptNeedsAcceptance(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=22\n"
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{})
	if plan.Preflight.OK || !plan.AcceptRequired || plan.Guided {
		t.Fatalf("plan = %+v", plan)
	}
	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	if status := m.Snapshot(); status.Cause != "experimental_not_accepted" {
		t.Fatalf("status = %+v", status)
	}
	if fake.seen("apt-get install") {
		t.Fatal("apt ran without accept_experimental")
	}
	if !m.Plan(context.Background(), Options{AcceptExperimental: true}).Preflight.OK {
		t.Fatal("accepted experimental tier must pass preflight")
	}
}

func TestFencingKindsAreNormalized(t *testing.T) {
	opts := Options{Fencing: []string{"SBD", " pve ", "bogus", "sbd", "", "redfish"}}.Normalized()
	if got := strings.Join(opts.Fencing, ","); got != "sbd,pve,redfish" {
		t.Fatalf("fencing = %q", got)
	}
	if (Options{}).Normalized().Fencing == nil {
		t.Fatal("fencing must never be nil")
	}
}

// A unit that is disabled (or not installed yet) and merely "failed" is not
// running: a package postinst that enables it afterwards must still be undone.
// Found on Ubuntu 24.04, where a previous purge left sbd.service failed.
func TestRestorableTreatsFailedAsNotRunning(t *testing.T) {
	cases := []struct {
		enabled, active string
		want            bool
	}{
		{"disabled", "inactive", true},
		{"not-found", "inactive", true},
		{"not-found", "failed", true},
		{"disabled", "failed", true},
		{"enabled", "failed", false},
		{"disabled", "active", false},
		{"disabled", "activating", false},
		{"masked", "inactive", false},
		{"static", "inactive", false},
	}
	for _, tc := range cases {
		got := unitSnap{unit: "sbd.service", enabled: tc.enabled, active: tc.active}.restorable()
		if got != tc.want {
			t.Errorf("%s/%s restorable = %v, want %v", tc.enabled, tc.active, got, tc.want)
		}
	}
}

// Debian 13 ships three more units that the packages enable (8.32).
var debianExtraUnits = []string{"pcsd-ruby.service", "logd.service", "sbd_remote.service"}

func TestModuleUnitListsIncludeDebianUnits(t *testing.T) {
	units := strings.Join(moduleUnits(false), ",")
	for _, unit := range debianExtraUnits {
		if !strings.Contains(units, unit) {
			t.Fatalf("moduleUnits misses %s: %s", unit, units)
		}
	}
	services := strings.Join(Services, ",")
	for _, name := range []string{"pcsd-ruby", "logd", "sbd_remote"} {
		if !strings.Contains(services, name) {
			t.Fatalf("Services misses %s: %s", name, services)
		}
	}
}

func TestDebianExtraUnitsAreSnapshottedAndRestored(t *testing.T) {
	fake := newFakeNode()
	fake.enableAfterInstall = debianExtraUnits
	fake.activateAfterInstall = []string{"logd.service"}
	_, status := startAndWait(t, fake, Options{})
	assertNoMask(t, fake)
	if status.State != StateInstalled {
		t.Fatalf("status = %+v", status)
	}
	for _, unit := range debianExtraUnits {
		if !fake.seen("systemctl is-enabled " + unit) {
			t.Fatalf("%s was not snapshotted: %v", unit, fake.commands)
		}
		if !fake.seen("systemctl disable " + unit) {
			t.Fatalf("%s was not restored: %v", unit, fake.commands)
		}
	}
	if !fake.seen("systemctl stop logd.service") {
		t.Fatalf("running logd was not stopped: %v", fake.commands)
	}
	if len(status.UnitsActive) != 0 {
		t.Fatalf("units_active = %v", status.UnitsActive)
	}
}

func TestDebianExtraUnitEnabledBeforeIsProtected(t *testing.T) {
	fake := newFakeNode()
	fake.enabledServices["pcsd-ruby.service"] = true
	fake.activeServices["logd.service"] = true
	fake.enableAfterInstall = debianExtraUnits
	_, status := startAndWait(t, fake, Options{})
	if fake.seen("systemctl disable pcsd-ruby.service") || fake.seen("systemctl stop logd.service") || fake.seen("systemctl disable logd.service") {
		t.Fatalf("a unit that was on before was touched: %v", fake.commands)
	}
	if !fake.seen("systemctl disable sbd_remote.service") {
		t.Fatalf("the off unit was not restored: %v", fake.commands)
	}
	if status.State != StateInstalled {
		t.Fatalf("status = %+v", status)
	}
}

// Distros without these units (Ubuntu, Fedora): not-found stays not-found,
// nothing is disabled for them and the log does not claim a restore.
func TestNotFoundUnitsAreHandled(t *testing.T) {
	fake := newFakeNode()
	for _, unit := range append([]string{"corosync.service", "pacemaker.service", "pcsd.service", "sbd.service"}, debianExtraUnits...) {
		fake.notFoundUnits[unit] = true
	}
	_, status := startAndWait(t, fake, Options{})
	assertNoMask(t, fake)
	if status.State != StateInstalled || len(status.UnitsActive) != 0 || len(status.UnknownUnits) != 0 {
		t.Fatalf("status = %+v", status)
	}
	if fake.seen("systemctl disable") || fake.seen("systemctl stop") {
		t.Fatalf("a missing unit was disabled/stopped: %v", fake.commands)
	}
	for _, line := range status.Log {
		if strings.Contains(line, "units restored") {
			t.Fatalf("log claims a restore that never ran: %q", line)
		}
	}
}

func TestNotFoundBeforeButPresentAfterIsRestored(t *testing.T) {
	fake := newFakeNode()
	fake.notFoundUnits["logd.service"] = true
	fake.enableAfterInstall = []string{"logd.service"}
	_, status := startAndWait(t, fake, Options{})
	if !fake.seen("systemctl disable logd.service") || status.State != StateInstalled {
		t.Fatalf("status = %+v cmds=%v", status, fake.commands)
	}
	logged := strings.Join(status.Log, "\n")
	if !strings.Contains(logged, "units restored") || !strings.Contains(logged, "logd.service") {
		t.Fatalf("log = %s", logged)
	}
}

func TestManualDisableCommandCoversPcsdAndDebianUnits(t *testing.T) {
	cmds := strings.Join(manualCommandsFor(aptManager{}, FamilyApt, []string{"pacemaker"}), "\n")
	if !strings.Contains(cmds, "systemctl disable corosync pacemaker pcsd sbd") {
		t.Fatalf("manual disable misses pcsd: %s", cmds)
	}
	if !strings.Contains(cmds, "apt-get install -y --no-install-recommends pacemaker") {
		t.Fatalf("manual apt command differs from the product's: %s", cmds)
	}
	dnf := strings.Join(manualCommandsFor(dnfManager{}, FamilyDnf, []string{"pacemaker"}), "\n")
	if !strings.Contains(dnf, "--setopt=install_weak_deps=False pacemaker") {
		t.Fatalf("manual dnf command differs from the product's: %s", dnf)
	}
}

// 8.32 P1: a unit the operator enabled but stopped before the install must
// not be disabled by WebKVM, but if the postinst starts it the result is
// "partial", never "installed".
func TestProtectedEnabledInactiveUnitStartedByInstallIsReported(t *testing.T) {
	fake := newFakeNode()
	fake.enabledServices["corosync.service"] = true
	fake.activeServices["corosync.service"] = false
	fake.activateAfterInstall = []string{"corosync.service"}
	_, status := startAndWait(t, fake, Options{})
	assertNoMask(t, fake)
	if status.State != StatePartial || status.Cause != "services_active" {
		t.Fatalf("status = %+v", status)
	}
	if !containsAny(status.UnitsActive, "corosync.service") {
		t.Fatalf("units_active = %v", status.UnitsActive)
	}
	commands := strings.Join(status.Commands, "\n")
	if !strings.Contains(commands, "sudo systemctl stop corosync.service") {
		t.Fatalf("commands = %v", status.Commands)
	}
	if strings.Contains(commands, "disable --now corosync.service") {
		t.Fatalf("WebKVM must not disable an operator-enabled unit: %v", status.Commands)
	}
	if fake.seen("systemctl disable corosync.service") || fake.seen("systemctl stop corosync.service") {
		t.Fatalf("the installer touched a protected unit: %v", fake.commands)
	}
}

// The service sandbox (ProtectSystem=full) makes dpkg fail with a read-only
// filesystem. It must be a distinct cause, not "unknown".
func TestClassifySandboxReadonly(t *testing.T) {
	out := "dpkg: unable to create '/usr/bin/x': Read-only file system\nE: Sub-process /usr/bin/dpkg returned an error code (1)"
	if got := classify(out); got != "sandbox_readonly" {
		t.Fatalf("classify = %q, want sandbox_readonly", got)
	}
}

// A failed install must keep its diagnosis when the UI polls the status:
// Reconcile used to clear FailedStep/Cause/Error and leave a bare
// "not installed" (8.37).
func TestReconcileKeepsInstallFailure(t *testing.T) {
	dir := t.TempDir()
	writePersisted(t, dir, Status{
		State:      StatePartial,
		FailedStep: "install_packages",
		Cause:      "sandbox_readonly",
		Error:      "dpkg: unable to create '/usr/bin/x': Read-only file system",
	})
	fake := newFakeNode()
	m := New(dir, fake.runner())
	for i := 0; i < 2; i++ {
		status := m.Reconcile(context.Background())
		if status.State != StateNotInstalled {
			t.Fatalf("poll %d: state = %q, want not_installed", i, status.State)
		}
		if status.FailedStep != "install_packages" || status.Cause != "sandbox_readonly" {
			t.Fatalf("poll %d: failure lost: %+v", i, status)
		}
		if !strings.Contains(status.Error, "Read-only file system") {
			t.Fatalf("poll %d: error lost: %q", i, status.Error)
		}
	}
}
