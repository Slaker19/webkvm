package nodes

import (
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var nodeSSHUserRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)

// ValidateRemoteLibvirtURI checks a URI before it is stored for a remote
// libvirt node. Only qemu+ssh://[user@]host[:port]/system is accepted: no
// password, no query options (command=, netcat=, keyfile=, no_verify=...
// can run local programs or disable verification) and no plain qemu+tcp.
// Errors are *ValidationError with CodeInvalidURI.
func ValidateRemoteLibvirtURI(raw string) error {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return invalidf(CodeInvalidURI, "invalid libvirt URI; use qemu+ssh://user@host/system")
	}
	switch u.Scheme {
	case "qemu+ssh":
	case "qemu+tcp":
		return invalidf(CodeInvalidURI, "plain qemu+tcp is not allowed (no encryption or authentication); use qemu+ssh://user@host/system or an Incus node with mTLS")
	default:
		return invalidf(CodeInvalidURI, "URI must use qemu+ssh:// and end in /system; use an Incus node for other transports")
	}
	if u.Opaque != "" || u.Hostname() == "" || u.Path != "/system" {
		return invalidf(CodeInvalidURI, "URI must look like qemu+ssh://user@host/system")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return invalidf(CodeInvalidURI, "URI options (?command=, ?netcat=, ?keyfile=, ?no_verify=...) are not allowed")
	}
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return invalidf(CodeInvalidURI, "passwords in libvirt URIs are not supported; use SSH keys")
		}
		if !nodeSSHUserRE.MatchString(u.User.Username()) {
			return invalidf(CodeInvalidURI, "invalid SSH username in URI")
		}
	}
	host := u.Hostname()
	if net.ParseIP(host) == nil && !validHostname(host) {
		return invalidf(CodeInvalidURI, "invalid host in URI")
	}
	return nil
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

// RedactURI hides any userinfo (user and password) of a URI so it can be
// logged or audited without leaking credentials.
func RedactURI(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		if i := strings.Index(raw, "@"); i >= 0 {
			if j := strings.Index(raw, "://"); j >= 0 && j < i {
				return raw[:j+3] + "***@" + raw[i+1:]
			}
		}
		return raw
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	return strings.Replace(u.String(), "%2A%2A%2A", "***", 1)
}

// Transport selects how libvirt reaches a remote node over SSH.
type Transport string

const (
	// TransportLibSSH2 uses libvirt's built-in libssh2 client (qemu+libssh2):
	// explicit key file and WebKVM's own pinned known_hosts with strict
	// verification, and, unlike libssh, no multi-second stall on replies
	// larger than ~4 KB. The default.
	TransportLibSSH2 Transport = "libssh2"
	// TransportLibSSH uses libvirt's built-in libssh client: explicit key
	// file and a dedicated known_hosts with strict verification.
	TransportLibSSH Transport = "libssh"
	// TransportSSH uses the system ssh binary (qemu+ssh). It can only be
	// pointed at the key file; the host key is verified by the system ssh
	// against ITS known_hosts, so BuildConnURI pre-checks that the host key
	// is pinned in WebKVM's store before ever handing the URI out.
	TransportSSH Transport = "ssh"
)

// DefaultTransport is the transport used unless configured otherwise. See
// TestDefaultTransportIsLibSSH2 for why it must not go back to libssh.
const DefaultTransport = TransportLibSSH2

// ParseTransport maps a configuration value to a Transport; anything
// unknown falls back to DefaultTransport. Values stored before libssh2 was
// added ("libssh", "ssh") keep their meaning.
func ParseTransport(v string) Transport {
	switch Transport(strings.TrimSpace(v)) {
	case TransportSSH:
		return TransportSSH
	case TransportLibSSH:
		return TransportLibSSH
	case TransportLibSSH2:
		return TransportLibSSH2
	}
	return DefaultTransport
}

// ConnURIOption customises BuildConnURI.
type ConnURIOption func(*connURIOptions)

type connURIOptions struct {
	transport Transport
	keyPath   string
}

// WithKeyPath makes BuildConnURI use another private key of the store (the
// pending rotation key) instead of the active one. The path is still checked
// against the strict store-path alphabet.
func WithKeyPath(p string) ConnURIOption {
	return func(o *connURIOptions) { o.keyPath = p }
}

// WithTransport selects the transport (default DefaultTransport).
func WithTransport(t Transport) ConnURIOption {
	return func(o *connURIOptions) { o.transport = t }
}

var safeStorePathRE = regexp.MustCompile(`^/[A-Za-z0-9_@%+=:,./-]+$`)

// NodeSSHTarget extracts user (default root), host and port (default 22)
// from a stored libvirt node URI after re-validating it.
func NodeSSHTarget(rawURI string) (user, host string, port int, err error) {
	if err = ValidateRemoteLibvirtURI(rawURI); err != nil {
		return "", "", 0, err
	}
	u, _ := url.Parse(strings.TrimSpace(rawURI))
	if u.Scheme != "qemu+ssh" {
		return "", "", 0, invalidf(CodeInvalidURI, "managed SSH keys only apply to qemu+ssh nodes")
	}
	user = "root"
	if u.User != nil {
		user = u.User.Username()
	}
	port = 22
	if p := u.Port(); p != "" {
		n, perr := strconv.Atoi(p)
		if perr != nil || n < 1 || n > 65535 {
			return "", "", 0, invalidf(CodeInvalidURI, "invalid port in URI")
		}
		port = n
	}
	return user, u.Hostname(), port, nil
}

