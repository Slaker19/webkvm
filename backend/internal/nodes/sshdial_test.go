package nodes

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// fwdSSHServer is an in-process SSH node: pubkey auth against one allowed
// key and direct-tcpip forwarding restricted to 127.0.0.1 (permitopen).
type fwdSSHServer struct {
	ln      net.Listener
	host    string
	port    int
	signer  ssh.Signer
	allowed ssh.PublicKey
	mu      sync.Mutex
	conns   []net.Conn
	accepts atomic.Int32
}

func startFwdSSH(t *testing.T, allowed ssh.PublicKey) *fwdSSHServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &fwdSSHServer{ln: ln, signer: newSigner(t), allowed: allowed}
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	s.host = h
	s.port, _ = strconv.Atoi(p)
	t.Cleanup(func() { _ = ln.Close(); s.dropAll() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepts.Add(1)
			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.mu.Unlock()
			go s.serve(c)
		}
	}()
	return s
}

func (s *fwdSSHServer) set(sg ssh.Signer, al ssh.PublicKey) {
	s.mu.Lock()
	s.signer, s.allowed = sg, al
	s.mu.Unlock()
}

func (s *fwdSSHServer) dropAll() {
	s.mu.Lock()
	for _, c := range s.conns {
		_ = c.Close()
	}
	s.conns = nil
	s.mu.Unlock()
}

