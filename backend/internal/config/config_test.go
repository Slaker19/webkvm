package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadFromDotEnvFile confirms the .env file is auto-loaded
// when present in the working directory. We use a temp dir as
// the CWD and write a .env there so we don't pollute the repo.
func TestLoadFromDotEnvFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir) // writable temp dir, not the /opt/webkvm default
	envPath := filepath.Join(dir, ".env")
	contents := strings.Join([]string{
		"PORT=9999",
		"BIND_ADDR=127.0.0.1",
		"LIBVIRT_URI=qemu+unix:///system_test",
		"PUBLIC_HOST=10.0.0.99",
		"VNC_PROXY_HOST=10.0.0.100",
		"WEBKVM_VERSION=test-v1.2.3",
		"WEBKVM_BUILD_TIME=2026-06-25T16:00:00Z",
		"WEBKVM_TRUST_PROXY=1",
		"WEBKVM_TRUSTED_RATELIMIT_CIDRS=10.0.0.0/8,192.168.0.0/16",
		"# DATA_DIR is intentionally not set so we get the default",
		"FOO=bar", // arbitrary var the backend doesn't read — should be ignored
	}, "\n")
	if err := os.WriteFile(envPath, []byte(contents), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	// Move into the temp dir so godotenv finds the file via CWD.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if chdirErr := os.Chdir(dir); chdirErr != nil {
		t.Fatalf("chdir: %v", chdirErr)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Port != 9999 {
		t.Errorf("Port: want 9999, got %d", cfg.Port)
	}
	if cfg.BindAddr != "127.0.0.1" {
		t.Errorf("BindAddr: want 127.0.0.1, got %q", cfg.BindAddr)
	}
	if cfg.LibvirtURI != "qemu+unix:///system_test" {
		t.Errorf("LibvirtURI: want %q, got %q", "qemu+unix:///system_test", cfg.LibvirtURI)
	}
	if cfg.PublicHost != "10.0.0.99" {
		t.Errorf("PublicHost: want %q, got %q", "10.0.0.99", cfg.PublicHost)
	}
	if cfg.VNCProxyHost != "10.0.0.100" {
		t.Errorf("VNCProxyHost: want %q, got %q", "10.0.0.100", cfg.VNCProxyHost)
	}
	if cfg.Version != "test-v1.2.3" {
		t.Errorf("Version: want %q, got %q", "test-v1.2.3", cfg.Version)
	}
	if cfg.BuildTime != "2026-06-25T16:00:00Z" {
		t.Errorf("BuildTime: want %q, got %q", "2026-06-25T16:00:00Z", cfg.BuildTime)
	}
}

// TestEnvVarBeatsDotEnv confirms that values already in the
// environment take precedence over .env (godotenv.Load, not
// Overload). This is the production safety property.
func TestEnvVarBeatsDotEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	envPath := filepath.Join(dir, ".env")
	if err := os.WriteFile(envPath, []byte("PORT=9999\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	origCwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	t.Setenv("PORT", "7777")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 7777 {
		t.Errorf("env var must beat .env: want 7777, got %d", cfg.Port)
	}
}

// TestLoadNoDotEnvFile: missing .env is not an error.
func TestLoadNoDotEnvFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	origCwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	// Debug: list the dir and confirm we're really in it.
	cur, _ := os.Getwd()
	entries, _ := os.ReadDir(cur)
	t.Logf("CWD=%s, entries=%d", cur, len(entries))
	for _, e := range entries {
		t.Logf("  %s", e.Name())
	}
	t.Logf("PORT env = %q", os.Getenv("PORT"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("default Port: want 8080, got %d", cfg.Port)
	}
	if cfg.BindAddr != "127.0.0.1" {
		t.Errorf("default BindAddr: want 127.0.0.1, got %q", cfg.BindAddr)
	}
}

// TestLoadMalformedDotEnvFile confirms that a malformed .env file does not
// crash the config loader; it simply ignores the file (or the malformed parts)
// and falls back to environment variables and defaults.
func TestLoadMalformedDotEnvFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir) // writable temp dir
	envPath := filepath.Join(dir, ".env")
	// "MALFORMED LINE" causes godotenv.Read to return an error.
	if err := os.WriteFile(envPath, []byte("MALFORMED LINE\nPORT=9999\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	origCwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Since Read returns an error, the whole file is likely skipped by godotenv,
	// returning an empty map, so PORT should be the default 8080.
	if cfg.Port != 8080 {
		t.Errorf("Port: want 8080 (fallback), got %d", cfg.Port)
	}
	if cfg.DataDir != dir {
		t.Errorf("DataDir: want %q, got %q", dir, cfg.DataDir)
	}
}

// TestLoadMissingFieldsDotEnv confirms that empty values in .env do not
// override defaults (since they might just be empty declarations like "PORT=").
func TestLoadMissingFieldsDotEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	envPath := filepath.Join(dir, ".env")
	// PORT= means empty string, should fall back to 8080
	if err := os.WriteFile(envPath, []byte("PORT=\nBIND_ADDR=\n"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	origCwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("Port: want 8080, got %d", cfg.Port)
	}
	if cfg.BindAddr != "127.0.0.1" {
		t.Errorf("BindAddr: want 127.0.0.1, got %q", cfg.BindAddr)
	}
}

// TestLoadWeakJWTSecret confirms that a weak JWT_SECRET fails validation
// and returns an error instead of letting the application start.
func TestLoadWeakJWTSecret(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)

	// Test a few weak secrets
	weakSecrets := []string{
		"secret",
		"password",
		"changeme",
		"aaaaaaaaaaaaaaaa", // length 16, but only lower case
		"1234567890123456", // length 16, but only digits
	}

	origCwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	for _, secret := range weakSecrets {
		t.Run("weak_secret_"+secret, func(t *testing.T) {
			t.Setenv("JWT_SECRET", secret)

			_, err := Load()
			if err == nil {
				t.Fatalf("Load with weak secret %q expected to fail, but succeeded", secret)
			}

			if !strings.Contains(err.Error(), "this is not allowed") {
				t.Errorf("Expected error to contain 'this is not allowed', got: %v", err)
			}
		})
	}
}

// TestResolveJWTSecretStrong confirms that a strong JWT_SECRET is accepted.
func TestResolveJWTSecretStrong(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)

	origCwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	t.Setenv("JWT_SECRET", "Strong-Secret-123!@#")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with strong secret expected to succeed, got: %v", err)
	}
	if cfg.JWTSecret != "Strong-Secret-123!@#" {
		t.Errorf("JWTSecret: want 'Strong-Secret-123!@#', got %q", cfg.JWTSecret)
	}
}