// BuildConnURI turns the stored plain URI of a node into the URI handed to
// libvirt. The user-visible URI never carries options; the key file,
// known_hosts and transport parameters are added here and nowhere else, with
// paths taken from the store (never from input) and checked against a strict
// alphabet. The host key must already be pinned.
func BuildConnURI(n Node, s *SSHKeyStore, opts ...ConnURIOption) (string, error) {
	o := connURIOptions{transport: DefaultTransport}
	for _, f := range opts {
		f(&o)
	}
	if s == nil {
		return "", ErrNoKey
	}
	user, host, port, err := NodeSSHTarget(n.URI)
	if err != nil {
		return "", err
	}
	info, err := s.Info()
	if err != nil {
		return "", err
	}
	if !info.Configured {
		return "", ErrNoKey
	}
	keyPath, khPath := filepath.Clean(s.KeyPath()), filepath.Clean(s.KnownHostsPath())
	if o.keyPath != "" {
		keyPath = filepath.Clean(o.keyPath)
	}
	if !safeStorePathRE.MatchString(keyPath) || !safeStorePathRE.MatchString(khPath) || strings.Contains(keyPath, "..") {
		return "", errors.New("cluster SSH store path is not usable")
	}
	if _, ok := s.PinnedHostKey(host, port); !ok {
		return "", ErrHostKeyUnpinned
	}
	q := url.Values{}
	switch o.transport {
	case TransportSSH:
		q.Set("keyfile", keyPath)
		q.Set("no_tty", "1")
	case TransportLibSSH:
		q.Set("keyfile", keyPath)
		q.Set("known_hosts", khPath)
		q.Set("known_hosts_verify", "normal")
		q.Set("sshauth", "privkey")
		q.Set("no_tty", "1")
	default: // TransportLibSSH2
		q.Set("keyfile", keyPath)
		q.Set("known_hosts", khPath)
		q.Set("known_hosts_verify", "normal")
		q.Set("no_tty", "1")
	}
	scheme := "qemu+libssh2"
	switch o.transport {
	case TransportSSH:
		scheme = "qemu+ssh"
	case TransportLibSSH:
		scheme = "qemu+libssh"
	}
	u := url.URL{
		Scheme:   scheme,
		User:     url.User(user),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		Path:     "/system",
		RawQuery: q.Encode(),
	}
	return u.String(), nil
}

// localAddrLookup returns this machine's hostname and interface addresses.
// A variable so tests can inject a fixed view.
var localAddrLookup = func() (string, []net.IP) {
	name, _ := os.Hostname()
	var ips []net.IP
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			switch v := a.(type) {
			case *net.IPNet:
				ips = append(ips, v.IP)
			case *net.IPAddr:
				ips = append(ips, v.IP)
			}
		}
	}
	return name, ips
}

// IsLocalHost reports whether host names this machine: loopback, "localhost",
// the hostname (short or FQDN form) or one of its interface addresses. No DNS
// resolution is performed.
func IsLocalHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	name, ips := localAddrLookup()
	name = strings.ToLower(strings.TrimSpace(name))
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return true
		}
		for _, l := range ips {
			if l.Equal(ip) {
				return true
			}
		}
		return false
	}
	if name == "" {
		return false
	}
	short := name
	if i := strings.IndexByte(name, '.'); i > 0 {
		short = name[:i]
	}
	return host == name || host == short
}

// maxUIURLLen bounds the stored operator-network address.
const maxUIURLLen = 253

// ValidateUIURL checks and normalizes the address of a node's WebKVM on the
// operator's network. Empty is allowed (no address). Otherwise it must be
// http(s)://host[:port] with no userinfo, query, fragment or path, and the
// host must not be loopback, unspecified or link-local. The result is
// scheme://host[:port] with a lowercase host. Errors are *ValidationError
// with CodeInvalidUIURL.
func ValidateUIURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	bad := func(msg string) (string, error) { return "", invalidf(CodeInvalidUIURL, "%s", msg) }
	if len(raw) > maxUIURLLen {
		return bad("address too long")
	}
	for _, c := range raw {
		if c <= ' ' || c == 0x7f {
			return bad("address contains spaces or control characters")
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return bad("invalid address; use https://host:port")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return bad("address must start with http:// or https://")
	}
	host := strings.ToLower(u.Hostname())
	if u.Opaque != "" || host == "" {
		return bad("address needs a host")
	}
	if u.User != nil {
		return bad("address must not contain credentials")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") {
		return bad("address must not contain a query or fragment")
	}
	if u.Path != "" && u.Path != "/" {
		return bad("address must not contain a path")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			return bad("address must be reachable from your PC (not loopback, unspecified or link-local)")
		}
	} else {
		if host == "localhost" || strings.HasSuffix(host, ".localhost") || !validHostname(host) {
			return bad("invalid or non-routable host in address")
		}
	}
	out := host
	if strings.Contains(host, ":") {
		out = "[" + host + "]"
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return bad("invalid port in address")
		}
		out += ":" + strconv.Itoa(n)
	}
	return scheme + "://" + out, nil
}
