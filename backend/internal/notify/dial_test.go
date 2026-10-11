package notify

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBlockedDestination(t *testing.T) {
	blocked := []string{"0.0.0.0", "0.1.2.3", "198.18.0.1", "198.19.255.255", "240.0.0.1", "255.255.255.255",
		"64:ff9b::7f00:1", "64:ff9b::a00:1", "64:ff9b:1::1", "::", "::ffff:240.0.0.1"}
	for _, s := range blocked {
		if !blockedDestination(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	// LAN and loopback notification targets (ntfy, Gotify) stay reachable.
	allowed := []string{"192.168.1.10", "10.0.0.5", "127.0.0.1", "8.8.8.8", "198.17.255.255", "2606:4700::1111"}
	for _, s := range allowed {
		if blockedDestination(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestSafeClientRefusesBlockedLiteralAndAllowsLoopback(t *testing.T) {
	c := newSafeClient(2 * time.Second)
	if resp, err := c.Get("http://240.0.0.1:9/"); err == nil {
		resp.Body.Close()
		t.Fatal("dial to 240.0.0.1 must be refused")
	}
	if resp, err := c.Get("http://[64:ff9b::7f00:1]:9/"); err == nil {
		resp.Body.Close()
		t.Fatal("dial to a NAT64 address must be refused")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("loopback webhook target must stay reachable: %v", err)
	}
	resp.Body.Close()
}
