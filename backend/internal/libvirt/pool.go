package libvirt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"webkvm/internal/nodes"
	"webkvm/internal/safego"
)

// Pool tunables. Variables so tests can shorten them.
const (
	poolConnectTimeout = 15 * time.Second
	poolBreakerWindow  = 15 * time.Second
	poolIdleTimeout    = 10 * time.Minute
	// poolSlowCall is the reply time above which a call to a node is logged
	// as libvirt_remote_slow (the libssh transport used to stall 5 s on every
	// reply > 4 KB; this keeps that class of problem visible in the logs).
	poolSlowCall = 3 * time.Second
)

// Pool states reported by Status.
const (
	PoolStateOpen    = "open"
	PoolStateClosed  = "closed"
	PoolStateFailing = "failing"
)

// PoolNodeStatus is the observable state of one node's pooled connection.
type PoolNodeStatus struct {
	State     string    `json:"state"`
	LastError string    `json:"last_error,omitempty"` // typed error code, never raw text
	LastOK    time.Time `json:"last_ok"`
}

type poolEntry struct {
	c        *Connector
	uri      string // stored (plain) node URI the entry was built from
	lastUsed time.Time
}

type poolFailure struct {
	at  time.Time
	err error
}

type poolCall struct {
	done chan struct{}
	c    *Connector
	err  error
}

// Pool keeps ONE long-lived Connector per node ID. Connects are
// single-flighted and bounded, a failing node is skipped for a short window
// (circuit breaker) and idle connections are evicted.
type Pool struct {
	buildURI  func(nodes.Node) (string, error)
	connectFn func(ctx context.Context, uri string) (*Connector, error)
	aliveFn   func(*Connector) bool
	now       func() time.Time

	connectTimeout time.Duration
	breaker        time.Duration
	idle           time.Duration
	slow           time.Duration

	mu       sync.Mutex
	entries  map[string]*poolEntry
	inflight map[string]*poolCall
	failed   map[string]poolFailure
	lastOK   map[string]time.Time
	gen      map[string]int      // bumped by Invalidate so stale connects are discarded
	slowN    map[string]int      // slow calls seen per node
	warned   map[string]poolWarn // last connect-failure WARN per node (log de-duplication)
	closed   bool
	stop     chan struct{}
}

// NewPool builds a pool. buildURI turns a stored node into the URI handed to
// libvirt (see nodes.BuildConnURI).
func NewPool(buildURI func(nodes.Node) (string, error)) *Pool {
	p := &Pool{
		buildURI:       buildURI,
		connectFn:      defaultPoolConnect,
		aliveFn:        func(c *Connector) bool { return c.IsConnected() },
		now:            time.Now,
		connectTimeout: poolConnectTimeout,
		breaker:        poolBreakerWindow,
		idle:           poolIdleTimeout,
		slow:           poolSlowCall,
		slowN:          map[string]int{},
		entries:        map[string]*poolEntry{},
		inflight:       map[string]*poolCall{},
		failed:         map[string]poolFailure{},
		lastOK:         map[string]time.Time{},
		gen:            map[string]int{},
		stop:           make(chan struct{}),
	}
	go p.janitor()
	return p
}

// SetConnectFunc replaces the function that opens connections. It exists so
// tests of other packages can run the pool without libvirt; alive reports
// whether a pooled connector is still usable (nil keeps the default).
func (p *Pool) SetConnectFunc(fn func(ctx context.Context, uri string) (*Connector, error), alive func(*Connector) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.connectFn = fn
	if alive != nil {
		p.aliveFn = alive
	}
}

// NewTestConnector returns a Connector with no libvirt connection, for tests
// that exercise pool wiring from other packages.
func NewTestConnector(uri string) *Connector { return &Connector{uri: uri} }

func defaultPoolConnect(_ context.Context, uri string) (*Connector, error) {
	c := NewConnector(uri, nil)
	c.keepAlive = true
	if err := c.Open(); err != nil {
		return nil, err
	}
	return c, nil
}

func (p *Pool) janitor() {
	defer safego.Recover("libvirt_pool_janitor")
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.evictIdle()
		}
	}
}

// evictIdle closes connections unused for the idle timeout.
func (p *Pool) evictIdle() {
	p.mu.Lock()
	var drop []*Connector
	for id, e := range p.entries {
		if p.now().Sub(e.lastUsed) >= p.idle {
			drop = append(drop, e.c)
			delete(p.entries, id)
		}
	}
	p.mu.Unlock()
	for _, c := range drop {
		c.Close()
	}
}

