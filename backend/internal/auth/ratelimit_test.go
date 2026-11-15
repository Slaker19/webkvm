package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newReq(remoteAddr, user string) *http.Request {
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = remoteAddr
	return r
}

func TestLoginRateLimiter_FirstAttemptAllowed(t *testing.T) {
	rl := NewLoginRateLimiter()
	ok, _ := rl.Allow(newReq("1.1.1.1:1234", "alice"), "alice")
	if !ok {
		t.Error("first attempt should be allowed")
	}
}

func TestLoginRateLimiter_5FailsLockout(t *testing.T) {
	rl := NewLoginRateLimiter()
	req := newReq("1.1.1.1:1234", "alice")

	// 4 fails: still allowed (limit is 5)
	for i := 0; i < 4; i++ {
		ok, _ := rl.Allow(req, "alice")
		if !ok {
			t.Fatalf("attempt %d should be allowed", i+1)
		}
		rl.RecordFailure(req, "alice")
	}

	// 5th fail: still allowed (Allow checks bucket, RecordFailure
	// triggers lockout)
	ok, _ := rl.Allow(req, "alice")
	if !ok {
		t.Error("5th attempt should still be allowed (limit reached but not yet locked)")
	}
	rl.RecordFailure(req, "alice")

	// 6th attempt: locked.
	ok, retry := rl.Allow(req, "alice")
	if ok {
		t.Error("6th attempt should be locked out")
	}
	if retry < 14*time.Minute || retry > 15*time.Minute+time.Second {
		t.Errorf("retry = %v, want ~15m", retry)
	}
}

func TestLoginRateLimiter_DifferentUsersIndependent(t *testing.T) {
	rl := NewLoginRateLimiter()
	ip := "1.1.1.1:1234"

	// Lock alice out.
	for i := 0; i < 5; i++ {
		rl.RecordFailure(newReq(ip, "alice"), "alice")
	}
	okA, _ := rl.Allow(newReq(ip, "alice"), "alice")
	if okA {
		t.Error("alice should be locked")
	}
	// Bob on the same IP is still fine.
	okB, _ := rl.Allow(newReq(ip, "bob"), "bob")
	if !okB {
		t.Error("bob should not be affected by alice's lockout")
	}
}

func TestLoginRateLimiter_DifferentIPsIndependent(t *testing.T) {
	rl := NewLoginRateLimiter()

	// Lock alice from IP1.
	for i := 0; i < 5; i++ {
		rl.RecordFailure(newReq("1.1.1.1:1", "alice"), "alice")
	}
	ok1, _ := rl.Allow(newReq("1.1.1.1:1", "alice"), "alice")
	if ok1 {
		t.Error("alice from IP1 should be locked")
	}
	// Alice from IP2 is still fine.
	ok2, _ := rl.Allow(newReq("2.2.2.2:1", "alice"), "alice")
	if !ok2 {
		t.Error("alice from IP2 should not be affected by IP1 lockout")
	}
}

func TestLoginRateLimiter_RecordSuccessClears(t *testing.T) {
	rl := NewLoginRateLimiter()
	req := newReq("1.1.1.1:1234", "alice")

	for i := 0; i < 3; i++ {
		rl.RecordFailure(req, "alice")
	}
	rl.RecordSuccess(req, "alice")

	// After success, attempts reset.
	for i := 0; i < 4; i++ {
		ok, _ := rl.Allow(req, "alice")
		if !ok {
			t.Fatalf("attempt %d after success should be allowed", i+1)
		}
		rl.RecordFailure(req, "alice")
	}
}

func TestLoginRateLimiter_KeyForStripsPort(t *testing.T) {
	// The same IP with different ports should be bucketed together.
	rl := NewLoginRateLimiter()
	ip1 := newReq("1.1.1.1:1000", "alice")
	ip2 := newReq("1.1.1.1:2000", "alice")

	for i := 0; i < 4; i++ {
		rl.RecordFailure(ip1, "alice")
	}
	rl.RecordFailure(ip2, "alice")

	// Now ip2 should also be locked (5th failure across both ports).
	ok, _ := rl.Allow(ip2, "alice")
	if ok {
		t.Error("ip:2000 should be locked (5th failure across ports)")
	}
}

func TestLoginRateLimiter_KeyForIPv6(t *testing.T) {
	// IPv6 with brackets.
	rl := NewLoginRateLimiter()
	ip1 := newReq("[::1]:1000", "alice")
	k := rl.keyFor(ip1, "alice")
	want := "::1|alice"
	if k != want {
		t.Errorf("key = %q, want %q", k, want)
	}
}

