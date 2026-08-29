package auth

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TrustedCIDRProvider lets the limiter honor X-Forwarded-For when
// running behind a reverse proxy. Implemented by *configstore.Store;
// declared as an interface so this package stays decoupled.
type TrustedCIDRProvider interface {
	// GetList returns the list of trusted CIDRs from
	// server.trusted_cidrs. Empty list = only loopback is trusted.
	GetList(key string) []string
	// GetBool returns the current value of server.trust_proxy. When
	// false, X-Forwarded-For is NEVER consulted, regardless of
	// trusted_cidrs.
	GetBool(key string) bool
}

// trustedCIDRsEnv is the legacy env var operators set to add CIDRs
// to the rate-limiter bypass list. Loopback (127.0.0.0/8, ::1) is
// always trusted. The format is a comma-separated list of CIDRs:
//
//	WEBKVM_TRUSTED_RATELIMIT_CIDRS=10.0.0.0/8,192.168.1.0/24
//
// New installs should populate server.trusted_cidrs in the Settings
// page instead; this env var is read once at construction as a
// fallback so older systemd units still work.
const trustedCIDRsEnv = "WEBKVM_TRUSTED_RATELIMIT_CIDRS"

// LoginRateLimiter is a small in-memory token bucket per (IP, username)
// tuple. After 5 failed attempts within a 15-minute window, the pair
// is locked out for 15 minutes. The limiter is intentionally simple —
// it lives in process memory and is reset on backend restart, which is
// fine for a single-host install.
//
// Requests from trusted sources (loopback, or any IP in
// WEBKVM_TRUSTED_RATELIMIT_CIDRS) skip the limiter entirely. This is
// for operators who have an external auth layer (e.g. wireguard VPN,
// single sign-on) and don't want their own admin CLI to be rate
// limited. It is NOT a default — by default, the limiter is strict
// and only loopback bypasses.
type LoginRateLimiter struct {
	mu        sync.Mutex
	failures  map[string]*rlBucket
	limit     int
	window    time.Duration
	lockout   time.Duration
	trusted   []*net.IPNet // CIDR allowlist parsed at construction
	skipCheck func(string) bool
	// settings is consulted for server.trust_proxy on every request
	// so a flip in the Settings page takes effect immediately. nil
	// is safe (defaults to "don't trust proxy").
	settings TrustedCIDRProvider
}

type rlBucket struct {
	failures   int
	firstFail  time.Time
	lockedTill time.Time
}

func NewLoginRateLimiter() *LoginRateLimiter {
	return NewLoginRateLimiterWithEnv("")
}

// NewLoginRateLimiterWithSettings wires the live settings store so
// the trust_proxy setting takes effect immediately. settings may be
// nil — in that case the limiter behaves as if trust_proxy is off
// (i.e. X-Forwarded-For is never consulted, even if the env-var
// CIDR list is populated).
func NewLoginRateLimiterWithSettings(settings TrustedCIDRProvider) *LoginRateLimiter {
	l := NewLoginRateLimiterWithEnv("")
	l.settings = settings
	return l
}

// NewLoginRateLimiterWithEnv is the testable constructor: callers
// can pass a fake env-var value. Empty string means "use the real
// environment", so production code keeps using NewLoginRateLimiter
// and tests can pin a known CIDR list.
func NewLoginRateLimiterWithEnv(envValue string) *LoginRateLimiter {
	if envValue == "" {
		envValue = os.Getenv(trustedCIDRsEnv)
	}
	trusted := parseTrustedCIDRs(envValue)
	return &LoginRateLimiter{
		failures: make(map[string]*rlBucket),
		limit:    5,
		window:   15 * time.Minute,
		lockout:  15 * time.Minute,
		trusted:  trusted,
		skipCheck: nil, // see isTrusted
	}
}

