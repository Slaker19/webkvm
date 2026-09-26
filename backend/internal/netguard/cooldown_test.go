package netguard

import (
	"sync"
	"testing"
	"time"
)

func TestCooldown_TripsAfterThreshold(t *testing.T) {
	c := NewCooldown(5, time.Minute)

	for i := 0; i < 4; i++ {
		if c.RecordFailure("192.168.1.171") {
			t.Fatalf("tripped early at failure %d", i+1)
		}
		if c.Blocked("192.168.1.171") {
			t.Fatalf("blocked before threshold at failure %d", i+1)
		}
	}
	if !c.RecordFailure("192.168.1.171") {
		t.Fatal("did not trip at the threshold")
	}
	if !c.Blocked("192.168.1.171") {
		t.Fatal("not blocked after tripping")
	}
	// Other peers must be unaffected.
	if c.Blocked("192.168.1.50") {
		t.Error("an unrelated IP was blocked")
	}
}

// A well-behaved client never fails a handshake, so it must never be
// throttled no matter how many connections it opens.
func TestCooldown_NeverBlocksHealthyClient(t *testing.T) {
	c := NewCooldown(5, time.Minute)
	for i := 0; i < 1000; i++ {
		if c.Blocked("10.0.0.5") {
			t.Fatal("a client that never failed a handshake was blocked")
		}
	}
}

// The block must lift on its own: no operator action, no permanent ban.
func TestCooldown_ExpiresAutomatically(t *testing.T) {
	c := NewCooldown(2, 50*time.Millisecond)
	now := time.Now()
	c.now = func() time.Time { return now }

	c.RecordFailure("1.2.3.4")
	c.RecordFailure("1.2.3.4")
	if !c.Blocked("1.2.3.4") {
		t.Fatal("not blocked after threshold")
	}

	now = now.Add(60 * time.Millisecond)
	if c.Blocked("1.2.3.4") {
		t.Error("still blocked after the cooldown expired")
	}
}

// Sporadic failures spread over time must not accumulate into a block.
func TestCooldown_FailureCountDecays(t *testing.T) {
	c := NewCooldown(3, time.Minute)
	now := time.Now()
	c.now = func() time.Time { return now }

	c.RecordFailure("5.5.5.5")
	c.RecordFailure("5.5.5.5")
	// Jump past the accumulation window: the count should reset.
	now = now.Add(failureWindow + time.Second)
	c.RecordFailure("5.5.5.5")

	if c.Blocked("5.5.5.5") {
		t.Error("blocked by failures spread across separate windows")
	}
}

// Observe must extract the peer IP from the exact message net/http emits,
// and must ignore anything else.
func TestCooldown_ObserveParsesHandshakeErrors(t *testing.T) {
	cases := []struct {
		msg      string
		wantIP   string
		wantSeen bool
	}{
		{"http: TLS handshake error from 192.168.1.171:34122: remote error: tls: unknown certificate", "192.168.1.171", true},
		{"http: TLS handshake error from [fe80::1]:443: remote error: tls: bad certificate", "fe80::1", true},
		{"http: superfluous response.WriteHeader call", "", false},
		{"http: panic serving 192.168.1.9:1234: boom", "", false},
	}
	for _, tc := range cases {
		c := NewCooldown(1, time.Minute)
		c.Observe(tc.msg)
		if got := c.Blocked(tc.wantIP); got != tc.wantSeen {
			t.Errorf("Observe(%q): blocked(%q)=%v, want %v", tc.msg, tc.wantIP, got, tc.wantSeen)
		}
		if !tc.wantSeen && c.Size() != 0 {
			t.Errorf("Observe(%q) tracked an IP it should have ignored", tc.msg)
		}
	}
}

// A panic message contains an "IP:port" too; it must never be mistaken for
// a handshake failure, or an unrelated bug could lock out a real user.
func TestCooldown_IgnoresNonHandshakeMessagesWithAddresses(t *testing.T) {
	c := NewCooldown(1, time.Minute)
	for i := 0; i < 50; i++ {
		c.Observe("http: panic serving 192.168.1.9:1234: runtime error")
	}
	if c.Blocked("192.168.1.9") {
		t.Error("a panic message blocked a client")
	}
}

func TestCooldown_OnTripFiresOnce(t *testing.T) {
	c := NewCooldown(2, time.Minute)
	var trips int
	var mu sync.Mutex
	c.OnTrip = func(string, int, time.Time) {
		mu.Lock()
		trips++
		mu.Unlock()
	}
	for i := 0; i < 100; i++ {
		c.RecordFailure("7.7.7.7")
	}
	mu.Lock()
	defer mu.Unlock()
	if trips != 1 {
		t.Errorf("OnTrip fired %d times, want 1 (per-event logging would re-amplify)", trips)
	}
}

// The tracking map must not grow without bound across many short-lived
// peers, or it becomes a memory leak of its own.
func TestCooldown_SweepBoundsMemory(t *testing.T) {
	c := NewCooldown(100, time.Second)
	now := time.Now()
	c.now = func() time.Time { return now }

	for i := 0; i < 5000; i++ {
		c.RecordFailure(randIP(i))
	}
	if c.Size() != 5000 {
		t.Fatalf("Size=%d, want 5000", c.Size())
	}
	now = now.Add(failureWindow + 2*time.Second)
	c.Sweep()
	if c.Size() != 0 {
		t.Errorf("Size=%d after sweep, want 0", c.Size())
	}
}

func TestCooldown_ConcurrentAccessIsSafe(t *testing.T) {
	c := NewCooldown(10, time.Second)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				c.RecordFailure(randIP(i % 20))
				c.Blocked(randIP(i % 20))
				if i%100 == 0 {
					c.Sweep()
				}
			}
		}(g)
	}
	wg.Wait()
}

func randIP(i int) string {
	return "10." + itoa(i/65536%256) + "." + itoa(i/256%256) + "." + itoa(i%256)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [3]byte
	p := len(b)
	for n > 0 {
		p--
		b[p] = byte('0' + n%10)
		n /= 10
	}
	return string(b[p:])
}