// Connector returns the node's live connector, connecting (once, however
// many callers) when needed. Failures are typed (see ErrNodeUnreachable...)
// and a failing node answers instantly during the breaker window.
func (p *Pool) Connector(ctx context.Context, n nodes.Node) (*Connector, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, ErrNodeUnreachable
	}
	if f, ok := p.failed[n.ID]; ok {
		if p.now().Sub(f.at) < p.breaker {
			p.mu.Unlock()
			return nil, f.err
		}
		delete(p.failed, n.ID)
	}
	if e, ok := p.entries[n.ID]; ok {
		if e.uri == n.URI && p.aliveFn(e.c) {
			e.lastUsed = p.now()
			c := e.c
			p.mu.Unlock()
			return c, nil
		}
		// Edited URI or dead connection: replace it.
		delete(p.entries, n.ID)
		go e.c.Close()
	}
	call, running := p.inflight[n.ID]
	if !running {
		call = &poolCall{done: make(chan struct{})}
		p.inflight[n.ID] = call
		go p.connect(n, call, p.gen[n.ID])
	}
	p.mu.Unlock()

	select {
	case <-call.done:
		return call.c, call.err
	case <-ctx.Done():
		return nil, ClassifyNodeError(ctx.Err())
	}
}

// ObserveCall records how long a call to a node took and logs WARN
// libvirt_remote_slow when it exceeded the threshold (3 s). It never blocks
// or fails the call; op is a short fixed label ("connect", "request"...).
func (p *Pool) ObserveCall(nodeID, op string, d time.Duration) {
	if p == nil || d < p.slow {
		return
	}
	p.mu.Lock()
	p.slowN[nodeID]++
	n := p.slowN[nodeID]
	p.mu.Unlock()
	slog.Warn("libvirt_remote_slow", "node", nodeID, "op", op, "ms", d.Milliseconds(), "slow_calls", n)
}

// SlowCalls reports how many slow calls were observed for a node.
func (p *Pool) SlowCalls(nodeID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.slowN[nodeID]
}

func (p *Pool) connect(n nodes.Node, call *poolCall, gen int) {
	defer safego.Recover("libvirt_pool_connect")
	started := time.Now() // real time: p.now may be a test clock
	defer func() { p.ObserveCall(n.ID, "connect", time.Since(started)) }()
	uri, err := p.resolveURI(n)
	var c *Connector
	if err == nil {
		c, err = p.dial(uri)
	}
	if err != nil {
		err = ClassifyNodeError(err)
	}
	// The local libvirt client may be built without libssh2 (Ubuntu 26.04):
	// classify, cache process-wide, and retry this connect once with the URI
	// rebuilt for the effective transport (libssh, see
	// nodes.EffectiveTransport). An explicit libssh/ssh choice is not
	// rewritten, so its typed error stands.
	if errors.Is(err, ErrTransportUnsupported) && nodes.TransportOfURI(uri) == nodes.TransportLibSSH2 {
		if nodes.MarkTransportUnsupported(nodes.TransportLibSSH2) {
			slog.Warn("libvirt_transport_fallback", "from", string(nodes.TransportLibSSH2), "to", string(nodes.TransportLibSSH),
				"reason", "transport_unsupported", "hint", "the local libvirt client lacks libssh2; set cluster.libvirt_transport to choose explicitly")
		}
		if retry, rerr := p.resolveURI(n); rerr == nil && retry != uri {
			uri = retry
			c, err = p.dial(uri)
			if err != nil {
				err = ClassifyNodeError(err)
			}
		}
	}

	p.mu.Lock()
	delete(p.inflight, n.ID)
	switch {
	case p.closed || p.gen[n.ID] != gen:
		// Invalidated or closed while connecting: do not cache the result.
		if c != nil {
			go c.Close()
			c = nil
		}
		if err == nil {
			err = ErrNodeUnreachable
		}
	case err != nil:
		p.failed[n.ID] = poolFailure{at: p.now(), err: err}
	default:
		now := p.now()
		p.entries[n.ID] = &poolEntry{c: c, uri: n.URI, lastUsed: now}
		p.lastOK[n.ID] = now
		delete(p.failed, n.ID)
		delete(p.warned, n.ID) // a later failure is news again
	}
	call.c, call.err = c, err
	p.mu.Unlock()
	close(call.done)
	if err != nil {
		code := errCode(err)
		if p.shouldWarn(n.ID, code) {
			slog.Warn("libvirt_pool_connect_failed", "node", n.ID, "code", code)
		} else {
			slog.Debug("libvirt_pool_connect_failed", "node", n.ID, "code", code)
		}
	}
}

// poolWarnRepeat is how long an unchanged connect failure of one node stays
// quiet (DEBUG) after its WARN: a node whose access is simply not set up yet is
// probed every few seconds and must not flood the log (36 identical WARNs in
// 6 minutes were seen live) or hide other problems.
const poolWarnRepeat = 10 * time.Minute

