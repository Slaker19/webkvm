package netguard

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"log"
	"math/big"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// End-to-end reproduction of the production incident: a client that
// refuses the server's self-signed certificate and immediately retries,
// with no backoff. Before this package, every attempt produced one
// ErrorLog line (~310 bytes), so the client's connection rate translated
// directly into disk writes. This test asserts that the wiring in
// cmd/server actually breaks that link on a real TLS listener.
func TestServerUnderHandshakeFlood(t *testing.T) {
	cert := selfSignedCert(t)

	var logLines int64
	aggregated := NewAggregatingWriter(
		writerFunc(func(p []byte) (int, error) { atomic.AddInt64(&logLines, 1); return len(p), nil }),
		func(string, int, time.Time, time.Time) {},
		time.Hour,
	)
	defer aggregated.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	limited := NewLimitListener(ln, DefaultPerIPLimit)

	srv := &http.Server{
		Handler:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}},
		ErrorLog:  log.New(aggregated, "", 0),
	}
	go srv.ServeTLS(limited, "", "")
	defer srv.Close()

	addr := ln.Addr().String()

	// Hammer the listener the way the real client did: connect, reject
	// the certificate, drop, repeat.
	const attempts = 400
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < attempts/8; j++ {
				c, err := tls.Dial("tcp", addr, &tls.Config{
					// InsecureSkipVerify stays false on purpose: we
					// WANT the handshake to fail, exactly as the
					// browser rejecting an untrusted cert does.
					ServerName: "webkvm.invalid",
				})
				if err == nil {
					c.Close()
				}
			}
		}()
	}
	wg.Wait()

	// Give the server a moment to finish logging the failures.
	time.Sleep(250 * time.Millisecond)

	got := atomic.LoadInt64(&logLines)
	if got == 0 {
		t.Fatal("no handshake errors logged at all; the test is not exercising the failure path")
	}
	// The whole point: log volume must be bounded by the number of
	// distinct peers, not by the number of attempts. Loopback is one
	// peer, so a handful of lines is expected, never hundreds.
	if got > 10 {
		t.Errorf("logged %d lines for %d failed handshakes; amplification is not bounded", got, attempts)
	}
	t.Logf("%d failed handshakes produced %d log lines", attempts, got)

	// The server must still serve legitimate traffic afterwards.
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("server unusable after flood: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status=%d after flood, want 204", resp.StatusCode)
	}

	// And the per-IP accounting must not have leaked: every flood
	// connection was closed, so slots have to come back.
	waitFor(t, func() bool { return limited.ActiveFor("127.0.0.1") <= 1 })
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "webkvm-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
