package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"webkvm/internal/audit"
	"webkvm/internal/notify"
)

func TestHostUpdatesEndpoints(t *testing.T) {
	aud, err := audit.New(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{audit: aud}

	// 1. GetHostUpdates returns 200 and valid JSON
	req := httptest.NewRequest(http.MethodGet, "/api/host/updates", nil)
	rec := httptest.NewRecorder()
	h.GetHostUpdates(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp HostUpdatesSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}

	// 2. Parser regex test
	sampleLine := "linux-image-generic/resolute-updates 7.0.0-38.38 amd64 [upgradable from: 7.0.0-34.34]"
	m := upgradableLineRE.FindStringSubmatch(sampleLine)
	if len(m) < 6 {
		t.Fatalf("expected regex match, got %+v", m)
	}
	if m[1] != "linux-image-generic" || m[3] != "7.0.0-38.38" || m[5] != "7.0.0-34.34" {
		t.Fatalf("unexpected parsed values: %+v", m)
	}

	// 3. Test unpacking and setting up regexes
	unpackLine := "Unpacking linux-image-generic (7.0.0-38.38) over (7.0.0-34.34) ..."
	um := unpackingPkgRE.FindStringSubmatch(unpackLine)
	if len(um) < 2 || um[1] != "linux-image-generic" {
		t.Fatalf("unexpected unpack match: %+v", um)
	}

	setupLine := "Setting up linux-image-generic (7.0.0-38.38) ..."
	sm := settingUpPkgRE.FindStringSubmatch(setupLine)
	if len(sm) < 2 || sm[1] != "linux-image-generic" {
		t.Fatalf("unexpected setup match: %+v", sm)
	}

	// 4. Test checkRebootRequired
	rebootReq, pkgs := checkRebootRequired()
	_ = rebootReq
	_ = pkgs
}

func TestHostUpgradeConcurrentGuard(t *testing.T) {
	aud, err := audit.New(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{audit: aud}

	updatesMu.Lock()
	isUpgradingHost = true
	updatesMu.Unlock()

	defer func() {
		updatesMu.Lock()
		isUpgradingHost = false
		isRefreshingHost = false
		updatesMu.Unlock()
	}()

	// When an upgrade is in progress, Refresh should return 409 Conflict
	reqRefresh := httptest.NewRequest(http.MethodPost, "/api/host/updates/refresh", nil)
	recRefresh := httptest.NewRecorder()
	h.RefreshHostUpdates(recRefresh, reqRefresh)
	if recRefresh.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d", recRefresh.Code)
	}

	// Upgrade should also return 409 Conflict
	reqUpgrade := httptest.NewRequest(http.MethodPost, "/api/host/updates/upgrade", nil)
	recUpgrade := httptest.NewRecorder()
	h.UpgradeHostUpdates(recUpgrade, reqUpgrade)
	if recUpgrade.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict, got %d", recUpgrade.Code)
	}

	// Reset upgrading, set refreshing
	updatesMu.Lock()
	isUpgradingHost = false
	isRefreshingHost = true
	updatesMu.Unlock()

	// When refresh is in progress, another refresh should return 409 Conflict
	recRefresh2 := httptest.NewRecorder()
	h.RefreshHostUpdates(recRefresh2, reqRefresh)
	if recRefresh2.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict while refreshing, got %d", recRefresh2.Code)
	}

	// When refresh is in progress, upgrade should also return 409 Conflict
	recUpgrade2 := httptest.NewRecorder()
	h.UpgradeHostUpdates(recUpgrade2, reqUpgrade)
	if recUpgrade2.Code != http.StatusConflict {
		t.Fatalf("expected 409 conflict while refreshing, got %d", recUpgrade2.Code)
	}
}

