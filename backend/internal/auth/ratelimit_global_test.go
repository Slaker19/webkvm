package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeRateSettings implements RateLimitSettings for tests.
type fakeRateSettings struct {
	enabled    bool
	rps        int
	burst      int
	trusted    []string
	trustProxy bool
}

func (f *fakeRateSettings) GetBool(k string) bool {
	switch k {
	case "server.rate_limit_enabled":
		return f.enabled
	case "server.trust_proxy":
		return f.trustProxy
	}
	return false
}

func (f *fakeRateSettings) GetInt(k string) int {
	switch k {
	case "server.rate_limit_rps":
		return f.rps
	case "server.rate_limit_burst":
		return f.burst
	}
	return 0
}

func (f *fakeRateSettings) GetList(k string) []string {
	if k == "server.trusted_cidrs" {
		return f.trusted
	}
	return nil
}

// doRequest drives the middleware once and returns the recorder.
func doRequest(l *GlobalRateLimiter, remote string, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	if remote != "" {
		req.RemoteAddr = remote
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rr := httptest.NewRecorder()
	l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)
	return rr
}

func TestGlobalRateLimiter_ExhaustsAndRecovers(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 100, burst: 3})
	t.Cleanup(l.Close)

	// burst=3: first three pass, fourth trips 429.
	for i := 0; i < 3; i++ {
		if rr := doRequest(l, "10.0.0.1:1234", ""); rr.Code != http.StatusOK {
			t.Fatalf("request %d: got %d, want 200", i+1, rr.Code)
		}
	}
	rr := doRequest(l, "10.0.0.1:1234", "")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("4th request: got %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Error("429 response must carry Retry-After")
	}
	// Different IP is unaffected (per-IP buckets).
	if rr := doRequest(l, "10.0.0.2:1234", ""); rr.Code != http.StatusOK {
		t.Errorf("different IP: got %d, want 200", rr.Code)
	}
}

func TestGlobalRateLimiter_HeadersPresent(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 10, burst: 5})
	t.Cleanup(l.Close)
	rr := doRequest(l, "10.1.1.1:1234", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rr.Code)
	}
	if rr.Header().Get("X-RateLimit-Limit") != "5" {
		t.Errorf("X-RateLimit-Limit = %q, want 5", rr.Header().Get("X-RateLimit-Limit"))
	}
	if rr.Header().Get("X-RateLimit-Remaining") == "" {
		t.Error("X-RateLimit-Remaining missing")
	}
	if _, err := strconv.ParseInt(rr.Header().Get("X-RateLimit-Reset"), 10, 64); err != nil {
		t.Errorf("X-RateLimit-Reset invalid: %v", err)
	}
}

// TestGlobalRateLimiter_CIDRExemption: an IP inside a trusted CIDR is
// never limited, even when the same IP would otherwise be exhausted.
func TestGlobalRateLimiter_CIDRExemption(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{
		enabled: true, rps: 100, burst: 1, trusted: []string{"10.99.0.0/16"},
	})
	t.Cleanup(l.Close)

	// Exhaust the loopback? No — trusted IP path: 10.99.5.5 (in CIDR).
	for i := 0; i < 5; i++ {
		rr := doRequest(l, "10.99.5.5:1234", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("trusted CIDR request %d: got %d, want 200", i+1, rr.Code)
		}
	}
	// Loopback is always trusted too.
	for i := 0; i < 5; i++ {
		rr := doRequest(l, "127.0.0.1:1234", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("loopback request %d: got %d, want 200", i+1, rr.Code)
		}
	}
	// An untrusted IP with the same tiny burst does trip.
	rr := doRequest(l, "10.98.1.1:1234", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("untrusted first request: got %d, want 200", rr.Code)
	}
	if rr := doRequest(l, "10.98.1.1:1234", ""); rr.Code != http.StatusTooManyRequests {
		t.Errorf("untrusted exhausted request: got %d, want 429", rr.Code)
	}
}

// TestGlobalRateLimiter_EnvCIDRExemption: the legacy env-var CIDR list is
// honored at construction.
func TestGlobalRateLimiter_EnvCIDRExemption(t *testing.T) {
	t.Setenv(trustedCIDRsEnv, "172.16.0.0/12")
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 100, burst: 1})
	t.Cleanup(l.Close)
	for i := 0; i < 5; i++ {
		rr := doRequest(l, "172.16.9.9:1234", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("env CIDR request %d: got %d, want 200", i+1, rr.Code)
		}
	}
}

// TestGlobalRateLimiter_BearerExemption: an API-token (Bearer) request
// skips the general limit entirely. (In production the JWT middleware
// validates the token first; here the limiter only sees the header.)
func TestGlobalRateLimiter_BearerExemption(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 100, burst: 1})
	t.Cleanup(l.Close)
	for i := 0; i < 5; i++ {
		rr := doRequest(l, "203.0.113.7:1234", "some.api.token")
		if rr.Code != http.StatusOK {
			t.Fatalf("bearer request %d: got %d, want 200", i+1, rr.Code)
		}
	}
}

