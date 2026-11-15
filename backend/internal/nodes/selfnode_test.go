package nodes

import (
	"errors"
	"fmt"
	"net"
	"testing"
)

// fakeSelf pins this machine's identity for the test: hostname, interface
// addresses and the /etc/hosts aliases of the hostname.
func fakeSelf(t *testing.T, hostname string, ips []string, aliases []string) {
	t.Helper()
	prevAddr, prevAlias := localAddrLookup, localHostAliases
	localAddrLookup = func() (string, []net.IP) {
		var out []net.IP
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return hostname, out
	}
	localHostAliases = func(string) []string { return aliases }
	t.Cleanup(func() { localAddrLookup, localHostAliases = prevAddr, prevAlias })
}

func TestParseHostsAliases(t *testing.T) {
	content := "127.0.0.1 localhost\n" +
		"# 10.0.0.1 webkvm-a.commented\n" +
		"10.20.30.11\twebkvm-a.lab.example webkvm-a # trailing\n" +
		"10.20.30.12 webkvm-b.lab.example webkvm-b\n"
	got := parseHostsAliases(content, "webkvm-a")
	want := map[string]bool{"webkvm-a.lab.example": true, "webkvm-a": true, "10.20.30.11": true}
	if len(got) != len(want) {
		t.Fatalf("aliases = %v, want %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected alias %q in %v", g, got)
		}
	}
	if len(parseHostsAliases(content, "")) != 0 {
		t.Fatal("empty hostname must not match")
	}
}

func TestRegistryFlagsSelfRegistration(t *testing.T) {
	fakeSelf(t, "webkvm-a", []string{"10.20.30.11", "192.168.50.31"}, []string{"webkvm-a.lab.example"})
	r := newReg(t)
	cases := []struct {
		name, uri string
		self      bool
	}{
		{"by-ip", "qemu+ssh://operator@10.20.30.11/system", true},
		{"by-vip", "qemu+ssh://root@192.168.50.31:22/system", true},
		{"by-hostname", "qemu+ssh://root@WEBKVM-A/system", true},
		{"by-fqdn", "qemu+ssh://root@webkvm-a.lab.example./system", true},
		{"other-ip", "qemu+ssh://root@10.20.30.12/system", false},
		{"other-name", "qemu+ssh://root@webkvm-b/system", false},
	}
	ids := map[string]string{}
	for _, c := range cases {
		n, err := r.Create(c.name, c.uri)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		ids[c.name] = n.ID
	}
	byID := map[string]Node{}
	for _, n := range r.List() {
		byID[n.ID] = n
	}
	for _, c := range cases {
		if got := byID[ids[c.name]].IsSelf; got != c.self {
			t.Errorf("%s: is_self = %v, want %v", c.name, got, c.self)
		}
		if got, _ := r.Get(ids[c.name]); got.IsSelf != c.self {
			t.Errorf("%s: Get is_self = %v, want %v", c.name, got.IsSelf, c.self)
		}
	}
	if byID["local"].IsSelf {
		t.Error("the local node itself is never flagged is_self")
	}
}

func TestSelfMatchesLocalNodeName(t *testing.T) {
	fakeSelf(t, "webkvm-a", nil, nil)
	r := newReg(t)
	// The local node name follows the hostname; an entry addressed by it is
	// this machine even without an interface address match.
	n, err := r.Create("dup", "qemu+ssh://root@webkvm-a/system")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(n.ID); !got.IsSelf {
		t.Fatal("entry addressed by the local node name must be is_self")
	}
}

func TestSelfNeverForIncus(t *testing.T) {
	fakeSelf(t, "webkvm-a", []string{"10.20.30.11"}, nil)
	r := newReg(t)
	r.mu.Lock()
	r.nodes["inc"] = &Node{ID: "inc", Name: "inc", URI: "https://10.20.30.11:8443", Backend: BackendIncus, Type: NodeTypeRemote, Enabled: true}
	r.mu.Unlock()
	if got, _ := r.Get("inc"); got.IsSelf {
		t.Fatal("Incus endpoints are not flagged (the local socket is a different path)")
	}
}

func TestSelfIsNotPersisted(t *testing.T) {
	fakeSelf(t, "webkvm-a", []string{"10.20.30.11"}, nil)
	dir := t.TempDir()
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	n, err := r.Create("dup", "qemu+ssh://root@10.20.30.11/system")
	if err != nil {
		t.Fatal(err)
	}
	// After a failover the shared registry is read by another machine: it
	// must recompute, never trust a stored flag.
	fakeSelf(t, "webkvm-b", []string{"10.20.30.12"}, nil)
	r2, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := r2.Get(n.ID); got.IsSelf {
		t.Fatal("is_self must be recomputed on the machine that reads the registry")
	}
}

