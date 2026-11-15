package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// --- in-process node: SSH server (direct-tcpip, permitopen 127.0.0.1) -----------

type fwdNode struct {
	ln     net.Listener
	host   string
	port   int
	signer ssh.Signer
	mu     sync.Mutex
	conns  []net.Conn
}

func startFwdNode(t *testing.T, allowed ssh.PublicKey) *fwdNode {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	n := &fwdNode{ln: ln, signer: newAPISigner(t)}
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	n.host = h
	n.port, _ = strconv.Atoi(p)
	t.Cleanup(func() {
		_ = ln.Close()
		n.mu.Lock()
		for _, c := range n.conns {
			_ = c.Close()
		}
		n.mu.Unlock()
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.mu.Lock()
			n.conns = append(n.conns, c)
			n.mu.Unlock()
			go n.serve(c, allowed)
		}
	}()
	return n
}

func (n *fwdNode) serve(c net.Conn, allowed ssh.PublicKey) {
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) == string(allowed.Marshal()) {
			return nil, nil
		}
		return nil, errors.New("denied")
	}}
	cfg.AddHostKey(n.signer)
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		_ = c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if nc.ChannelType() != "direct-tcpip" || ssh.Unmarshal(nc.ExtraData(), &p) != nil || p.Host != "127.0.0.1" {
			_ = nc.Reject(ssh.Prohibited, "permitopen")
			continue
		}
		go func(nc ssh.NewChannel, addr string) {
			up, err := net.Dial("tcp", addr)
			if err != nil {
				_ = nc.Reject(ssh.ConnectionFailed, "x")
				return
			}
			ch, chReqs, err := nc.Accept()
			if err != nil {
				_ = up.Close()
				return
			}
			go ssh.DiscardRequests(chReqs)
			go func() { _, _ = io.Copy(ch, up); _ = ch.Close() }()
			_, _ = io.Copy(up, ch)
			_ = up.Close()
			_ = ch.Close()
		}(nc, net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
	}
	_ = sc.Close()
}

// fakeVNC greets with a banner, then echoes. It reports closed connections.
type fakeVNC struct {
	ln     net.Listener
	port   int
	closed atomic.Int32
	mu     sync.Mutex
	conns  []net.Conn
}

func startFakeVNC(t *testing.T) *fakeVNC {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	v := &fakeVNC{ln: ln, port: ln.Addr().(*net.TCPAddr).Port}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			v.mu.Lock()
			v.conns = append(v.conns, c)
			v.mu.Unlock()
			go func() {
				_, _ = c.Write([]byte("RFB 003.008\n"))
				_, _ = io.Copy(c, c)
				v.closed.Add(1)
				_ = c.Close()
			}()
		}
	}()
	return v
}

func (v *fakeVNC) dropAll() {
	v.mu.Lock()
	for _, c := range v.conns {
		_ = c.Close()
	}
	v.mu.Unlock()
}

// --- fake compute backend (never reaches libvirt) -------------------------------

type fakeConsoleBackend struct {
	compute.Backend
	localCalled atomic.Bool
	stream      compute.ConsoleStream
	ip          string
}

func (f *fakeConsoleBackend) GetDomain(id string) (models.VM, error) {
	return models.VM{ID: id, Name: "vm-" + id}, nil
}
func (f *fakeConsoleBackend) GetVNCInfo(string) (compute.GraphicsInfo, error) {
	f.localCalled.Store(true)
	return compute.GraphicsInfo{Type: "vnc", Port: 5900, Host: "127.0.0.1"}, nil
}
func (f *fakeConsoleBackend) GetSPICEInfo(string) (compute.GraphicsInfo, error) {
	f.localCalled.Store(true)
	return compute.GraphicsInfo{Type: "spice", Port: 5901, Host: "127.0.0.1"}, nil
}
func (f *fakeConsoleBackend) GetDomainIP(string) string { return f.ip }
func (f *fakeConsoleBackend) OpenSerialConsole(string) (compute.ConsoleStream, error) {
	return f.stream, nil
}

