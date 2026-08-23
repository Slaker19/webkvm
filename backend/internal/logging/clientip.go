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

// ClientIP returns the best-known client IP for a request, given
// the current proxy-trust setting.
//
//   - If TrustProxy() is true and X-Forwarded-For is set, the FIRST
//     hop in the header is returned (the actual client). This is
//     the X-Forwarded-For convention: each proxy appends, so the
//     leftmost is the original sender.
//   - Otherwise, the TCP peer address (RemoteAddr) is used, with
//     the port stripped.
//
// Use this in BOTH the request logger (for the remote_ip field)
// and the audit logger (for the IP field on entries) so they
// always agree. Don't call r.Header.Get("X-Forwarded-For")
// directly — that's what introduced the original "logs disagree
// with audit" bug.
func ClientIP(r *http.Request) string {
	if TrustProxy() {
		if v := r.Header.Get("X-Forwarded-For"); v != "" {
			// Take the leftmost (original client). Comma is the
			// X-Forwarded-For separator per RFC 7239 §5.2.
			first := strings.TrimSpace(v)
			if i := strings.Index(v, ","); i >= 0 {
				first = strings.TrimSpace(v[:i])
			}
			if first != "" {
				return first
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