func TestLoginRateLimiter_Concurrent(t *testing.T) {
	rl := NewLoginRateLimiter()

	const goroutines = 50
	const opsPerG = 20
	var allowed int64
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			req := newReq("1.1.1.1:1234", "alice")
			for j := 0; j < opsPerG; j++ {
				ok, _ := rl.Allow(req, "alice")
				if ok {
					atomic.AddInt64(&allowed, 1)
				}
				rl.RecordFailure(req, "alice")
			}
		}()
	}
	wg.Wait()

	// After lockout, no more should be allowed.
	ok, _ := rl.Allow(newReq("1.1.1.1:1234", "alice"), "alice")
	if ok {
		t.Error("after concurrent burst, alice should be locked")
	}
	t.Logf("allowed before lockout: %d", atomic.LoadInt64(&allowed))
}

func TestLoginRateLimiter_MiddlewarePassthrough(t *testing.T) {
	rl := NewLoginRateLimiter()
	called := false
	h := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest("POST", "/api/auth/login", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !called {
		t.Error("middleware did not call downstream")
	}
}

func TestWriteRateLimited_Headers(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteRateLimited(rr, 42*time.Second)
	if rr.Code != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := rr.Header().Get("Retry-After"); got != "42" {
		t.Errorf("Retry-After = %q, want 42", got)
	}
	if rr.Body.Len() == 0 {
		t.Error("body is empty")
	}
}

func TestLoginRateLimiter_LoopbackTrusted(t *testing.T) {
	rl := NewLoginRateLimiterWithEnv("")
	req := newReq("127.0.0.1:54321", "alice")

	// 6 fails from loopback should never lock the account out.
	for i := 0; i < 6; i++ {
		rl.RecordFailure(req, "alice")
	}
	ok, retry := rl.Allow(req, "alice")
	if !ok {
		t.Errorf("loopback should be trusted; got locked out (retry=%v)", retry)
	}
}

func TestLoginRateLimiter_LoopbackIPv6Trusted(t *testing.T) {
	rl := NewLoginRateLimiterWithEnv("")
	req := newReq("[::1]:54321", "alice")

	rl.RecordFailure(req, "alice")
	rl.RecordFailure(req, "alice")
	ok, _ := rl.Allow(req, "alice")
	if !ok {
		t.Error("loopback IPv6 should be trusted")
	}
}

func TestLoginRateLimiter_TrustedCIDRBypass(t *testing.T) {
	rl := NewLoginRateLimiterWithEnv("10.0.0.0/8,192.168.5.0/24")
	req := newReq("10.1.2.3:443", "alice")

	for i := 0; i < 6; i++ {
		rl.RecordFailure(req, "alice")
	}
	ok, _ := rl.Allow(req, "alice")
	if !ok {
		t.Error("IP in 10.0.0.0/8 should be trusted")
	}
}

func TestLoginRateLimiter_OutsideTrustedCIDRIsLimited(t *testing.T) {
	rl := NewLoginRateLimiterWithEnv("10.0.0.0/8")
	req := newReq("203.0.113.7:443", "alice")

	for i := 0; i < 5; i++ {
		rl.RecordFailure(req, "alice")
	}
	ok, _ := rl.Allow(req, "alice")
	if ok {
		t.Error("IP outside 10.0.0.0/8 should hit the limiter after 5 fails")
	}
}

func TestLoginRateLimiter_InvalidCIDRSkipped(t *testing.T) {
	rl := NewLoginRateLimiterWithEnv("not-a-cidr,10.0.0.0/8,,garbage")
	req := newReq("10.0.0.5:80", "alice")

	rl.RecordFailure(req, "alice")
	rl.RecordFailure(req, "alice")
	ok, _ := rl.Allow(req, "alice")
	if !ok {
		t.Error("valid CIDR in a list with garbage entries should still apply")
	}
}

func TestParseTrustedCIDRs(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"10.0.0.0/8", 1},
		{"10.0.0.0/8,192.168.1.0/24", 2},
		{"  10.0.0.0/8  ,  192.168.1.0/24  ", 2},
		{"not-a-cidr", 0},
		{"10.0.0.0/8,bogus,192.168.1.0/24", 2},
		{",,,", 0},
	}
	for _, tc := range tests {
		got := len(parseTrustedCIDRs(tc.in))
		if got != tc.want {
			t.Errorf("parseTrustedCIDRs(%q) returned %d CIDRs, want %d", tc.in, got, tc.want)
		}
	}
}

