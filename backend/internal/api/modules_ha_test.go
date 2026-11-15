package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"webkvm/internal/configstore"
	"webkvm/internal/hainstall"
	"webkvm/internal/sysutil"

	"github.com/go-chi/chi/v5"
)

func haTestHandler(t *testing.T) (*Handler, *configstore.Store, string) {
	t.Helper()
	dir := t.TempDir()
	settings, err := configstore.New(dir, configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	return &Handler{
		settings:  settings,
		haInstall: hainstall.New(dir, &sysutil.MockCommandRunner{}),
	}, settings, dir
}

func moduleMux(h *Handler) *chi.Mux {
	calls := 0
	ok := func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}
	r := chi.NewRouter()
	r.Route("/api/ha", func(r chi.Router) {
		r.Use(h.requireClusterHA)
		r.Get("/readiness", ok)
	})
	r.Route("/api/firewall/cluster", func(r chi.Router) {
		r.Use(h.requireClusterHA)
		r.Get("/", ok)
	})
	return r
}

func doRequest(t *testing.T, handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestClusterHAModuleDefaultsOff(t *testing.T) {
	h, settings, _ := haTestHandler(t)
	if h.clusterHAEnabled() {
		t.Fatal("modules.cluster_ha must default to false")
	}
	if settings.GetBool("modules.cluster_ha") {
		t.Fatal("setting default is not false")
	}
}

func TestHARoutesReturn404WhenModuleDisabled(t *testing.T) {
	h, _, _ := haTestHandler(t)
	mux := moduleMux(h)
	for _, path := range []string{"/api/ha/readiness", "/api/firewall/cluster"} {
		rec := doRequest(t, mux, http.MethodGet, path, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != "module_not_installed" {
			t.Fatalf("%s code = %q", path, body["code"])
		}
	}
}

func TestHARoutesWorkWhenModuleEnabled(t *testing.T) {
	h, settings, _ := haTestHandler(t)
	if _, failed, err := settings.SetMany(map[string]any{"modules.cluster_ha": true}); err != nil || len(failed) > 0 {
		t.Fatalf("enable setting: %v %v", err, failed)
	}
	mux := moduleMux(h)
	for _, path := range []string{"/api/ha/readiness", "/api/firewall/cluster"} {
		rec := doRequest(t, mux, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", path, rec.Code)
		}
	}
}

func TestGetHAModuleReportsDisabledAndState(t *testing.T) {
	h, _, _ := haTestHandler(t)
	rec := doRequest(t, http.HandlerFunc(h.GetHAModule), http.MethodGet, "/api/modules/ha", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body haModuleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Enabled {
		t.Fatal("module reported enabled")
	}
	if body.Status.State != hainstall.StateNotInstalled {
		t.Fatalf("state = %q", body.Status.State)
	}
}

// The shared DATA_DIR may carry "installed" written by another member. The
// local API must answer with what this node's dpkg actually has.
func TestGetHAModuleIgnoresAnotherMembersInstalledState(t *testing.T) {
	h, _, dir := haTestHandler(t)
	state := `{"hosts":{"other-node":{"state":"installed","hostname":"other-node"}}}`
	if err := os.WriteFile(filepath.Join(dir, "ha-module.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	h.haInstall = hainstall.New(dir, &sysutil.MockCommandRunner{})

	rec := doRequest(t, http.HandlerFunc(h.GetHAModule), http.MethodGet, "/api/modules/ha", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body haModuleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status.State != hainstall.StateNotInstalled {
		t.Fatalf("state = %q, want not_installed", body.Status.State)
	}
	if len(body.Status.Missing) != 8 {
		t.Fatalf("missing = %v, want the eight packages", body.Status.Missing)
	}
}

func TestEnableRequiresInstalledPackages(t *testing.T) {
	h, _, _ := haTestHandler(t)
	rec := doRequest(t, http.HandlerFunc(h.EnableHAModule), http.MethodPost, "/api/modules/ha/enable", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "install_required") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestEnableAfterInstallAndDisableHidesAgain(t *testing.T) {
	h, settings, dir := haTestHandler(t)
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	state := `{"hosts":{"` + host + `":{"state":"installed","hostname":"` + host + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "ha-module.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	h.haInstall = hainstall.New(dir, &sysutil.MockCommandRunner{})

	if rec := doRequest(t, http.HandlerFunc(h.EnableHAModule), http.MethodPost, "/api/modules/ha/enable", ""); rec.Code != http.StatusOK {
		t.Fatalf("enable = %d %s", rec.Code, rec.Body.String())
	}
	if !h.clusterHAEnabled() {
		t.Fatal("module not enabled after enable")
	}
	if rec := doRequest(t, http.HandlerFunc(h.DisableHAModule), http.MethodPost, "/api/modules/ha/disable", ""); rec.Code != http.StatusOK {
		t.Fatalf("disable = %d", rec.Code)
	}
	if h.clusterHAEnabled() {
		t.Fatal("module still enabled after disable")
	}
	_ = settings
}

func TestInstallReturnsAcceptedAndPreflightFailureIsRecorded(t *testing.T) {
	h, _, _ := haTestHandler(t)
	rec := doRequest(t, http.HandlerFunc(h.InstallHAModule), http.MethodPost, "/api/modules/ha/install", `{"linstor":false}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.haInstall.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("install did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	status := h.haInstall.Snapshot()
	if status.State != hainstall.StateNotInstalled || status.FailedStep != "preflight" {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestUninstallRequiresConfirmation(t *testing.T) {
	h, _, _ := haTestHandler(t)
	rec := doRequest(t, http.HandlerFunc(h.UninstallHAModule), http.MethodPost, "/api/modules/ha/uninstall", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "confirmation_required") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestUninstallRefusesConfiguredCluster(t *testing.T) {
	isolateHAArtifacts(t)
	dir := t.TempDir()
	status := `<pacemaker-result><status code="0" message="OK"/><crm_mon><cluster_name>webkvm-ha</cluster_name><nodes><node name="n1" online="true"/></nodes></crm_mon></pacemaker-result>`
	runner := &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "pcs" {
			return []byte(status), nil
		}
		return nil, nil
	}}
	previous := sysutil.DefaultRunner
	t.Cleanup(func() { sysutil.DefaultRunner = previous })
	sysutil.DefaultRunner = runner
	h := &Handler{
		settings:  mustSettings(t, dir),
		haInstall: hainstall.New(dir, runner),
	}
	rec := doRequest(t, http.HandlerFunc(h.UninstallHAModule), http.MethodPost, "/api/modules/ha/uninstall", `{"confirm":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "uninstall_blocked") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

type recordingRunner struct {
	sysutil.MockCommandRunner
	mu    sync.Mutex
	calls []string
}

func newRecordingRunner(handler func(context.Context, string, ...string) ([]byte, error)) *recordingRunner {
	return &recordingRunner{MockCommandRunner: sysutil.MockCommandRunner{Handler: handler}}
}

func (r *recordingRunner) record(name string, args ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, name+" "+strings.Join(args, " "))
}

func (r *recordingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.record(name, args...)
	return r.MockCommandRunner.Run(ctx, name, args...)
}

func (r *recordingRunner) RunWithTimeout(name string, timeout time.Duration, args ...string) ([]byte, error) {
	r.record(name, args...)
	return r.MockCommandRunner.RunWithTimeout(name, timeout, args...)
}

func (r *recordingRunner) Commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// isolateHAArtifacts points the uninstall artifact checks at an empty temp
// tree so the tests never read the host's real cluster configuration.
// It returns the temp root; tests create files under it to simulate artifacts.
func isolateHAArtifacts(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	prevFiles, prevGlobs := haClusterArtifactFiles, haClusterArtifactGlobs
	t.Cleanup(func() { haClusterArtifactFiles, haClusterArtifactGlobs = prevFiles, prevGlobs })
	haClusterArtifactFiles = []string{
		filepath.Join(root, "etc/corosync/corosync.conf"),
		filepath.Join(root, "var/lib/pacemaker/cib/cib.xml"),
	}
	haClusterArtifactGlobs = []string{
		filepath.Join(root, "etc/drbd.d/*.res"),
		filepath.Join(root, "var/lib/linstor.d/*.res"),
	}
	return root
}

func writeArtifact(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func uninstallHandler(t *testing.T, runner *recordingRunner) (*Handler, *configstore.Store) {
	t.Helper()
	isolateHAArtifacts(t)
	dir := t.TempDir()
	previous := sysutil.DefaultRunner
	t.Cleanup(func() { sysutil.DefaultRunner = previous })
	sysutil.DefaultRunner = runner
	settings := mustSettings(t, dir)
	return &Handler{settings: settings, haInstall: hainstall.New(dir, runner)}, settings
}

// A probe that cannot answer is not a green light: removal stays blocked.
func TestUninstallBlocksWhenProbeFails(t *testing.T) {
	runner := newRecordingRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "pcs" {
			return nil, errors.New("pcs: cluster status unavailable")
		}
		return nil, errors.New("not found")
	})
	h, _ := uninstallHandler(t, runner)
	rec := doRequest(t, http.HandlerFunc(h.UninstallHAModule), http.MethodPost, "/api/modules/ha/uninstall", `{"confirm":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "pacemaker_probe_failed") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// A stopped cluster can still leave its configuration on disk; purge would
// delete it, so the artifacts block the removal even if pcs says "not configured".
func TestUninstallBlocksOnClusterArtifacts(t *testing.T) {
	runner := newRecordingRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "pcs" {
			return []byte(`<pacemaker-result><status code="102" message="not connected"/></pacemaker-result>`), nil
		}
		return nil, errors.New("not found")
	})
	h, _ := uninstallHandler(t, runner)
	writeArtifact(t, haClusterArtifactFiles[0])
	rec := doRequest(t, http.HandlerFunc(h.UninstallHAModule), http.MethodPost, "/api/modules/ha/uninstall", `{"confirm":true}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "config_present:"+haClusterArtifactFiles[0]) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	for _, name := range []string{"test", "sh"} {
		for _, call := range runner.Commands() {
			if strings.HasPrefix(call, name+" ") {
				t.Fatalf("artifact check must not shell out: %s", call)
			}
		}
	}
}

// The artifact checks are done in Go: every glob (DRBD and LINSTOR resource
// files) is a blocker on its own.
func TestUninstallBlocksOnResourceGlobs(t *testing.T) {
	for _, pattern := range []string{"etc/drbd.d/*.res", "var/lib/linstor.d/*.res"} {
		t.Run(pattern, func(t *testing.T) {
			h, _ := uninstallHandler(t, uninstallRunner(false))
			root := filepath.Dir(filepath.Dir(filepath.Dir(haClusterArtifactFiles[0]))) // <root>/etc/corosync/x -> <root>
			writeArtifact(t, filepath.Join(root, strings.Replace(pattern, "*", "r0", 1)))
			rec := doRequest(t, http.HandlerFunc(h.UninstallHAModule), http.MethodPost, "/api/modules/ha/uninstall", `{"confirm":true}`)
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "config_present:"+filepath.Join(root, pattern)) {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

// Fail-closed: a check that cannot answer (anything but "does not exist") is
// a blocker, never silently "absent".
func TestUninstallBlocksWhenArtifactCheckFails(t *testing.T) {
	t.Run("file", func(t *testing.T) {
		h, _ := uninstallHandler(t, uninstallRunner(false))
		// A regular file where a directory is expected makes os.Stat fail with ENOTDIR.
		writeArtifact(t, filepath.Dir(haClusterArtifactFiles[0]))
		rec := doRequest(t, http.HandlerFunc(h.UninstallHAModule), http.MethodPost, "/api/modules/ha/uninstall", `{"confirm":true}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "config_check_failed:"+haClusterArtifactFiles[0]) {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})
	t.Run("glob", func(t *testing.T) {
		h, _ := uninstallHandler(t, uninstallRunner(false))
		writeArtifact(t, filepath.Dir(haClusterArtifactGlobs[0]))
		rec := doRequest(t, http.HandlerFunc(h.UninstallHAModule), http.MethodPost, "/api/modules/ha/uninstall", `{"confirm":true}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "config_check_failed:"+haClusterArtifactGlobs[0]) {
			t.Fatalf("body = %s", rec.Body.String())
		}
	})
}

func uninstallRunner(purge bool) *recordingRunner {
	return newRecordingRunner(func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "pcs":
			return []byte(`<pacemaker-result><status code="102" message="not connected"/></pacemaker-result>`), nil
		case "test", "sh":
			return nil, errors.New("not found")
		case "dpkg-query":
			return []byte("install ok installed"), nil
		case "env":
			if len(args) >= 3 && args[1] == "apt-get" && (args[2] == "remove" || args[2] == "purge") {
				return nil, nil
			}
		}
		return nil, nil
	})
}

func TestUninstallRunsInBackgroundAndUsesRemoveByDefault(t *testing.T) {
	runner := uninstallRunner(false)
	h, settings := uninstallHandler(t, runner)
	rec := doRequest(t, http.HandlerFunc(h.UninstallHAModule), http.MethodPost, "/api/modules/ha/uninstall", `{"confirm":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for h.haInstall.Busy() {
		if time.Now().After(deadline) {
			t.Fatal("uninstall did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if h.haInstall.Snapshot().State != hainstall.StateNotInstalled {
		t.Fatalf("state = %q", h.haInstall.Snapshot().State)
	}
	if settings.GetBool("modules.cluster_ha") {
		t.Fatal("module flag stayed enabled after a confirmed uninstall")
	}
	foundRemove := false
	for _, command := range runner.Commands() {
		if strings.Contains(command, "apt-get remove -y") {
			foundRemove = true
		}
		if strings.Contains(command, "apt-get purge") {
			t.Fatal("default uninstall purged configuration")
		}
	}
	if !foundRemove {
		t.Fatal("apt-get remove was not used")
	}
}

func TestUninstallPurgeRequiresExplicitFlag(t *testing.T) {
	runner := uninstallRunner(true)
	h, _ := uninstallHandler(t, runner)
	rec := doRequest(t, http.HandlerFunc(h.UninstallHAModule), http.MethodPost, "/api/modules/ha/uninstall", `{"confirm":true,"purge":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (%s)", rec.Code, rec.Body.String())
	}
	for h.haInstall.Busy() {
		time.Sleep(5 * time.Millisecond)
	}
	joined := strings.Join(runner.Commands(), "\n")
	if !strings.Contains(joined, "apt-get purge -y") {
		t.Fatal("explicit purge flag did not purge")
	}
}

func mustSettings(t *testing.T, dir string) *configstore.Store {
	t.Helper()
	settings, err := configstore.New(dir, configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestGetHAModuleNeverReturnsNullArrays(t *testing.T) {
	h, _, _ := haTestHandler(t)
	rec := doRequest(t, http.HandlerFunc(h.GetHAModule), http.MethodGet, "/api/modules/ha", "")
	var body struct {
		Status map[string]json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"units_active", "unknown_units", "steps", "missing"} {
		if v, ok := body.Status[key]; !ok || string(v) == "null" {
			t.Fatalf("%s = %s, want an array", key, v)
		}
	}
	for _, gone := range []string{"masks_left", "protected_units"} {
		if _, ok := body.Status[gone]; ok {
			t.Fatalf("%s must not be returned", gone)
		}
	}
}

func TestPreflightHAModuleAcceptsNewFieldsAndReturnsArrays(t *testing.T) {
	h, _, _ := haTestHandler(t)
	rec := doRequest(t, http.HandlerFunc(h.PreflightHAModule), http.MethodPost, "/api/modules/ha/preflight",
		`{"linstor":false,"accept_experimental":true,"fencing":["SBD","bogus","pve"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var plan map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"packages", "missing", "services", "manual_commands", "fencing"} {
		if v, ok := plan[key]; !ok || string(v) == "null" {
			t.Fatalf("plan.%s = %s, want an array", key, v)
		}
	}
	if string(plan["fencing"]) != `["sbd","pve"]` {
		t.Fatalf("fencing = %s, want invalid kinds dropped", plan["fencing"])
	}
	if _, ok := plan["guided"]; !ok {
		t.Fatal("plan.guided missing")
	}
}
