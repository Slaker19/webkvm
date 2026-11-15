package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"webkvm/internal/sysutil"
)

// haNode is a tiny node: os-release, installed set and the commands seen.
type haNode struct {
	mu          sync.Mutex
	release     string
	installed   map[string]bool
	repos       string
	commands    []string
	failInstall string
	syncDB      bool
	diskKB      int64           // free KiB on / (0 = plenty)
	allPresent  bool            // every package reports installed
	noCandidate map[string]bool // apt-cache has no candidate
	// duringInstall runs inside the package-manager call (signal tests).
	duringInstall func()
}

func (h *haNode) runner() *sysutil.MockCommandRunner {
	return &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.commands = append(h.commands, name+" "+strings.Join(args, " "))
		switch name {
		case "cat":
			return []byte(h.release), nil
		case "id":
			return []byte("0\n"), nil
		case "hostname":
			return []byte("n1.example.com\n"), nil
		case "df":
			free := h.diskKB
			if free == 0 {
				free = 90000000
			}
			return []byte(fmt.Sprintf("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 1 1 %d 1%% /\n", free)), nil
		case "dpkg-query":
			pkg := args[len(args)-1]
			if h.allPresent || h.installed[pkg] {
				return []byte("install ok installed"), nil
			}
			return nil, errors.New("not installed")
		case "apt-cache":
			if h.noCandidate[args[len(args)-1]] {
				return []byte("Candidate: (none)\n"), nil
			}
			return []byte("Candidate: 1.0\n"), nil
		case "env":
			if len(args) > 2 && args[1] == "apt-get" && args[2] == "install" {
				for _, a := range args[3:] {
					if !strings.HasPrefix(a, "-") {
						h.installed[a] = true
					}
				}
			}
		case "timedatectl":
			return []byte("yes\n"), nil
		case "sh":
			script := strings.Join(args, " ")
			if h.syncDB && strings.Contains(script, "pacman/sync") && !strings.Contains(script, "mtime") {
				return []byte("/var/lib/pacman/sync/core.db\n"), nil
			}
		case "pacman":
			return nil, errors.New("exit status 1")
		case "rpm":
			if h.allPresent || h.installed[args[len(args)-1]] {
				return []byte("1"), nil
			}
			return nil, errors.New("exit status 1")
		case "dnf":
			switch {
			case args[0] == "install":
				if h.duringInstall != nil {
					h.mu.Unlock()
					h.duringInstall()
					h.mu.Lock()
				}
				if h.failInstall != "" {
					return []byte(h.failInstall), errors.New("exit status 1")
				}
				for _, a := range args {
					if !strings.HasPrefix(a, "-") && a != "install" {
						h.installed[a] = true
					}
				}
			case args[0] == "-q" && args[1] == "repolist":
				return []byte("repo id\n" + h.repos), nil
			case args[1] == "repoquery":
				return []byte("1.0-1\n"), nil
			}
		case "systemctl":
			if args[0] == "is-enabled" {
				return []byte("disabled\n"), errors.New("exit status 1")
			}
			if args[0] == "is-active" {
				return []byte("inactive\n"), errors.New("exit status 3")
			}
		}
		return nil, nil
	}}
}

