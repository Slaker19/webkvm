package hainstall

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const (
	fedoraRelease = "ID=fedora\nVERSION_ID=41\n"
	rockyRelease  = "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\nVERSION_ID=\"9.4\"\n"
	archRelease   = "ID=arch\nPRETTY_NAME=\"Arch Linux\"\n"
)

func (f *fakeNode) ranAny(substrs ...string) bool {
	for _, s := range substrs {
		if f.seen(s) {
			return true
		}
	}
	return false
}

func TestFedoraInstallsWithDnfAndRestoresUnits(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = fedoraRelease
	fake.enableAfterInstall = []string{"corosync.service", "sbd.service"}
	fake.activateAfterInstall = []string{"corosync.service"}
	fake.enabledServices["pcsd.service"] = true // already enabled before: untouched
	_, status := startAndWait(t, fake, Options{AcceptExperimental: true})
	if status.State != StateInstalled {
		t.Fatalf("status = %+v", status)
	}
	if !fake.seen("dnf install -y --setopt=install_weak_deps=False pacemaker corosync pcs pacemaker-cli sbd fence-agents-all resource-agents drbd-utils") {
		t.Fatalf("dnf install not run as expected: %v", fake.commands)
	}
	if fake.ranAny("apt-get", "dpkg-query", "pacman") {
		t.Fatalf("another package manager ran: %v", fake.commands)
	}
	if !fake.seen("systemctl disable corosync.service") || !fake.seen("systemctl disable sbd.service") {
		t.Fatalf("new units were not restored: %v", fake.commands)
	}
	if fake.seen("systemctl disable pcsd.service") || fake.seen("systemctl stop pcsd.service") {
		t.Fatalf("a unit that was enabled before was touched: %v", fake.commands)
	}
	if len(status.UnitsActive) != 0 {
		t.Fatalf("units_active = %v", status.UnitsActive)
	}
}

func TestFedoraNeedsAcceptExperimental(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = fedoraRelease
	m, status := startAndWait(t, fake, Options{})
	if status.Cause != "experimental_not_accepted" || fake.seen("dnf install") {
		t.Fatalf("status = %+v", status)
	}
	plan := m.Plan(context.Background(), Options{})
	if !plan.AcceptRequired || plan.Family != FamilyDnf || plan.Tier != TierExperimental || plan.Guided || len(plan.ReposNeeded) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

func TestDnfFailureStillRestoresAndReportsPartial(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = fedoraRelease
	fake.aptFailText = "Error: Unable to find a match: sbd"
	fake.effectsOnFail = true
	fake.enableAfterInstall = []string{"corosync.service"}
	_, status := startAndWait(t, fake, Options{AcceptExperimental: true})
	if status.Cause != "package_unavailable" {
		t.Fatalf("status = %+v", status)
	}
	if status.State == StateInstalled {
		t.Fatal("a failed dnf install must never be installed")
	}
}

func TestDnfCandidateFallsBackToList(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = fedoraRelease
	fake.repoqueryFails = true
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{AcceptExperimental: true})
	if !plan.Preflight.OK || !fake.seen("dnf -q list --available") {
		t.Fatalf("plan = %+v cmds=%v", plan.Preflight, fake.commands)
	}
}

func TestRockyBlocksWithoutHARepoAndGivesExactCommand(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = rockyRelease
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{AcceptExperimental: true})
	if plan.Preflight.OK || len(plan.ReposNeeded) != 1 || !plan.Guided {
		t.Fatalf("plan = %+v", plan)
	}
	if got := plan.ReposNeeded[0].Command; got != "sudo dnf config-manager --set-enabled highavailability" {
		t.Fatalf("command = %q", got)
	}
	if plan.GuidedSteps[0].ID != "enable_ha_repo" {
		t.Fatalf("steps = %+v", plan.GuidedSteps)
	}
	if err := m.Start(Options{AcceptExperimental: true}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	if status := m.Snapshot(); status.Cause != "repo_missing" || fake.seen("dnf install") || fake.seen("config-manager --set-enabled") && !fake.seen("repolist") {
		t.Fatalf("status = %+v", status)
	}
	if fake.seen("dnf config-manager") {
		t.Fatal("WebKVM must never enable repositories by itself")
	}
}

func TestRockyWithHARepoInstalls(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = rockyRelease
	fake.repos = "highavailability Rocky Linux 9 - High Availability\n"
	_, status := startAndWait(t, fake, Options{AcceptExperimental: true})
	if status.State != StateInstalled {
		t.Fatalf("status = %+v", status)
	}
}