// TestGlobalRateLimiter_Disabled: when disabled, everything passes.
func TestGlobalRateLimiter_Disabled(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: false, rps: 1, burst: 1})
	t.Cleanup(l.Close)
	for i := 0; i < 5; i++ {
		rr := doRequest(l, "198.51.100.1:1234", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("disabled request %d: got %d, want 200", i+1, rr.Code)
		}
	}
}

// TestGlobalRateLimiter_NonAPIAndHealthExempt: SPA shell / static assets
// and /api/health are never throttled.
func TestGlobalRateLimiter_NonAPIAndHealthExempt(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 100, burst: 1})
	t.Cleanup(l.Close)
	for _, path := range []string{"/", "/static/app.js", "/api/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "203.0.113.5:1234"
		rr := httptest.NewRecorder()
		l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: got %d, want 200 (exempt)", path, rr.Code)
		}
	}
}

// TestGlobalRateLimiter_SweepPurgesIdleBuckets: buckets idle past the TTL
// are removed so the map cannot grow without bound.
func TestGlobalRateLimiter_SweepPurgesIdleBuckets(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 10, burst: 5})
	t.Cleanup(l.Close)

	// Create a bucket from an IP, then age it past the TTL.
	_ = doRequest(l, "10.50.0.1:1234", "")
	l.mu.Lock()
	if _, ok := l.buckets["10.50.0.1"]; !ok {
		t.Fatal("bucket not created")
	}
	l.buckets["10.50.0.1"].last = time.Now().Add(-(rateLimitTTL + time.Hour))
	l.mu.Unlock()

	l.sweepOnce(time.Now(), l.ttl)

	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.buckets["10.50.0.1"]; ok {
		t.Error("idle bucket not purged by sweeper")
	}
}

// TestGlobalRateLimiter_Concurrent: many goroutines hammer the middleware
// from distinct IPs. Run with -race; the map must not collide, and after a
// sweep nothing is retained for idle IPs.
func TestGlobalRateLimiter_Concurrent(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 1000, burst: 1000})
	t.Cleanup(l.Close)

	const workers = 32
	const perWorker = 50
	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			ip := fmt.Sprintf("10.%d.%d.%d:1234", w/100, w%100, w)
			for i := 0; i < perWorker; i++ {
				rr := doRequest(l, ip, "")
				if rr.Code != http.StatusOK {
					t.Errorf("worker %d req %d: got %d, want 200", w, i, rr.Code)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	// All 32 buckets exist, none leaked.
	l.mu.Lock()
	if got := len(l.buckets); got != workers {
		t.Errorf("bucket count = %d, want %d", got, workers)
	}
	// Age them all and sweep → map empties (memory released).
	for _, b := range l.buckets {
		b.last = time.Now().Add(-(rateLimitTTL + time.Hour))
	}
	l.mu.Unlock()
	l.sweepOnce(time.Now(), l.ttl)
	l.mu.Lock()
	if got := len(l.buckets); got != 0 {
		t.Errorf("after sweep bucket count = %d, want 0", got)
	}
	l.mu.Unlock()
}

// TestGlobalRateLimiter_UnknownIPAllowed: a request with no parseable IP
// still gets a bucket (key "unknown") and is not a crash path.
func TestGlobalRateLimiter_UnknownIPAllowed(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 10, burst: 5})
	t.Cleanup(l.Close)
	rr := doRequest(l, "", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("unknown ip: got %d, want 200", rr.Code)
	}
}

// TestGlobalRateLimiter_PerIPBucketsIsolated: two IPs must not share a
// bucket — exhausting one leaves the other untouched.
func TestGlobalRateLimiter_PerIPBucketsIsolated(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 100, burst: 1})
	t.Cleanup(l.Close)
	if rr := doRequest(l, "192.0.2.1:1234", ""); rr.Code != http.StatusOK {
		t.Fatal("first ip first request should pass")
	}
	if rr := doRequest(l, "192.0.2.1:1234", ""); rr.Code != http.StatusTooManyRequests {
		t.Fatal("first ip should be exhausted")
	}
	if rr := doRequest(l, "192.0.2.2:1234", ""); rr.Code != http.StatusOK {
		t.Fatal("second ip must have its own fresh bucket")
	}
}

// TestGlobalRateLimiter_ParamChangeRebuildsBucket: lowering rps/burst must
// apply to EXISTING buckets on their next request (not linger with the
// old parameters for the whole idle life).
func TestGlobalRateLimiter_ParamChangeRebuildsBucket(t *testing.T) {
	settings := &fakeRateSettings{enabled: true, rps: 1000, burst: 1000}
	l := NewGlobalRateLimiter(settings)
	t.Cleanup(l.Close)

	// Create a bucket with the wide-open policy.
	if rr := doRequest(l, "198.18.0.1:1234", ""); rr.Code != http.StatusOK {
		t.Fatal("initial request failed")
	}
	// Tighten the policy.
	settings.rps, settings.burst = 100, 1
	// The existing bucket must be rebuilt: first request 200, second 429.
	if rr := doRequest(l, "198.18.0.1:1234", ""); rr.Code != http.StatusOK {
		t.Fatalf("first request after policy change: got %d, want 200", rr.Code)
	}
	if rr := doRequest(l, "198.18.0.1:1234", ""); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second request after policy change: got %d, want 429", rr.Code)
	}
}
