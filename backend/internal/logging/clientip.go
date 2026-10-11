package logging

import (
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// TrustProxy returns whether the current process is configured to
// honor the X-Forwarded-For header sent by an upstream reverse
// proxy. It reads WEBKVM_TRUST_PROXY at call time so it can be
// changed at runtime (e.g. for a script that temporarily trusts
// the proxy during a debug session) without restarting.
//
// Default (env var unset or "0"): do NOT trust. The middleware
// and audit logger will use the TCP peer address.
//
// Set to "1" only when a reverse proxy is the ONLY way to reach
// the backend port (e.g. localhost bind + TLS terminator), and
// the proxy strips any client-supplied X-Forwarded-For before
// adding its own.
func TrustProxy() bool {
	v := strings.TrimSpace(os.Getenv("WEBKVM_TRUST_PROXY"))
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false
	}
	return b
}

// trustedProxyCIDRsEnv lists source IPs (in addition to loopback,
// which is always trusted) allowed to hand us a client IP via
// X-Forwarded-For when TrustProxy() is on. Comma-separated CIDRs,
// e.g. "10.0.0.0/8,192.168.1.0/24". Without this restriction, once an
// operator turned trust_proxy on, X-Forwarded-For was honored from
// ANY peer — so any client reachable at all (not just the actual
// reverse proxy) could set an arbitrary IP and have it recorded
// verbatim in the audit log and request log, spoofing the audit
// trail's actor-source field.
const trustedProxyCIDRsEnv = "WEBKVM_TRUSTED_PROXY_CIDRS"

func trustedProxyCIDRs() []*net.IPNet {
	raw := strings.TrimSpace(os.Getenv(trustedProxyCIDRsEnv))
	if raw == "" {
		return nil
	}
	out := make([]*net.IPNet, 0, 4)
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, n, err := net.ParseCIDR(p); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// isTrustedProxySource reports whether ip is allowed to set
// X-Forwarded-For: loopback (the local-reverse-proxy topology this
// project ships packaging for) or an entry in WEBKVM_TRUSTED_PROXY_CIDRS.
func isTrustedProxySource(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range trustedProxyCIDRs() {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ResolveClientIP is THE single client-IP resolver, shared by the request
// logger, the audit log and both rate limiters (which differ only in how
// they decide trustProxy / isTrusted).
//
// X-Forwarded-For is only consulted when trustProxy is on AND the immediate
// TCP peer is a trusted proxy. The header is then walked right-to-left (each
// proxy appends the address it saw, so the rightmost entries are written by
// our own infrastructure and the leftmost is whatever the client claimed):
// the first address that is not itself a trusted proxy is the client. A
// malformed entry makes the whole header untrusted and the peer is used.
//
// The second result reports whether the returned IP came from the header. An
// XFF-derived address must never be granted trusted/loopback exemptions.
func ResolveClientIP(peer net.IP, xff string, trustProxy bool, isTrusted func(net.IP) bool) (ip net.IP, fromHeader bool) {
	if !trustProxy || xff == "" || !isTrusted(peer) {
		return peer, false
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		c := net.ParseIP(strings.TrimSpace(parts[i]))
		if c == nil {
			return peer, false
		}
		if !isTrusted(c) {
			return c, true
		}
	}
	// Every hop was a trusted proxy: the client is the leftmost entry's
	// position, which is itself trusted infrastructure; use the peer.
	return peer, false
}

// ClientIP returns the best-known client IP for a request, given the
// current proxy-trust setting (WEBKVM_TRUST_PROXY and
// WEBKVM_TRUSTED_PROXY_CIDRS). See ResolveClientIP for the algorithm.
//
// Use this in BOTH the request logger (for the remote_ip field) and the
// audit logger (for the IP field on entries) so they always agree. Don't
// call r.Header.Get("X-Forwarded-For") directly.
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	ip, _ := ResolveClientIP(peer, r.Header.Get("X-Forwarded-For"), TrustProxy(), isTrustedProxySource)
	if ip == nil {
		return host
	}
	return ip.String()
}
