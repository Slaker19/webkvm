package netguard

import (
	"regexp"
	"sync"
	"time"
)

// A TLS handshake is cheap for the client and expensive for the server:
// the server performs the certificate signature and key exchange before it
// can know the client will reject the result. A peer that refuses our
// certificate and reconnects without backoff therefore imposes an
// asymmetric CPU cost — measured in production at ~25% of a core for a
// single LAN client looping at ~83 handshakes/second.
//
// Suppressing the log output bounds the disk cost but not the CPU cost,
// because the crypto has already happened by the time anything is logged.
// The only way to stop paying is to refuse the connection before the
// handshake, at accept() time, which costs a close() and nothing else.
//
// Cooldown implements that: it counts handshake failures per IP and, past
// a threshold, drops new connections from that IP for a short window. A
// correctly configured client never fails a handshake, so it can never be
// throttled. A looping client is throttled within a second and re-admitted
// automatically once it stops, with no operator action and no permanent
// ban.

const (
	// DefaultFailureThreshold is how many handshake failures an IP may
	// accumulate within one window before it is put on cooldown.
	// Legitimate clients produce zero; a handful of retries after a
	// certificate change is tolerated.
	DefaultFailureThreshold = 20

	// DefaultCooldown is how long connections from a failing IP are
	// refused. Short enough that a user who fixes their trust store is
	// back within seconds, long enough to collapse a tight retry loop.
	DefaultCooldown = 10 * time.Second

	// failureWindow is the period over which failures accumulate before
	// the counter decays. Without decay, an IP that failed occasionally
	// over days would eventually trip the threshold.
	failureWindow = 30 * time.Second
)

// handshakeErrIP extracts the peer IP from the message net/http emits for
// a failed TLS handshake:
//
//	http: TLS handshake error from 192.168.1.171:34122: remote error: ...
//
// Matching on this string is unavoidable: net/http exposes handshake
// failures through ErrorLog only — ConnState reports StateNew then
// StateClosed with no indication of why, and TLSConfig callbacks do not
// fire for a client-side rejection. The pattern is anchored to the fixed
// prefix so an unrelated message can never be misread as a peer address.
var handshakeErrIP = regexp.MustCompile(`^http: TLS handshake error from (\[?[0-9a-fA-F:.]+\]?):\d+:`)

// Cooldown tracks per-IP handshake failures and reports which IPs should
// currently be refused. It is safe for concurrent use.
type Cooldown struct {
	mu        sync.Mutex
	state     map[string]*failState
	threshold int
	penalty   time.Duration
	window    time.Duration
	now       func() time.Time

	// OnTrip fires the first time an IP enters cooldown, so the event
	// can be logged exactly once instead of per refused connection.
	OnTrip func(ip string, failures int, until time.Time)
}

type failState struct {
	failures  int
	windowEnd time.Time
	until     time.Time // zero when not on cooldown
	tripped   bool
}

// NewCooldown returns a Cooldown with the given threshold and penalty.
// Non-positive values fall back to the package defaults.
func NewCooldown(threshold int, penalty time.Duration) *Cooldown {
	if threshold <= 0 {
		threshold = DefaultFailureThreshold
	}
	if penalty <= 0 {
		penalty = DefaultCooldown
	}
	return &Cooldown{
		state:     map[string]*failState{},
		threshold: threshold,
		penalty:   penalty,
		window:    failureWindow,
		now:       time.Now,
	}
}

// RecordFailure notes one failed handshake for ip and reports whether that
// failure pushed the IP into cooldown.
func (c *Cooldown) RecordFailure(ip string) bool {
	if ip == "" {
		return false
	}
	now := c.now()

	c.mu.Lock()
	st, ok := c.state[ip]
	if !ok {
		st = &failState{windowEnd: now.Add(c.window)}
		c.state[ip] = st
	}
	// Decay: a new window resets the count so sporadic failures never
	// accumulate into a block.
	if now.After(st.windowEnd) {
		st.failures = 0
		st.windowEnd = now.Add(c.window)
		st.tripped = false
	}
	st.failures++

	var tripped bool
	var until time.Time
	if st.failures >= c.threshold {
		st.until = now.Add(c.penalty)
		until = st.until
		if !st.tripped {
			st.tripped = true
			tripped = true
		}
	}
	failures := st.failures
	c.mu.Unlock()

	if tripped && c.OnTrip != nil {
		c.OnTrip(ip, failures, until)
	}
	return tripped
}

// Blocked reports whether connections from ip should currently be refused
// before the TLS handshake.
func (c *Cooldown) Blocked(ip string) bool {
	if ip == "" {
		return false
	}
	now := c.now()

	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.state[ip]
	if !ok {
		return false
	}
	if st.until.IsZero() || now.After(st.until) {
		return false
	}
	return true
}

// Observe inspects one ErrorLog message and records a failure when it is a
// TLS handshake error. Messages of any other kind are ignored, so an
// unrelated server error can never throttle a client.
func (c *Cooldown) Observe(msg string) {
	if m := handshakeErrIP.FindStringSubmatch(msg); m != nil {
		ip := m[1]
		// Strip brackets from IPv6 literals so the key matches what
		// net.SplitHostPort returns at accept time.
		if len(ip) > 1 && ip[0] == '[' && ip[len(ip)-1] == ']' {
			ip = ip[1 : len(ip)-1]
		}
		c.RecordFailure(ip)
	}
}

// Sweep drops entries whose cooldown and failure window have both expired,
// keeping the map bounded when many distinct peers misbehave over time.
func (c *Cooldown) Sweep() {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for ip, st := range c.state {
		if now.After(st.windowEnd) && (st.until.IsZero() || now.After(st.until)) {
			delete(c.state, ip)
		}
	}
}

// Size reports how many IPs are currently tracked. Exposed for tests and
// diagnostics.
func (c *Cooldown) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.state)
}
