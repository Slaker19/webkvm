package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const (
	DiskPoolName = "webkvm-disks"
	ISOPoolName  = "ISOS"

	// DefaultJWTSecret is the placeholder documented in early
	// installs. The server refuses to boot with this value (or any
	// other obviously-default string) and generates a random secret
	// on first run.
	DefaultJWTSecret = "change-me-in-production"
)

type Config struct {
	Port       int
	BindAddr   string
	LibvirtURI string
	JWTSecret  string
	DataDir    string
	Version    string
	BuildTime  string
	RepoDir    string
	LogFile    string

	// VNCProxyHost is the host the backend opens TCP connections
	// to when proxying the noVNC WebSocket to libvirt's VNC port.
	// On a regular install the VNC socket is on 127.0.0.1, so
	// empty falls back to 127.0.0.1. Override with the
	// VNC_PROXY_HOST env var for a custom libvirtd bind address.
	VNCProxyHost string

	// PublicHost is the host baked into the .rdp and .vv (SPICE)
	// files the user downloads. It must be reachable from the
	// user's client machine. Empty falls back to the first
	// non-loopback IPv4 of the running process. Override with the
	// PUBLIC_HOST env var (set to the host's LAN IP).
	PublicHost string

	// CORSOrigin controls the Access-Control-Allow-Origin header.
	// Set to the scheme+host of the reverse proxy (e.g. https://webkvm.local)
	// to restrict CORS. Defaults to "*" (all origins) for LAN
	// compatibility. Override with CORS_ORIGIN env var.
	CORSOrigin string
}

// Load assembles the config from environment variables. For the JWT
// secret, if no env var is set, we look for {DataDir}/jwt.key; if
// that is missing too, we generate a random 256-bit secret, persist
// it to that path with 0600 permissions, and use it. The default
// placeholder is never accepted: setting JWT_SECRET to it (or leaving
// it unset on a fresh install) is treated as "no secret configured".
//
// .env loading: if a .env file exists in the current working
// directory (typically the repo root for `make dev` or the
// directory you started the server from), it is loaded as a
// fallback for any env var that is NOT already set in the
// process environment. Missing file is not an error — the
// systemd unit sets env vars via the unit file, so .env is purely
// a developer convenience.
//
// We use godotenv.Read (not Load) to avoid mutating os.Environ
// as a side effect — that side effect is what makes the call
// hard to test in isolation and would surprise a power user
// running the same binary in two shells.
func Load() (*Config, error) {
	dotenv, _ := godotenv.Read(".env") // best-effort; missing file is fine

	dataDir := envStrFrom("DATA_DIR", defaultDataDir(), dotenv)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	secret, err := resolveJWTSecret(envStrFrom("JWT_SECRET", "", dotenv), dataDir)
	if err != nil {
		return nil, err
	}

	return &Config{
		Port:         envIntFrom("PORT", 8080, dotenv),
		// BindAddr defaults to 127.0.0.1 (loopback) — external clients
		// reach the backend through Caddy (which terminates TLS). The
		// Caddyfile points at 127.0.0.1:PORT, so the backend never
		// needs to be exposed. Operators that run without a reverse
		// proxy can override with BIND_ADDR=0.0.0.0.
		BindAddr:     envStrFrom("BIND_ADDR", "127.0.0.1", dotenv),
		LibvirtURI:   envStrFrom("LIBVIRT_URI", "qemu:///system", dotenv),
		JWTSecret:    secret,
		DataDir:      dataDir,
		Version:      envStrFrom("WEBKVM_VERSION", "dev", dotenv),
		BuildTime:    envStrFrom("WEBKVM_BUILD_TIME", "unknown", dotenv),
		// RepoDir is where the backend looks for helper scripts
		// (setup-bridge.sh, etc.) and the live source tree. The
		// systemd units set REPO_DIR; WEBKVM_REPO_DIR is the
		// fallback for dotfiles.
		RepoDir:      envStrFrom("REPO_DIR", envStrFrom("WEBKVM_REPO_DIR", defaultDataDir(), dotenv), dotenv),
		VNCProxyHost: envStrFrom("VNC_PROXY_HOST", "127.0.0.1", dotenv),
		PublicHost:   envStrFrom("PUBLIC_HOST", "", dotenv),
		CORSOrigin:   envStrFrom("CORS_ORIGIN", "*", dotenv),
		LogFile:      envStrFrom("WEBKVM_LOG_FILE", "", dotenv),
	}, nil
}

