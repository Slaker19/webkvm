// Global per-IP rate limiting (V13-SEC-02).
//
// A token bucket (golang.org/x/time/rate) per client IP guards the whole
// /api/* surface against scanners and abuse. Design properties:
//
//   - Concurrency: the IP→bucket map is protected by a sync.Mutex; each
//     bucket's rate.Limiter is itself goroutine-safe.
//   - Memory: a background sweeper goroutine drops buckets that have been
//     idle for TTL, so a distributed scan can never grow the map without
//     bound (no limiter without a sweeper is a guaranteed leak).
//   - Exemptions are evaluated BEFORE any token is consumed: requests
//     authenticated via a Bearer token (API tokens / explicit sessions)
//     and requests from trusted CIDRs skip the general limit entirely.
//     Because this middleware runs AFTER the JWT middleware, any request
//     that still carries an Authorization header here has already been
//     validated — a forged header is rejected upstream before we ever
//     see it, so the exemption cannot be gamed.
//   - Standards: 429 Too Many Requests with Retry-After, plus the
//     X-RateLimit-* response headers on every request.
package auth

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// RateLimitSettings is the slice of the settings store the global
// limiter reads live (hot reload). Implemented by *configstore.Store;
// nil is safe and falls back to hardcoded defaults.
type RateLimitSettings interface {
	GetBool(key string) bool
	GetInt(key string) int
	GetList(key string) []string
}

// GlobalRateLimitDefaults are the values used when no settings store is
// wired and no env var is set. 50 rps with a 100-token burst absorbs a
// full page load (every VM's sparkline + the lists) on a large fleet
// while still throttling brute-force scans.
const (
	DefaultRateLimitEnabled = true
	DefaultRateLimitRPS     = 50
	DefaultRateLimitBurst   = 100
	// rateLimitSweepInterval / rateLimitTTL bound the IP map memory:
	// buckets idle for more than rateLimitTTL are purged by a sweeper
	// that runs every rateLimitSweepInterval.
	rateLimitSweepInterval = 5 * time.Minute
	rateLimitTTL           = 15 * time.Minute
)

// GlobalRateLimiter is a per-IP token bucket with a background sweeper.
type GlobalRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipRateBucket

	// trusted CIDRs parsed once from the legacy env var.
	trusted []*net.IPNet
	// settings is consulted on every request for the live values
	// (enabled, rps, burst, trusted_cidrs, trust_proxy).
	settings RateLimitSettings
	// ttl bounds how long an idle bucket survives.
	ttl time.Duration

	stop     chan struct{}
	stopOnce sync.Once
}

// ipRateBucket wraps a token limiter plus the timestamp of its last use.
// The creation-time params are kept so a settings change can rebuild the
// limiter (existing buckets must adopt new rps/burst immediately, not
// keep stale parameters for their whole idle life).
type ipRateBucket struct {
	lim   *rate.Limiter
	last  time.Time
	rps   float64
	burst int
}

// NewGlobalRateLimiter builds the limiter and starts its sweeper.
// settings may be nil (defaults apply); rps/burst/enabled come from
// settings at bucket-creation time, and a change rebuilds existing
// buckets on their next request.
func NewGlobalRateLimiter(settings RateLimitSettings) *GlobalRateLimiter {
	l := &GlobalRateLimiter{
		buckets:  make(map[string]*ipRateBucket),
		trusted:  parseTrustedCIDRs(os.Getenv(trustedCIDRsEnv)),
		settings: settings,
		ttl:      rateLimitTTL,
		stop:     make(chan struct{}),
	}
	go l.sweep(rateLimitSweepInterval)
	return l
}

// Close stops the sweeper goroutine.
func (l *GlobalRateLimiter) Close() {
	l.stopOnce.Do(func() { close(l.stop) })
}

// enabled returns the live rate_limit_enabled flag (default true).
func (l *GlobalRateLimiter) enabled() bool {
	if l.settings != nil {
		return l.settings.GetBool("server.rate_limit_enabled")
	}
	return DefaultRateLimitEnabled
}

