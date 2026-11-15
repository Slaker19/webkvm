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
	"testing"
	"time"

	"webkvm/internal/sysutil"
)

type fakeNode struct {
	mu                   sync.Mutex
	osRelease            string
	installed            map[string]bool
	candidates           map[string]string
	lockBusy             bool
	drbd                 bool
	aptFail              int    // network failures before success; -1 = always fail
	aptFailText          string // deterministic failure text (non-network)
	activeServices       map[string]bool
	enabledServices      map[string]bool
	activateAfterInstall []string
	enableAfterInstall   []string        // units a postinst enables
	unknownUnits         map[string]bool // systemctl queries time out
	notFoundUnits        map[string]bool // unit file does not exist on this distro
	stickyUnits          map[string]bool // disable/stop have no effect
	effectsOnFail        bool            // postinst effects happen even when apt fails
	panicOnInstall       bool
	staysUnknown         bool // unit queries keep failing after the install
	commands             []string
	installCalls         int
	watchdogDevices      string            // output of ls -1 /dev/watchdog*
	watchdogDriver       string            // output of lsmod | grep
	virt                 string            // systemd-detect-virt output ("" = none)
	repoqueryFails       bool              // dnf repoquery errors, forcing the list fallback
	repos                string            // dnf repolist output
	pacmanRepos          map[string]string // package -> official repo in pacman -Si
	noSyncDB             bool
	syncStale            bool
}

func newFakeNode() *fakeNode {
	return &fakeNode{
		osRelease:       "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
		installed:       map[string]bool{},
		candidates:      map[string]string{},
		drbd:            true,
		activeServices:  map[string]bool{},
		enabledServices: map[string]bool{},
		unknownUnits:    map[string]bool{},
		notFoundUnits:   map[string]bool{},
		stickyUnits:     map[string]bool{},
	}
}

func (f *fakeNode) record(command string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, command)
}

func (f *fakeNode) countPrefix(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, command := range f.commands {
		if strings.HasPrefix(command, prefix) {
			n++
		}
	}
	return n
}

func (f *fakeNode) seen(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, command := range f.commands {
		if strings.Contains(command, substr) {
			return true
		}
	}
	return false
}

func (f *fakeNode) markAllInstalled(names []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, name := range names {
		f.installed[name] = true
	}
}