func TestRepoNamesPerDistro(t *testing.T) {
	cases := map[string]string{
		"ID=rocky\nVERSION_ID=8.10\n":             "ha",
		"ID=rocky\nVERSION_ID=9.4\n":              "highavailability",
		"ID=almalinux\nVERSION_ID=9.4\n":          "highavailability",
		"ID=rhel\nVERSION_ID=9.4\n":               "rhel-9-for-x86_64-highavailability-rpms",
		"ID=ol\nID_LIKE=fedora\nVERSION_ID=9.3\n": "ol9_addons",
	}
	for release, want := range cases {
		if got := haRepoCandidates(ParseDistro([]byte(release)), "")[0]; got != want {
			t.Errorf("%q: repo = %s, want %s", release, got, want)
		}
	}
	rhel := repoNeed(ParseDistro([]byte("ID=rhel\nVERSION_ID=9.4\n")), "")
	if !strings.HasPrefix(rhel.Command, "sudo subscription-manager repos --enable") {
		t.Fatalf("rhel command = %q", rhel.Command)
	}
}

func TestDnfStorageOptInIsGuidedOnly(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = fedoraRelease
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{AcceptExperimental: true, Linstor: true})
	for _, pkg := range plan.Packages {
		if isLinstorPackage(pkg.Name) && (pkg.Origin != OriginManual || !pkg.Guided) {
			t.Fatalf("linstor package = %+v", pkg)
		}
	}
	if fake.ranAny("curl", "wget") {
		t.Fatal("no remote script may run")
	}
	_, status := startAndWait(t, fake, Options{AcceptExperimental: true, Linstor: true})
	if status.State != StateInstalled || fake.seen("linstor-controller") && fake.seen("dnf install -y --setopt=install_weak_deps=False linstor") {
		t.Fatalf("status = %+v", status)
	}
}

func TestArchAllAURIsGuidedAndNothingIsInstalledByWebKVM(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = archRelease
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{AcceptExperimental: true})
	if !plan.Preflight.OK || plan.CanInstall || !plan.Guided || plan.Tier != TierPartial || !plan.AcceptRequired {
		t.Fatalf("plan = %+v", plan)
	}
	for _, pkg := range plan.Packages {
		if pkg.Origin != OriginAUR || !pkg.Guided {
			t.Fatalf("package = %+v", pkg)
		}
	}
	var aur *GuidedStep
	for i := range plan.GuidedSteps {
		if plan.GuidedSteps[i].ID == "aur_build" {
			aur = &plan.GuidedSteps[i]
		}
	}
	if aur == nil || aur.RunAs != "user" || !strings.Contains(strings.Join(aur.Commands, "\n"), "yay -S --needed corosync resource-agents fence-agents sbd pacemaker pcs") {
		t.Fatalf("aur step = %+v", aur)
	}
	if !strings.Contains(strings.Join(aur.Commands, "\n"), "never as root") {
		t.Fatal("the NORMAL user warning is missing")
	}
	m2, status := startAndWait(t, fake, Options{AcceptExperimental: true})
	if status.State != StatePartialGuided || len(status.GuidedMissing) != 6 {
		t.Fatalf("status = %+v", status)
	}
	_ = m2
	for _, command := range fake.commands {
		if strings.HasPrefix(command, "pacman -S ") || strings.Contains(command, "makepkg") || strings.Contains(command, "yay") || strings.Contains(command, "paru") {
			t.Fatalf("WebKVM ran %q", command)
		}
	}
	if fake.seen("systemctl disable") || fake.seen("systemctl stop") {
		t.Fatal("nothing was installed, no unit may be touched")
	}
}

func TestArchVerifyMovesFromPartialGuidedToInstalled(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = archRelease
	m, status := startAndWait(t, fake, Options{AcceptExperimental: true})
	if status.State != StatePartialGuided {
		t.Fatalf("status = %+v", status)
	}
	fake.markAllInstalled([]string{"corosync", "resource-agents", "fence-agents", "sbd"})
	if got := m.Verify(context.Background()); got.State != StatePartialGuided || strings.Join(got.GuidedMissing, ",") != "pacemaker,pcs" {
		t.Fatalf("after partial AUR build: %+v", got)
	}
	fake.markAllInstalled([]string{"pacemaker", "pcs"})
	if got := m.Verify(context.Background()); got.State != StateInstalled || len(got.GuidedMissing) != 0 {
		t.Fatalf("after full AUR build: %+v", got)
	}
}