type remoteConsoleFixture struct {
	h    *Handler
	be   *fakeConsoleBackend
	node nodes.Node
	srv  *fwdNode
	vnc  *fakeVNC
	ts   *httptest.Server
	xml  *atomic.Value
}

func fakeDomXML(port int, listen string) string {
	return "<domain><devices><graphics type='vnc' port='" + strconv.Itoa(port) +
		"' autoport='yes' listen='" + listen + "'><listen type='address' address='" + listen + "'/></graphics>" +
		"<graphics type='spice' port='" + strconv.Itoa(port) + "' listen='" + listen + "'/></devices></domain>"
}

func newRemoteConsoleFixture(t *testing.T, pin bool) *remoteConsoleFixture {
	t.Helper()
	store := nodes.NewSSHKeyStore(t.TempDir())
	info, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(info.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	srv := startFwdNode(t, pub)
	if pin {
		if err := store.PinHostKey(srv.host, srv.port, ssh.FingerprintSHA256(srv.signer.PublicKey()), false); err != nil {
			t.Fatal(err)
		}
	}
	vnc := startFakeVNC(t)
	node := nodes.Node{ID: "node-" + t.Name(), Name: "far", URI: "qemu+ssh://root@" + srv.host + ":" + strconv.Itoa(srv.port) + "/system", Enabled: true}
	be := &fakeConsoleBackend{ip: ""}
	h := &Handler{compute: be, cfg: &config.Config{}, sshKeys: store, remote: &remoteTarget{node: node}}

	xml := &atomic.Value{}
	xml.Store(fakeDomXML(vnc.port, "127.0.0.1"))
	oldG := remoteGraphicsInfo
	remoteGraphicsInfo = func(_ *Handler, _ string, typ string) (compute.GraphicsInfo, error) {
		gi, err := libvirt.ParseGraphicsXML(xml.Load().(string), typ)
		return compute.GraphicsInfo{Type: gi.Type, Port: gi.Port, Host: gi.Host}, err
	}
	t.Cleanup(func() {
		remoteGraphicsInfo = oldG
		nodeDialersMu.Lock()
		if e := nodeDialers[node.ID]; e != nil {
			_ = e.d.Close()
			delete(nodeDialers, node.ID)
		}
		nodeDialersMu.Unlock()
	})

	r := chi.NewRouter()
	r.Get("/api/vms/{id}/vnc", h.VNCProxy)
	r.Get("/api/vms/{id}/spice-ws", h.SPICEProxy)
	r.Get("/api/vms/{id}/serial", h.SerialProxy)
	r.Get("/api/vms/{id}/graphics", h.GetGraphics)
	r.Get("/api/vms/{id}/rdp", h.DownloadRDP)
	r.Get("/api/vms/{id}/spice", h.DownloadSPICE)
	r.Post("/api/vms/{id}/vnc-ticket", h.VNCTicket)
	r.Post("/api/vms/{id}/console-ticket", h.VMConsoleTicket)
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return &remoteConsoleFixture{h: h, be: be, node: node, srv: srv, vnc: vnc, ts: ts, xml: xml}
}

func (f *remoteConsoleFixture) wsURL(path string) string {
	return "ws" + strings.TrimPrefix(f.ts.URL, "http") + path
}

func readWS(t *testing.T, ws *websocket.Conn, want string) {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var got []byte
	for len(got) < len(want) {
		_, m, err := ws.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v (got %q want %q)", err, got, want)
		}
		got = append(got, m...)
	}
	if string(got) != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestRemoteVNCProxyEndToEnd(t *testing.T) {
	f := newRemoteConsoleFixture(t, true)
	open := func() *websocket.Conn {
		ws, resp, err := websocket.DefaultDialer.Dial(f.wsURL("/api/vms/v1/vnc"), nil)
		if resp != nil && resp.Body != nil {
			defer resp.Body.Close()
		}
		if err != nil {
			t.Fatal(err)
		}
		return ws
	}
	// Warm up the shared SSH client so goroutine accounting is stable.
	ws := open()
	readWS(t, ws, "RFB 003.008\n")
	_ = ws.Close()
	waitFor(t, func() bool { return f.vnc.closed.Load() == 1 })
	time.Sleep(200 * time.Millisecond)
	base := runtime.NumGoroutine()

	// Bytes flow both ways.
	ws = open()
	readWS(t, ws, "RFB 003.008\n")
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("ClientInit")); err != nil {
		t.Fatal(err)
	}
	readWS(t, ws, "ClientInit")
	if f.be.localCalled.Load() {
		t.Fatal("the local backend was consulted for a remote VM")
	}
	// Browser closes -> upstream VNC connection closes.
	_ = ws.Close()
	waitFor(t, func() bool { return f.vnc.closed.Load() == 2 })

	// Upstream closes -> browser socket closes.
	ws = open()
	readWS(t, ws, "RFB 003.008\n")
	f.vnc.dropAll()
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("browser side stayed open after upstream closed")
	}
	_ = ws.Close()

	// No goroutine leak.
	waitFor(t, func() bool { return runtime.NumGoroutine() <= base+2 })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("condition not reached (goroutines=%d)", runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRemoteVNCProxyTypedErrorsBeforeUpgrade(t *testing.T) {
	// Unpinned host key fails closed with a typed JSON error, no WebSocket.
	f := newRemoteConsoleFixture(t, false)
	_, resp, err := websocket.DefaultDialer.Dial(f.wsURL("/api/vms/v1/vnc"), nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("unpinned: err=%v resp=%v", err, resp)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "host_key_unpinned") || !strings.Contains(string(b), `"node":"far"`) {
		t.Fatalf("body %s", b)
	}

	f = newRemoteConsoleFixture(t, true)
	f.xml.Store(strings.ReplaceAll(fakeDomXML(5900, "127.0.0.1"), "port='5900'", "port='-1'"))
	res, gerr := http.Get(f.ts.URL + "/api/vms/v1/vnc")
	if gerr != nil {
		t.Fatalf("get vnc: %v", gerr)
	}
	defer res.Body.Close()
	b, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusConflict || !strings.Contains(string(b), "vm_not_running") {
		t.Fatalf("not running: %d %s", res.StatusCode, b)
	}
	f.xml.Store(fakeDomXML(5900, "192.0.2.9"))
	res, gerr = http.Get(f.ts.URL + "/api/vms/v1/vnc")
	if gerr != nil {
		t.Fatalf("get vnc: %v", gerr)
	}
	defer res.Body.Close()
	b, _ = io.ReadAll(res.Body)
	if res.StatusCode != http.StatusNotImplemented || !strings.Contains(string(b), "remote_unsupported") {
		t.Fatalf("non-loopback listen: %d %s", res.StatusCode, b)
	}
}

func TestRemoteVNCForwardLimitIs429(t *testing.T) {
	f := newRemoteConsoleFixture(t, true)
	var open []*websocket.Conn
	defer func() {
		for _, w := range open {
			_ = w.Close()
		}
	}()
	var last *http.Response
	for i := 0; i < 20; i++ {
		ws, resp, err := websocket.DefaultDialer.Dial(f.wsURL("/api/vms/v1/vnc"), nil)
		if err != nil {
			last = resp
			break
		}
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		open = append(open, ws)
	}
	if last != nil && last.Body != nil {
		defer last.Body.Close()
	}
	if last == nil || last.StatusCode != http.StatusTooManyRequests || len(open) != 16 {
		t.Fatalf("open=%d resp=%v", len(open), last)
	}
}

func TestRemoteGraphicsReadsRemoteDomainXML(t *testing.T) {
	f := newRemoteConsoleFixture(t, true)
	f.xml.Store(fakeDomXML(5937, "127.0.0.1"))
	res, err := http.Get(f.ts.URL + "/api/vms/v1/graphics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(b), `"port":5937`) {
		t.Fatalf("%d %s", res.StatusCode, b)
	}
	if f.be.localCalled.Load() {
		t.Fatal("graphics info came from the local backend")
	}
}

func TestConsoleTicketsBoundToNodeAndVM(t *testing.T) {
	f := newRemoteConsoleFixture(t, true)
	other := *f.h
	other.remote = &remoteTarget{node: nodes.Node{ID: "other-node", Name: "other"}}
	local := *f.h
	local.remote = nil
	local.cfg = &config.Config{}

	issue := func(h *Handler, vm string) string {
		req := httptest.NewRequest(http.MethodPost, "/x", nil)
		rc := chi.NewRouteContext()
		rc.URLParams.Add("id", vm)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
		req.Header.Set("X-User", "admin")
		req.Header.Set("X-Role", models.RoleAdmin)
		w := httptest.NewRecorder()
		h.VNCTicket(w, req)
		s := w.Body.String()
		i := strings.Index(s, `"vnc_ticket":"`)
		if i < 0 {
			t.Fatalf("no ticket: %s", s)
		}
		s = s[i+len(`"vnc_ticket":"`):]
		return s[:strings.Index(s, `"`)]
	}
	check := func(h *Handler, tk, vm string) int {
		req := httptest.NewRequest(http.MethodGet, "/x?vt="+tk, nil)
		w := httptest.NewRecorder()
		if h.checkConsoleTicket(w, req, vm) {
			return 200
		}
		return w.Code
	}
	tk := issue(f.h, "v1")
	if c := check(f.h, tk, "v1"); c != 200 {
		t.Errorf("same node+vm: %d", c)
	}
	if c := check(&other, tk, "v1"); c != 403 {
		t.Errorf("other node: %d", c)
	}
	if c := check(&local, tk, "v1"); c != 403 {
		t.Errorf("remote ticket on local: %d", c)
	}
	if c := check(f.h, tk, "v2"); c != 403 {
		t.Errorf("other vm: %d", c)
	}
	ltk := issue(&local, "v1")
	if c := check(&local, ltk, "v1"); c != 200 {
		t.Errorf("local ticket on local: %d", c)
	}
	if c := check(f.h, ltk, "v1"); c != 403 {
		t.Errorf("local ticket replayed on remote: %d", c)
	}
	if c := check(f.h, "never-minted", "v1"); c != 403 {
		t.Errorf("unknown ticket on remote: %d", c)
	}
	if c := check(&local, "never-minted", "v1"); c != 200 {
		t.Errorf("unknown ticket on local must keep legacy behaviour: %d", c)
	}
}

type fakeStream struct {
	in     chan []byte
	sent   chan []byte
	closed chan struct{}
	once   sync.Once
}

func (s *fakeStream) Recv(buf []byte) (int, error) {
	select {
	case b := <-s.in:
		return copy(buf, b), nil
	case <-s.closed:
		return 0, io.EOF
	}
}
func (s *fakeStream) Send(b []byte) (int, error) {
	s.sent <- append([]byte(nil), b...)
	return len(b), nil
}
func (s *fakeStream) Finish() error { s.once.Do(func() { close(s.closed) }); return nil }
func (s *fakeStream) Free()         {}

func TestRemoteSerialThroughRemoteStream(t *testing.T) {
	f := newRemoteConsoleFixture(t, true)
	st := &fakeStream{in: make(chan []byte, 1), sent: make(chan []byte, 4), closed: make(chan struct{})}
	f.be.stream = st
	ws, resp, err := websocket.DefaultDialer.Dial(f.wsURL("/api/vms/v1/serial"), nil)
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	st.in <- []byte("login: ")
	readWS(t, ws, "login: ")
	_ = ws.WriteMessage(websocket.TextMessage, []byte("root\n"))
	select {
	case b := <-st.sent:
		if string(b) != "root\n" {
			t.Fatalf("sent %q", b)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing reached the remote stream")
	}
}

func TestRemoteConsoleFilesUseNodeHost(t *testing.T) {
	f := newRemoteConsoleFixture(t, true)
	f.h.cfg = &config.Config{PublicHost: "203.0.113.200"} // THIS host: must never appear
	f.h.remote.node.URI = "qemu+ssh://root@10.77.0.5/system"
	get := func(p string) (int, string) {
		res, err := http.Get(f.ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	code, body := get("/api/vms/v1/rdp")
	if code != 200 || !strings.Contains(body, "10.77.0.5:3389") || strings.Contains(body, "203.0.113.200") {
		t.Errorf("rdp: %d %s", code, body)
	}
	// The .vv is a direct connection from the operator's machine, so the
	// graphics device must listen beyond loopback on the node.
	f.xml.Store(fakeDomXML(5901, "0.0.0.0"))
	code, body = get("/api/vms/v1/spice")
	if code != 200 || !strings.Contains(body, "host=10.77.0.5") || strings.Contains(body, "203.0.113.200") {
		t.Errorf("spice: %d %s", code, body)
	}
	// A node whose address is this very machine has no usable address.
	f.h.remote.node.URI = "qemu+ssh://root@127.0.0.1:22/system"
	for _, p := range []string{"/api/vms/v1/rdp", "/api/vms/v1/spice"} {
		if code, body := get(p); code != http.StatusNotImplemented || !strings.Contains(body, "remote_unsupported") {
			t.Errorf("%s: %d %s", p, code, body)
		}
	}
}

// A graphics device bound to the node's loopback is reachable only through the
// SSH tunnel: a .vv pointing at the node's address would be a dead file, so the
// answer is an honest 501 instead (found live: .vv for a loopback-only VNC).
func TestRemoteVVRefusedWhenGraphicsIsLoopbackOnly(t *testing.T) {
	f := newRemoteConsoleFixture(t, true)
	f.h.remote.node.URI = "qemu+ssh://root@10.77.0.5/system"
	f.xml.Store(fakeDomXML(5901, "127.0.0.1"))
	res, err := http.Get(f.ts.URL + "/api/vms/v1/spice")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusNotImplemented || !strings.Contains(string(b), "graphics_loopback_only") || strings.Contains(string(b), "host=") {
		t.Fatalf("loopback-only graphics must be refused: %d %s", res.StatusCode, b)
	}
	// A specific non-loopback listen address is where the file must point.
	f.xml.Store(fakeDomXML(5901, "10.77.0.9"))
	res, err = http.Get(f.ts.URL + "/api/vms/v1/spice")
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(b), "host=10.77.0.9") {
		t.Fatalf("specific listen address must be used: %d %s", res.StatusCode, b)
	}
}

func TestConsoleRoutesAreClassB(t *testing.T) {
	for _, k := range []string{"GET /graphics", "GET /vnc", "GET /spice-ws", "GET /serial", "POST /console-ticket",
		"POST /vnc-ticket", "GET /rdp", "GET /spice"} {
		if vmRouteClasses[k] != classB {
			t.Errorf("%s is %q, want B", k, vmRouteClasses[k])
		}
	}
}

func TestForgetNodeDialerDropsCachedEntries(t *testing.T) {
	nodeDialersMu.Lock()
	saved := nodeDialers
	nodeDialers = map[string]*dialerEntry{}
	nodeDialersMu.Unlock()
	t.Cleanup(func() {
		nodeDialersMu.Lock()
		nodeDialers = saved
		nodeDialersMu.Unlock()
	})

	mk := func() *dialerEntry { return &dialerEntry{uri: "qemu+ssh://u@h/system", d: &nodes.SSHDialer{}} }
	nodeDialersMu.Lock()
	nodeDialers["n1"] = mk()
	nodeDialers["n2"] = mk()
	nodeDialersMu.Unlock()

	forgetNodeDialer("n1")
	nodeDialersMu.Lock()
	_, has1 := nodeDialers["n1"]
	_, has2 := nodeDialers["n2"]
	nodeDialersMu.Unlock()
	if has1 || !has2 {
		t.Fatalf("forgetNodeDialer(n1): has1=%v has2=%v, want false/true", has1, has2)
	}

	forgetAllNodeDialers()
	nodeDialersMu.Lock()
	n := len(nodeDialers)
	nodeDialersMu.Unlock()
	if n != 0 {
		t.Fatalf("forgetAllNodeDialers left %d entries", n)
	}
}
