package nodes

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Errors specific to the SSH dialer (host key / no-key errors are shared
// with the key store).
var (
	ErrSSHAuthDenied   = errors.New("the node rejected the cluster SSH key")
	ErrTooManyForwards = errors.New("too many concurrent SSH forwards to this node")
	ErrForwardDenied   = errors.New("the node refused the port forward")
	ErrDialerClosed    = errors.New("SSH dialer is closed")
)

const (
	sshDialTimeout        = 10 * time.Second
	sshIdleTimeout        = 2 * time.Minute
	sshKeepAliveEvery     = 30 * time.Second
	sshKeepAliveTimeout   = 15 * time.Second
	sshMaxForwardsPerNode = 16
)

// SSHDialer opens TCP connections on a managed node through one shared,
// long-lived SSH client (direct-tcpip channels). The host key must be pinned
// in the cluster known_hosts and authentication uses the cluster key; there
// is no insecure fallback. Safe for concurrent use.
type SSHDialer struct {
	store *SSHKeyStore
	// keyName is the store file holding the private key (active by default).
	keyName string
	user    string
	host    string
	port    int

	dial          func(ctx context.Context, network, addr string) (net.Conn, error)
	idleTimeout   time.Duration
	keepAlive     time.Duration
	maxForwards   int
	connectLimit  time.Duration
	keepAliveWait time.Duration

	mu      sync.Mutex
	client  *ssh.Client
	gen     int
	active  int
	idle    *time.Timer
	closed  bool
	connect sync.Mutex // serializes (re)connects
}

// NewSSHDialer builds a dialer for a qemu+ssh node. Nothing is contacted yet.
func NewSSHDialer(store *SSHKeyStore, node Node) (*SSHDialer, error) {
	if store == nil {
		return nil, ErrNoKey
	}
	user, host, port, err := NodeSSHTarget(node.URI)
	if err != nil {
		return nil, err
	}
	d := &net.Dialer{Timeout: sshDialTimeout}
	return &SSHDialer{
		store: store, keyName: sshKeyFile, user: user, host: host, port: port,
		dial:        d.DialContext,
		idleTimeout: sshIdleTimeout, keepAlive: sshKeepAliveEvery, keepAliveWait: sshKeepAliveTimeout,
		maxForwards: sshMaxForwardsPerNode, connectLimit: sshDialTimeout,
	}, nil
}

// Host returns the node address this dialer connects to.
func (d *SSHDialer) Host() string { return d.host }

// DialTCP opens a connection to addr as seen FROM the node (normally
// 127.0.0.1:port). The connection must be closed by the caller.
func (d *SSHDialer) DialTCP(ctx context.Context, addr string) (net.Conn, error) {
	if err := d.acquire(); err != nil {
		return nil, err
	}
	conn, err := d.dialOnce(ctx, addr)
	if errors.Is(err, errStaleClient) && ctx.Err() == nil {
		// The shared client was stale: reconnect once.
		conn, err = d.dialOnce(ctx, addr)
	}
	if err != nil {
		d.release()
		if errors.Is(err, errStaleClient) {
			err = ErrHostUnreachable
		}
		return nil, err
	}
	return &forwardConn{Conn: conn, d: d}, nil
}

var errStaleClient = errors.New("stale SSH client")

func (d *SSHDialer) acquire() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return ErrDialerClosed
	}
	if d.active >= d.maxForwards {
		return ErrTooManyForwards
	}
	d.active++
	if d.idle != nil {
		d.idle.Stop()
		d.idle = nil
	}
	return nil
}

func (d *SSHDialer) release() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.active--
	if d.active == 0 && d.client != nil && !d.closed {
		gen := d.gen
		d.idle = time.AfterFunc(d.idleTimeout, func() { d.dropIfIdle(gen) })
	}
}

func (d *SSHDialer) dropIfIdle(gen int) {
	d.mu.Lock()
	var c *ssh.Client
	if d.gen == gen && d.active == 0 {
		c, d.client = d.client, nil
	}
	d.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// Close drops the SSH client and refuses further dials.
func (d *SSHDialer) Close() error {
	d.mu.Lock()
	d.closed = true
	c := d.client
	d.client = nil
	if d.idle != nil {
		d.idle.Stop()
		d.idle = nil
	}
	d.mu.Unlock()
	if c != nil {
		return c.Close()
	}
	return nil
}

func (d *SSHDialer) dialOnce(ctx context.Context, addr string) (net.Conn, error) {
	c, gen, err := d.getClient(ctx)
	if err != nil {
		return nil, err
	}
	type res struct {
		c   net.Conn
		err error
	}
	ch := make(chan res, 1)
	go func() {
		conn, err := c.Dial("tcp", addr)
		ch <- res{conn, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			var oce *ssh.OpenChannelError
			if errors.As(r.err, &oce) {
				return nil, ErrForwardDenied
			}
			d.discard(gen)
			return nil, errStaleClient
		}
		return r.c, nil
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				_ = r.c.Close()
			}
		}()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, ErrHostTimeout
		}
		return nil, ctx.Err()
	}
}

