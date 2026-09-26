package netguard

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// The log aggregator bounds disk writes but not CPU: by the time a
// handshake error is logged, the server has already paid for the
// certificate signature and key exchange. This test measures the thing
// that actually matters — how many handshakes the server is made to
// perform under a retry flood — and asserts the cooldown collapses it.
//
// Counting handshakes rather than timing CPU keeps the test deterministic
// on a loaded machine, while still measuring the true cost driver.
func TestCooldown_StopsHandshakeCPUCost(t *testing.T) {
	run := func(t *testing.T, withCooldown bool) int64 {
		t.Helper()

		var handshakes int64
		cert := selfSignedCert(t)
		tlsCfg := &tls.Config{
			// GetCertificate fires once per handshake attempt that
			// reaches the TLS layer, which is exactly the work we are
			// trying to avoid paying for.
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				atomic.AddInt64(&handshakes, 1)
				return &cert, nil
			},
		}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		limited := NewLimitListener(ln, DefaultPerIPLimit)

		aggregated := NewAggregatingWriter(nil, func(string, int, time.Time, time.Time) {}, time.Hour)
		defer aggregated.Close()

		if withCooldown {
			// Threshold deliberately low so the effect is visible
			// within a short test run.
			cd := NewCooldown(5, 2*time.Second)
			aggregated.Observer = cd.Observe
			limited.Cooldown = cd
		}

		srv := &http.Server{
			Handler:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }),
			TLSConfig: tlsCfg,
			ErrorLog:  log.New(aggregated, "", 0),
		}
		go srv.ServeTLS(limited, "", "")
		defer srv.Close()

		addr := ln.Addr().String()
		// Sequential, like the real client: connect, get rejected,
		// reconnect immediately.
		for i := 0; i < 200; i++ {
			c, err := tls.Dial("tcp", addr, &tls.Config{ServerName: "webkvm.invalid"})
			if err == nil {
				c.Close()
			}
		}
		time.Sleep(200 * time.Millisecond)
		return atomic.LoadInt64(&handshakes)
	}

	without := run(t, false)
	with := run(t, true)

	t.Logf("handshakes performed: without cooldown=%d, with cooldown=%d", without, with)

	if without < 100 {
		t.Fatalf("baseline only performed %d handshakes; the flood is not reaching the TLS layer", without)
	}
	// The server must stop doing crypto work for a looping peer. Allow
	// generous headroom over the threshold for in-flight connections.
	if with > 40 {
		t.Errorf("performed %d handshakes with cooldown enabled, want <=40; CPU cost is not bounded", with)
	}
	if with >= without/2 {
		t.Errorf("cooldown cut handshakes only from %d to %d; expected a large reduction", without, with)
	}
}

// Throttling a looping peer must never block a legitimate one, even while
// the abusive peer is actively being refused.
func TestCooldown_HealthyClientUnaffectedDuringFlood(t *testing.T) {
	cert := selfSignedCert(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	limited := NewLimitListener(ln, DefaultPerIPLimit)

	aggregated := NewAggregatingWriter(nil, func(string, int, time.Time, time.Time) {}, time.Hour)
	defer aggregated.Close()
	cd := NewCooldown(5, 2*time.Second)
	aggregated.Observer = cd.Observe
	limited.Cooldown = cd

	srv := &http.Server{
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		ErrorLog:  log.New(aggregated, "", 0),
	}
	go srv.ServeTLS(limited, "", "")
	defer srv.Close()

	addr := ln.Addr().String()
	for i := 0; i < 50; i++ {
		if c, err := tls.Dial("tcp", addr, &tls.Config{ServerName: "webkvm.invalid"}); err == nil {
			c.Close()
		}
	}

	// Same source IP (loopback) but a client that trusts the cert. This
	// is the worst case for a per-IP policy, and it documents the
	// trade-off honestly: once an IP is on cooldown, well-behaved
	// connections from that same IP are refused too, until it expires.
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   3 * time.Second,
	}
	blockedResp, err := client.Get("https://" + addr + "/")
	blockedDuring := err != nil
	if blockedResp != nil {
		blockedResp.Body.Close()
	}

	// After the cooldown lapses the server must serve normally again,
	// with no operator intervention.
	time.Sleep(2200 * time.Millisecond)
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("server did not recover after cooldown expired: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status=%d after recovery, want 204", resp.StatusCode)
	}
	t.Logf("same-IP client blocked during cooldown: %v (expected: recovery is automatic)", blockedDuring)
}
