package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/config"
)

// writeUpdater drops a stand-in for packaging/standalone/update.sh under dir
// and returns its path.
func writeUpdater(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "update.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// --- findUpdater: which script, which of the two modes -----------------------

func TestFindUpdater_ReleaseModeWithoutCheckout(t *testing.T) {
	// install.sh points REPO_DIR at DATA_DIR/source, a directory it only
	// creates. No .git in there must select the release path, never source.
	dataDir := t.TempDir()
	updater := writeUpdater(t, t.TempDir())

	got, sourceMode, err := findUpdater(dataDir, []string{updater})
	if err != nil {
		t.Fatalf("findUpdater: %v", err)
	}
	if got != updater {
		t.Errorf("path = %q, want %q", got, updater)
	}
	if sourceMode {
		t.Error("sourceMode = true without a checkout; the release path must win")
	}
}

func TestFindUpdater_SourceModeInsideCheckout(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	updater := writeUpdater(t, filepath.Join(repo, "packaging", "standalone"))

	got, sourceMode, err := findUpdater(repo, []string{updater})
	if err != nil {
		t.Fatalf("findUpdater: %v", err)
	}
	if got != updater {
		t.Errorf("path = %q, want %q", got, updater)
	}
	if !sourceMode {
		t.Error("sourceMode = false inside a checkout; --source must be passed")
	}
}

func TestFindUpdater_GitFileCountsAsCheckout(t *testing.T) {
	// Worktrees and submodules keep .git as a *file*, not a directory.
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	updater := writeUpdater(t, t.TempDir())

	_, sourceMode, err := findUpdater(repo, []string{updater})
	if err != nil {
		t.Fatalf("findUpdater: %v", err)
	}
	if !sourceMode {
		t.Error("a .git file was not recognised as a checkout")
	}
}

func TestFindUpdater_SkipsDirectories(t *testing.T) {
	// A directory sitting where the script should be (someone ran
	// `mkdir /usr/local/bin/webkvm-update`) must not shadow the real copy
	// further down the list.
	updater := writeUpdater(t, t.TempDir())
	blocker := filepath.Join(t.TempDir(), "webkvm-update")
	if err := os.Mkdir(blocker, 0o755); err != nil {
		t.Fatal(err)
	}

	got, _, err := findUpdater("", []string{blocker, updater})
	if err != nil {
		t.Fatalf("findUpdater: %v", err)
	}
	if got != updater {
		t.Errorf("path = %q, want the directory candidate to be skipped", got)
	}
}

func TestFindUpdater_NotFound(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "update.sh")
	got, sourceMode, err := findUpdater(t.TempDir(), []string{missing})
	if err == nil {
		t.Fatal("expected an error when no updater exists on disk")
	}
	if got != "" || sourceMode {
		t.Errorf("got (%q, %v), want no path and no source mode on failure", got, sourceMode)
	}
	if !strings.Contains(err.Error(), "updater not found") {
		t.Errorf("error %q should say what is missing", err)
	}
}

func TestDefaultUpdaterPaths_CoversInstalledAndRepoCopies(t *testing.T) {
	repo := t.TempDir()
	want := filepath.Join(repo, "packaging", "standalone", "update.sh")
	found := false
	for _, p := range defaultUpdaterPaths(repo) {
		if p == "" {
			t.Error("candidate list must not contain empty paths")
		}
		if p == want {
			found = true
		}
	}
	if !found {
		t.Errorf("repo copy %q missing from %v", want, defaultUpdaterPaths(repo))
	}
}

// --- SystemUpdate: gates and the mode it announces ---------------------------

type launchedUpdate struct {
	updater string
	args    []string
	repoDir string
}

// withStubbedUpdater makes the handler runnable in a test: it claims to be
// root, searches only the candidate list given here (never the machine's real
// /usr/local/bin), and records launches instead of spawning anything.
func withStubbedUpdater(t *testing.T, candidates ...string) chan launchedUpdate {
	t.Helper()
	origRoot, origLaunch, origPaths := isRoot, launchUpdater, updaterPaths
	origLaunchable := updaterLaunchable
	launched := make(chan launchedUpdate, 1)
	isRoot = func() bool { return true }
	updaterPaths = func(string) []string { return candidates }
	// Hosts without systemd-run (CI containers) must not turn every
	// update test into a 503.
	updaterLaunchable = func() error { return nil }
	launchUpdater = func(u string, a []string, r string) {
		launched <- launchedUpdate{updater: u, args: a, repoDir: r}
	}
	t.Cleanup(func() {
		isRoot = origRoot
		launchUpdater = origLaunch
		updaterPaths = origPaths
		updaterLaunchable = origLaunchable
	})
	return launched
}

