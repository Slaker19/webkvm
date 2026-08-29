package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"webkvm/internal/models"
)

// SettingsProvider is the small slice of the config store the auth
// package needs. Implemented by *configstore.Store; declared as an
// interface so this package doesn't depend on the store directly and
// tests can inject a fake.
type SettingsProvider interface {
	GetDuration(key string) time.Duration
	GetBool(key string) bool
}

type Manager struct {
	secret    []byte
	blacklist *TokenBlacklist
	// settings is consulted for hot-reloadable values: token TTL and
	// the API-tokens allow flag. nil is safe (a zero-value behaves as
	// "no settings": use the hardcoded defaults). Tests that don't
	// care can pass nil.
	settings SettingsProvider
	// tokenValidator, if set, is tried as a fallback after JWT
	// validation fails. It returns (username, role, error) and is
	// implemented by the API-tokens store (see internal/tokens).
	tokenValidator TokenValidator
}

// TokenValidator checks a raw token string and returns the
// (username, role) of its owner. Used by Middleware as a fallback
// after JWT validation, so API tokens and session JWTs can share
// the same Authorization: Bearer header.
type TokenValidator func(token string) (username, role string, err error)

// SetTokenValidator wires a fallback validator into the middleware.
func (m *Manager) SetTokenValidator(v TokenValidator) {
	m.tokenValidator = v
}

type Claims struct {
	Username           string `json:"username"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"mcp,omitempty"`
	JTI                string `json:"jti,omitempty"`
	jwt.RegisteredClaims
}

// NewManager constructs a Manager. The settings argument may be nil —
// in that case the package falls back to hardcoded defaults
// (24h TTL, API tokens allowed). Production code always passes the
// live *configstore.Store so a change on the Settings page takes
// effect on the next request.
func NewManager(secret string, settings SettingsProvider) *Manager {
	return &Manager{
		secret:    []byte(secret),
		blacklist: NewTokenBlacklist(),
		settings:  settings,
	}
}

// Close releases resources held by the Manager, notably the token blacklist's
// GC goroutine. It is safe to call once at process shutdown. Subsequent calls
// are no-ops.
func (m *Manager) Close() {
	if m.blacklist != nil {
		m.blacklist.Close()
	}
}

// the configstore is constructed if it wasn't available when
// NewManager ran. Safe to call once at startup.
func (m *Manager) SetSettings(s SettingsProvider) { m.settings = s }

// Blacklist exposes the denylist so handlers can revoke tokens
// (e.g. on /api/auth/logout).
func (m *Manager) Blacklist() *TokenBlacklist { return m.blacklist }

// defaultTokenTTL is the fallback when no settings store is wired.
// Matches the schema default for auth.token_ttl.
const defaultTokenTTL = 24 * time.Hour

// TokenTTL returns the configured session lifetime. Hot-reloadable:
// the next call after the Settings page changes the value returns
// the new one. Falls back to defaultTokenTTL when settings is nil
// or the value can't be parsed.
func (m *Manager) TokenTTL() time.Duration {
	if m.settings == nil {
		return defaultTokenTTL
	}
	if d := m.settings.GetDuration("auth.token_ttl"); d > 0 {
		return d
	}
	return defaultTokenTTL
}

// AllowAPITokens reflects the auth.allow_api_tokens setting. When
// false, Middleware will not fall back to the API-token validator
// even if it is configured, so an issued token's authorizations
// are cut off without a restart.
func (m *Manager) AllowAPITokens() bool {
	if m.settings == nil {
		return true
	}
	return m.settings.GetBool("auth.allow_api_tokens")
}

func (m *Manager) newJTI() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// GenerateToken issues a token with the current TokenTTL().
func (m *Manager) GenerateToken(username, role string) (string, int64, error) {
	return m.GenerateTokenWithJTI(username, role, m.newJTI())
}

// GenerateTokenWithMustChange is the canonical constructor for new
// tokens: it stamps the user's current must_change_password flag
// into the claims so a server restart with no in-memory state can
// still decide. The middleware in api/router.go always consults the
// live store too, so a stale claim can't keep a forced-rotation
// user locked out.
func (m *Manager) GenerateTokenWithMustChange(username, role string, mustChange bool) (string, int64, error) {
	return m.GenerateTokenFull(username, role, m.newJTI(), mustChange)
}