// parseTrustedCIDRs parses a comma-separated list of CIDRs and
// returns the parsed IPNet slice. Silently skips entries that
// don't parse (the operator can find out by hitting the endpoint
// and seeing the request still rate-limited).
func parseTrustedCIDRs(raw string) []*net.IPNet {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	out := make([]*net.IPNet, 0, 4)
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		_, n, err := net.ParseCIDR(p)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// peerIP returns the immediate TCP peer's address (RemoteAddr, port
// stripped), with no X-Forwarded-For handling.
func peerIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

// isTrustedProxyPeer reports whether ip is allowed to hand us a client
// IP via X-Forwarded-For: loopback, an env-configured CIDR, or a live
// server.trusted_cidrs entry.
func (l *LoginRateLimiter) isTrustedProxyPeer(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range l.trusted {
		if n.Contains(ip) {
			return true
		}
	}
	if l.settings != nil {
		for _, cidr := range l.settings.GetList("server.trusted_cidrs") {
			if _, n, err := net.ParseCIDR(strings.TrimSpace(cidr)); err == nil && n.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// clientIP resolves the request's real client IP. X-Forwarded-For is
// honored ONLY when server.trust_proxy is live-enabled in settings AND
// the immediate TCP peer is itself a trusted proxy source — i.e. only
// when the request actually arrived via a proxy we trust to set that
// header honestly. Without the peer check, ANY request landing on a
// process whose immediate peer happens to be loopback (the case for
// every request when a local reverse proxy fronts the backend, a
// topology this project documents and ships packaging for) would
// resolve to "loopback" and bypass the limiter for 100% of traffic
// regardless of the real client. Previously l.settings was stored but
// never consulted here at all, so server.trust_proxy had no effect on
// the rate limiter no matter how an operator configured it.
func (l *LoginRateLimiter) clientIP(r *http.Request) net.IP {
	peer := peerIP(r)
	trustProxy := l.settings != nil && l.settings.GetBool("server.trust_proxy")
	if trustProxy && l.isTrustedProxyPeer(peer) {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			first := v
			if i := strings.Index(v, ","); i >= 0 {
				first = v[:i]
			}
			if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
				return ip
			}
		}
	}
	return peer
}

// isTrusted returns true if the request's real client IP (see
// clientIP) is loopback or matches one of the configured trusted
// CIDRs (env-var at construction, or live server.trusted_cidrs).
func (l *LoginRateLimiter) isTrusted(r *http.Request) bool {
	ip := l.clientIP(r)
	if ip == nil {
		return false
	}
	return l.isTrustedProxyPeer(ip)
}

func (l *LoginRateLimiter) keyFor(r *http.Request, username string) string {
	ip := l.clientIP(r)
	host := "unknown"
	if ip != nil {
		host = ip.String()
	}
	return host + "|" + username
}

// Allow checks whether the request should be allowed. Returns true and
// nil on success; on lockout returns false and a Retry-After duration.
// Trusted sources (loopback, configured CIDRs) are always allowed.
func (l *LoginRateLimiter) Allow(r *http.Request, username string) (bool, time.Duration) {
	if l.isTrusted(r) {
		return true, 0
	}
	k := l.keyFor(r, username)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.failures[k]
	if !ok {
		return true, 0
	}
	if !b.lockedTill.IsZero() && now.Before(b.lockedTill) {
		return false, b.lockedTill.Sub(now)
	}
	// Window expired: reset.
	if now.Sub(b.firstFail) > l.window {
		delete(l.failures, k)
		return true, 0
	}
	return true, 0
}

// RecordFailure records a failed login. If the limit is reached, the
// bucket is locked for `lockout`. Trusted sources are not recorded.
func (l *LoginRateLimiter) RecordFailure(r *http.Request, username string) {
	if l.isTrusted(r) {
		return
	}
	k := l.keyFor(r, username)
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.failures[k]
	if !ok {
		l.failures[k] = &rlBucket{failures: 1, firstFail: now}
		return
	}
	if now.Sub(b.firstFail) > l.window {
		l.failures[k] = &rlBucket{failures: 1, firstFail: now}
		return
	}
	b.failures++
	if b.failures >= l.limit {
		b.lockedTill = now.Add(l.lockout)
	}
}

// RecordSuccess clears any failures for the (ip, user) pair.
// Trusted sources have no entry to clear, so this is a no-op.
func (l *LoginRateLimiter) RecordSuccess(r *http.Request, username string) {
	if l.isTrusted(r) {
		return
	}
	k := l.keyFor(r, username)
	l.mu.Lock()
	delete(l.failures, k)
	l.mu.Unlock()
}

// Middleware applies the limiter. `getUser` extracts the username from
// the request so we can rate-limit per account (not just per IP). For
// the login endpoint, we read the body before calling the limiter —
// the handler does that itself, so we just expose RecordFailure.
func (l *LoginRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
	})
}

// Helper to set a 429 response with Retry-After.
func WriteRateLimited(w http.ResponseWriter, retryAfter time.Duration) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"too many failed login attempts; try again later"}`))
}