func (h *haNode) saw(prefix string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.commands {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func newEnv(h *haNode, euid int, interactive bool, stdin string) (haDepsEnv, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return haDepsEnv{Out: &out, Err: &errb, In: strings.NewReader(stdin), Runner: h.runner(), DataDir: "", EUID: euid, Interactive: interactive, Poll: time.Millisecond}, &out, &errb
}

func fedoraNode() *haNode {
	return &haNode{release: "ID=fedora\nVERSION_ID=41\n", installed: map[string]bool{}}
}

func TestHACheckIsReadOnlyAndNeedsNoRoot(t *testing.T) {
	h := fedoraNode()
	env, out, _ := newEnv(h, 1000, false, "")
	if code := runHA([]string{"deps", "check"}, env, false); code != exitHAMissing {
		t.Fatalf("code = %d\n%s", code, out)
	}
	for _, banned := range []string{"dnf install", "systemctl disable", "systemctl stop"} {
		if h.saw(banned) {
			t.Fatalf("check ran %q", banned)
		}
	}
	if !strings.Contains(out.String(), "WebKVM installs") {
		t.Fatalf("plan not printed:\n%s", out)
	}
}

func TestHACheckJSONContract(t *testing.T) {
	env, out, _ := newEnv(fedoraNode(), 1000, false, "")
	runHA([]string{"deps", "check", "--json"}, env, false)
	var doc struct {
		Plan   map[string]any `json:"plan"`
		Status map[string]any `json:"status"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, key := range []string{"family", "tier", "guided", "accept_required", "repos_needed", "can_install"} {
		if doc.Plan[key] == nil {
			t.Fatalf("plan.%s missing", key)
		}
	}
	if doc.Status["units_active"] == nil || doc.Status["unknown_units"] == nil {
		t.Fatalf("status = %v", doc.Status)
	}
}

func TestHAInstallRequiresRoot(t *testing.T) {
	env, _, errb := newEnv(fedoraNode(), 1000, true, "y\n")
	if code := runHA([]string{"deps", "install", "--yes"}, env, false); code != exitHAUsage || !strings.Contains(errb.String(), "root") {
		t.Fatalf("code = %d %s", code, errb)
	}
}

func TestHAInstallExperimentalNeedsAccept(t *testing.T) {
	h := fedoraNode()
	env, _, _ := newEnv(h, 0, false, "")
	if code := runHA([]string{"deps", "install", "--yes"}, env, false); code != exitHANotInstall || h.saw("dnf install") {
		t.Fatalf("code = %d", code)
	}
}

func TestHAInstallNeedsConfirmationWithoutYes(t *testing.T) {
	h := fedoraNode()
	env, _, errb := newEnv(h, 0, false, "")
	if code := runHA([]string{"deps", "install", "--accept-experimental"}, env, false); code != exitHAUsage || h.saw("dnf install") || !strings.Contains(errb.String(), "--yes") {
		t.Fatalf("code = %d %s", code, errb)
	}
	h = fedoraNode()
	env, _, _ = newEnv(h, 0, true, "n\n")
	if code := runHA([]string{"deps", "install", "--accept-experimental"}, env, false); code != exitHAUsage || h.saw("dnf install") {
		t.Fatalf("declined install ran: code %d", code)
	}
}

func TestHAInstallConfirmedRuns(t *testing.T) {
	h := fedoraNode()
	env, out, _ := newEnv(h, 0, true, "y\n")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--accept-experimental"}, env, false); code != exitHAOK {
		t.Fatalf("code = %d\n%s", code, out)
	}
	if !h.saw("dnf install -y") || !strings.Contains(out.String(), "Result: installed") {
		t.Fatalf("out:\n%s", out)
	}
}

// SIGTERM/SIGINT during the package manager call must not kill the CLI: the
// install finishes and the units are restored (found on Fedora 44: the old CLI
// died at once and left corosync/pcsd enabled and running).
func TestHAInstallSurvivesSIGTERMAndRestoresUnits(t *testing.T) {
	h := fedoraNode()
	h.duringInstall = func() {
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(50 * time.Millisecond)
	}
	env, out, errb := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes", "--accept-experimental"}, env, false); code != exitHAOK {
		t.Fatalf("code = %d\n%s%s", code, out, errb)
	}
	if !strings.Contains(out.String(), "Result: installed") || !strings.Contains(errb.String(), "interrupt received") {
		t.Fatalf("out:\n%s\nerr:\n%s", out, errb)
	}
}

func TestHAInstallPackageFailureExit11(t *testing.T) {
	h := fedoraNode()
	h.failInstall = "Error: Unable to find a match: sbd"
	env, _, _ := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes", "--accept-experimental"}, env, false); code != exitHAPackageFail {
		t.Fatalf("code = %d", code)
	}
}

func TestHAInstallRockyWithoutRepoExit10(t *testing.T) {
	h := &haNode{release: "ID=rocky\nVERSION_ID=9.4\nID_LIKE=\"rhel centos fedora\"\n", installed: map[string]bool{}}
	env, out, _ := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes", "--accept-experimental"}, env, false); code != exitHANotInstall || h.saw("dnf install") {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out.String(), "sudo dnf config-manager --set-enabled highavailability") {
		t.Fatalf("exact repo command missing:\n%s", out)
	}
}

func TestHAArchGuidedExit13(t *testing.T) {
	h := &haNode{release: "ID=arch\n", installed: map[string]bool{}, syncDB: true}
	env, out, _ := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes", "--accept-experimental"}, env, false); code != exitHAManualNeeded {
		t.Fatalf("code = %d\n%s", code, out)
	}
	if !strings.Contains(out.String(), "yay -S --needed") || !strings.Contains(out.String(), "NORMAL user") || h.saw("pacman -S ") {
		t.Fatalf("AUR guidance missing or WebKVM installed:\n%s", out)
	}
}

func TestHAUsage(t *testing.T) {
	env, _, errb := newEnv(fedoraNode(), 0, false, "")
	if code := runHA([]string{"deps"}, env, false); code != exitHAUsage || !strings.Contains(errb.String(), "Usage") {
		t.Fatalf("code = %d", code)
	}
	if code := runHA([]string{"deps", "check", "--bogus"}, env, false); code != exitHAUsage {
		t.Fatalf("code = %d", code)
	}
}

func ubuntuNode() *haNode {
	return &haNode{release: "ID=ubuntu\nVERSION_ID=\"24.04\"\n", installed: map[string]bool{}}
}

// Re-running on a node that already has everything must be rc 0 even when a
// preflight check that no longer matters fails (found on Debian 13: disk).
func TestHAInstallNothingMissingExitsZeroDespiteLowDisk(t *testing.T) {
	h := ubuntuNode()
	h.allPresent = true
	h.diskKB = 500 * 1024
	env, out, errb := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes"}, env, false); code != exitHAOK {
		t.Fatalf("code = %d\n%s%s", code, out, errb)
	}
	if !strings.Contains(out.String(), "Nothing for WebKVM to install") {
		t.Fatalf("out:\n%s", out)
	}
	if h.saw("env DEBIAN_FRONTEND=noninteractive apt-get") || h.saw("systemctl disable") || h.saw("systemctl stop") {
		t.Fatalf("a mutating command ran: %v", h.commands)
	}
	if _, err := os.Stat(filepath.Join(env.DataDir, "ha-module.json")); !os.IsNotExist(err) {
		t.Fatalf("a no-op install created runtime state: %v", err)
	}
}

func TestHAInstallNothingMissingJSONExitsZero(t *testing.T) {
	h := ubuntuNode()
	h.allPresent = true
	h.diskKB = 500 * 1024
	env, out, _ := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes", "--json"}, env, false); code != exitHAOK {
		t.Fatalf("code = %d\n%s", code, out)
	}
	var doc map[string]any
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc["plan"] == nil || doc["status"] == nil {
		t.Fatalf("json = %v err=%v\n%s", doc, err, out)
	}
}

// Strict preflight is kept whenever something must be installed.
func TestHAInstallLowDiskStillBlocksWhenSomethingMissing(t *testing.T) {
	h := ubuntuNode()
	h.diskKB = 500 * 1024
	env, _, _ := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes"}, env, false); code != exitHAPackageFail {
		t.Fatalf("code = %d", code)
	}
	if h.saw("env DEBIAN_FRONTEND=noninteractive apt-get install") {
		t.Fatalf("installed despite a failed preflight: %v", h.commands)
	}
}

// An unsupported distro never reports "nothing to install".
func TestHAInstallUnsupportedDistroStillExit10(t *testing.T) {
	h := &haNode{release: "ID=plan9\n", installed: map[string]bool{}, allPresent: true}
	env, _, _ := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes"}, env, false); code != exitHANotInstall {
		t.Fatalf("code = %d", code)
	}
}

// --storage without the LINBIT repository: repo_scope blocks like the other
// scope blocks (rc 10), --json keeps the storage plan, and the table does not
// promise that WebKVM installs packages that have no candidate.
func TestHAInstallStorageWithoutLinbitExit10(t *testing.T) {
	newNode := func() *haNode {
		h := ubuntuNode()
		h.noCandidate = map[string]bool{"linstor-controller": true, "linstor-satellite": true}
		return h
	}
	h := newNode()
	env, out, _ := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes", "--storage"}, env, false); code != exitHANotInstall {
		t.Fatalf("code = %d\n%s", code, out)
	}
	if h.saw("env DEBIAN_FRONTEND=noninteractive apt-get install") {
		t.Fatal("installed despite no candidate")
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "linstor-") && strings.Contains(line, "WebKVM installs") {
			t.Fatalf("linstor without candidate is labelled WebKVM installs: %q", line)
		}
	}

	h = newNode()
	env, out, _ = newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	if code := runHA([]string{"deps", "install", "--yes", "--storage", "--json"}, env, false); code != exitHANotInstall {
		t.Fatalf("json code = %d\n%s", code, out)
	}
	var doc struct {
		Cause string `json:"cause"`
		Plan  struct {
			Linstor bool `json:"linstor"`
		} `json:"plan"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil || doc.Cause != "repo_scope" || !doc.Plan.Linstor {
		t.Fatalf("json = %+v err=%v\n%s", doc, err, out)
	}
}

func TestHAInstallHeaderStateIsReconciled(t *testing.T) {
	h := ubuntuNode()
	h.allPresent = true
	env, out, _ := newEnv(h, 0, false, "")
	env.DataDir = t.TempDir()
	runHA([]string{"deps", "install", "--yes"}, env, false)
	if strings.Contains(out.String(), "state: not_installed") || !strings.Contains(out.String(), "state: installed") {
		t.Fatalf("header:\n%s", out)
	}
}
