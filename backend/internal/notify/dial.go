package notify

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Notification targets are admin-configured and frequently live on the LAN
// (Gotify, ntfy, an internal relay), so private and loopback ranges stay
// reachable. What is never a legitimate webhook destination, and is a classic
// way to reach internal services through translation or reserved space, is
// refused: "this network", benchmarking, reserved/class E, and NAT64.
var neverDialNets = mustParseCIDRs(
	"0.0.0.0/8",      // "this network"
	"198.18.0.0/15",  // benchmarking
	"240.0.0.0/4",    // reserved (class E, incl. broadcast)
	"64:ff9b::/96",   // NAT64 well-known prefix
	"64:ff9b:1::/48", // local-use NAT64
	"::/128",         // unspecified
	"100::/64",       // discard-only
	"2001:db8::/32",  // documentation
	"fec0::/10",      // deprecated site-local
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err == nil && n != nil {
			out = append(out, n)
		}
	}
	return out
}

// blockedDestination reports whether ip falls in a range notifications must
// never be sent to.
func blockedDestination(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range neverDialNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// newSafeClient builds the notifier's HTTP client: it resolves the host
// itself, drops blocked addresses and dials the approved IP directly (no
// second lookup, so DNS rebinding cannot swap the target), and re-checks
// every redirect hop through the same dialer.
func newSafeClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(addr)
				if err != nil {
					return nil, err
				}
				var ips []net.IP
				if lit := net.ParseIP(host); lit != nil {
					ips = []net.IP{lit}
				} else {
					ips, err = net.DefaultResolver.LookupIP(ctx, "ip", host)
					if err != nil {
						return nil, err
					}
				}
				var lastErr error = fmt.Errorf("notify: %s resolves only to blocked addresses", host)
				for _, ip := range ips {
					if blockedDestination(ip) {
						continue
					}
					conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
					if derr == nil {
						return conn, nil
					}
					lastErr = derr
				}
				return nil, lastErr
			},
		},
	}
}
