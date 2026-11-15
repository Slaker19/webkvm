package nodes

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
)

// Access states of a remote libvirt node (Node.Access). Empty means unknown:
// never probed over SSH, or the last attempt failed for an unrelated reason.
const (
	AccessOK       = "ok"
	AccessNotSetUp = "not_set_up"
)

// localHostAliases returns the names /etc/hosts gives this machine's hostname
// (typically its FQDN). Local file read only, never DNS. A variable so tests
// can inject a fixed view.
var localHostAliases = func(hostname string) []string {
	data, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return nil
	}
	return parseHostsAliases(string(data), hostname)
}

// parseHostsAliases returns every field (address and names) of the
// /etc/hosts lines that name hostname, compared case-insensitively.
func parseHostsAliases(content, hostname string) []string {
	hostname = normHost(hostname)
	if hostname == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(content, "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		match := false
		for _, f := range fields[1:] {
			if normHost(f) == hostname {
				match = true
				break
			}
		}
		if match {
			out = append(out, fields...)
		}
	}
	return out
}

func normHost(h string) string {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
}

// selfIdentity is this machine's view of itself, built once per listing:
// hostname (short and full), /etc/hosts aliases, the local node name and
// interface addresses. No network I/O.
type selfIdentity struct {
	names map[string]bool
	ips   []net.IP
}

func currentSelfIdentity(localNodeName string) selfIdentity {
	id := selfIdentity{names: map[string]bool{}}
	add := func(n string) {
		n = normHost(n)
		if n == "" {
			return
		}
		if ip := net.ParseIP(n); ip != nil {
			id.ips = append(id.ips, ip)
			return
		}
		id.names[n] = true
		if i := strings.IndexByte(n, '.'); i > 0 {
			id.names[n[:i]] = true
		}
	}
	name, ips := localAddrLookup()
	add(name)
	add(localNodeName)
	id.ips = append(id.ips, ips...)
	for _, a := range localHostAliases(name) {
		add(a)
	}
	delete(id.names, "local")
	return id
}

// matches reports whether host designates this machine.
func (id selfIdentity) matches(host string) bool {
	host = normHost(host)
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return true
		}
		for _, l := range id.ips {
			if l.Equal(ip) {
				return true
			}
		}
		return false
	}
	return id.names[host]
}

// selfCandidate reports whether n is a remote libvirt (qemu+ssh/qemu+tcp)
// entry whose URI host is this machine. Incus endpoints are not considered: the local Incus socket is
// a different access path with its own project scoping.
func (id selfIdentity) selfCandidate(n *Node) bool {
	if n == nil || n.IsLocal() || n.Driver() != BackendLibvirt {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(n.URI))
	if err != nil || u.Hostname() == "" || (u.Scheme != "qemu+ssh" && u.Scheme != "qemu+tcp") {
		return false
	}
	// A loopback address on a non-default port is usually a tunnel or port
	// forward to ANOTHER machine: not flagged.
	if p := u.Port(); p != "" && p != "22" && p != "16509" && isLoopbackName(u.Hostname()) {
		return false
	}
	return id.matches(u.Hostname())
}

func isLoopbackName(h string) bool {
	h = normHost(h)
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// selfIdentityLocked builds the identity from the registry's local node.
// Callers hold r.mu (read or write).
func (r *Registry) selfIdentityLocked() selfIdentity {
	localName := ""
	for _, n := range r.nodes {
		if n.IsLocal() {
			localName = n.Name
			break
		}
	}
	return currentSelfIdentity(localName)
}

// AccessSetupCode returns the code of an error that only means WebKVM's
// managed SSH access to the node is not set up yet (no cluster key, host key
// not confirmed, key not authorized on the node). It understands the
// package sentinels and any error carrying a Code() (libvirt.NodeError).
func AccessSetupCode(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	switch {
	case errors.Is(err, ErrNoKey):
		return "no_key", true
	case errors.Is(err, ErrHostKeyUnpinned):
		return "host_key_unpinned", true
	case errors.Is(err, ErrSSHAuthDenied):
		return "ssh_auth_denied", true
	}
	var coded interface{ Code() string }
	if errors.As(err, &coded) {
		switch c := coded.Code(); c {
		case "no_key", "host_key_unpinned", "ssh_auth_denied":
			return c, true
		}
	}
	return "", false
}

// MarkAccessNotSetUp records that a caller (e.g. VM federation) found the
// node's SSH access not set up. The host may well be reachable, so the status
// is left alone. No-op for the local node, unknown or disabled nodes.
func (r *Registry) MarkAccessNotSetUp(id, code string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok || n.IsLocal() || !n.Enabled {
		return
	}
	n.Access = AccessNotSetUp
	n.AccessCode = code
}