func (f *fakeNode) runner() *sysutil.MockCommandRunner {
	return &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		f.record(name + " " + strings.Join(args, " "))
		f.mu.Lock()
		defer f.mu.Unlock()
		switch name {
		case "cat":
			return []byte(f.osRelease), nil
		case "id":
			return []byte("0\n"), nil
		case "sh":
			script := strings.Join(args, " ")
			switch {
			case strings.Contains(script, "lsmod"):
				if f.watchdogDriver == "" {
					return nil, errors.New("exit status 1")
				}
				return []byte(f.watchdogDriver), nil
			case strings.Contains(script, "pacman/sync") && strings.Contains(script, "-mtime"):
				if f.syncStale {
					return []byte("/var/lib/pacman/sync/extra.db\n"), nil
				}
				return nil, nil
			case strings.Contains(script, "pacman/sync"):
				if f.noSyncDB {
					return nil, nil
				}
				return []byte("/var/lib/pacman/sync/core.db\n"), nil
			case strings.Contains(script, "/dev/watchdog"):
				if f.watchdogDevices == "" {
					return nil, errors.New("exit status 2")
				}
				return []byte(f.watchdogDevices), nil
			}
			if f.lockBusy {
				return []byte("4242\n"), nil
			}
			return nil, nil
		case "systemd-detect-virt":
			if f.virt == "" {
				return []byte("none\n"), errors.New("exit status 1")
			}
			return []byte(f.virt + "\n"), nil
		case "rpm":
			pkg := args[len(args)-1]
			if f.installed[pkg] {
				return []byte("1.0-1"), nil
			}
			return []byte("package " + pkg + " is not installed\n"), errors.New("exit status 1")
		case "pacman":
			pkg := args[len(args)-1]
			if args[0] == "-Si" {
				if repo, ok := f.pacmanRepos[pkg]; ok {
					return []byte("Repository      : " + repo + "\nName            : " + pkg + "\nVersion         : 9.9-1\n"), nil
				}
				return []byte("error: package '" + pkg + "' was not found\n"), errors.New("exit status 1")
			}
			if args[0] == "-S" {
				f.installCalls++
				if f.aptFailText != "" {
					return []byte(f.aptFailText), errors.New("exit status 1")
				}
				for _, arg := range args {
					if !strings.HasPrefix(arg, "-") {
						f.installed[arg] = true
					}
				}
				return nil, nil
			}
			if len(args) > 0 && args[0] == "-Q" && f.installed[pkg] {
				return []byte(pkg + " 1.0-1\n"), nil
			}
			if len(args) > 0 && args[0] == "-Q" {
				return []byte("error: package '" + pkg + "' was not found\n"), errors.New("exit status 1")
			}
			return nil, errors.New("unexpected pacman call")
		case "dnf":
			return f.dnf(args)
		case "apt-cache":
			if len(args) > 1 {
				if candidate, ok := f.candidates[args[1]]; ok {
					if candidate == "" {
						return []byte("Candidate: (none)\n"), nil
					}
					return []byte("Candidate: " + candidate + "\n"), nil
				}
				return []byte("Candidate: 1.0\n"), nil
			}
			return nil, nil
		case "dpkg-query":
			pkg := args[len(args)-1]
			if f.installed[pkg] {
				return []byte("install ok installed"), nil
			}
			return nil, errors.New("package not installed")
		case "df":
			return []byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 100000 1000 50000000 1% /\n"), nil
		case "timedatectl":
			return []byte("yes\n"), nil
		case "hostname":
			return []byte("webkvm-ha1.example.com\n"), nil
		case "getent":
			return nil, nil
		case "modinfo":
			if f.drbd {
				return []byte("filename: drbd.ko\n"), nil
			}
			return nil, errors.New("module not found")
		case "test":
			return nil, errors.New("not found")
		case "cp", "install", "rm":
			return nil, nil
		case "systemctl":
			if len(args) > 1 && f.unknownUnits[args[1]] && (args[0] == "is-enabled" || args[0] == "is-active") {
				return nil, context.DeadlineExceeded
			}
			if len(args) > 1 && args[0] == "is-enabled" {
				if f.notFoundUnits[args[1]] && !f.enabledServices[args[1]] {
					return []byte("Failed to get unit file state for " + args[1] + ": No such file or directory\n"), nil
				}
				if f.enabledServices[args[1]] {
					return []byte("enabled\n"), nil
				}
				return []byte("disabled\n"), nil
			}
			if len(args) > 1 && args[0] == "is-active" {
				if f.activeServices[args[1]] {
					return []byte("active\n"), nil
				}
				return []byte("inactive\n"), nil
			}
			if len(args) > 1 && args[0] == "disable" {
				for _, unit := range args[1:] {
					if !f.stickyUnits[unit] {
						f.enabledServices[unit] = false
					}
				}
			}
			if len(args) > 1 && args[0] == "stop" {
				for _, unit := range args[1:] {
					if !f.stickyUnits[unit] {
						f.activeServices[unit] = false
					}
				}
			}
			return nil, nil
		case "env":
			if len(args) < 3 || args[1] != "apt-get" {
				return nil, nil
			}
			switch args[2] {
			case "update":
				return nil, nil
			case "install":
				f.installCalls++
				if f.panicOnInstall {
					panic("boom")
				}
				if f.effectsOnFail {
					f.postinst()
				}
				if f.aptFailText != "" {
					return []byte(f.aptFailText), errors.New("exit status 100")
				}
				if f.aptFail > 0 {
					f.aptFail--
					return []byte("Temporary failure resolving 'archive.ubuntu.com'"), errors.New("exit status 100")
				}
				if f.aptFail < 0 {
					return []byte("Failed to fetch http://archive.ubuntu.com"), errors.New("exit status 100")
				}
				for _, arg := range args[3:] {
					if strings.HasPrefix(arg, "-") {
						continue
					}
					f.installed[arg] = true
				}
				for _, unit := range f.activateAfterInstall {
					f.activeServices[unit] = true
				}
				for _, unit := range f.enableAfterInstall {
					f.enabledServices[unit] = true
				}
				if !f.staysUnknown {
					f.unknownUnits = map[string]bool{}
				}
				return nil, nil
			case "purge", "remove":
				for _, arg := range args[3:] {
					if strings.HasPrefix(arg, "-") {
						continue
					}
					f.installed[arg] = false
				}
				return nil, nil
			}
		}
		return nil, nil
	}}
}