func newUpdateHandler(t *testing.T, repoDir string) *Handler {
	t.Helper()
	lg, err := audit.New(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{cfg: &config.Config{RepoDir: repoDir}, audit: lg}
}

func postUpdate(h *Handler) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.SystemUpdate(rec, httptest.NewRequest(http.MethodPost, "/api/system/update", nil))
	return rec
}

func updateBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response %q is not JSON: %v", rec.Body, err)
	}
	return body
}

// awaitLaunch waits for the background goroutine to hand the launch over.
func awaitLaunch(t *testing.T, ch chan launchedUpdate) launchedUpdate {
	t.Helper()
	select {
	case lu := <-ch:
		return lu
	case <-time.After(2 * time.Second):
		t.Fatal("updater was never launched")
		return launchedUpdate{}
	}
}

func TestSystemUpdate_RequiresOptInEnv(t *testing.T) {
	withStubbedUpdater(t, "/nonexistent/update.sh")
	t.Setenv("WEBKVM_ALLOW_UPDATE", "")

	rec := postUpdate(newUpdateHandler(t, t.TempDir()))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "WEBKVM_ALLOW_UPDATE") {
		t.Errorf("body %s should name the env var that turns the endpoint on", rec.Body)
	}
}

func TestSystemUpdate_MissingUpdaterIsServiceUnavailable(t *testing.T) {
	withStubbedUpdater(t) // no candidates at all
	t.Setenv("WEBKVM_ALLOW_UPDATE", "1")

	rec := postUpdate(newUpdateHandler(t, t.TempDir()))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "updater not found") {
		t.Errorf("body %s should explain that the updater is missing", rec.Body)
	}
}

func TestSystemUpdate_ReleaseModeIsAnnounced(t *testing.T) {
	launched := withStubbedUpdater(t, writeUpdater(t, t.TempDir()))
	t.Setenv("WEBKVM_ALLOW_UPDATE", "1")
	repo := t.TempDir() // no .git → release path

	rec := postUpdate(newUpdateHandler(t, repo))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	body := updateBody(t, rec)
	if body["mode"] != "release" {
		t.Errorf("mode = %q, want release (no checkout, so no rebuild)", body["mode"])
	}
	if body["status"] != "updating" || body["log"] == "" {
		t.Errorf("body %v should report status and the log file to tail", body)
	}

	lu := awaitLaunch(t, launched)
	if len(lu.args) != 0 {
		t.Errorf("args = %v, want none: release mode must not pass --source", lu.args)
	}
	if lu.repoDir != repo {
		t.Errorf("repoDir = %q, want %q so the script can locate the checkout if it ever needs one", lu.repoDir, repo)
	}
	if lu.updater != body["updater"] {
		t.Errorf("launched %q but announced %q", lu.updater, body["updater"])
	}
}

func TestSystemUpdate_SourceModePassesFlag(t *testing.T) {
	launched := withStubbedUpdater(t, writeUpdater(t, t.TempDir()))
	t.Setenv("WEBKVM_ALLOW_UPDATE", "1")
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := postUpdate(newUpdateHandler(t, repo))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body %s", rec.Code, rec.Body)
	}
	if mode := updateBody(t, rec)["mode"]; mode != "source" {
		t.Errorf("mode = %q, want source inside a checkout", mode)
	}

	lu := awaitLaunch(t, launched)
	if len(lu.args) != 1 || lu.args[0] != "--source" {
		t.Errorf("args = %v, want [--source]", lu.args)
	}
}

func TestSystemUpdate_RefusesWhenTheUpdaterCannotBeLaunched(t *testing.T) {
	// No systemd-run means no transient unit, and a plain child would be
	// killed by the updater's own `systemctl stop` — leaving the service
	// down mid-update. Answering 202 would point the operator at a log
	// nobody writes, so this has to fail before the response.
	launched := withStubbedUpdater(t, writeUpdater(t, t.TempDir()))
	t.Setenv("WEBKVM_ALLOW_UPDATE", "1")
	orig := updaterLaunchable
	updaterLaunchable = func() error { return errors.New("systemd-run not found") }
	t.Cleanup(func() { updaterLaunchable = orig })

	rec := postUpdate(newUpdateHandler(t, t.TempDir()))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "systemd-run") {
		t.Errorf("body %s should name the missing tool", rec.Body)
	}
	select {
	case lu := <-launched:
		t.Errorf("updater was launched (%v) even though it could not survive the restart", lu)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/webkvm-update": "'/usr/local/bin/webkvm-update'",
		"/tmp/it's here":               `'/tmp/it'\''s here'`,
	}
	for in, want := range cases {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