type poolWarn struct {
	code string
	at   time.Time
}

// shouldWarn reports whether this failure deserves a WARN: the first one, a
// change of failure code, or the same one again after poolWarnRepeat.
func (p *Pool) shouldWarn(node, code string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.warned == nil {
		p.warned = map[string]poolWarn{}
	}
	now := p.now()
	if w, ok := p.warned[node]; ok && w.code == code && now.Sub(w.at) < poolWarnRepeat {
		return false
	}
	p.warned[node] = poolWarn{code: code, at: now}
	return true
}

func errCode(err error) string {
	var ne *NodeError
	if errors.As(err, &ne) {
		return ne.Code()
	}
	return "error"
}

func (p *Pool) resolveURI(n nodes.Node) (string, error) {
	if n.IsLocal() {
		return n.URI, nil
	}
	if p.buildURI == nil {
		return "", ErrNodeUnreachable
	}
	return p.buildURI(n)
}

// dial runs connectFn bounded by the connect timeout (libvirt's connect has
// no timeout of its own); a connector that finishes late is closed.
func (p *Pool) dial(uri string) (*Connector, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.connectTimeout)
	defer cancel()
	type res struct {
		c   *Connector
		err error
	}
	ch := make(chan res, 1)
	p.mu.Lock()
	fn := p.connectFn
	p.mu.Unlock()
	go func() {
		defer safego.Recover("libvirt_pool_dial")
		c, err := fn(ctx, uri)
		ch <- res{c, err}
	}()
	select {
	case r := <-ch:
		return r.c, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ErrNodeTimeout
	}
}

// Invalidate closes and forgets a node's connection and failure state, e.g.
// after the node was edited, disabled or deleted, or its key rotated.
func (p *Pool) Invalidate(nodeID string) {
	p.mu.Lock()
	e := p.entries[nodeID]
	delete(p.entries, nodeID)
	delete(p.failed, nodeID)
	p.gen[nodeID]++
	p.mu.Unlock()
	if e != nil {
		e.c.Close()
	}
}

// InvalidateAll invalidates every node (used after SSH key rotation).
func (p *Pool) InvalidateAll() {
	p.mu.Lock()
	ids := map[string]struct{}{}
	for id := range p.entries {
		ids[id] = struct{}{}
	}
	for id := range p.failed {
		ids[id] = struct{}{}
	}
	p.mu.Unlock()
	for id := range ids {
		p.Invalidate(id)
	}
}

// Close shuts the pool down and closes every connection.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	close(p.stop)
	var cs []*Connector
	for id, e := range p.entries {
		cs = append(cs, e.c)
		delete(p.entries, id)
	}
	p.mu.Unlock()
	for _, c := range cs {
		c.Close()
	}
}

// Status reports a node's pooled-connection state.
func (p *Pool) Status(nodeID string) PoolNodeStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := PoolNodeStatus{State: PoolStateClosed, LastOK: p.lastOK[nodeID]}
	if f, ok := p.failed[nodeID]; ok && p.now().Sub(f.at) < p.breaker {
		st.State, st.LastError = PoolStateFailing, errCode(f.err)
		return st
	}
	if _, ok := p.entries[nodeID]; ok {
		st.State = PoolStateOpen
	}
	return st
}

// MigrateDomain moves a domain between two nodes, CLIENT-ORCHESTRATED: this
// process talks to both libvirtds through pooled connectors, so no
// node-to-node SSH is needed (source and destination only need to reach each
// other's migration data port). Works in any direction, including to/from
// the local node.
func (p *Pool) MigrateDomain(ctx context.Context, src, dst nodes.Node, id string, live, sharedStorage bool) error {
	s, err := p.Connector(ctx, src)
	if err != nil {
		return err
	}
	d, err := p.Connector(ctx, dst)
	if err != nil {
		return err
	}
	return s.MigrateDomainVia(ctx, d, id, live, sharedStorage, MigrationURI(dst))
}

var defaultPool atomic.Pointer[Pool]

// SetDefaultPool registers the process-wide pool used by the URI-based
// federation wrappers. nil unregisters it (legacy per-call connections).
func SetDefaultPool(p *Pool) { defaultPool.Store(p) }

// DefaultPool returns the registered pool or nil.
func DefaultPool() *Pool { return defaultPool.Load() }

// uriNode wraps a plain URI as a pool key for URI-based wrappers.
func uriNode(uri string) nodes.Node {
	h := sha256.Sum256([]byte(uri))
	return nodes.Node{ID: "uri-" + hex.EncodeToString(h[:8]), URI: uri, Type: nodes.NodeTypeRemote, Enabled: true}
}