func TestArchOfficialPackagesAreInstalledWithNeededNeverSy(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = archRelease
	fake.pacmanRepos = map[string]string{"corosync": "extra", "sbd": "extra", "pcs": "chaotic-aur"}
	fake.enableAfterInstall = []string{"corosync.service"}
	fake.activateAfterInstall = []string{"corosync.service"}
	fake.enabledServices["pacemaker.service"] = true
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{AcceptExperimental: true})
	if !plan.CanInstall || strings.Join(installable(plan.Packages), ",") != "corosync,sbd" {
		t.Fatalf("plan = %+v", plan)
	}
	for _, pkg := range plan.Packages {
		if pkg.Name == "pcs" && (pkg.Origin != OriginThirdParty || !pkg.Guided) {
			t.Fatalf("a third-party repo package must be guided: %+v", pkg)
		}
	}
	if err := m.Start(Options{AcceptExperimental: true}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	status := m.Snapshot()
	if !fake.seen("pacman -S --needed --noconfirm corosync sbd") {
		t.Fatalf("pacman install missing: %v", fake.commands)
	}
	for _, command := range fake.commands {
		if strings.HasPrefix(command, "pacman -Sy") || strings.HasPrefix(command, "pacman -Syu") || strings.HasPrefix(command, "pacman -Su") {
			t.Fatalf("forbidden pacman call %q", command)
		}
	}
	if status.State != StatePartialGuided || !fake.seen("systemctl disable corosync.service") {
		t.Fatalf("status = %+v cmds=%v", status, fake.commands)
	}
	if fake.seen("systemctl disable pacemaker.service") {
		t.Fatal("pre-enabled unit touched")
	}
	if len(status.UnitsActive) != 0 {
		t.Fatalf("units_active = %v", status.UnitsActive)
	}
}

func TestArchPacmanFailureCleansUpAndSuggestsSyu(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = archRelease
	fake.pacmanRepos = map[string]string{"corosync": "extra"}
	fake.aptFailText = "error: target not found: corosync"
	_, status := startAndWait(t, fake, Options{AcceptExperimental: true})
	if status.Cause != "package_unavailable" || status.State == StateInstalled {
		t.Fatalf("status = %+v", status)
	}
	if !strings.Contains(strings.Join(status.Commands, "\n"), "sudo pacman -Syu") {
		t.Fatalf("commands = %v", status.Commands)
	}
}

func TestArchMissingSyncDBBlocksWithGuidedSyu(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = archRelease
	fake.noSyncDB = true
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{AcceptExperimental: true})
	if plan.Preflight.OK || plan.GuidedSteps[0].ID != "sync_db" || plan.GuidedSteps[0].Commands[len(plan.GuidedSteps[0].Commands)-1] != "sudo pacman -Syu" {
		t.Fatalf("plan = %+v", plan)
	}
	fake.noSyncDB, fake.syncStale = false, true
	plan = m.Plan(context.Background(), Options{AcceptExperimental: true})
	if !plan.Preflight.OK || plan.GuidedSteps[0].ID != "sync_db" {
		t.Fatalf("stale db must warn, not block: %+v", plan)
	}
}

func TestArchDerivativesAndStorageOptIn(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = "ID=cachyos\nID_LIKE=arch\n"
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{AcceptExperimental: true, Linstor: true})
	names := []string{}
	for _, pkg := range plan.Packages {
		names = append(names, pkg.Name)
	}
	if !containsAny(names, "drbd-dkms") || plan.Family != FamilyPacman {
		t.Fatalf("plan = %+v", plan)
	}
	foundStorage := false
	for _, step := range plan.GuidedSteps {
		if step.ID == "storage_manual" {
			foundStorage = true
			commands := strings.Join(step.Commands, "\n")
			utils := strings.Index(commands, "LINBIT/drbd-utils")
			dkms := strings.Index(commands, "drbd-dkms")
			if utils == -1 || dkms == -1 || utils > dkms {
				t.Fatalf("drbd-utils must be built before drbd-dkms: %v", step.Commands)
			}
		}
		if step.ID == "aur_build" {
			for _, command := range step.Commands {
				if strings.Contains(command, "drbd-dkms") {
					t.Fatalf("drbd-dkms must not be in the AUR batch: %v", step.Commands)
				}
			}
		}
	}
	if !foundStorage {
		t.Fatalf("steps = %+v", plan.GuidedSteps)
	}
}

