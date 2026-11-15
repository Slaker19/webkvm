package nodes

import (
	"net/url"
	"strings"
	"sync"
)

// transportSupport caches, for the life of the process, the SSH transports
// the LOCAL libvirt client library turned out not to support (it is loaded
// in-process, so the answer cannot change without a restart). It is filled
// by classifying a real connection attempt (transport_unsupported), which
// costs one fast local failure (~30 ms) and needs no extra exec.
var transportSupport = struct {
	sync.Mutex
	unsupported map[Transport]bool
}{unsupported: map[Transport]bool{}}

// MarkTransportUnsupported records that the local libvirt client cannot use
// t. It returns true the first time (callers log the fallback once).
func MarkTransportUnsupported(t Transport) bool {
	transportSupport.Lock()
	defer transportSupport.Unlock()
	if transportSupport.unsupported[t] {
		return false
	}
	transportSupport.unsupported[t] = true
	return true
}

// TransportUnsupported reports whether t is known to be unsupported.
func TransportUnsupported(t Transport) bool {
	transportSupport.Lock()
	defer transportSupport.Unlock()
	return transportSupport.unsupported[t]
}

// ResetTransportSupport forgets every cached result (tests).
func ResetTransportSupport() {
	transportSupport.Lock()
	defer transportSupport.Unlock()
	transportSupport.unsupported = map[Transport]bool{}
}

// EffectiveTransport resolves the configured transport against what the
// local libvirt client supports. Only the preferred default, libssh2 (fast,
// see TestDefaultTransportIsLibSSH2), falls back, and only to libssh: same
// managed key file, same pinned known_hosts, strict verification, so the
// fallback never weakens host-key checking (it is slower on replies larger
// than ~4 KB). An explicit libssh or ssh choice is never rewritten (operator
// override), and libssh2 is not switched to the system ssh binary, which
// verifies against a different known_hosts. fellBack reports a substitution.
func EffectiveTransport(configured Transport) (t Transport, fellBack bool) {
	if configured == TransportLibSSH2 && TransportUnsupported(TransportLibSSH2) && !TransportUnsupported(TransportLibSSH) {
		return TransportLibSSH, true
	}
	return configured, false
}

// TransportOfURI returns the SSH transport of a libvirt connection URI
// ("" for anything else).
func TransportOfURI(raw string) Transport {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.ToLower(u.Scheme) {
	case "qemu+libssh2":
		return TransportLibSSH2
	case "qemu+libssh":
		return TransportLibSSH
	case "qemu+ssh":
		return TransportSSH
	}
	return ""
}