func (f *fakeNode) postinst() {
	for _, unit := range f.activateAfterInstall {
		f.activeServices[unit] = true
	}
	for _, unit := range f.enableAfterInstall {
		f.enabledServices[unit] = true
	}
}

func (f *fakeNode) dnf(args []string) ([]byte, error) {
	joined := strings.Join(args, " ")
	pkg := args[len(args)-1]
	switch {
	case strings.Contains(joined, "repoquery"):
		if f.repoqueryFails {
			return []byte("No such command: repoquery"), errors.New("exit status 2")
		}
		if candidate, ok := f.candidates[pkg]; ok {
			return []byte(candidate), nil
		}
		return []byte("2.1-1.fc41\n"), nil
	case strings.Contains(joined, "list --available"):
		if candidate, ok := f.candidates[pkg]; ok && candidate == "" {
			return []byte("Error: No matching Packages to list\n"), errors.New("exit status 1")
		}
		return []byte("Available Packages\n" + pkg + ".x86_64    3.0-1.fc41    fedora\n"), nil
	case args[0] == "repolist" || (len(args) > 1 && args[1] == "repolist"):
		if f.repos == "" {
			return []byte("repo id repo name\nbaseos Rocky Linux BaseOS\nappstream AppStream\n"), nil
		}
		return []byte("repo id repo name\n" + f.repos), nil
	case args[0] == "install":
		f.installCalls++
		if f.aptFailText != "" {
			return []byte(f.aptFailText), errors.New("exit status 1")
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") || arg == "install" {
				continue
			}
			f.installed[arg] = true
		}
		f.postinst()
		return nil, nil
	case args[0] == "remove":
		for _, arg := range args {
			if strings.HasPrefix(arg, "-") || arg == "remove" {
				continue
			}
			f.installed[arg] = false
		}
		return nil, nil
	}
	return nil, nil
}