// GenerateTokenFull lets the caller pass jti (used by Refresh to
// rotate jti) and the must_change flag (the value at issuance time).
func (m *Manager) GenerateTokenFull(username, role, jti string, mustChange bool) (string, int64, error) {
	ttl := m.TokenTTL()
	expiresAt := time.Now().Add(ttl)
	claims := Claims{
		Username:           username,
		Role:               role,
		MustChangePassword: mustChange,
		JTI:                jti,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "webkvm",
			ID:        jti,
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", 0, err
	}
	return signed, expiresAt.Unix(), nil
}

// GenerateTokenWithJTI is kept for backward compatibility — it
// issues a token with mustChange=false. New code should call
// GenerateTokenWithMustChange.
func (m *Manager) GenerateTokenWithJTI(username, role, jti string) (string, int64, error) {
	return m.GenerateTokenFull(username, role, jti, false)
}

func (m *Manager) ValidateToken(tokenStr string) (*Claims, error) {
	claims, err := m.parseAndCheck(tokenStr)
	if err != nil {
		return nil, err
	}
	if m.blacklist.IsRevoked(claims.JTI, tokenStr) {
		return nil, fmt.Errorf("token revoked")
	}
	return claims, nil
}

// parseAndCheck is the JWT-only validation (signature, alg, exp). It
// does NOT consult the blacklist; use ValidateToken for that.
func (m *Manager) parseAndCheck(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return m.secret, nil
	})
	if err != nil {
		return nil, err
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}

	return claims, nil
}

// Revoke adds a token to the blacklist for the remaining lifetime.
func (m *Manager) Revoke(tokenStr string) error {
	claims, err := m.parseAndCheck(tokenStr)
	if err != nil {
		// Allow revoking already-expired tokens silently.
		claims = &Claims{}
	}
	exp := time.Now().Add(1 * time.Hour)
	if claims.ExpiresAt != nil {
		exp = claims.ExpiresAt.Time
	}
	m.blacklist.Revoke(claims.JTI, tokenStr, exp)
	return nil
}

func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Genuinely public: the login endpoint, the health probe, embedded
		// static assets, and VM cover images (guarded by an unguessable
		// UUID in the URL).
		if path == "/api/auth/login" || path == "/api/health" ||
			strings.HasPrefix(path, "/static/") ||
			strings.HasPrefix(path, "/api/covers/") ||
			path == "/api/system/cert" {
			next.ServeHTTP(w, r)
			return
		}

		// The noVNC/RDP/SPICE console pages open in a new tab, which cannot
		// set request headers, so the token travels as a query parameter.
		consolePath := strings.HasPrefix(path, "/console/") ||
			isVNCConsolePath(path) || isRDPConsolePath(path) || isSPICEConsolePath(path) ||
			strings.HasSuffix(path, "/serial") || path == "/api/host/terminal"

		// Embedded terminals (VM serial / host PTY) authenticate with
		// short-lived single-use tickets minted by an authenticated API
		// call — no long-lived credentials travel in URLs. The host PTY
		// re-checks the role the ticket was issued with.
		if tk := r.URL.Query().Get("ticket"); tk != "" {
			user, role, ok := ConsumeTicket(tk)
			if !ok {
				http.Error(w, `{"error":"invalid or expired ticket"}`, http.StatusUnauthorized)
				return
			}
			if strings.HasSuffix(path, "/api/host/terminal") && role != string(models.RoleAdmin) {
				http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
				return
			}
			// Downstream middlewares (RequireAtLeast) resolve identity
			// from these headers against the user store — restore the
			// real username the ticket was issued to.
			r.Header.Set("X-User", user)
			r.Header.Set("X-Role", role)
			next.ServeHTTP(w, r)
			return
		}

		// VNC console ticket: unlike the single-use ticket above, this one
		// is reusable within its (much shorter than a session JWT) lifetime
		// — the console page opens in a new tab and then opens its own
		// WebSocket, and may reconnect several times, so a single-use
		// ticket can't cover it. Scoped to one VM ID and a short endpoint
		// allowlist (see vncTicketAllowedPath) — never a stand-in for the
		// full session JWT.
		if vt := r.URL.Query().Get("vt"); vt != "" {
			vmID, allowed := vncTicketAllowedPath(path)
			if !allowed {
				http.Error(w, `{"error":"ticket not valid for this endpoint"}`, http.StatusForbidden)
				return
			}
			user, role, ok := CheckVNCTicket(vt, vmID)
			if !ok {
				http.Error(w, `{"error":"invalid or expired ticket"}`, http.StatusUnauthorized)
				return
			}
			r.Header.Set("X-User", user)
			r.Header.Set("X-Role", role)
			next.ServeHTTP(w, r)
			return
		}

		// Everything else outside /api/* is the embedded SPA shell, served
		// without auth (the SPA itself redirects to the login page).
		if !consolePath && !strings.HasPrefix(path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}

		tokenStr := ""
		if authHeader := r.Header.Get("Authorization"); authHeader != "" {
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				tokenStr = parts[1]
			}
		}
		if tokenStr == "" && consolePath {
			// The console tab opens in a new browser tab and cannot set
			// request headers, so the token travels in the query string
			// for these endpoints only. Tokens in URLs leak into
			// access/proxy logs, browser history and Referer headers, so
			// every other route rejects query tokens and requires either
			// the Authorization header or a short-lived ticket (see the
			// `ticket` branch above) — which is how /api/events (SSE)
			// authenticates, since EventSource also cannot set headers.
			tokenStr = r.URL.Query().Get("token")
		}
		if tokenStr == "" {
			http.Error(w, `{"error":"missing authorization header"}`, http.StatusUnauthorized)
			return
		}

		claims, err := m.ValidateToken(tokenStr)
		if err != nil {
			// Fall back to API token validation if configured AND
			// enabled in the settings store. A flip of
			// auth.allow_api_tokens takes effect on the next
			// request, with no restart required.
			if m.tokenValidator != nil && m.AllowAPITokens() {
				if u, role, tokErr := m.tokenValidator(tokenStr); tokErr == nil {
					r.Header.Set("X-User", u)
					r.Header.Set("X-Role", role)
					next.ServeHTTP(w, r)
					return
				}
			}
			http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
			return
		}

		r.Header.Set("X-User", claims.Username)
		r.Header.Set("X-Role", claims.Role)
		next.ServeHTTP(w, r)
	})
}