func TestPkgLockPerFamily(t *testing.T) {
	for _, release := range []string{fedoraRelease, archRelease} {
		fake := newFakeNode()
		fake.osRelease = release
		fake.lockBusy = true
		pf := New(t.TempDir(), fake.runner()).Preflight(context.Background(), Options{AcceptExperimental: true})
		var lock *Check
		for i := range pf.Checks {
			if pf.Checks[i].ID == "pkg_lock" {
				lock = &pf.Checks[i]
			}
			if pf.Checks[i].ID == "apt_lock" {
				t.Fatal("apt_lock was replaced by pkg_lock")
			}
		}
		if lock == nil || lock.OK || pf.OK {
			t.Fatalf("%q: lock check = %+v", release, lock)
		}
	}
}

func TestWatchdogAndVirtChecks(t *testing.T) {
	cases := []struct {
		name, devices, driver, virt, wantType, wantHint string
		wantOK                                          bool
	}{
		{"hardware", "/dev/watchdog\n/dev/watchdog0\n", "iTCO_wdt 16384 1\n", "", "hardware", "physical", true},
		{"i6300esb in a vm", "/dev/watchdog0\n", "i6300esb 16384 0\n", "kvm", "i6300esb", "vm", true},
		{"softdog", "/dev/watchdog\n", "softdog 16384 0\n", "", "softdog", "physical", false},
		{"none in a vm", "", "", "kvm", "none", "vm", false},
		{"none on metal", "", "", "", "none", "physical", false},
		{"builtin driver", "/dev/watchdog0\n", "", "", "hardware", "physical", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := newFakeNode()
			fake.watchdogDevices, fake.watchdogDriver, fake.virt = tc.devices, tc.driver, tc.virt
			pf := New(t.TempDir(), fake.runner()).Preflight(context.Background(), Options{})
			if pf.Watchdog.Type != tc.wantType || pf.FencingHint != tc.wantHint {
				t.Fatalf("watchdog = %+v hint = %s", pf.Watchdog, pf.FencingHint)
			}
			if pf.Watchdog.Devices == nil {
				t.Fatal("devices must never be null")
			}
			for _, check := range pf.Checks {
				if check.ID == "watchdog" && (check.OK != tc.wantOK || check.Severity != "warning" || (!check.OK && check.Hint == "")) {
					t.Fatalf("watchdog check = %+v", check)
				}
			}
			if tc.virt != "" && pf.Virt != tc.virt || tc.virt == "" && pf.Virt != "none" {
				t.Fatalf("virt = %q", pf.Virt)
			}
			if !pf.OK {
				t.Fatalf("a missing watchdog is a warning, never a blocker: %+v", pf)
			}
		})
	}
}

func TestPlanJSONContractArraysNeverNull(t *testing.T) {
	for _, release := range []string{"ID=ubuntu\nVERSION_ID=\"24.04\"\n", fedoraRelease, archRelease, "ID=alpine\n"} {
		fake := newFakeNode()
		fake.osRelease = release
		plan := New(t.TempDir(), fake.runner()).Plan(context.Background(), Options{})
		data, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		_ = json.Unmarshal(data, &raw)
		for _, key := range []string{"packages", "missing", "services", "manual_commands", "repos_needed", "guided_steps", "fencing", "guided", "can_install", "accept_required", "family", "tier"} {
			if v, ok := raw[key]; !ok || v == nil {
				t.Fatalf("%q: plan.%s is %v in %s", release, key, v, data)
			}
		}
		pf := raw["preflight"].(map[string]any)
		for _, key := range []string{"checks", "watchdog", "virt", "fencing_hint"} {
			if v, ok := pf[key]; !ok || v == nil {
				t.Fatalf("%q: preflight.%s missing in %s", release, key, data)
			}
		}
	}
}

func TestUninstallPerFamily(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = fedoraRelease
	fake.markAllInstalled([]string{"pacemaker", "corosync"})
	m := New(t.TempDir(), fake.runner())
	if err := m.StartUninstall(false); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	if !fake.seen("dnf remove -y pacemaker corosync") || m.Snapshot().State != StateNotInstalled {
		t.Fatalf("cmds=%v status=%+v", fake.commands, m.Snapshot())
	}

	fake = newFakeNode()
	fake.osRelease = archRelease
	fake.markAllInstalled([]string{"pacemaker"})
	m = New(t.TempDir(), fake.runner())
	if err := m.StartUninstall(false); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	status := m.Snapshot()
	if fake.seen("pacman -R") || status.State != StatePartial || !strings.Contains(strings.Join(status.Commands, " "), "sudo pacman -R pacemaker") {
		t.Fatalf("operator-built packages must be a manual command: %+v cmds=%v", status, fake.commands)
	}
}

