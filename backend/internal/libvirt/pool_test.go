package libvirt

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"webkvm/internal/nodes"
)

func testPool(t *testing.T, connect func(ctx context.Context, uri string) (*Connector, error)) *Pool {
	t.Helper()
	p := NewPool(func(n nodes.Node) (string, error) { return "qemu+libssh://root@" + n.ID + "/system", nil })
	p.connectFn = connect
	p.aliveFn = func(*Connector) bool { return true }
	t.Cleanup(p.Close)
	return p
}

func remoteNode(id string) nodes.Node {
	return nodes.Node{ID: id, URI: "qemu+ssh://root@" + id + "/system", Type: nodes.NodeTypeRemote, Enabled: true}
}

func TestPoolReusesConnectorAndSingleflights(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	p := testPool(t, func(ctx context.Context, uri string) (*Connector, error) {
		calls.Add(1)
		<-release
		return &Connector{uri: uri}, nil
	})
	n := remoteNode("n1")
	var wg sync.WaitGroup
	got := make([]*Connector, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := p.Connector(context.Background(), n)
			if err != nil {
				t.Error(err)
			}
			got[i] = c
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	for _, c := range got {
		if c == nil || c != got[0] {
			t.Fatal("callers did not share one connector")
		}
	}
	again, _ := p.Connector(context.Background(), n)
	if again != got[0] || calls.Load() != 1 {
		t.Fatalf("calls = %d, reuse = %v", calls.Load(), again == got[0])
	}
	if got[0].uri != "qemu+libssh://root@n1/system" {
		t.Fatalf("connected to %q (must use buildURI)", got[0].uri)
	}
	if st := p.Status("n1"); st.State != PoolStateOpen || st.LastOK.IsZero() {
		t.Fatalf("status = %+v", st)
	}
	if st := p.Status("other"); st.State != PoolStateClosed {
		t.Fatalf("status = %+v", st)
	}
}

func TestPoolCircuitBreakerAndRecovery(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	p := testPool(t, func(ctx context.Context, uri string) (*Connector, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("ssh: handshake failed: Permission denied (publickey)")
		}
		return &Connector{uri: uri}, nil
	})
	now := time.Now()
	p.now = func() time.Time { return now }
	n := remoteNode("n1")
	_, err := p.Connector(context.Background(), n)
	if !errors.Is(err, ErrSSHAuthDenied) {
		t.Fatalf("err = %v", err)
	}
	if st := p.Status("n1"); st.State != PoolStateFailing || st.LastError != "ssh_auth_denied" {
		t.Fatalf("status = %+v", st)
	}
	for i := 0; i < 3; i++ {
		if _, err := p.Connector(context.Background(), n); !errors.Is(err, ErrSSHAuthDenied) {
			t.Fatalf("breaker err = %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("breaker did not skip connects: %d", calls.Load())
	}
	now = now.Add(16 * time.Second)
	fail.Store(false)
	if _, err := p.Connector(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if st := p.Status("n1"); st.State != PoolStateOpen || st.LastError != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestPoolInvalidateClosesAndResetsBreaker(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	p := testPool(t, func(ctx context.Context, uri string) (*Connector, error) {
		calls.Add(1)
		if fail.Load() {
			return nil, errors.New("connection refused")
		}
		return &Connector{uri: uri}, nil
	})
	n := remoteNode("n1")
	if _, err := p.Connector(context.Background(), n); !errors.Is(err, ErrNodeUnreachable) {
		t.Fatal(err)
	}
	fail.Store(false)
	p.Invalidate("n1") // clears the breaker immediately
	c1, err := p.Connector(context.Background(), n)
	if err != nil {
		t.Fatal(err)
	}
	p.Invalidate("n1")
	if st := p.Status("n1"); st.State != PoolStateClosed {
		t.Fatalf("status = %+v", st)
	}
	c2, err := p.Connector(context.Background(), n)
	if err != nil || c2 == c1 {
		t.Fatalf("invalidate must force a new connector: %v", err)
	}
	// Edited URI replaces the entry without an explicit Invalidate.
	n.URI = "qemu+ssh://root@elsewhere/system"
	c3, _ := p.Connector(context.Background(), n)
	if c3 == c2 {
		t.Fatal("URI change reused the old connector")
	}
	p.InvalidateAll()
	if p.Status("n1").State != PoolStateClosed {
		t.Fatal("InvalidateAll left the node open")
	}
}

func TestPoolInvalidateDuringConnectDiscardsResult(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	p := testPool(t, func(ctx context.Context, uri string) (*Connector, error) {
		close(started)
		<-release
		return &Connector{uri: uri}, nil
	})
	done := make(chan error, 1)
	go func() { _, err := p.Connector(context.Background(), remoteNode("n1")); done <- err }()
	<-started
	p.Invalidate("n1")
	close(release)
	if err := <-done; err == nil {
		t.Fatal("stale connect result was handed out")
	}
	if p.Status("n1").State == PoolStateOpen {
		t.Fatal("stale connection cached")
	}
}

func TestPoolConnectTimeoutAndCallerContext(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	p := testPool(t, func(ctx context.Context, uri string) (*Connector, error) {
		<-block
		return &Connector{uri: uri}, nil
	})
	p.connectTimeout = 100 * time.Millisecond
	_, err := p.Connector(context.Background(), remoteNode("n1"))
	if !errors.Is(err, ErrNodeTimeout) {
		t.Fatalf("err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	p.connectTimeout = time.Minute
	if _, err := p.Connector(ctx, remoteNode("n2")); !errors.Is(err, ErrNodeTimeout) {
		t.Fatalf("ctx err = %v", err)
	}
}

func TestPoolIdleEviction(t *testing.T) {
	p := testPool(t, func(ctx context.Context, uri string) (*Connector, error) { return &Connector{uri: uri}, nil })
	now := time.Now()
	p.now = func() time.Time { return now }
	if _, err := p.Connector(context.Background(), remoteNode("n1")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(9 * time.Minute)
	p.evictIdle()
	if p.Status("n1").State != PoolStateOpen {
		t.Fatal("evicted too early")
	}
	now = now.Add(2 * time.Minute)
	p.evictIdle()
	if p.Status("n1").State != PoolStateClosed {
		t.Fatal("idle connection not evicted")
	}
}

func TestPoolBuildURIErrorsAreTypedAndLocalSkipsBuild(t *testing.T) {
	built := 0
	p := NewPool(func(n nodes.Node) (string, error) { built++; return "", nodes.ErrHostKeyUnpinned })
	p.connectFn = func(ctx context.Context, uri string) (*Connector, error) { return &Connector{uri: uri}, nil }
	p.aliveFn = func(*Connector) bool { return true }
	t.Cleanup(p.Close)
	_, err := p.Connector(context.Background(), remoteNode("n1"))
	var ne *NodeError
	if !errors.Is(err, ErrHostKeyUnpinned) || !errors.As(err, &ne) || ne.Code() != "host_key_unpinned" || ne.HTTPStatus() != 409 {
		t.Fatalf("err = %v", err)
	}
	local := nodes.Node{ID: "local", URI: "qemu:///system", Type: nodes.NodeTypeLocal}
	c, err := p.Connector(context.Background(), local)
	if err != nil || c.uri != "qemu:///system" || built != 1 {
		t.Fatalf("local: %v %v built=%d", c, err, built)
	}
}

func TestPoolClose(t *testing.T) {
	p := testPool(t, func(ctx context.Context, uri string) (*Connector, error) { return &Connector{uri: uri}, nil })
	if _, err := p.Connector(context.Background(), remoteNode("n1")); err != nil {
		t.Fatal(err)
	}
	p.Close()
	p.Close()
	if _, err := p.Connector(context.Background(), remoteNode("n1")); err == nil {
		t.Fatal("closed pool handed out a connector")
	}
}

func TestClassifyNodeError(t *testing.T) {
	cases := []struct {
		err  error
		want *NodeError
	}{
		{errors.New("libvirt connect: ssh: connect to host x port 22: Connection refused"), ErrNodeUnreachable},
		{errors.New("operation timed out"), ErrNodeTimeout},
		{context.DeadlineExceeded, ErrNodeTimeout},
		{errors.New("authentication failed: Permission denied (publickey)"), ErrSSHAuthDenied},
		{errors.New("Host key verification failed."), ErrHostKeyUnpinned},
		{errors.New("REMOTE HOST IDENTIFICATION HAS CHANGED! offending key"), ErrHostKeyChanged},
		{errors.New("libvirt: unknown URI scheme: transport support was not enabled"), ErrTransportUnsupported},
		// Real libssh2 texts (live, libvirt 12 / libssh2 1.11).
		{errors.New("virError(Code=45, Domain=50, Message='authentication failed: !!! SSH HOST KEY VERIFICATION FAILED !!!: Identity of host '10.20.30.10:22' differs from stored identity. Please verify the new host key 'AAAA' to avoid possible man in the middle attack.')"), ErrHostKeyChanged},
		{errors.New("virError(Code=85, Domain=50, Message='SSH transport error: No user interaction callback provided: Can't verify the session host key')"), ErrHostKeyUnpinned},
		{errors.New("virError(Code=45, Domain=50, Message='authentication failed: Username/PublicKey combination invalid')"), ErrSSHAuthDenied},
		{errors.New("virError(Code=45, Domain=50, Message='authentication failed: Authentication failed (username/password)')"), ErrSSHAuthDenied},
		{errors.New("virError(Code=1, Domain=50, Message='libssh2 transport support was not enabled')"), ErrTransportUnsupported},
		{errors.New("virError(Code=38, Domain=50, Message='Failed to connect socket to 10.0.0.9:22: Connection refused')"), ErrLibvirtUnavailable},
		{errors.New("sh: 1: nc: not found; virt-ssh-helper: command not found"), ErrLibvirtUnavailable},
		{nodes.ErrNoKey, ErrNoSSHKey},
		{nodes.ErrHostKeyUnpinned, ErrHostKeyUnpinned},
		{nodes.ErrHostKeyChanged, ErrHostKeyChanged},
		{nodes.ErrHostTimeout, ErrNodeTimeout},
		{nodes.ErrHostUnreachable, ErrNodeUnreachable},
		{errors.New("something strange"), ErrNodeUnreachable},
	}
	for _, c := range cases {
		got := ClassifyNodeError(c.err)
		if !errors.Is(got, c.want) {
			t.Errorf("%v -> %v, want %v", c.err, got, c.want.Code())
		}
		if got.Error() != c.want.Error() {
			t.Errorf("message leaks raw text: %q", got.Error())
		}
	}
	for e, want := range map[*NodeError][2]any{
		ErrNodeUnreachable: {"node_unreachable", 502}, ErrNodeTimeout: {"node_timeout", 504},
		ErrSSHAuthDenied: {"ssh_auth_denied", 502}, ErrHostKeyUnpinned: {"host_key_unpinned", 409},
		ErrHostKeyChanged: {"host_key_changed", 409}, ErrTransportUnsupported: {"transport_unsupported", 501},
	} {
		if e.Code() != want[0] || e.HTTPStatus() != want[1] {
			t.Errorf("%v: %s/%d", e, e.Code(), e.HTTPStatus())
		}
	}
	if ClassifyNodeError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

func TestMigrateFlagsClientOrchestrated(t *testing.T) {
	if migrateFlagsClient(false, true) != migrateFlags(false, true) {
		t.Fatal("offline flags must be unchanged")
	}
	live := migrateFlagsClient(true, false)
	if live&migrateFlags(true, false) != live || live == migrateFlags(true, false) {
		t.Fatalf("client flags must be the p2p flags minus PEER2PEER: %b vs %b", live, migrateFlags(true, false))
	}
}

func TestWrappersUsePoolWhenRegistered(t *testing.T) {
	p := testPool(t, func(ctx context.Context, uri string) (*Connector, error) { return &Connector{uri: uri}, nil })
	SetDefaultPool(p)
	t.Cleanup(func() { SetDefaultPool(nil) })
	// The fake connector has no libvirt connection: the wrapper must go
	// through the pool (typed error) instead of dialing the URI itself.
	_, err := ListRemoteDomains("qemu+ssh://root@never-dialed.invalid/system")
	if !errors.Is(err, ErrNodeUnreachable) {
		t.Fatalf("err = %v", err)
	}
	if st := p.Status(uriNode("qemu+ssh://root@never-dialed.invalid/system").ID); st.State != PoolStateOpen {
		t.Fatalf("wrapper did not use the pool: %+v", st)
	}
	if DefaultPool() != p {
		t.Fatal("default pool")
	}
}

// NewConnector(uri, nil) must never dereference cfg.
func TestNilConfigConnectorIsSafe(t *testing.T) {
	c := NewConnector("test:///default", nil)
	c.EnsureDefaults() // connects to the in-process test driver, then must stop at cfg == nil
	c.Close()
	c2 := &Connector{}
	c2.migrateLegacyISOPool()
	var ps *PoolPurposeStore
	if ps.Get("x") != "" || ps.Set("x", "disk") != nil || ps.Delete("x") != nil || ps.Load() != nil || ps.Save() != nil {
		t.Fatal("nil purpose store must be a no-op")
	}
	if LoadCIFSSecrets(c2) != nil || LoadCHAPSecrets(c2) != nil {
		t.Fatal("secrets loaders")
	}
	if _, err := c2.RefreshCIFSSecretIfNeeded(context.Background(), "p"); err == nil {
		t.Fatal("expected error, not panic")
	}
}

type recHandler struct {
	mu   sync.Mutex
	recs []slog.Record
}

func (h *recHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *recHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.recs = append(h.recs, r)
	h.mu.Unlock()
	return nil
}
func (h *recHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *recHandler) WithGroup(string) slog.Handler      { return h }
func (h *recHandler) count(msg string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, r := range h.recs {
		if r.Message == msg {
			n++
		}
	}
	return n
}

func TestPoolLogsSlowRemoteCalls(t *testing.T) {
	rec := &recHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(rec))
	defer slog.SetDefault(old)

	p := NewPool(nil)
	defer p.Close()
	if p.slow != 3*time.Second {
		t.Fatalf("slow threshold = %v, want 3s", p.slow)
	}
	p.ObserveCall("n1", "request", 2999*time.Millisecond)
	if rec.count("libvirt_remote_slow") != 0 || p.SlowCalls("n1") != 0 {
		t.Fatal("a fast call must not be logged")
	}
	p.ObserveCall("n1", "request", 3*time.Second)
	p.ObserveCall("n1", "request", 5003*time.Millisecond)
	if rec.count("libvirt_remote_slow") != 2 || p.SlowCalls("n1") != 2 || p.SlowCalls("n2") != 0 {
		t.Fatalf("logged=%d slow=%d", rec.count("libvirt_remote_slow"), p.SlowCalls("n1"))
	}
	var nilPool *Pool
	nilPool.ObserveCall("n", "x", time.Hour) // must not panic
}

// v11 live defect: Ubuntu 26.04's libvirt client is built without libssh2.
// The default (libssh2) must not leave the node unusable: the first
// transport_unsupported failure is classified, cached process-wide, and the
// same connect is retried once with libssh; later connects go straight to it.
func TestPoolFallsBackFromUnsupportedLibSSH2(t *testing.T) {
	nodes.ResetTransportSupport()
	t.Cleanup(nodes.ResetTransportSupport)
	var dials []string
	var mu sync.Mutex
	p := NewPool(func(n nodes.Node) (string, error) {
		tr, _ := nodes.EffectiveTransport(nodes.TransportLibSSH2)
		return "qemu+" + string(tr) + "://root@" + n.ID + "/system", nil
	})
	p.connectFn = func(_ context.Context, uri string) (*Connector, error) {
		mu.Lock()
		dials = append(dials, uri)
		mu.Unlock()
		if uri[:13] == "qemu+libssh2:" {
			return nil, errors.New("libssh2 transport support was not enabled: Function not implemented")
		}
		return &Connector{uri: uri}, nil
	}
	p.aliveFn = func(*Connector) bool { return true }
	t.Cleanup(p.Close)
	c, err := p.Connector(context.Background(), remoteNode("n1"))
	if err != nil || c == nil || c.uri != "qemu+libssh://root@n1/system" {
		t.Fatalf("c = %+v err = %v dials = %v", c, err, dials)
	}
	if len(dials) != 2 || !nodes.TransportUnsupported(nodes.TransportLibSSH2) {
		t.Fatalf("want one libssh2 attempt then libssh: %v", dials)
	}
	if _, err := p.Connector(context.Background(), remoteNode("n2")); err != nil || len(dials) != 3 || dials[2] != "qemu+libssh://root@n2/system" {
		t.Fatalf("cached: later nodes must go straight to libssh: %v %v", err, dials)
	}
}

// An explicit libssh/ssh choice that the client lacks is not rewritten: the
// typed transport_unsupported error is returned (operator override).
func TestPoolExplicitTransportIsNotRewritten(t *testing.T) {
	nodes.ResetTransportSupport()
	t.Cleanup(nodes.ResetTransportSupport)
	calls := 0
	p := NewPool(func(n nodes.Node) (string, error) { return "qemu+libssh://root@" + n.ID + "/system", nil })
	p.connectFn = func(_ context.Context, uri string) (*Connector, error) {
		calls++
		return nil, errors.New("libssh transport support was not enabled")
	}
	t.Cleanup(p.Close)
	_, err := p.Connector(context.Background(), remoteNode("n1"))
	if !errors.Is(err, ErrTransportUnsupported) || calls != 1 {
		t.Fatalf("err = %v calls = %d", err, calls)
	}
}
