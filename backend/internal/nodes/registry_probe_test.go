package nodes

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// listenTCP returns a local listener whose address is a reachable "node".
func listenTCP(t *testing.T) (host, port string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	h, p, _ := net.SplitHostPort(l.Addr().String())
	return h, p
}

func sshURI(host, port string) string { return "qemu+ssh://u@" + host + ":" + port + "/system" }

func newReg(t *testing.T) *Registry {
	t.Helper()
	r, err := New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	r.httpTimeout = 200 * time.Millisecond
	return r
}

// A probe in flight must not resurrect a node deleted meanwhile.
func TestProbeNodeDoesNotResurrectDeletedNode(t *testing.T) {
	r := newReg(t)
	host, port := listenTCP(t)
	n, err := r.Create("remote", sshURI(host, port))
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	r.SetRemoteProber(func(string) (int, int64, int64, error) {
		close(entered)
		<-release
		return 4, 1, 2, nil
	})
	done := make(chan error, 1)
	go func() { _, err := r.ProbeNode(n.ID); done <- err }()
	<-entered
	if err := r.Delete(n.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; !errors.Is(err, ErrNotFound) {
		t.Fatalf("probe err = %v, want ErrNotFound", err)
	}
	if _, ok := r.Get(n.ID); ok {
		t.Fatal("deleted node was resurrected by the probe")
	}
}

// A concurrent Update (name/enabled) must survive a finishing probe.
func TestProbeNodeKeepsConcurrentUpdate(t *testing.T) {
	r := newReg(t)
	host, port := listenTCP(t)
	n, _ := r.Create("before", sshURI(host, port))
	entered, release := make(chan struct{}), make(chan struct{})
	r.SetRemoteProber(func(string) (int, int64, int64, error) {
		close(entered)
		<-release
		return 8, 10, 20, nil
	})
	done := make(chan struct{})
	go func() { _, _ = r.ProbeNode(n.ID); close(done) }()
	<-entered
	if _, err := r.Update(n.ID, "after", "", nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	got, _ := r.Get(n.ID)
	if got.Name != "after" {
		t.Fatalf("name = %q, probe overwrote the concurrent update", got.Name)
	}
	if got.Status != "online" || got.CPUs != 8 {
		t.Fatalf("telemetry not written: %+v", got)
	}
}

// A probe that finishes after the URI changed must not store telemetry of the old target.
func TestProbeNodeSkipsWriteWhenURIChanged(t *testing.T) {
	r := newReg(t)
	host, port := listenTCP(t)
	n, _ := r.Create("remote", sshURI(host, port))
	entered, release := make(chan struct{}), make(chan struct{})
	r.SetRemoteProber(func(string) (int, int64, int64, error) {
		close(entered)
		<-release
		return 8, 10, 20, nil
	})
	done := make(chan struct{})
	go func() { _, _ = r.ProbeNode(n.ID); close(done) }()
	<-entered
	newURI := sshURI(host, "1")
	if _, err := r.Update(n.ID, "", newURI, nil); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	got, _ := r.Get(n.ID)
	if got.URI != newURI || got.CPUs != 0 || got.Status == "online" {
		t.Fatalf("stale probe result applied: %+v", got)
	}
}

// A hung local prober must not hold any registry lock.
func TestLocalProbeDoesNotHoldLock(t *testing.T) {
	r := newReg(t)
	r.proberTimeout = 300 * time.Millisecond
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r.SetLocalProber(func() (string, string, int64, int64, int64, string, error) {
		<-block
		return "online", "", 0, 0, 0, "", nil
	})
	done := make(chan Node, 1)
	go func() { n, _ := r.ProbeNode("local"); done <- n }()
	time.Sleep(50 * time.Millisecond)
	listed := make(chan struct{})
	go func() { r.List(); close(listed) }()
	select {
	case <-listed:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("List blocked while the local prober was hanging")
	}
	select {
	case n := <-done:
		if n.Status != "degraded" || n.LastError == "" {
			t.Fatalf("hung local prober should degrade the node: %+v", n)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ProbeNode did not time out")
	}
}

// One node whose libvirt probe hangs must not block ProbeAllStale.
func TestProbeAllStaleDeadlineWithHangingNode(t *testing.T) {
	r := newReg(t)
	r.proberTimeout = time.Minute // prove the global deadline alone bounds the round
	r.probeDeadline = 300 * time.Millisecond
	host, port := listenTCP(t)
	n, _ := r.Create("slow", sshURI(host, port))
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r.SetRemoteProber(func(uri string) (int, int64, int64, error) {
		if uri == "qemu:///system" {
			return 1, 1, 1, nil
		}
		<-block
		return 0, 0, 0, nil
	})
	start := time.Now()
	list := r.ProbeAllStale(time.Second)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("ProbeAllStale took %v", d)
	}
	for _, x := range list {
		if x.ID == n.ID && (x.Status != "offline" || x.LastError == "") {
			t.Fatalf("slow node not marked unreachable: %+v", x)
		}
		if x.IsLocal() && x.Status != "online" {
			t.Fatalf("local node should still be probed: %+v", x)
		}
	}
}

func TestRemoteProberTimeoutBoundsProbeNode(t *testing.T) {
	r := newReg(t)
	r.proberTimeout = 200 * time.Millisecond
	host, port := listenTCP(t)
	n, _ := r.Create("slow", sshURI(host, port))
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	r.SetRemoteProber(func(string) (int, int64, int64, error) { <-block; return 1, 1, 1, nil })
	start := time.Now()
	got, err := r.ProbeNode(n.ID)
	if err != nil || got.Status != "online" || got.CPUs != 0 {
		t.Fatalf("got %+v err=%v", got, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("prober timeout not applied")
	}
}

func TestNodeNameValidation(t *testing.T) {
	r := newReg(t)
	bad := []string{"local", "LOCAL", "", "   ", "n_0123456789abcdef", "has space", "bad/name", "ñandú", string(make([]byte, 64))}
	for _, name := range bad {
		if _, err := r.Create(name, "https://127.0.0.1:1"); !errors.Is(err, ErrInvalid) {
			t.Errorf("Create(%q) err = %v, want ErrInvalid", name, err)
		}
	}
	var ve *ValidationError
	_, err := r.Create("local", "https://127.0.0.1:1")
	if !errors.As(err, &ve) || ve.Code != CodeReservedName {
		t.Fatalf("local should be reserved_name, got %v", err)
	}
	n, err := r.Create("  node-1.lab_A  ", "  https://127.0.0.1:1  ")
	if err != nil {
		t.Fatal(err)
	}
	if n.Name != "node-1.lab_A" || n.URI != "https://127.0.0.1:1" {
		t.Fatalf("not trimmed: %+v", n)
	}
	if _, err := r.Update(n.ID, "local", "", nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rename to local: %v", err)
	}
	if _, err := r.Update(n.ID, "", "  https://127.0.0.2:1 ", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.Get(n.ID); got.URI != "https://127.0.0.2:1" {
		t.Fatalf("uri not trimmed: %q", got.URI)
	}
	if _, err := r.Update("missing", "x", "", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	other, _ := r.Create("other", "https://127.0.0.1:2")
	if _, err := r.Update(other.ID, "node-1.lab_A", "", nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup: %v", err)
	}
}

func TestLegacyStoredNodesStillLoad(t *testing.T) {
	dir := t.TempDir()
	legacy := []*Node{
		{ID: "n_aaaaaaaaaaaaaaaa", Name: "my legacy node", URI: "qemu+tcp://10.0.0.5:16509/system", Type: NodeTypeRemote, Enabled: true, CreatedAt: time.Now()},
		{ID: "n_bbbbbbbbbbbbbbbb", Name: "local", URI: "qemu+ssh://u@h/system?command=/bin/x", Type: NodeTypeRemote, Enabled: true, CreatedAt: time.Now()},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(dir, "nodes.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"n_aaaaaaaaaaaaaaaa", "n_bbbbbbbbbbbbbbbb"} {
		if _, ok := r.Get(id); !ok {
			t.Fatalf("legacy node %s dropped", id)
		}
	}
	// Enabling/disabling a legacy-named node keeps working.
	off := false
	if _, err := r.Update("n_aaaaaaaaaaaaaaaa", "my legacy node", "", &off); err != nil {
		t.Fatalf("update legacy node: %v", err)
	}
}

func TestLegacyTCPNodeShowsWarningAndOptionURIsAreNotProbed(t *testing.T) {
	r := newReg(t)
	host, port := listenTCP(t)
	called := 0
	r.SetRemoteProber(func(string) (int, int64, int64, error) { called++; return 1, 1, 1, nil })
	r.mu.Lock()
	r.nodes["n_t"] = &Node{ID: "n_t", Name: "tcp", URI: "qemu+tcp://" + net.JoinHostPort(host, port) + "/system", Type: NodeTypeRemote, Enabled: true}
	r.nodes["n_q"] = &Node{ID: "n_q", Name: "opt", URI: "qemu+ssh://u@" + net.JoinHostPort(host, port) + "/system?command=/bin/evil", Type: NodeTypeRemote, Enabled: true}
	r.mu.Unlock()
	got, err := r.ProbeNode("n_t")
	if err != nil || got.Status != "degraded" || got.LastError == "" {
		t.Fatalf("tcp legacy: %+v err=%v", got, err)
	}
	before := called
	if _, err := r.ProbeNode("n_q"); err != nil {
		t.Fatal(err)
	}
	if called != before {
		t.Fatal("libvirt prober was called for a URI with options")
	}
}

func TestProbeTargetsIPv6(t *testing.T) {
	cases := []struct{ uri, url, tcp string }{
		{"[::1]", "https://[::1]:8080/api/health", "[::1]:22"},
		{"::1", "https://[::1]:8080/api/health", "[::1]:22"},
		{"qemu+ssh://u@[::1]/system", "https://[::1]:8080/api/health", "[::1]:22"},
		{"qemu+ssh://u@[::1]:2222/system", "https://[::1]:8080/api/health", "[::1]:2222"},
		{"qemu+tcp://[fd00::5]/system", "https://[fd00::5]:8080/api/health", "[fd00::5]:16509"},
		{"host.example", "https://host.example:8080/api/health", "host.example:22"},
		{"10.0.0.1:9443", "https://10.0.0.1:9443/api/health", "10.0.0.1:9443"},
		{"https://h:8080/", "https://h:8080/api/health", ""},
	}
	for _, c := range cases {
		u, tcp := probeTargets(c.uri)
		if u != c.url || tcp != c.tcp {
			t.Errorf("probeTargets(%q) = %q,%q want %q,%q", c.uri, u, tcp, c.url, c.tcp)
		}
	}
}

func TestClusterSummaryIgnoresUnknownAndDisabled(t *testing.T) {
	r := newReg(t)
	r.mu.Lock()
	r.nodes["local"].Status = "online"
	r.nodes["a"] = &Node{ID: "a", Name: "a", Enabled: true, Status: "unknown"}
	r.nodes["b"] = &Node{ID: "b", Name: "b", Enabled: false, Status: "offline"}
	r.nodes["c"] = &Node{ID: "c", Name: "c", Enabled: true, Status: "offline"}
	r.mu.Unlock()
	s := r.ClusterSummary()
	if s.TotalNodes != 4 || s.OnlineNodes != 1 || s.OfflineNodes != 1 {
		t.Fatalf("summary = %+v", s)
	}
}

// A bare TCP handshake to the SSH port is "tcp_reachable", never the
// misleading "reachable" (no login and no libvirt were checked).
func TestProbeTCPOnlyReportsTCPReachable(t *testing.T) {
	r := newReg(t)
	host, port := listenTCP(t)
	n, err := r.Create("remote", sshURI(host, port))
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ProbeNode(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "online" || got.LibvirtStatus != "tcp_reachable" {
		t.Fatalf("status=%q libvirt_status=%q", got.Status, got.LibvirtStatus)
	}
}
