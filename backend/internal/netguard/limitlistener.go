package netguard

import (
	"net"
	"sync"
)

// DefaultPerIPLimit is the maximum number of simultaneous connections a
// single remote IP may hold against the server.
//
// The number is deliberately generous for real usage: a browser opens at
// most ~6 connections per origin, plus a persistent SSE stream, plus any
// VNC/serial WebSockets for consoles opened side by side. 64 leaves a
// wide margin for several tabs and a multi-view page on the same machine,
// while still bounding a runaway client to a tiny fraction of the 65536
// file descriptor limit.
const DefaultPerIPLimit = 64

// LimitListener wraps a net.Listener and caps concurrent connections per
// remote IP. Connections beyond the cap are accepted and closed
// immediately rather than left pending, so the client gets a fast,
// unambiguous failure and the kernel backlog never fills.
//
// The limit is per IP, not global: one misbehaving host cannot deny
// service to every other user, which is what a single global cap would
// allow.
type LimitListener struct {
	net.Listener
	limit int

	mu     sync.Mutex
	active map[string]int
	// OnReject, when set, is called each time a connection is dropped
	// for exceeding the limit. Used for observability.
	OnReject func(ip string, active int)

	// Cooldown, when set, refuses connections from IPs that are failing
	// TLS handshakes in a tight loop. This is checked before the
	// connection is handed to the server, so the expensive handshake is
	// never performed — that is where the CPU saving comes from.
	Cooldown *Cooldown
	// OnCooldownDrop fires for each connection refused due to cooldown.
	OnCooldownDrop func(ip string)

	// Exempt, when set, reports whether a source IP is excluded from the
	// per-IP cap and from Cooldown (loopback and trusted reverse proxies: behind a proxy
	// every user shares its IP, so long-lived SSE/console streams would
	// otherwise fill the slots and the proxy would answer 502).
	Exempt func(ip string) bool
}

// NewLimitListener wraps l with a per-IP concurrency cap. A limit <= 0
// means DefaultPerIPLimit.
func NewLimitListener(l net.Listener, limit int) *LimitListener {
	if limit <= 0 {
		limit = DefaultPerIPLimit
	}
	return &LimitListener{Listener: l, limit: limit, active: map[string]int{}}
}

// Accept returns the next connection, skipping any that would exceed the
// per-IP limit. It keeps looping instead of returning an error, because
// returning an error from Accept would make http.Server tear down the
// whole listener over one abusive peer.
func (l *LimitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ip := hostOf(c.RemoteAddr())

		// Exempt sources (loopback, trusted reverse proxies) skip both
		// the per-IP cap and the handshake cooldown. Behind a proxy every
		// user shares its IP, so a burst of failed handshakes from one
		// client (or a local health check) would otherwise lock the whole
		// proxy — and with it every user — out of the server.
		if l.Exempt != nil && l.Exempt(ip) {
			return c, nil
		}

		// Refuse before the handshake. Closing here costs one syscall,
		// versus a full RSA signature and key exchange if we let the
		// connection reach the TLS layer.
		if l.Cooldown != nil && l.Cooldown.Blocked(ip) {
			_ = c.Close()
			if l.OnCooldownDrop != nil {
				l.OnCooldownDrop(ip)
			}
			continue
		}

		l.mu.Lock()
		n := l.active[ip]
		if n >= l.limit {
			l.mu.Unlock()
			if l.OnReject != nil {
				l.OnReject(ip, n)
			}
			_ = c.Close()
			continue
		}
		l.active[ip] = n + 1
		l.mu.Unlock()

		return &trackedConn{Conn: c, parent: l, ip: ip}, nil
	}
}

// release decrements the counter for ip, deleting the entry when it hits
// zero so the map cannot grow without bound across many short-lived peers.
func (l *LimitListener) release(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := l.active[ip]; n <= 1 {
		delete(l.active, ip)
	} else {
		l.active[ip] = n - 1
	}
}

// ActiveFor reports the number of tracked connections for an IP. Exposed
// for tests and diagnostics.
func (l *LimitListener) ActiveFor(ip string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active[ip]
}

// trackedConn decrements the per-IP counter exactly once, on first Close.
type trackedConn struct {
	net.Conn
	parent *LimitListener
	ip     string
	once   sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	// Release after the close so a double Close from net/http cannot
	// decrement twice and let the limit drift upward.
	c.once.Do(func() { c.parent.release(c.ip) })
	return err
}

func hostOf(a net.Addr) string {
	if a == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return a.String()
	}
	return host
}