// params returns the live rps/burst (env defaults when unset).
func (l *GlobalRateLimiter) params() (float64, int) {
	rps := float64(DefaultRateLimitRPS)
	burst := DefaultRateLimitBurst
	if l.settings != nil {
		if v := l.settings.GetInt("server.rate_limit_rps"); v > 0 {
			rps = float64(v)
		}
		if v := l.settings.GetInt("server.rate_limit_burst"); v > 0 {
			burst = v
		}
	}
	return rps, burst
}

// exempt reports whether the request must skip the general limit. It is
// evaluated BEFORE consuming any token. A Bearer header means a validated
// API-token/session request (invalid ones never reach us — the JWT
// middleware runs first and 401s them). Trusted sources (loopback +
// configured CIDRs) are always exempt.
func (l *GlobalRateLimiter) exempt(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return true
	}
	ip := clientIPOf(r, l.trusted, l.settings)
	return isTrustedPeer(ip, l.trusted, l.settings)
}

// limiterFor returns the token bucket for ip, creating it (with the
// current live params) if needed, and stamps its last-use time.
func (l *GlobalRateLimiter) limiterFor(ip net.IP) *rate.Limiter {
	key := "unknown"
	if ip != nil {
		key = ip.String()
	}
	rps, burst := l.params()
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[key]
	if !ok || b.rps != rps || b.burst != burst {
		// New IP, or the operator changed rps/burst since the bucket was
		// created: rebuild so the new policy applies immediately instead
		// of lingering with stale parameters.
		b = &ipRateBucket{lim: rate.NewLimiter(rate.Limit(rps), burst), rps: rps, burst: burst}
		l.buckets[key] = b
	}
	b.last = now
	return b.lim
}

// Middleware rate-limits /api/* requests per client IP. Non-API paths
// (embedded SPA shell, static assets, console pages) and /api/health are
// intentionally excluded so page loads and health probes are never
// throttled.
func (l *GlobalRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if !l.enabled() ||
			!strings.HasPrefix(path, "/api/") ||
			path == "/api/health" ||
			l.exempt(r) {
			next.ServeHTTP(w, r)
			return
		}

		lim := l.limiterFor(clientIPOf(r, l.trusted, l.settings))
		res := lim.Reserve()
		if !res.OK() {
			// Burst <= 0: misconfiguration — don't break the API, just
			// let it through.
			next.ServeHTTP(w, r)
			return
		}
		delay := res.Delay()

		// Standard X-RateLimit-* headers on every response. Remaining is the
		// number of tokens currently available (0 when the bucket is empty).
		now := time.Now()
		tokens := lim.Tokens()
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(lim.Burst()))
		remaining := tokens
		if remaining < 0 {
			remaining = 0
		}
		if remaining > float64(lim.Burst()) {
			remaining = float64(lim.Burst())
		}
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatFloat(remaining, 'f', 0, 64))
		reset := now
		if tokens < float64(lim.Burst()) {
			refill := time.Duration((float64(lim.Burst()) - tokens) / float64(lim.Limit()) * float64(time.Second))
			reset = now.Add(refill)
		}
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))

		if delay > 0 {
			res.Cancel()
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", strconv.Itoa(int(delay.Seconds())+1))
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"too many requests; try again later"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sweep is the background goroutine that drops buckets idle longer than
// ttl, bounding the map's memory under scans or DDoS.
func (l *GlobalRateLimiter) sweep(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.sweepOnce(time.Now(), l.ttl)
		}
	}
}

// sweepOnce drops buckets whose last use predates (now - ttl). Exported
// logic for tests; the goroutine calls it on every tick.
func (l *GlobalRateLimiter) sweepOnce(now time.Time, ttl time.Duration) {
	cutoff := now.Add(-ttl)
	l.mu.Lock()
	for k, b := range l.buckets {
		if b.last.Before(cutoff) {
			delete(l.buckets, k)
		}
	}
	l.mu.Unlock()
}