// discard forgets the client of generation gen (it failed).
func (d *SSHDialer) discard(gen int) {
	d.mu.Lock()
	var c *ssh.Client
	if d.gen == gen {
		c, d.client = d.client, nil
	}
	d.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

func (d *SSHDialer) getClient(ctx context.Context) (*ssh.Client, int, error) {
	d.mu.Lock()
	if d.client != nil {
		c, g := d.client, d.gen
		d.mu.Unlock()
		return c, g, nil
	}
	d.mu.Unlock()

	d.connect.Lock()
	defer d.connect.Unlock()
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, 0, ErrDialerClosed
	}
	if d.client != nil {
		c, g := d.client, d.gen
		d.mu.Unlock()
		return c, g, nil
	}
	d.mu.Unlock()

	c, err := d.newClient(ctx)
	if err != nil {
		return nil, 0, err
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		_ = c.Close()
		return nil, 0, ErrDialerClosed
	}
	d.gen++
	gen := d.gen
	d.client = c
	d.mu.Unlock()
	go d.watch(c, gen)
	return c, gen, nil
}

// watch keeps the client alive and forgets it when the connection ends.
func (d *SSHDialer) watch(c *ssh.Client, gen int) {
	done := make(chan struct{})
	go func() {
		_ = c.Wait()
		close(done)
	}()
	t := time.NewTicker(d.keepAlive)
	defer t.Stop()
	for {
		select {
		case <-done:
			d.discard(gen)
			return
		case <-t.C:
			ok := make(chan error, 1)
			go func() {
				_, _, err := c.SendRequest("keepalive@openssh.com", true, nil)
				ok <- err
			}()
			select {
			case err := <-ok:
				if err != nil {
					d.discard(gen)
					return
				}
			case <-time.After(d.keepAliveWait):
				d.discard(gen)
				return
			case <-done:
				d.discard(gen)
				return
			}
		}
	}
}

// verifyHostKey requires the key to be pinned: no entry -> unpinned, a
// different entry -> changed.
func (s *SSHKeyStore) verifyHostKey(host string, port int, key ssh.PublicKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readKnownHosts(host, port)
	if err != nil {
		return ErrHostKeyUnpinned
	}
	found := false
	raw := key.Marshal()
	for _, e := range entries {
		if !e.host {
			continue
		}
		found = true
		if e.typ == key.Type() && bytes.Equal(e.key, raw) {
			return nil
		}
	}
	if found {
		return ErrHostKeyChanged
	}
	return ErrHostKeyUnpinned
}

func (d *SSHDialer) signer() (ssh.Signer, error) {
	d.store.mu.Lock()
	b, err := d.store.readFile(d.keyName, true)
	d.store.mu.Unlock()
	if err != nil {
		return nil, ErrNoKey
	}
	return ssh.ParsePrivateKey(b)
}

func (d *SSHDialer) newClient(ctx context.Context) (*ssh.Client, error) {
	sg, err := d.signer()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, d.connectLimit)
	defer cancel()
	target := net.JoinHostPort(d.host, strconv.Itoa(d.port))
	conn, err := d.dial(ctx, "tcp", target)
	if err != nil {
		return nil, classifyDialErr(ctx, err)
	}
	var hostErr error
	cfg := &ssh.ClientConfig{
		User: d.user,
		Auth: []ssh.AuthMethod{ssh.PublicKeys(sg)},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			hostErr = d.store.verifyHostKey(d.host, d.port, key)
			return hostErr
		},
		Timeout: d.connectLimit,
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	sc, chans, reqs, err := ssh.NewClientConn(conn, target, cfg)
	if !stop() && err == nil {
		// Context ended concurrently with a successful handshake.
		_ = sc.Close()
		return nil, classifyDialErr(ctx, ctx.Err())
	}
	if err != nil {
		_ = conn.Close()
		switch {
		case hostErr != nil:
			return nil, hostErr
		case strings.Contains(err.Error(), "unable to authenticate"):
			return nil, ErrSSHAuthDenied
		}
		return nil, classifyDialErr(ctx, err)
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(sc, chans, reqs), nil
}

// forwardConn releases the forward slot exactly once on Close.
type forwardConn struct {
	net.Conn
	d    *SSHDialer
	once sync.Once
}

func (f *forwardConn) Close() error {
	err := f.Conn.Close()
	f.once.Do(f.d.release)
	return err
}

// ProbePendingKey opens (and immediately closes) one SSH connection to the
// node's SSH endpoint authenticating with the PENDING rotation key and the
// pinned host key. It touches no pooled connection and no libvirt: it only
// proves that the node would accept the new key. Errors are the typed ones
// of this package (ErrSSHAuthDenied, ErrHostKeyUnpinned, ErrHostKeyChanged,
// ErrHostTimeout, ErrHostUnreachable) or ErrNoPending.
func (s *SSHKeyStore) ProbePendingKey(ctx context.Context, node Node) error {
	d, err := NewSSHDialer(s, node)
	if err != nil {
		return err
	}
	d.keyName = sshPendingKey
	d.dial = s.dial
	s.mu.Lock()
	_, perr := s.readFile(sshPendingKey, true)
	s.mu.Unlock()
	if perr != nil {
		return ErrNoPending
	}
	defer func() { _ = d.Close() }()
	c, err := d.newClient(ctx)
	if err != nil {
		return err
	}
	return c.Close()
}
