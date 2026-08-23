package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webkvm/internal/config"
)

// TestSystemCert returns the certificate file with download headers.
func TestSystemCert(t *testing.T) {
	dir := t.TempDir()
	certDir := filepath.Join(dir, "certs")
	if err := os.MkdirAll(certDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(filepath.Join(certDir, "webkvm.crt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	h := &Handler{cfg: &config.Config{DataDir: dir}}
	req := httptest.NewRequest(http.MethodGet, "/api/system/cert", nil)
	rec := httptest.NewRecorder()
	h.SystemCert(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if rec.Body.String() != content {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "pem") {
		t.Fatalf("expected pem content type, got %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "webkvm.crt") {
		t.Fatalf("expected attachment filename, got %q", cd)
	}
}

// TestSystemCertMissing returns 404 when no certificate is configured.
func TestSystemCertMissing(t *testing.T) {
	dir := t.TempDir()
	h := &Handler{cfg: &config.Config{DataDir: dir}}
	req := httptest.NewRequest(http.MethodGet, "/api/system/cert", nil)
	rec := httptest.NewRecorder()
	h.SystemCert(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}