func TestPkgManagerCmd_WrapsInSystemdRun(t *testing.T) {
	orig := systemdRunAvailable
	systemdRunAvailable = func() bool { return true }
	t.Cleanup(func() { systemdRunAvailable = orig })

	cmd := pkgManagerCmd(context.Background(), "apt-get",
		[]string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"},
		"-y", "dist-upgrade")

	// argv[0] is systemd-run; the escaped namespace args come next, then "--",
	// then the real package manager.
	if got := cmd.Args[0]; got != "systemd-run" {
		t.Fatalf("args[0] = %q, want systemd-run", got)
	}
	joined := strings.Join(cmd.Args, "\x00")
	for _, want := range []string{"--pipe", "--collect", "--quiet",
		"--setenv=DEBIAN_FRONTEND=noninteractive", "--setenv=LC_ALL=C",
		"\x00--\x00", "apt-get", "-y", "dist-upgrade"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
}

func TestPkgManagerCmd_FallsBackToDirectChild(t *testing.T) {
	orig := systemdRunAvailable
	systemdRunAvailable = func() bool { return false }
	t.Cleanup(func() { systemdRunAvailable = orig })

	cmd := pkgManagerCmd(context.Background(), "dnf",
		[]string{"LC_ALL=C"}, "-y", "upgrade")

	if got := cmd.Args[0]; got != "dnf" {
		t.Fatalf("args[0] = %q, want dnf", got)
	}
	if !slices.Contains(cmd.Env, "LC_ALL=C") {
		t.Errorf("env %q missing LC_ALL=C", cmd.Env)
	}
}

func TestHostUpgradeCode(t *testing.T) {
	cases := []struct {
		detail string
		want   string
	}{
		{"unable to create '/etc/default/motd-news.dpkg-new': Read-only file system", errHostUpgradeROFS},
		{"dpkg: error: unable to remove newly-extracted version: Read-only file system", errHostUpgradeROFS},
		{"E: Sub-process /usr/bin/dpkg returned an error code (1)", errHostUpgradeFailed},
		{"", errHostUpgradeFailed},
	}
	for _, c := range cases {
		if got := hostUpgradeCode(c.detail); got != c.want {
			t.Errorf("hostUpgradeCode(%q) = %q, want %q", c.detail, got, c.want)
		}
	}
}

func TestHostUpgradeErrCode(t *testing.T) {
	e := &hostUpgradeErr{code: errHostUpgradeROFS, msg: "boom"}
	if e.Error() != "boom" || e.Code() != errHostUpgradeROFS {
		t.Errorf("unexpected coded error: %+v", e)
	}
	if codeOf(e) != errHostUpgradeROFS {
		t.Errorf("codeOf(coded) = %q, want %q", codeOf(e), errHostUpgradeROFS)
	}
	if codeOf(fmt.Errorf("plain")) != "" {
		t.Errorf("codeOf(plain error) should be empty")
	}
}

func TestUpgradeCmd_FullVsSelective(t *testing.T) {
	orig := systemdRunAvailable
	systemdRunAvailable = func() bool { return false }
	t.Cleanup(func() { systemdRunAvailable = orig })

	// Full apt upgrade is dist-upgrade, never --only-upgrade.
	cmd := upgradeCmd(context.Background(), PkgManagerAPT, nil)
	if cmd.Args[0] != "apt-get" {
		t.Fatalf("args[0] = %q, want apt-get", cmd.Args[0])
	}
	joined := strings.Join(cmd.Args, "\x00")
	if !strings.Contains(joined, "dist-upgrade") || strings.Contains(joined, "--only-upgrade") {
		t.Errorf("full apt args unexpected: %v", cmd.Args)
	}

	// Selective apt upgrade targets only the named packages.
	cmd = upgradeCmd(context.Background(), PkgManagerAPT, []string{"base-files", "motd-news-config"})
	joined = strings.Join(cmd.Args, "\x00")
	for _, want := range []string{"install", "--only-upgrade", "base-files", "motd-news-config"} {
		if !strings.Contains(joined, want) {
			t.Errorf("selective apt args %v missing %q", cmd.Args, want)
		}
	}
	if strings.Contains(joined, "dist-upgrade") {
		t.Errorf("selective apt must not be dist-upgrade: %v", cmd.Args)
	}

	// dnf selective appends the package after "upgrade".
	cmd = upgradeCmd(context.Background(), PkgManagerDNF, []string{"vim"})
	if !strings.Contains(strings.Join(cmd.Args, "\x00"), "-y\x00upgrade\x00vim") {
		t.Errorf("dnf selective args unexpected: %v", cmd.Args)
	}

	// pacman full uses -Syu; selective uses -S with the packages.
	cmd = upgradeCmd(context.Background(), PkgManagerPacman, nil)
	if !strings.Contains(strings.Join(cmd.Args, "\x00"), "-Syu") {
		t.Errorf("pacman full args unexpected: %v", cmd.Args)
	}
	cmd = upgradeCmd(context.Background(), PkgManagerPacman, []string{"vim"})
	if !strings.Contains(strings.Join(cmd.Args, "\x00"), "-S\x00--noconfirm\x00vim") {
		t.Errorf("pacman selective args unexpected: %v", cmd.Args)
	}
}

func TestSafePkgNameRE(t *testing.T) {
	for _, ok := range []string{"base-files", "linux-image-generic", "motd-news-config", "a", "pkg+name~x"} {
		if !safePkgNameRE.MatchString(ok) {
			t.Errorf("%q should be a valid package name", ok)
		}
	}
	for _, bad := range []string{"-y", "--force-yes", "pkg name", "pkg;rm", "", "/etc/passwd"} {
		if safePkgNameRE.MatchString(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestNotifyUpgrade(t *testing.T) {
	n, err := notify.New(t.TempDir(), notify.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{notifier: n}

	h.notifyUpgrade(PkgManagerAPT, nil)
	ev := n.Events()
	if len(ev) != 1 || ev[0].Level != "info" || ev[0].Subject != "Host upgrade completed" {
		t.Fatalf("unexpected events after success: %+v", ev)
	}

	h.notifyUpgrade(PkgManagerAPT, errors.New("boom"))
	ev = n.Events()
	if len(ev) != 2 || ev[0].Level != "error" || !strings.Contains(ev[0].Subject, "failed") {
		t.Fatalf("unexpected events after failure: %+v", ev)
	}

	// nil notifier (plain unit-test handler) is a no-op, never panics.
	(&Handler{}).notifyUpgrade(PkgManagerAPT, nil)
}

func TestStdoutStderrMerge(t *testing.T) {
	cmd := exec.Command("sh", "-c", "echo stdout_line; echo stderr_line >&2")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd.Start failed: %v", err)
	}
	scanner := bufio.NewScanner(stdout)
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("cmd.Wait failed: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
}