func waitDone(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for m.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("install did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPreflightUnsupportedDistroStopsBeforeApt(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = "ID=alpine\nVERSION_ID=3.20\n"
	m := New(t.TempDir(), fake.runner())
	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	status := m.Snapshot()
	if status.State != StateNotInstalled {
		t.Fatalf("state = %q, want not_installed", status.State)
	}
	if status.FailedStep != "preflight" || status.Cause != "unsupported_distro" {
		t.Fatalf("failed step/cause = %q/%q", status.FailedStep, status.Cause)
	}
	if fake.seen("apt-get install") {
		t.Fatal("apt-get install ran after a failed preflight")
	}
}

func TestMissingPackageStopsBeforeApt(t *testing.T) {
	fake := newFakeNode()
	// Neither the split package nor the monolithic alternative has a candidate.
	fake.candidates["resource-agents-base"] = ""
	fake.candidates["resource-agents"] = ""
	m := New(t.TempDir(), fake.runner())
	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	status := m.Snapshot()
	if status.FailedStep != "preflight" {
		t.Fatalf("failed step = %q, want preflight", status.FailedStep)
	}
	if !strings.Contains(status.Error, "resource-agents") {
		t.Fatalf("error does not list the missing package: %q", status.Error)
	}
	if fake.seen("apt-get install") {
		t.Fatal("apt-get install ran when a package has no candidate")
	}
}

func TestPkgLockBusyBlocksBeforeAnything(t *testing.T) {
	fake := newFakeNode()
	fake.lockBusy = true
	m := New(t.TempDir(), fake.runner())
	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	status := m.Snapshot()
	if status.FailedStep != "preflight" || !strings.Contains(status.Error, "pkg_lock") {
		t.Fatalf("unexpected preflight result: %+v", status)
	}
	if fake.seen("apt-get install") {
		t.Fatal("apt ran with the lock busy")
	}
}

func TestNetworkFailureRetriesThenFails(t *testing.T) {
	fake := newFakeNode()
	fake.aptFail = -1
	m := New(t.TempDir(), fake.runner())
	m.sleep = func(time.Duration) {}
	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	status := m.Snapshot()
	if status.State != StatePartial {
		t.Fatalf("state = %q, want partial", status.State)
	}
	if status.Cause != "network" {
		t.Fatalf("cause = %q, want network", status.Cause)
	}
	if fake.installCalls != 3 {
		t.Fatalf("install attempts = %d, want 3 (network retry)", fake.installCalls)
	}
	if len(status.Commands) == 0 {
		t.Fatal("no manual commands after failure")
	}
}

func TestPackageFailureIsPartialWithoutPurging(t *testing.T) {
	fake := newFakeNode()
	fake.aptFailText = "E: Unable to locate package resource-agents"
	m := New(t.TempDir(), fake.runner())
	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	status := m.Snapshot()
	if status.State != StatePartial {
		t.Fatalf("state = %q, want partial", status.State)
	}
	if status.Cause != "package_unavailable" {
		t.Fatalf("cause = %q, want package_unavailable", status.Cause)
	}
	if !strings.Contains(strings.Join(status.Commands, "\n"), "apt-get install -y") {
		t.Fatalf("manual install command missing: %v", status.Commands)
	}
	if fake.seen("apt-get purge") {
		t.Fatal("failure must not purge packages")
	}
	if status.FailedStep != "install_packages" {
		t.Fatalf("failed step = %q, want install_packages", status.FailedStep)
	}
}

func TestSecondRunIsIdempotent(t *testing.T) {
	fake := newFakeNode()
	fake.markAllInstalled(Packages)
	m := New(t.TempDir(), fake.runner())
	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	status := m.Snapshot()
	if status.State != StateInstalled {
		t.Fatalf("state = %q, want installed", status.State)
	}
	if fake.installCalls != 0 || fake.seen("apt-get install") {
		t.Fatalf("apt-get install ran on an already installed node (calls=%d)", fake.installCalls)
	}
	if fake.seen("systemctl disable") || fake.seen("systemctl stop") {
		t.Fatalf("a fully installed node must not touch units: %v", fake.commands)
	}
	if len(status.UnitsActive) != 0 || len(status.UnknownUnits) != 0 {
		t.Fatalf("unexpected units reported: %+v", status)
	}
}

func TestUninstallPurgesOnlyInstalledPackages(t *testing.T) {
	fake := newFakeNode()
	fake.markAllInstalled(Packages)
	delete(fake.installed, "sbd")
	m := New(t.TempDir(), fake.runner())
	if err := m.StartUninstall(true); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	if !fake.seen("apt-get purge -y") {
		t.Fatal("explicit purge did not purge")
	}
	if strings.Contains(strings.Join(fake.commands, "\n"), "purge -y pacemaker corosync pcs sbd") {
		t.Fatal("uninstall asked apt to purge a package that was not installed")
	}
	if m.Snapshot().State != StateNotInstalled {
		t.Fatalf("state = %q", m.Snapshot().State)
	}
}

// Removing packages must be the default; purge deletes corosync.conf and is
// only used with the extra explicit flag.
func TestUninstallUsesRemoveByDefault(t *testing.T) {
	fake := newFakeNode()
	fake.markAllInstalled(Packages)
	m := New(t.TempDir(), fake.runner())
	if err := m.StartUninstall(false); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	if !fake.seen("apt-get remove -y") {
		t.Fatal("default uninstall did not use apt-get remove")
	}
	if fake.seen("apt-get purge") {
		t.Fatal("default uninstall purged configuration")
	}
	if m.Snapshot().State != StateNotInstalled {
		t.Fatalf("state = %q", m.Snapshot().State)
	}
}

func TestInterruptedInstallIsReportedAsPartial(t *testing.T) {
	dir := t.TempDir()
	m := New(dir, newFakeNode().runner())
	m.mu.Lock()
	m.status = Status{State: StateInstalling, Missing: []string{"pcs"}}
	m.saveLocked()
	m.mu.Unlock()

	reloaded := New(dir, newFakeNode().runner())
	status := reloaded.Snapshot()
	if status.State != StatePartial || status.Cause != "interrupted" {
		t.Fatalf("reloaded status = %+v", status)
	}
}

func TestPlanListsOriginsAndMissing(t *testing.T) {
	fake := newFakeNode()
	fake.markAllInstalled([]string{"pacemaker"})
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{Linstor: true})
	if plan.Preflight.OK != true {
		t.Fatalf("preflight failed: %+v", plan.Preflight)
	}
	if len(plan.Missing) != len(Packages)-1+len(LinstorPackages)+1 {
		t.Fatalf("missing = %v", plan.Missing)
	}
	origins := map[string]string{}
	for _, pkg := range plan.Packages {
		origins[pkg.Name] = pkg.Origin
	}
	if origins["linstor-controller"] == "" || origins["linstor-controller"] == "distro" {
		t.Fatalf("LINSTOR origin not shown: %q", origins["linstor-controller"])
	}
	if origins["pacemaker"] != OriginOfficial || origins["linstor-controller"] != OriginThirdParty {
		t.Fatalf("pacemaker origin = %q", origins["pacemaker"])
	}
	if fmt.Sprint(plan.Services) != fmt.Sprint(Services) {
		t.Fatalf("services = %v", plan.Services)
	}
}