// drbd-utils is not packaged by Rocky/Alma/RHEL (checked on Rocky 10 with the
// HighAvailability and CRB repositories enabled): it must not block the
// cluster stack, and WebKVM must not ask dnf for it either.
func TestRockyWithoutDrbdUtilsStillInstallsTheClusterStack(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = rockyRelease
	fake.repos = "highavailability Rocky Linux 9 - High Availability\n"
	fake.candidates = map[string]string{"drbd-utils": ""}
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{AcceptExperimental: true})
	if !plan.Preflight.OK || !plan.CanInstall {
		t.Fatalf("plan = %+v", plan.Preflight)
	}
	for _, pkg := range plan.Packages {
		if pkg.Name == "drbd-utils" && (pkg.Origin != OriginManual || !pkg.Guided) {
			t.Fatalf("drbd-utils = %+v", pkg)
		}
	}
	_, status := startAndWait(t, fake, Options{AcceptExperimental: true})
	if status.State != StateInstalled || status.Cause != "" {
		t.Fatalf("status = %+v", status)
	}
	if !fake.seen("dnf install -y --setopt=install_weak_deps=False pacemaker corosync pcs pacemaker-cli sbd fence-agents-all resource-agents") || fake.seen("resource-agents drbd-utils") {
		t.Fatalf("commands = %v", fake.commands)
	}
}

// The manual command list is meant to be run top to bottom: the repository
// (or the full upgrade) must come before the install that needs it. Found on
// Alma 9, where `dnf install` was listed before `config-manager --set-enabled`.
func TestManualCommandsEnableRepoBeforeInstall(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = rockyRelease
	plan := New(t.TempDir(), fake.runner()).Plan(context.Background(), Options{AcceptExperimental: true})
	repo, install := -1, -1
	for i, c := range plan.ManualCommands {
		if strings.Contains(c, "config-manager --set-enabled") {
			repo = i
		}
		if strings.HasPrefix(c, "sudo dnf install -y --setopt=install_weak_deps=False pacemaker") {
			install = i
		}
	}
	if repo < 0 || install < 0 || repo > install {
		t.Fatalf("repo at %d, install at %d: %v", repo, install, plan.ManualCommands)
	}
}

// 8.34 defect 2: the CLI reuses the plan it just resolved, so a check does
// not ask pacman about every package a second time.
func TestReconcilePlanReusesResolvedPackages(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = archRelease
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{AcceptExperimental: true})
	before := fake.countPrefix("pacman -")
	status := m.ReconcilePlan(context.Background(), plan)
	if got := fake.countPrefix("pacman -"); got != before {
		t.Fatalf("ReconcilePlan repeated pacman queries: %d -> %d", before, got)
	}
	if status.State != StatePartialGuided {
		t.Fatalf("status = %+v", status)
	}
}

// Debian 13 splits crm_mon into pacemaker-cli-utils; without it `pcs status`
// fails after a successful install (found in the live Debian 13 test).
func TestAptProfileInstallsCrmMon(t *testing.T) {
	for _, spec := range profile(FamilyApt, false) {
		if spec.Logical == "pacemaker-cli-utils" {
			return
		}
	}
	t.Fatal("apt profile must install pacemaker-cli-utils (crm_mon)")
}

// Ubuntu 24.04's curated resource-agents-base lacks VirtualDomain and
// Filesystem (KVM/storage HA). The monolithic resource-agents has no
// candidate there, so the profile must add resource-agents-extra. Debian's
// monolithic package already provides them and must not get a duplicate.
func TestUbuntuSplitAgentsAddExtraOnlyForBase(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = "ID=ubuntu\nVERSION_ID=\"24.04\"\n"
	fake.candidates["resource-agents"] = ""
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{})
	found := false
	for _, pkg := range plan.Packages {
		if pkg.Name == "resource-agents-extra" {
			found = true
		}
	}
	if !found {
		t.Fatalf("resource-agents-extra missing from Ubuntu plan: %+v", plan.Missing)
	}

	debian := newFakeNode()
	debian.markAllInstalled([]string{"resource-agents"})
	plan = New(t.TempDir(), debian.runner()).Plan(context.Background(), Options{})
	for _, pkg := range plan.Packages {
		if pkg.Name == "resource-agents-extra" {
			t.Fatalf("Debian must not add resource-agents-extra: %+v", plan.Missing)
		}
	}
}
