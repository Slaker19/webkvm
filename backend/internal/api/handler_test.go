package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"webkvm/internal/config"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}

func newHealthHandler(t *testing.T, version, buildTime string) *Handler {
	t.Helper()
	// /api/health calls syscall.Statfs on cfg.DataDir, so we need
	// a real directory that exists. t.TempDir() is perfect.
	dir := t.TempDir()
	return &Handler{
		cfg: &config.Config{
			DataDir:   dir,
			Version:   version,
			BuildTime: buildTime,
		},
		StartedAt: time.Now().Add(-30 * time.Second),
	}
}

// TestHealth_NilLibvirtReturns503 confirms the handler tolerates a
// nil libvirt Connector (the unit-test scenario) and reports it
// honestly as "degraded" with HTTP 503. In production lv is never
// nil; the if/else branch is exercised by integration tests / the
// deployment smoke test.
func TestHealth_NilLibvirtReturns503(t *testing.T) {
	h := newHealthHandler(t, "phase1.2-foo-1234567", "2026-06-25T16:00:00Z")
	if h.lv != nil {
		t.Fatalf("expected h.lv to be nil for this test, got %T", h.lv)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	h.Health(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (libvirt down); body = %q",
			rr.Code, rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "degraded" {
		t.Errorf("status = %v, want degraded", body["status"])
	}
	if body["libvirt"] != "down" {
		t.Errorf("libvirt = %v, want down", body["libvirt"])
	}
	if body["version"] != "phase1.2-foo-1234567" {
		t.Errorf("version = %v, want phase1.2-foo-1234567", body["version"])
	}
	if body["build_time"] != "2026-06-25T16:00:00Z" {
		t.Errorf("build_time = %v", body["build_time"])
	}
	if _, ok := body["uptime"]; !ok {
		t.Errorf("uptime missing: %v", body)
	}
	if up, ok := body["uptime"].(float64); !ok || up < 30 {
		t.Errorf("uptime = %v, want >= 30", body["uptime"])
	}
}

// TestHealth_IncludesDiskKeysOn503 confirms the disk_free /
// disk_total fields are always present (when Statfs succeeds) so
// a monitoring agent can graph them regardless of health.
func TestHealth_IncludesDiskKeysOn503(t *testing.T) {
	h := newHealthHandler(t, "dev", "unknown")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	h.Health(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["disk_free"]; !ok {
		t.Errorf("disk_free missing: %v", body)
	}
	if _, ok := body["disk_total"]; !ok {
		t.Errorf("disk_total missing: %v", body)
	}
}

// TestHealth_NilLvAndMissingDataDir still returns 503: libvirt
// is the primary dependency and the handler must not mask its
// failure with a successful disk check.
func TestHealth_NilLvAndMissingDataDir(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{
			DataDir: "/this/path/does/not/exist/anywhere",
			Version: "dev",
		},
		StartedAt: time.Now(),
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	h.Health(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %q",
			rr.Code, rr.Body.String())
	}
}

// TestHealth_RegularFileAsDataDir does not panic when the
// DataDir is a regular file (Statfs may succeed and return
// zeroed stats). The handler must not 500 on that; 503 is fine
// because libvirt is nil in this test (which is the "degraded"
// reason), and disk_free/disk_total are still reported.
func TestHealth_RegularFileAsDataDir(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "iam-a-file")
	if err := os.WriteFile(f, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	h := &Handler{
		cfg:       &config.Config{DataDir: f, Version: "dev"},
		StartedAt: time.Now(),
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	h.Health(rr, req)
	if rr.Code == http.StatusInternalServerError {
		t.Fatalf("status = 500 is never expected; body = %q", rr.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["disk_free"]; !ok {
		t.Errorf("disk_free missing on regular-file path: %v", body)
	}
}

// TestHealth_NoAuthRequired confirms the handler does not consult
// r.Header for Authorization. The full router-level guarantee is
// tested by the integration suite (and is in the must-change
// whitelist so the auth middleware skips it).
func TestHealth_NoAuthRequired(t *testing.T) {
	h := newHealthHandler(t, "dev", "unknown")
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	// No Authorization header on purpose.
	h.Health(rr, req)
	if rr.Code == http.StatusUnauthorized {
		t.Fatalf("health must not require auth, got 401")
	}
}
