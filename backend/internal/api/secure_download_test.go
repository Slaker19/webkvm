package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// NOTE: httptest.NewServer always binds 127.0.0.1, which safeDownloadURL
// correctly rejects as loopback. That's why the transport-level tests
// below drive downloadWithClient (injecting a plain client) while the
// URL-validation behavior is covered separately against the real
// secureDownloadWithProgress entry point.

func TestDownloadWithClient_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	n, err := downloadWithClient(http.DefaultClient, "test-job-1", srv.URL, dest, 1<<20)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != int64(len("hello world")) {
		t.Errorf("written = %d, want %d", n, len("hello world"))
	}
	data, rerr := os.ReadFile(dest)
	if rerr != nil {
		t.Fatalf("read dest: %v", rerr)
	}
	if string(data) != "hello world" {
		t.Errorf("content = %q", string(data))
	}
}

func TestDownloadWithClient_EnforcesMaxBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 100))
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	_, err := downloadWithClient(http.DefaultClient, "test-job-2", srv.URL, dest, 50)
	if err == nil {
		t.Fatal("expected error for oversized download, got nil")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("expected dest file to be removed after exceeding maxBytes")
	}
}

func TestDownloadWithClient_HTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	_, err := downloadWithClient(http.DefaultClient, "test-job-3", srv.URL, dest, 1<<20)
	if err == nil {
		t.Fatal("expected error for 404 response, got nil")
	}
}

// TestSecureDownloadWithProgress_RejectsLoopback proves the SSRF guard
// fires before any file is created — this is the check PullBaseCloudImage
// was missing entirely.
func TestSecureDownloadWithProgress_RejectsLoopback(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	_, err := secureDownloadWithProgress("test-job-4", "http://127.0.0.1:1/nope", dest, 5*time.Second, 1<<20)
	if err == nil {
		t.Fatal("expected error for loopback URL, got nil")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("expected no file to be created when URL is rejected pre-flight")
	}
}

func TestSecureDownloadWithProgress_RejectsNonHTTPScheme(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	_, err := secureDownloadWithProgress("test-job-5", "file:///etc/passwd", dest, 5*time.Second, 1<<20)
	if err == nil {
		t.Fatal("expected error for file:// URL, got nil")
	}
}
