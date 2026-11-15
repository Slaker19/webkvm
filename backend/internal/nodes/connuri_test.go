package nodes

import (
	"encoding/base64"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"
)

// pinForTest writes a known_hosts line directly (no network).
func pinForTest(t *testing.T, s *SSHKeyStore, host string, port int) {
	t.Helper()
	sg := newSigner(t)
	line := knownHostsName(host, port) + " " + sg.PublicKey().Type() + " " + base64.StdEncoding.EncodeToString(sg.PublicKey().Marshal()) + "\n"
	f, err := os.OpenFile(s.KnownHostsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func readyStore(t *testing.T) *SSHKeyStore {
	t.Helper()
	s := NewSSHKeyStore(t.TempDir())
	if _, err := s.Generate(); err != nil {
		t.Fatal(err)
	}
	return s
}

// Regression guard (no live measurement): the DEFAULT transport must stay
// libssh2. libvirt's libssh client stalls 5 s (or hangs) on every reply
// larger than ~4 KB on the lab nodes; libssh2 is fast and verifies the host
// key against the managed known_hosts just the same.
func TestDefaultTransportIsLibSSH2(t *testing.T) {
	if DefaultTransport != TransportLibSSH2 || string(DefaultTransport) != "libssh2" {
		t.Fatalf("DefaultTransport = %q", DefaultTransport)
	}
	s := readyStore(t)
	pinForTest(t, s, "node1.example", 22)
	got, err := BuildConnURI(Node{URI: "qemu+ssh://admin@node1.example/system"}, s)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "qemu+libssh2" || u.User.Username() != "admin" || u.Host != "node1.example:22" || u.Path != "/system" ||
		q.Get("keyfile") != s.KeyPath() || q.Get("known_hosts") != s.KnownHostsPath() ||
		q.Get("known_hosts_verify") != "normal" || q.Get("no_tty") != "1" || len(q) != 4 {
		t.Fatalf("uri = %s", got)
	}
	if strings.Contains(got, "ignore") || strings.Contains(got, "no_verify") {
		t.Fatalf("verification must never be disabled: %s", got)
	}
}

func TestBuildConnURILibSSH(t *testing.T) {
	s := readyStore(t)
	pinForTest(t, s, "node1.example", 22)
	got, err := BuildConnURI(Node{URI: "qemu+ssh://admin@node1.example/system"}, s, WithTransport(TransportLibSSH))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if u.Scheme != "qemu+libssh" || u.User.Username() != "admin" || u.Host != "node1.example:22" || u.Path != "/system" ||
		q.Get("keyfile") != s.KeyPath() || q.Get("known_hosts") != s.KnownHostsPath() ||
		q.Get("known_hosts_verify") != "normal" || q.Get("sshauth") != "privkey" || q.Get("no_tty") != "1" || len(q) != 5 {
		t.Fatalf("uri = %s", got)
	}
	// Default user is root, explicit port is kept.
	pinForTest(t, s, "node2.example", 2222)
	got, err = BuildConnURI(Node{URI: "qemu+ssh://node2.example:2222/system"}, s, WithTransport(TransportLibSSH))
	if err != nil || !strings.HasPrefix(got, "qemu+libssh://root@node2.example:2222/system?") {
		t.Fatalf("uri = %s err = %v", got, err)
	}
}

func TestBuildConnURISSHFallbackAndIPv6(t *testing.T) {
	s := readyStore(t)
	pinForTest(t, s, "fd00::10", 22)
	got, err := BuildConnURI(Node{URI: "qemu+ssh://root@[fd00::10]/system"}, s, WithTransport(TransportSSH))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	q := u.Query()
	if u.Scheme != "qemu+ssh" || u.Hostname() != "fd00::10" || u.Port() != "22" || q.Get("keyfile") != s.KeyPath() || q.Get("no_tty") != "1" || len(q) != 2 {
		t.Fatalf("uri = %s", got)
	}
	got, err = BuildConnURI(Node{URI: "qemu+ssh://root@[fd00::10]/system"}, s)
	if err != nil || !strings.HasPrefix(got, "qemu+libssh2://root@[fd00::10]:22/system?") {
		t.Fatalf("uri = %s err = %v", got, err)
	}
	for in, want := range map[string]Transport{
		"ssh": TransportSSH, "libssh": TransportLibSSH, "libssh2": TransportLibSSH2,
		" libssh2 ": TransportLibSSH2, "": TransportLibSSH2, "tcp": TransportLibSSH2, "LIBSSH": TransportLibSSH2,
		"ssh?no_verify=1": TransportLibSSH2,
	} {
		if got := ParseTransport(in); got != want {
			t.Errorf("ParseTransport(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildConnURIRequiresKeyAndPin(t *testing.T) {
	n := Node{URI: "qemu+ssh://root@node1.example/system"}
	if _, err := BuildConnURI(n, nil); err != ErrNoKey {
		t.Fatalf("nil store err = %v", err)
	}
	empty := NewSSHKeyStore(t.TempDir())
	if _, err := BuildConnURI(n, empty); err != ErrNoKey {
		t.Fatalf("no key err = %v", err)
	}
	s := readyStore(t)
	for _, tr := range []Transport{TransportLibSSH2, TransportLibSSH, TransportSSH} {
		if _, err := BuildConnURI(n, s, WithTransport(tr)); err != ErrHostKeyUnpinned {
			t.Fatalf("%s: unpinned err = %v", tr, err)
		}
	}
	// A pin for another port does not count.
	pinForTest(t, s, "node1.example", 2222)
	if _, err := BuildConnURI(n, s); err != ErrHostKeyUnpinned {
		t.Fatalf("other port err = %v", err)
	}
}

func TestBuildConnURIHostileInputCannotInjectParameters(t *testing.T) {
	s := readyStore(t)
	pinForTest(t, s, "node1.example", 22)
	hostile := []string{
		"qemu+ssh://root@node1.example/system?command=/bin/sh",
		"qemu+ssh://root@node1.example/system?keyfile=/etc/shadow",
		"qemu+ssh://root@node1.example/system?no_verify=1",
		"qemu+ssh://root@node1.example/system?",
		"qemu+ssh://root@node1.example/system#frag",
		"qemu+ssh://root@node1.example/system%3Fno_verify=1",
		"qemu+ssh://root:pw@node1.example/system",
		"qemu+ssh://ro%26ot@node1.example/system",
		"qemu+ssh://root@node1.example&no_verify=1/system",
		"qemu+ssh://root@node1.example/system/../../x?y",
		"qemu+tcp://node1.example/system",
		"qemu+libssh://root@node1.example/system",
		"qemu+libssh2://root@node1.example/system",
		"qemu+libssh2://root@node1.example/system?known_hosts_verify=ignore",
		"qemu:///system",
		"qemu+ssh://root@node1.example:99999/system",
		"qemu+ssh://root@node1.example/session",
		" qemu+ssh://root@node1.example/system\n?no_verify=1",
	}
	for _, raw := range hostile {
		for _, tr := range []Transport{TransportLibSSH2, TransportLibSSH, TransportSSH} {
			got, err := BuildConnURI(Node{URI: raw}, s, WithTransport(tr))
			if err == nil {
				t.Errorf("%q (%s) accepted: %s", raw, tr, got)
			}
			if got != "" {
				t.Errorf("%q: URI returned with error: %s", raw, got)
			}
		}
	}
	// Whatever is accepted only ever carries the fixed parameter set.
	got, err := BuildConnURI(Node{URI: "qemu+ssh://root@node1.example/system"}, s)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	for k := range u.Query() {
		switch k {
		case "keyfile", "known_hosts", "known_hosts_verify", "sshauth", "no_tty":
		default:
			t.Errorf("unexpected parameter %q", k)
		}
	}
}

func TestBuildConnURIRejectsUnsafeStorePath(t *testing.T) {
	s := NewSSHKeyStore(t.TempDir() + "/data dir&x=1")
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		t.Skip(err)
	}
	if _, err := s.Generate(); err != nil {
		t.Fatal(err)
	}
	pinForTest(t, s, "node1.example", 22)
	if got, err := BuildConnURI(Node{URI: "qemu+ssh://root@node1.example/system"}, s); err == nil {
		t.Fatalf("unsafe store path accepted: %s", got)
	}
}

func TestIsLocalHost(t *testing.T) {
	prev := localAddrLookup
	localAddrLookup = func() (string, []net.IP) {
		return "Node-A.lab.example", []net.IP{net.ParseIP("192.0.2.10"), net.ParseIP("2001:db8::10")}
	}
	t.Cleanup(func() { localAddrLookup = prev })
	for _, h := range []string{"localhost", "127.0.0.1", "::1", "[::1]", "node-a", "NODE-A.lab.example", "node-a.lab.example.", "192.0.2.10", "2001:db8::10", "[2001:db8::10]"} {
		if !IsLocalHost(h) {
			t.Errorf("%q should be local", h)
		}
	}
	for _, h := range []string{"", "node-b", "node-a.other.example", "192.0.2.11", "2001:db8::11", "lab.example"} {
		if IsLocalHost(h) {
			t.Errorf("%q should not be local", h)
		}
	}
}
