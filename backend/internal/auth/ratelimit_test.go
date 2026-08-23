package auth

import (
	"net/http"
	"net/http/httptest"
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
	ip1 := newReq("[::1]:1000", "alice")
	k := keyFor(ip1, "alice")
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
