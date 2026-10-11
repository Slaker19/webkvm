package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// #16: an X-Forwarded-For value claiming to be loopback must neither be
// used as the client identity nor grant the trusted exemption.
func TestLoginRateLimiter_XFFLoopbackSpoofNotExempt(t *testing.T) {
	s := &fakeRateSettings{trustProxy: true}
	l := NewLoginRateLimiterWithSettings(s)

	// Local reverse proxy (loopback peer) forwarding a remote attacker that
	// prepended a forged 127.0.0.1.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-For", "127.0.0.1, 203.0.113.9")
	if got := l.clientIP(req); got.String() != "203.0.113.9" {
		t.Fatalf("client = %v, want 203.0.113.9", got)
	}
	if l.isTrusted(req) {
		t.Fatal("XFF-derived IP must not be trusted")
	}

	// Entire chain forged as loopback: falls back to the real peer, which
	// is the (trusted) local proxy itself, but is not XFF-derived.
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	if l.isTrusted(req) {
		t.Fatal("XFF-derived remote IP must not be trusted")
	}

	// Direct loopback peer without header keeps the exemption.
	direct := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	direct.RemoteAddr = "127.0.0.1:5555"
	if !l.isTrusted(direct) {
		t.Fatal("direct loopback peer should stay exempt")
	}
}

// Buckets are keyed on the proxy-observed client, not a forged leftmost IP.
func TestGlobalRateLimiter_XFFSpoofCannotRotateBuckets(t *testing.T) {
	l := NewGlobalRateLimiter(&fakeRateSettings{enabled: true, rps: 100, burst: 1, trustProxy: true})
	t.Cleanup(l.Close)
	codes := []int{}
	for _, forged := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3", "4.4.4.4"} {
		req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", forged+", 203.0.113.9")
		rr := httptest.NewRecorder()
		l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rr, req)
		codes = append(codes, rr.Code)
	}
	if codes[3] != http.StatusTooManyRequests {
		t.Fatalf("forged leftmost IPs must not rotate buckets: %v", codes)
	}
}