// isVNCConsolePath returns true if path is exactly /api/vms/{id}/vnc.
func isVNCConsolePath(path string) bool {
	if !strings.HasPrefix(path, "/api/vms/") {
		return false
	}
	rest := strings.TrimPrefix(path, "/api/vms/")
	return len(rest) > 0 && strings.Count(rest, "/") == 1 && strings.HasSuffix(rest, "/vnc")
}

// isRDPConsolePath returns true if path is exactly /api/vms/{id}/rdp.
func isRDPConsolePath(path string) bool {
	if !strings.HasPrefix(path, "/api/vms/") {
		return false
	}
	rest := strings.TrimPrefix(path, "/api/vms/")
	return len(rest) > 0 && strings.Count(rest, "/") == 1 && strings.HasSuffix(rest, "/rdp")
}

// isSPICEConsolePath returns true if path is exactly /api/vms/{id}/spice.
func isSPICEConsolePath(path string) bool {
	if !strings.HasPrefix(path, "/api/vms/") {
		return false
	}
	rest := strings.TrimPrefix(path, "/api/vms/")
	return len(rest) > 0 && strings.Count(rest, "/") == 1 && strings.HasSuffix(rest, "/spice")
}

// isClipboardPath returns true if path is exactly /api/vms/{id}/clipboard.
func isClipboardPath(path string) bool {
	if !strings.HasPrefix(path, "/api/vms/") {
		return false
	}
	rest := strings.TrimPrefix(path, "/api/vms/")
	return len(rest) > 0 && strings.Count(rest, "/") == 1 && strings.HasSuffix(rest, "/clipboard")
}

// vncTicketAllowedPath reports whether path is one a VNC console
// ticket may authenticate, and extracts the VM ID it's scoped to.
// Deliberately a short, fixed allowlist — unlike the session JWT, a
// VNC ticket must never authenticate arbitrary API routes.
func vncTicketAllowedPath(path string) (vmID string, ok bool) {
	if strings.HasPrefix(path, "/console/") {
		rest := strings.TrimPrefix(path, "/console/")
		if rest != "" && !strings.Contains(rest, "/") {
			return rest, true
		}
		return "", false
	}
	if !strings.HasPrefix(path, "/api/vms/") {
		return "", false
	}
	rest := strings.TrimPrefix(path, "/api/vms/")
	for _, suffix := range []string{"/vnc", "/clipboard", "/reboot", "/shutdown", "/forceoff"} {
		if strings.HasSuffix(rest, suffix) && strings.Count(rest, "/") == 1 {
			return strings.TrimSuffix(rest, suffix), true
		}
	}
	return "", false
}
