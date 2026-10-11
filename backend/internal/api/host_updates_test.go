package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"

	"webkvm/internal/audit"
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