func TestLoginRateLimiter_RecordSuccessNoopForTrusted(t *testing.T) {
	rl := NewLoginRateLimiterWithEnv("")
	trustedReq := newReq("127.0.0.1:1234", "alice")
	rl.RecordFailure(trustedReq, "alice")
	// Loopback never got recorded, so success is a no-op (no panic).
	rl.RecordSuccess(trustedReq, "alice")
	// And Allow still says OK.
	ok, _ := rl.Allow(trustedReq, "alice")
	if !ok {
		t.Error("loopback should still be allowed after a no-op success")
	}
}

type testTrustedSettings struct {
	trustProxy bool
	cidrs      []string
}

func (s testTrustedSettings) GetBool(string) bool     { return s.trustProxy }
func (s testTrustedSettings) GetList(string) []string { return s.cidrs }

// Multi-node: NPM connects from 192.168.1.8 and forwards the real client in
// X-Forwarded-For. The resolver must return the client, not the proxy.
func TestResolveClientIPUsesSettingsAndXFF(t *testing.T) {
	settings := testTrustedSettings{
		trustProxy: true,
		cidrs:      []string{"192.168.1.8/32"},
	}
	r := httptest.NewRequest("POST", "/api/auth/login", nil)
	r.RemoteAddr = "192.168.1.8:443"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	ip, fromHeader := ResolveClientIP(r, settings)
	if ip == nil || ip.String() != "203.0.113.9" || !fromHeader {
		t.Fatalf("ResolveClientIP = (%v,%v), want (203.0.113.9,true)", ip, fromHeader)
	}
}

func TestRateLimiterLockoutPersistsImmediately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratelimit.json")
	rl := NewLoginRateLimiter()
	if err := rl.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	r := newReq("203.0.113.9:5555", "alice")
	for i := 0; i < 5; i++ {
		rl.RecordFailure(r, "alice")
	}
	if ok, _ := rl.Allow(r, "alice"); ok {
		t.Fatal("expected the in-process lockout")
	}

	// No Flush: a lockout bypasses the coalescing window on purpose.
	rl2 := NewLoginRateLimiter()
	if err := rl2.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	ok, retry := rl2.Allow(r, "alice")
	if ok {
		t.Fatal("lockout did not survive a restart")
	}
	if retry <= 0 {
		t.Fatalf("retry-after = %v, want > 0", retry)
	}
}

func TestRateLimiterCountersSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratelimit.json")
	rl := NewLoginRateLimiter()
	if err := rl.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	r := newReq("203.0.113.10:5555", "bob")
	for i := 0; i < 3; i++ {
		rl.RecordFailure(r, "bob")
	}
	if err := rl.Flush(); err != nil {
		t.Fatal(err)
	}

	rl2 := NewLoginRateLimiter()
	if err := rl2.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	if ok, _ := rl2.Allow(r, "bob"); !ok {
		t.Fatal("3 restored failures must not lock out yet")
	}
	for i := 0; i < 2; i++ {
		rl2.RecordFailure(r, "bob")
	}
	if ok, _ := rl2.Allow(r, "bob"); ok {
		t.Fatal("counter did not survive the restart (expected 5 total)")
	}
}

func TestRateLimiterDropsStaleCountersOnLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ratelimit.json")
	now := time.Now()
	f := rateLimitFile{Version: 1, Buckets: []rateLimitEntry{
		// Window long gone: drop it.
		{Key: "198.51.100.1|bob", Failures: 4, FirstFail: now.Add(-time.Hour)},
		// Active lockout outlives the counting window: keep it.
		{Key: "198.51.100.2|bob", Failures: 5, FirstFail: now.Add(-time.Hour), LockedTill: now.Add(10 * time.Minute)},
	}}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	rl := NewLoginRateLimiter()
	if err := rl.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	if ok, _ := rl.Allow(newReq("198.51.100.1:1234", "bob"), "bob"); !ok {
		t.Fatal("stale counter must be dropped on load")
	}
	if ok, _ := rl.Allow(newReq("198.51.100.2:1234", "bob"), "bob"); ok {
		t.Fatal("active lockout must be restored on load")
	}
}

func TestRateLimiterPersistIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ratelimit.json")
	rl := NewLoginRateLimiter()
	if err := rl.EnablePersistence(path); err != nil {
		t.Fatal(err)
	}
	r := newReq("203.0.113.11:5555", "carol")
	rl.RecordFailure(r, "carol")
	for i := 0; i < 4; i++ {
		rl.RecordFailure(r, "carol")
	}
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal("temp file must not be left behind after the rename")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("ratelimit.json mode = %o, want 600", perm)
	}
}