func TestClusterSummarySkipsSelfAndCountsNoAccess(t *testing.T) {
	fakeSelf(t, "webkvm-a", []string{"10.20.30.11"}, nil)
	r := newReg(t)
	dup, _ := r.Create("dup", "qemu+ssh://root@10.20.30.11/system")
	other, _ := r.Create("other", "qemu+ssh://root@10.20.30.12/system")
	r.mu.Lock()
	r.nodes["local"].Status = "online"
	r.nodes[dup.ID].Status = "online"
	r.nodes[other.ID].Status = "online"
	r.nodes[other.ID].Access = AccessNotSetUp
	r.mu.Unlock()
	s := r.ClusterSummary()
	if s.TotalNodes != 2 || s.OnlineNodes != 1 || s.NoAccessNodes != 1 || s.OfflineNodes != 0 {
		t.Fatalf("summary = %+v, want total 2, online 1, no_access 1", s)
	}
}

type codedErr string

func (c codedErr) Error() string { return "coded " + string(c) }
func (c codedErr) Code() string  { return string(c) }

func TestAccessSetupCode(t *testing.T) {
	for err, want := range map[error]string{
		ErrNoKey:                         "no_key",
		ErrHostKeyUnpinned:               "host_key_unpinned",
		ErrSSHAuthDenied:                 "ssh_auth_denied",
		fmt.Errorf("wrap: %w", ErrNoKey): "no_key",
		codedErr("ssh_auth_denied"):      "ssh_auth_denied",
		fmt.Errorf("x: %w", codedErr("host_key_unpinned")): "host_key_unpinned",
	} {
		if got, ok := AccessSetupCode(err); !ok || got != want {
			t.Errorf("%v: code = %q,%v want %q", err, got, ok, want)
		}
	}
	for _, err := range []error{nil, errors.New("boom"), codedErr("node_timeout"), ErrHostTimeout} {
		if _, ok := AccessSetupCode(err); ok {
			t.Errorf("%v must not be an access-setup error", err)
		}
	}
}

func TestProbeRecordsAccessState(t *testing.T) {
	fakeSelf(t, "webkvm-a", nil, nil)
	r := newReg(t)
	host, port := listenTCP(t)
	n, err := r.Create("remote", sshURI(host, port))
	if err != nil {
		t.Fatal(err)
	}
	var proberErr error
	r.SetRemoteProber(func(string) (int, int64, int64, error) {
		if proberErr != nil {
			return 0, 0, 0, proberErr
		}
		return 4, 1, 2, nil
	})

	proberErr = fmt.Errorf("open: %w", ErrNoKey)
	got, err := r.ProbeNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "online" || got.Access != AccessNotSetUp || got.AccessCode != "no_key" {
		t.Fatalf("no key: status=%q access=%q code=%q", got.Status, got.Access, got.AccessCode)
	}

	proberErr = nil
	got, _ = r.ProbeNode(n.ID)
	if got.Access != AccessOK || got.AccessCode != "" || got.CPUs != 4 {
		t.Fatalf("ok: access=%q code=%q cpus=%d", got.Access, got.AccessCode, got.CPUs)
	}

	// An unrelated failure (libvirt down, timeout) says nothing about access.
	proberErr = errors.New("libvirt down")
	got, _ = r.ProbeNode(n.ID)
	if got.Access != "" {
		t.Fatalf("unrelated error: access=%q, want unknown", got.Access)
	}
}

func TestMarkAccessNotSetUpKeepsStatus(t *testing.T) {
	r := newReg(t)
	n, _ := r.Create("remote", "qemu+ssh://root@10.0.0.9/system")
	r.mu.Lock()
	r.nodes[n.ID].Status = "online"
	r.mu.Unlock()
	r.MarkAccessNotSetUp(n.ID, "no_key")
	got, _ := r.Get(n.ID)
	if got.Status != "online" || got.Access != AccessNotSetUp || got.AccessCode != "no_key" {
		t.Fatalf("got status=%q access=%q code=%q", got.Status, got.Access, got.AccessCode)
	}
	r.MarkAccessNotSetUp("local", "no_key")
	if l, _ := r.Get("local"); l.Access != "" {
		t.Fatal("the local node has no SSH access state")
	}
}

func TestSelfLoopbackTunnelNotFlagged(t *testing.T) {
	fakeSelf(t, "webkvm-a", nil, nil)
	r := newReg(t)
	tunnel, err := r.Create("tunnel", "qemu+ssh://root@127.0.0.1:2222/system")
	if err != nil {
		t.Skipf("loopback URI rejected by validation: %v", err)
	}
	if got, _ := r.Get(tunnel.ID); got.IsSelf {
		t.Fatal("a loopback port forward may be another machine")
	}
	web, _ := r.Create("web", "https://127.0.0.1:54321")
	if got, _ := r.Get(web.ID); got.IsSelf {
		t.Fatal("only qemu+ssh/qemu+tcp entries are considered")
	}
}
