package logging

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP_NoProxyTrust_NoHeader(t *testing.T) {
	t.Setenv("WEBKVM_TRUST_PROXY", "")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.1:54321"
	got := ClientIP(req)
	if got != "192.0.2.1" {
		t.Fatalf("expected 192.0.2.1, got %q", got)
	}
}

func TestClientIP_NoProxyTrust_HeaderIgnored(t *testing.T) {
	t.Setenv("WEBKVM_TRUST_PROXY", "0")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:443"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	got := ClientIP(req)
	if got != "10.0.0.1" {
		t.Fatalf("without trust, X-Forwarded-For should be ignored; got %q", got)
	}
}

func TestClientIP_ProxyTrust_SingleIP(t *testing.T) {
	t.Setenv("WEBKVM_TRUST_PROXY", "1")
	t.Setenv("WEBKVM_TRUSTED_PROXY_CIDRS", "10.0.0.0/8")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	got := ClientIP(req)
	if got != "203.0.113.7" {
		t.Fatalf("with trust, X-Forwarded-For should be honored; got %q", got)
	}
}

func TestClientIP_ProxyTrust_ChainTakesLeftmost(t *testing.T) {
	t.Setenv("WEBKVM_TRUST_PROXY", "1")
	t.Setenv("WEBKVM_TRUSTED_PROXY_CIDRS", "10.0.0.0/8")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 198.51.100.1, 10.0.0.1")
	got := ClientIP(req)
	if got != "203.0.113.7" {
		t.Fatalf("chain: should return leftmost (original client); got %q", got)
	}
}

func TestClientIP_ProxyTrust_NoHeaderFallsBack(t *testing.T) {
	t.Setenv("WEBKVM_TRUST_PROXY", "1")
	t.Setenv("WEBKVM_TRUSTED_PROXY_CIDRS", "10.0.0.0/8")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:443"
	got := ClientIP(req)
	if got != "10.0.0.1" {
		t.Fatalf("with trust but no header, should fall back to peer; got %q", got)
	}
}

func TestClientIP_ProxyTrust_GarbageHeaderFallsBack(t *testing.T) {
	t.Setenv("WEBKVM_TRUST_PROXY", "1")
	t.Setenv("WEBKVM_TRUSTED_PROXY_CIDRS", "10.0.0.0/8")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:443"
	req.Header.Set("X-Forwarded-For", "   ")
	got := ClientIP(req)
	if got != "10.0.0.1" {
		t.Fatalf("empty X-Forwarded-For should fall back to peer; got %q", got)
	}
}

func TestClientIP_ProxyTrust_UntrustedPeerHeaderIgnored(t *testing.T) {
	// This is the fix under test: trust_proxy=1 alone must NOT let an
	// arbitrary peer spoof X-Forwarded-For. Without a matching
	// WEBKVM_TRUSTED_PROXY_CIDRS entry (and the peer not being
	// loopback), the header must be ignored.
	t.Setenv("WEBKVM_TRUST_PROXY", "1")
	t.Setenv("WEBKVM_TRUSTED_PROXY_CIDRS", "")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.50:443"
	req.Header.Set("X-Forwarded-For", "9.9.9.9")
	got := ClientIP(req)
	if got != "203.0.113.50" {
		t.Fatalf("untrusted peer's X-Forwarded-For must be ignored; got %q", got)
	}
}

func TestClientIP_ProxyTrust_LoopbackPeerAlwaysTrusted(t *testing.T) {
	t.Setenv("WEBKVM_TRUST_PROXY", "1")
	t.Setenv("WEBKVM_TRUSTED_PROXY_CIDRS", "")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	got := ClientIP(req)
	if got != "203.0.113.7" {
		t.Fatalf("loopback peer should always be a trusted proxy source; got %q", got)
	}
}

func TestClientIP_NoPortInRemoteAddr(t *testing.T) {
	t.Setenv("WEBKVM_TRUST_PROXY", "")
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.0.2.1" // no port
	got := ClientIP(req)
	if got != "192.0.2.1" {
		t.Fatalf("RemoteAddr without port should pass through; got %q", got)
	}
}

func TestTrustProxy(t *testing.T) {
	tests := []struct {
		env  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"1", true},
		{"true", true},
		{"false", false},
		{"garbage", false},
		{"yes", false},  // strconv.ParseBool doesn't accept "yes"
		{"True", true},
		{"FALSE", false},
	}
	for _, tc := range tests {
		got := TrustProxyAt(t, tc.env)
		if got != tc.want {
			t.Errorf("TrustProxy() with env=%q: got %v, want %v", tc.env, got, tc.want)
		}
	}
}

// TrustProxyAt is a small helper so each subtest can set its own env.
func TrustProxyAt(t *testing.T, env string) bool {
	t.Helper()
	if env == "" {
		// Can't actually unset with t.Setenv; empty string is fine.
		t.Setenv("WEBKVM_TRUST_PROXY", "")
	} else {
		t.Setenv("WEBKVM_TRUST_PROXY", env)
	}
	return TrustProxy()
}