// resolveJWTSecret returns a secret to use, never the default
// placeholder. If env is empty, persist and use a generated one. If
// env is set to the default placeholder, refuse to boot.
func resolveJWTSecret(env, dataDir string) (string, error) {
	keyPath := filepath.Join(dataDir, "jwt.key")

	// 1. Env explicitly set.
	if env != "" {
		if isDefaultOrWeak(env) {
			return "", fmt.Errorf("JWT_SECRET is set to the default placeholder; this is not allowed. Unset it (a random key will be generated and saved to %s) or set it to a strong value", keyPath)
		}
		return env, nil
	}

	// 2. Try persisted key.
	if data, err := os.ReadFile(keyPath); err == nil {
		secret := strings.TrimSpace(string(data))
		if secret != "" && !isDefaultOrWeak(secret) {
			return secret, nil
		}
	}

	// 3. Generate and persist.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate jwt secret: %w", err)
	}
	secret := base64.RawURLEncoding.EncodeToString(raw)
	tmpPath := keyPath + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(secret), 0600); err != nil {
		return "", fmt.Errorf("persist jwt key: %w", err)
	}
	if err := os.Rename(tmpPath, keyPath); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("rename jwt key: %w", err)
	}
	return secret, nil
}

// isDefaultOrWeak returns true if the secret is the well-known default
// placeholder or trivially guessable. We deliberately keep this list
// short and conservative.
func isDefaultOrWeak(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	switch low {
	case DefaultJWTSecret, "secret", "password", "changeme", "":
		return true
	}
	if len(s) < 16 {
		return true
	}
	// Reject secrets that don't have at least 2 of: uppercase, lowercase,
	// digit, special character. Pure alphabetic 16-char strings (e.g.
	// "aaaaaaaaaaaaaaaa") are trivially guessable.
	var hasUpper, hasLower, hasDigit, hasSpecial bool
	for _, c := range s {
		switch {
		case 'A' <= c && c <= 'Z':
			hasUpper = true
		case 'a' <= c && c <= 'z':
			hasLower = true
		case '0' <= c && c <= '9':
			hasDigit = true
		default:
			hasSpecial = true
		}
	}
	classes := 0
	if hasUpper {
		classes++
	}
	if hasLower {
		classes++
	}
	if hasDigit {
		classes++
	}
	if hasSpecial {
		classes++
	}
	if classes < 2 {
		return true
	}
	return false
}

func defaultDataDir() string {
	// Standalone install (e.g. running on a Debian host). Keep
	// this identical to the standalone installer and systemd unit
	// so a manually started binary uses the same persistent state
	// as a packaged installation.
	return "/opt/webkvm"
}

func (c *Config) PoolsDir() string {
	return filepath.Join(c.DataDir, "pools")
}

func (c *Config) DiskPoolPath() string {
	return filepath.Join(c.PoolsDir(), DiskPoolName)
}

func (c *Config) ISOPoolPath() string {
	return filepath.Join(c.PoolsDir(), ISOPoolName)
}

// CoversDir returns the directory where VM cover images are stored.
// The directory is created on demand by callers.
func (c *Config) CoversDir() string {
	return filepath.Join(c.DataDir, "covers")
}

// GroupsFile returns the path of the JSON file holding group definitions.
func (c *Config) GroupsFile() string {
	return filepath.Join(c.DataDir, "groups.json")
}

// AppliancesFile returns the path of the JSON file holding the editable
// appliance catalog. The file is seeded from built-in defaults on first run.
func (c *Config) AppliancesFile() string {
	return filepath.Join(c.DataDir, "appliances.json")
}

// AuditLogFile is the JSONL file where the audit logger appends.
func (c *Config) AuditLogFile() string {
	return filepath.Join(c.DataDir, "audit.log")
}

// ErrNotConfigured indicates the config couldn't be loaded because
// a required value was missing or invalid.
var ErrNotConfigured = errors.New("config: not configured")

func envStr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

// envStrFrom and envIntFrom are the .env-aware versions: they
// check os.Getenv first (real environment wins), then fall back
// to the parsed .env map, then to the hardcoded default.
func envStrFrom(key, fallback string, dotenv map[string]string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if v, ok := dotenv[key]; ok && v != "" {
		return v
	}
	return fallback
}

func envIntFrom(key string, fallback int, dotenv map[string]string) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	if v, ok := dotenv[key]; ok {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