// The state file is shared through DATA_DIR (DRBD). A member must never
// inherit another member's "installed" claim: the UI reads the local dpkg.
func TestSharedStateFromAnotherHostnameDoesNotClaimInstalled(t *testing.T) {
	dir := t.TempDir()
	file := persistedState{Hosts: map[string]Status{
		"other-node": {State: StateInstalled, Hostname: "other-node"},
	}}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ha-module.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	m := New(dir, newFakeNode().runner())
	m.hostname = "local-node"
	status := m.Reconcile(context.Background())
	if status.State != StateNotInstalled {
		t.Fatalf("state = %q, want not_installed for local-node", status.State)
	}
	if len(status.Missing) != len(Packages)+1 {
		t.Fatalf("missing = %v, want all packages", status.Missing)
	}
	if status.Hostname != "local-node" {
		t.Fatalf("hostname = %q", status.Hostname)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "ha-module.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved persistedState
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved.Hosts["other-node"]; !ok {
		t.Fatal("the other member status was dropped from the shared file")
	}
}

// A persisted "installed" for this hostname is still only a claim: removed
// packages must show up immediately, not after a manual Verify.
func TestReconcileOverridesInstalledWhenDpkgSaysMissing(t *testing.T) {
	dir := t.TempDir()
	host := localHostname()
	file := persistedState{Hosts: map[string]Status{
		host: {State: StateInstalled, Hostname: host},
	}}
	data, _ := json.Marshal(file)
	if err := os.WriteFile(filepath.Join(dir, "ha-module.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(dir, newFakeNode().runner())
	status := m.Reconcile(context.Background())
	if status.State != StateNotInstalled {
		t.Fatalf("state = %q, want not_installed", status.State)
	}
	if len(status.Missing) != len(Packages)+1 {
		t.Fatalf("missing = %v", status.Missing)
	}
}

func writePersisted(t *testing.T, dir string, status Status) {
	t.Helper()
	host := localHostname()
	status.Hostname = host
	data, err := json.Marshal(persistedState{Hosts: map[string]Status{host: status}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ha-module.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Ubuntu 24.04+ ships fence-agents/resource-agents only as split "-base"
// packages. When those are installed the module is complete: preflight must
// pass and apt must not run (regression from the lab nodes on 26.04).
func TestUbuntuSplitAgentsAlreadyInstalledSkipApt(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = "ID=ubuntu\nVERSION_ID=\"26.04\"\n"
	fake.candidates["fence-agents"] = ""
	fake.candidates["resource-agents"] = ""
	fake.markAllInstalled([]string{"pacemaker", "corosync", "pcs", "pacemaker-cli-utils", "sbd", "drbd-utils", "fence-agents-base", "resource-agents-base", "resource-agents-extra"})
	m := New(t.TempDir(), fake.runner())

	plan := m.Plan(context.Background(), Options{})
	if !plan.Preflight.OK {
		t.Fatalf("preflight blocked: %+v", plan.Preflight)
	}
	if len(plan.Missing) != 0 {
		t.Fatalf("missing = %v, want none", plan.Missing)
	}
	byLogical := map[string]Package{}
	for _, pkg := range plan.Packages {
		if pkg.Logical != "" {
			byLogical[pkg.Logical] = pkg
		}
	}
	if byLogical["fence-agents"].Name != "fence-agents-base" || !byLogical["fence-agents"].Installed {
		t.Fatalf("fence-agents resolved to %+v", byLogical["fence-agents"])
	}

	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	if status := m.Snapshot(); status.State != StateInstalled {
		t.Fatalf("state = %q (%+v)", status.State, status)
	}
	if fake.seen("apt-get install") {
		t.Fatal("apt-get install ran although every package was installed")
	}
}

// When nothing is installed on Ubuntu, apt is asked for the split names,
// never for the monolithic ones that have no candidate.
func TestUbuntuInstallsSplitAgentNames(t *testing.T) {
	fake := newFakeNode()
	fake.candidates["fence-agents"] = ""
	fake.candidates["resource-agents"] = ""
	m := New(t.TempDir(), fake.runner())
	if err := m.Start(Options{}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	if status := m.Snapshot(); status.State != StateInstalled {
		t.Fatalf("state = %q (%+v)", status.State, status)
	}
	if !fake.seen("fence-agents-base") || !fake.seen("resource-agents-base") {
		t.Fatalf("split names not installed: %v", fake.commands)
	}
	for _, command := range fake.commands {
		if strings.Contains(command, "apt-get install") && (strings.Contains(command, "fence-agents ") || strings.HasSuffix(command, "resource-agents")) {
			t.Fatalf("monolithic name passed to apt: %q", command)
		}
	}
}

// Debian keeps the monolithic packages: with no candidate for "-base" the
// installer falls back to fence-agents/resource-agents.
func TestDebianFallsBackToMonolithicAgents(t *testing.T) {
	fake := newFakeNode()
	fake.osRelease = "ID=debian\nVERSION_ID=\"12\"\n"
	fake.candidates["fence-agents-base"] = ""
	fake.candidates["resource-agents-base"] = ""
	m := New(t.TempDir(), fake.runner())
	plan := m.Plan(context.Background(), Options{})
	if !plan.Preflight.OK {
		t.Fatalf("preflight blocked: %+v", plan.Preflight)
	}
	got := strings.Join(plan.Missing, " ")
	if !strings.Contains(got, "fence-agents") || strings.Contains(got, "fence-agents-base") {
		t.Fatalf("missing = %v, want monolithic names", plan.Missing)
	}
	if cmds := strings.Join(plan.ManualCommands, "\n"); strings.Contains(cmds, "-base") {
		t.Fatalf("manual commands use split names on Debian: %s", cmds)
	}
}

// Uninstall removes only the real installed names.
func TestUninstallRemovesResolvedAgentNames(t *testing.T) {
	fake := newFakeNode()
	fake.markAllInstalled([]string{"pacemaker", "fence-agents-base"})
	m := New(t.TempDir(), fake.runner())
	if err := m.StartUninstall(false); err != nil {
		t.Fatal(err)
	}
	waitDone(t, m)
	if !fake.seen("apt-get remove -y pacemaker fence-agents-base") {
		t.Fatalf("unexpected uninstall commands: %v", fake.commands)
	}
}