func (s *fwdSSHServer) serve(c net.Conn) {
	s.mu.Lock()
	signer, allowed := s.signer, s.allowed
	s.mu.Unlock()
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if allowed != nil && string(k.Marshal()) == string(allowed.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	cfg.AddHostKey(signer)
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		_ = c.Close()
		return
	}
	go func() {
		for r := range reqs {
			if r.WantReply {
				_ = r.Reply(false, nil)
			}
		}
	}()
	for nc := range chans {
		if nc.ChannelType() != "direct-tcpip" {
			_ = nc.Reject(ssh.UnknownChannelType, "no")
			continue
		}
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil || p.Host != "127.0.0.1" {
			_ = nc.Reject(ssh.Prohibited, "permitopen")
			continue
		}
		go func(nc ssh.NewChannel, addr string) {
			up, err := net.Dial("tcp", addr)
			if err != nil {
				_ = nc.Reject(ssh.ConnectionFailed, err.Error())
				return
			}
			ch, chReqs, err := nc.Accept()
			if err != nil {
				_ = up.Close()
				return
			}
			go ssh.DiscardRequests(chReqs)
			go func() { _, _ = io.Copy(ch, up); _ = ch.CloseWrite() }()
			_, _ = io.Copy(up, ch)
			_ = up.Close()
			_ = ch.Close()
		}(nc, net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	}
	_ = sc.Close()
}

// echoServer is a fake VNC backend: echoes every byte.
func startEcho(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

type dialFixture struct {
	store *SSHKeyStore
	srv   *fwdSSHServer
	d     *SSHDialer
	echo  string
}

func newDialFixture(t *testing.T, pin bool) *dialFixture {
	t.Helper()
	store := NewSSHKeyStore(t.TempDir())
	info, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := parsePub(info.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	srv := startFwdSSH(t, pub)
	if pin {
		if err := store.PinHostKey(srv.host, srv.port, ssh.FingerprintSHA256(srv.signer.PublicKey()), false); err != nil {
			t.Fatal(err)
		}
	}
	d, err := NewSSHDialer(store, Node{ID: "n1", URI: "qemu+ssh://root@" + srv.host + ":" + strconv.Itoa(srv.port) + "/system"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	srv.accepts.Store(0) // ignore the host key capture done while pinning
	return &dialFixture{store: store, srv: srv, d: d, echo: "127.0.0.1:" + strconv.Itoa(startEcho(t))}
}

func roundTrip(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(c, buf); err != nil || string(buf) != msg {
		t.Fatalf("echo %q err %v", buf, err)
	}
}

func TestSSHDialerPinnedForwardsAndReuses(t *testing.T) {
	f := newDialFixture(t, true)
	for i := 0; i < 3; i++ {
		c, err := f.d.DialTCP(context.Background(), f.echo)
		if err != nil {
			t.Fatal(err)
		}
		roundTrip(t, c, "hello")
		_ = c.Close()
	}
	if n := f.srv.accepts.Load(); n != 1 {
		t.Fatalf("expected one reused SSH connection, got %d", n)
	}
}

func TestSSHDialerHostKeyUnpinnedFailsClosed(t *testing.T) {
	f := newDialFixture(t, false)
	_, err := f.d.DialTCP(context.Background(), f.echo)
	if !errors.Is(err, ErrHostKeyUnpinned) {
		t.Fatalf("got %v", err)
	}
}

func TestSSHDialerHostKeyChanged(t *testing.T) {
	f := newDialFixture(t, true)
	f.srv.set(newSigner(t), f.srv.allowed) // host key rotated after pinning
	_, err := f.d.DialTCP(context.Background(), f.echo)
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("got %v", err)
	}
}

func TestSSHDialerAuthDenied(t *testing.T) {
	f := newDialFixture(t, true)
	f.srv.set(f.srv.signer, newSigner(t).PublicKey()) // not the cluster key
	_, err := f.d.DialTCP(context.Background(), f.echo)
	if !errors.Is(err, ErrSSHAuthDenied) {
		t.Fatalf("got %v", err)
	}
}

func TestSSHDialerNoKey(t *testing.T) {
	store := NewSSHKeyStore(t.TempDir())
	d, _ := NewSSHDialer(store, Node{URI: "qemu+ssh://root@127.0.0.1:1/system"})
	if _, err := d.DialTCP(context.Background(), "127.0.0.1:1"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("got %v", err)
	}
}

func TestSSHDialerForwardOutsidePermitopenDenied(t *testing.T) {
	f := newDialFixture(t, true)
	_, err := f.d.DialTCP(context.Background(), "10.9.9.9:5900")
	if !errors.Is(err, ErrForwardDenied) {
		t.Fatalf("got %v", err)
	}
}

func TestSSHDialerReconnectsAfterServerDrop(t *testing.T) {
	f := newDialFixture(t, true)
	c, err := f.d.DialTCP(context.Background(), f.echo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	f.srv.dropAll()
	c, err = f.d.DialTCP(context.Background(), f.echo)
	if err != nil {
		t.Fatalf("no reconnect: %v", err)
	}
	roundTrip(t, c, "again")
	_ = c.Close()
	if n := f.srv.accepts.Load(); n != 2 {
		t.Fatalf("accepts=%d", n)
	}
}

func TestSSHDialerForwardLimit(t *testing.T) {
	f := newDialFixture(t, true)
	f.d.maxForwards = 3
	var open []net.Conn
	for i := 0; i < 3; i++ {
		c, err := f.d.DialTCP(context.Background(), f.echo)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, c)
	}
	if _, err := f.d.DialTCP(context.Background(), f.echo); !errors.Is(err, ErrTooManyForwards) {
		t.Fatalf("got %v", err)
	}
	_ = open[0].Close()
	_ = open[0].Close() // double close must not free two slots
	c, err := f.d.DialTCP(context.Background(), f.echo)
	if err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	if _, err := f.d.DialTCP(context.Background(), f.echo); !errors.Is(err, ErrTooManyForwards) {
		t.Fatalf("double close leaked a slot: %v", err)
	}
	_ = c.Close()
	for _, o := range open[1:] {
		_ = o.Close()
	}
}

func TestSSHDialerIdleCloseAndReopen(t *testing.T) {
	f := newDialFixture(t, true)
	f.d.idleTimeout = 50 * time.Millisecond
	c, err := f.d.DialTCP(context.Background(), f.echo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.d.mu.Lock()
		gone := f.d.client == nil
		f.d.mu.Unlock()
		if gone {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle client not closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	c, err = f.d.DialTCP(context.Background(), f.echo)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Close()
}

func TestSSHDialerConcurrentUse(t *testing.T) {
	f := newDialFixture(t, true)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				c, err := f.d.DialTCP(context.Background(), f.echo)
				if err != nil {
					t.Error(err)
					return
				}
				roundTrip(t, c, "x"+strconv.Itoa(i))
				_ = c.Close()
			}
		}(i)
	}
	wg.Wait()
	if n := f.srv.accepts.Load(); n != 1 {
		t.Fatalf("concurrent dials opened %d SSH connections", n)
	}
}

func TestSSHDialerClosed(t *testing.T) {
	f := newDialFixture(t, true)
	_ = f.d.Close()
	if _, err := f.d.DialTCP(context.Background(), f.echo); !errors.Is(err, ErrDialerClosed) {
		t.Fatalf("got %v", err)
	}
}
