package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/ssh"

	"webkvm/internal/audit"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

type sshFakeServer struct {
	mu     sync.Mutex
	host   string
	port   int
	signer ssh.Signer
}

func (f *sshFakeServer) get() ssh.Signer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.signer
}

func (f *sshFakeServer) set(s ssh.Signer) {
	f.mu.Lock()
	f.signer = s
	f.mu.Unlock()
}

func (f *sshFakeServer) fp() string { return ssh.FingerprintSHA256(f.get().PublicKey()) }

func newAPISigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, sk, _ := ed25519.GenerateKey(rand.Reader)
	sg, err := ssh.NewSignerFromKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

// startAPIFakeSSH is an in-process SSH server that shows a host key and
// refuses every login. It is the only "node" these tests ever talk to.
func startAPIFakeSSH(t *testing.T) *sshFakeServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &sshFakeServer{signer: newAPISigner(t)}
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	f.host = h
	f.port, _ = strconv.Atoi(p)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				cfg := &ssh.ServerConfig{NoClientAuth: false,
					PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, os.ErrPermission }}
				cfg.AddHostKey(f.get())
				_, _, _, _ = ssh.NewServerConn(c, cfg)
			}(c)
		}
	}()
	return f
}

type sshAPIFixture struct {
	h      *Handler
	reg    *nodes.Registry
	router http.Handler
	audit  *audit.Logger
	srv    *sshFakeServer
	node   nodes.Node
	dir    string
	// connect behaviour of the fake pool
	connectErr error
	probeErr   error
	connects   int
	// noLibSSH2 simulates a local libvirt client built without libssh2.
	noLibSSH2 bool
}

func newSSHAPIFixture(t *testing.T) *sshAPIFixture {
	t.Helper()
	dir := t.TempDir()
	reg, err := nodes.New(dir, "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	al, err := audit.New(filepath.Join(dir, "audit", "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = al.Close() })
	f := &sshAPIFixture{reg: reg, audit: al, srv: startAPIFakeSSH(t), dir: dir}
	keys := nodes.NewSSHKeyStore(dir)
	nodes.ResetTransportSupport()
	t.Cleanup(nodes.ResetTransportSupport)
	pool := libvirt.NewPool(ClusterSSHConnURI(keys, func() nodes.Transport { return nodes.DefaultTransport }))
	pool.SetConnectFunc(func(ctx context.Context, uri string) (*libvirt.Connector, error) {
		f.connects++
		if f.connectErr != nil {
			return nil, f.connectErr
		}
		if f.noLibSSH2 && strings.HasPrefix(uri, "qemu+libssh2:") {
			return nil, errors.New("libssh2 transport support was not enabled: Function not implemented")
		}
		return libvirt.NewTestConnector(uri), nil
	}, func(*libvirt.Connector) bool { return true })
	t.Cleanup(pool.Close)
	f.h = &Handler{nodes: reg, audit: al, sshKeys: keys, libvirtPool: pool}
	n, err := reg.Create("lab1", "qemu+ssh://root@"+f.srv.host+":"+strconv.Itoa(f.srv.port)+"/system")
	if err != nil {
		t.Fatal(err)
	}
	f.node = n
	prev := libvirtProbe
	libvirtProbe = func(*libvirt.Connector) (libvirtProbeResult, error) {
		if f.probeErr != nil {
			return libvirtProbeResult{}, f.probeErr
		}
		return libvirtProbeResult{Version: "10.0.0", KVM: true, Domains: 3}, nil
	}
	t.Cleanup(func() { libvirtProbe = prev })
	rt := chi.NewRouter()
	rt.Route("/api/nodes", func(r chi.Router) {
		r.Get("/{id}", f.h.GetNode)
		r.Put("/{id}", f.h.UpdateNode)
		r.Delete("/{id}", f.h.DeleteNode)
	})
	f.h.mountClusterSSH(rt)
	f.router = rt
	return f
}

func (f *sshAPIFixture) do(method, path, body, role string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		req.Header.Set("X-Role", role)
	}
	res := httptest.NewRecorder()
	f.router.ServeHTTP(res, req)
	return res
}

func (f *sshAPIFixture) admin(method, path, body string) *httptest.ResponseRecorder {
	return f.do(method, path, body, models.RoleAdmin)
}

func (f *sshAPIFixture) nodePath(suffix string) string { return "/api/nodes/" + f.node.ID + suffix }

func (f *sshAPIFixture) actions(t *testing.T, action string) []audit.Entry {
	t.Helper()
	e, _, err := f.audit.List(audit.ListOptions{Action: action}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func (f *sshAPIFixture) setup(t *testing.T) {
	t.Helper()
	if r := f.admin(http.MethodPost, "/api/cluster/ssh/key", ""); r.Code != http.StatusCreated {
		t.Fatalf("key: %d %s", r.Code, r.Body.String())
	}
}

func (f *sshAPIFixture) pin(t *testing.T) {
	t.Helper()
	r := f.admin(http.MethodPost, f.nodePath("/ssh/pin"), `{"fingerprint":"`+f.srv.fp()+`"}`)
	if r.Code != http.StatusOK {
		t.Fatalf("pin: %d %s", r.Code, r.Body.String())
	}
}

func TestClusterSSHEndpointsAreAdminOnly(t *testing.T) {
	f := newSSHAPIFixture(t)
	routes := [][2]string{
		{"GET", "/api/cluster/ssh"}, {"POST", "/api/cluster/ssh/key"}, {"POST", "/api/cluster/ssh/rotate"},
		{"POST", "/api/cluster/ssh/rotate/commit"}, {"POST", "/api/cluster/ssh/rotate/abort"},
		{"POST", "/api/cluster/ssh/rotate/test"}, {"POST", "/api/cluster/ssh/rotate/rollback"},
		{"GET", f.nodePath("/ssh-authorize")}, {"POST", f.nodePath("/ssh/hostkey")},
		{"POST", f.nodePath("/ssh/pin")}, {"POST", f.nodePath("/ssh/test")},
	}
	for _, rt := range routes {
		if r := f.do(rt[0], rt[1], "{}", ""); r.Code != http.StatusUnauthorized {
			t.Errorf("%v anonymous: %d", rt, r.Code)
		}
		for _, role := range []string{models.RoleOperator, models.RoleViewer} {
			if r := f.do(rt[0], rt[1], "{}", role); r.Code != http.StatusForbidden {
				t.Errorf("%v as %s: %d", rt, role, r.Code)
			}
		}
	}
}

func TestClusterSSHKeyLifecycleAndOverview(t *testing.T) {
	f := newSSHAPIFixture(t)
	r := f.admin(http.MethodGet, "/api/cluster/ssh", "")
	m := bodyJSON(t, r)
	if r.Code != 200 || m["configured"] != false || m["transport"] != "libssh2" {
		t.Fatalf("overview: %d %v", r.Code, m)
	}
	if r := f.admin(http.MethodPost, "/api/cluster/ssh/key", `{"algorithm":"rsa"}`); r.Code != 400 || bodyJSON(t, r)["code"] != "unsupported_algorithm" {
		t.Fatalf("rsa: %d %s", r.Code, r.Body.String())
	}
	r = f.admin(http.MethodPost, "/api/cluster/ssh/key", `{"algorithm":"ed25519"}`)
	k := bodyJSON(t, r)
	if r.Code != 201 || k["configured"] != true || !strings.HasPrefix(k["fingerprint"].(string), "SHA256:") || k["algorithm"] != "ssh-ed25519" {
		t.Fatalf("key: %d %v", r.Code, k)
	}
	if r := f.admin(http.MethodPost, "/api/cluster/ssh/key", ""); r.Code != 409 || bodyJSON(t, r)["code"] != "key_exists" {
		t.Fatalf("second key: %d %s", r.Code, r.Body.String())
	}
	m = bodyJSON(t, f.admin(http.MethodGet, "/api/cluster/ssh", ""))
	list := m["nodes"].([]any)
	if m["configured"] != true || m["fingerprint"] != k["fingerprint"] || len(list) != 1 {
		t.Fatalf("overview = %v", m)
	}
	nd := list[0].(map[string]any)
	if _, has := nd["ui_url"]; has {
		t.Fatalf("ui_url must be absent when unset: %v", nd)
	}
	hk := nd["host_key"].(map[string]any)
	if nd["name"] != "lab1" || nd["user"] != "root" || nd["host"] != f.srv.host || hk["state"] != "unknown" || nd["authorized"] != nil || nd["last_test"] != nil {
		t.Fatalf("node row = %v", nd)
	}
	if _, has := m["pending"]; has {
		t.Fatal("pending without rotation")
	}
	// Audit: action recorded, no key material.
	es := f.actions(t, "cluster_ssh.generate")
	if len(es) != 1 {
		t.Fatalf("audit = %v", es)
	}
	raw, _ := os.ReadFile(f.h.sshKeys.KeyPath())
	for _, e := range f.actions(t, "") {
		if strings.Contains(strings.ToLower(strings.TrimSpace(toJSONString(e))), "private key") || strings.Contains(toJSONString(e), strings.Split(string(raw), "\n")[1]) {
			t.Fatalf("audit leaks key material: %v", e)
		}
	}
}

func toJSONString(v any) string {
	b, _ := jsonMarshal(v)
	return string(b)
}

func TestClusterSSHRotationEndpoints(t *testing.T) {
	f := newSSHAPIFixture(t)
	if r := f.admin(http.MethodPost, "/api/cluster/ssh/rotate", ""); r.Code != 409 || bodyJSON(t, r)["code"] != "no_key" {
		t.Fatalf("rotate without key: %d %s", r.Code, r.Body.String())
	}
	f.setup(t)
	for _, p := range []string{"/commit", "/abort"} {
		if r := f.admin(http.MethodPost, "/api/cluster/ssh/rotate"+p, ""); r.Code != 409 || bodyJSON(t, r)["code"] != "no_pending" {
			t.Fatalf("%s without pending: %d %s", p, r.Code, r.Body.String())
		}
	}
	r := f.admin(http.MethodPost, "/api/cluster/ssh/rotate", "")
	p := bodyJSON(t, r)["pending"].(map[string]any)
	if r.Code != 201 || !strings.HasPrefix(p["fingerprint"].(string), "SHA256:") || !strings.Contains(p["command"].(string), "authorized_keys") {
		t.Fatalf("rotate: %d %s", r.Code, r.Body.String())
	}
	ov := bodyJSON(t, f.admin(http.MethodGet, "/api/cluster/ssh", ""))
	if ov["pending"].(map[string]any)["fingerprint"] != p["fingerprint"] {
		t.Fatalf("overview pending = %v", ov["pending"])
	}
	if r := f.admin(http.MethodPost, "/api/cluster/ssh/rotate/abort", ""); r.Code != 200 {
		t.Fatalf("abort: %d", r.Code)
	}
	f.admin(http.MethodPost, "/api/cluster/ssh/rotate", "")
	f.pin(t)
	if _, err := f.h.libvirtPool.Connector(context.Background(), f.node); err != nil {
		t.Fatal(err)
	}
	if f.h.libvirtPool.Status(f.node.ID).State != libvirt.PoolStateOpen {
		t.Fatal("pool not open")
	}
	// The node never accepted the pending key: a plain commit is refused.
	if r := f.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", ""); r.Code != 409 || bodyJSON(t, r)["code"] != "rotation_unverified" {
		t.Fatalf("unverified commit: %d %s", r.Code, r.Body.String())
	}
	if r := f.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", `{"force":true}`); r.Code != 200 {
		t.Fatalf("commit: %d %s", r.Code, r.Body.String())
	}
	if f.h.libvirtPool.Status(f.node.ID).State != libvirt.PoolStateClosed {
		t.Fatal("commit must invalidate pooled connections")
	}
	for _, a := range []string{"cluster_ssh.rotate", "cluster_ssh.rotate_abort", "cluster_ssh.rotate_commit_forced", "cluster_ssh.pin_hostkey"} {
		if len(f.actions(t, a)) == 0 {
			t.Errorf("no audit entry %s", a)
		}
	}
}

func TestNodeSSHAuthorize(t *testing.T) {
	f := newSSHAPIFixture(t)
	if r := f.admin(http.MethodGet, f.nodePath("/ssh-authorize"), ""); r.Code != 409 || bodyJSON(t, r)["code"] != "no_key" {
		t.Fatalf("no key: %d %s", r.Code, r.Body.String())
	}
	f.setup(t)
	r := f.admin(http.MethodGet, f.nodePath("/ssh-authorize"), "")
	m := bodyJSON(t, r)
	line := m["authorized_keys_line"].(string)
	if r.Code != 200 || m["user"] != "root" || m["host"] != f.srv.host ||
		!strings.HasPrefix(line, `restrict,port-forwarding,permitopen="127.0.0.1:*" ssh-ed25519 `) ||
		!strings.Contains(m["command"].(string), "'"+line+"'") || len(m["steps"].([]any)) == 0 {
		t.Fatalf("authorize: %d %v", r.Code, m)
	}
	if strings.Contains(r.Body.String(), "PRIVATE") {
		t.Fatal("private key in response")
	}
	// 404 for unknown, local node rejected.
	if r := f.admin(http.MethodGet, "/api/nodes/n_0000000000000000/ssh-authorize", ""); r.Code != 404 {
		t.Fatalf("unknown: %d", r.Code)
	}
	var localID string
	for _, n := range f.reg.List() {
		if n.IsLocal() {
			localID = n.ID
		}
	}
	for _, p := range []string{"/ssh-authorize", "/ssh/hostkey", "/ssh/test"} {
		m := "GET"
		if p != "/ssh-authorize" {
			m = "POST"
		}
		if r := f.admin(m, "/api/nodes/"+localID+p, "{}"); r.Code != 400 || bodyJSON(t, r)["code"] != "local_node" {
			t.Fatalf("local %s: %d %s", p, r.Code, r.Body.String())
		}
	}
}

func TestNodeSSHHostKeyCaptureAndPin(t *testing.T) {
	f := newSSHAPIFixture(t)
	r := f.admin(http.MethodPost, f.nodePath("/ssh/hostkey"), "")
	m := bodyJSON(t, r)
	if r.Code != 200 || m["fingerprint"] != f.srv.fp() || m["key_type"] != "ssh-ed25519" || m["state"] != "unknown" {
		t.Fatalf("hostkey: %d %v", r.Code, m)
	}
	if _, pinned := f.h.sshKeys.PinnedHostKey(f.srv.host, f.srv.port); pinned {
		t.Fatal("capture pinned the key")
	}
	if r := f.admin(http.MethodPost, f.nodePath("/ssh/pin"), `{"fingerprint":"SHA256:wrong"}`); r.Code != 409 || bodyJSON(t, r)["code"] != "fingerprint_mismatch" {
		t.Fatalf("mismatch: %d %s", r.Code, r.Body.String())
	}
	if r := f.admin(http.MethodPost, f.nodePath("/ssh/pin"), `{}`); r.Code != 400 {
		t.Fatalf("empty: %d", r.Code)
	}
	f.pin(t)
	m = bodyJSON(t, f.admin(http.MethodPost, f.nodePath("/ssh/hostkey"), ""))
	if m["state"] != "pinned" {
		t.Fatalf("state after pin = %v", m)
	}
	ov := bodyJSON(t, f.admin(http.MethodGet, "/api/cluster/ssh", ""))
	hk := ov["nodes"].([]any)[0].(map[string]any)["host_key"].(map[string]any)
	if hk["state"] != "pinned" || hk["fingerprint"] != f.srv.fp() {
		t.Fatalf("overview host_key = %v", hk)
	}
	// Host key rotates on the node.
	f.srv.set(newAPISigner(t))
	m = bodyJSON(t, f.admin(http.MethodPost, f.nodePath("/ssh/hostkey"), ""))
	if m["state"] != "changed" {
		t.Fatalf("changed state = %v", m)
	}
	if r := f.admin(http.MethodPost, f.nodePath("/ssh/pin"), `{"fingerprint":"`+f.srv.fp()+`"}`); r.Code != 409 || bodyJSON(t, r)["code"] != "host_key_changed" {
		t.Fatalf("pin over changed: %d %s", r.Code, r.Body.String())
	}
	if r := f.admin(http.MethodPost, f.nodePath("/ssh/pin"), `{"fingerprint":"`+f.srv.fp()+`","replace":true}`); r.Code != 200 {
		t.Fatalf("replace: %d %s", r.Code, r.Body.String())
	}
	if es := f.actions(t, "cluster_ssh.pin_hostkey"); len(es) != 2 || es[0].Detail["node_id"] != f.node.ID {
		t.Fatalf("audit = %+v", es)
	}
}

func TestNodeSSHHostKeyUnreachable(t *testing.T) {
	f := newSSHAPIFixture(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	n, err := f.reg.Create("dead", "qemu+ssh://root@127.0.0.1:"+p+"/system")
	if err != nil {
		t.Fatal(err)
	}
	r := f.admin(http.MethodPost, "/api/nodes/"+n.ID+"/ssh/hostkey", "")
	if r.Code != 502 || bodyJSON(t, r)["code"] != "node_unreachable" {
		t.Fatalf("unreachable: %d %s", r.Code, r.Body.String())
	}
}

func TestNodeSSHTestChain(t *testing.T) {
	f := newSSHAPIFixture(t)
	test := func() (map[string]any, int) {
		r := f.admin(http.MethodPost, f.nodePath("/ssh/test"), "")
		return bodyJSON(t, r), r.Code
	}
	m, code := test()
	if code != 200 || m["ok"] != false || m["code"] != "no_key" || m["ssh"].(map[string]any)["code"] != "no_key" || m["hint"] == "" {
		t.Fatalf("no_key: %v", m)
	}
	f.setup(t)
	if m, _ = test(); m["code"] != "host_key_unpinned" {
		t.Fatalf("unpinned: %v", m)
	}
	f.pin(t)
	m, _ = test()
	lv := m["libvirt"].(map[string]any)
	if m["ok"] != true || m["transport"] != "libssh2" || m["ssh"].(map[string]any)["ok"] != true || lv["ok"] != true || lv["version"] != "10.0.0" || lv["kvm"] != true || lv["domains"] != float64(3) {
		t.Fatalf("success: %v", m)
	}
	ov := bodyJSON(t, f.admin(http.MethodGet, "/api/cluster/ssh", ""))
	row := ov["nodes"].([]any)[0].(map[string]any)
	if row["authorized"] != true || row["last_test"].(map[string]any)["ok"] != true {
		t.Fatalf("row after ok test = %v", row)
	}

	f.h.libvirtPool.Invalidate(f.node.ID)
	f.connectErr = errors.New("ssh: handshake failed: Permission denied (publickey) /secret/path stderr")
	m, _ = test()
	if m["code"] != "ssh_auth_denied" || m["ssh"].(map[string]any)["ok"] != false {
		t.Fatalf("auth denied: %v", m)
	}
	if b := toJSONString(m); strings.Contains(b, "secret/path") || strings.Contains(b, "handshake") {
		t.Fatalf("raw error leaked: %s", b)
	}
	row = bodyJSON(t, f.admin(http.MethodGet, "/api/cluster/ssh", ""))["nodes"].([]any)[0].(map[string]any)
	if row["authorized"] != false {
		t.Fatalf("authorized = %v", row["authorized"])
	}

	f.h.libvirtPool.Invalidate(f.node.ID)
	f.connectErr = errors.New("libvirt: transport support was not enabled")
	if m, _ = test(); m["code"] != "transport_unsupported" {
		t.Fatalf("transport: %v", m)
	}
	f.h.libvirtPool.Invalidate(f.node.ID)
	f.connectErr = context.DeadlineExceeded
	if m, _ = test(); m["code"] != "node_timeout" {
		t.Fatalf("timeout: %v", m)
	}

	f.h.libvirtPool.Invalidate(f.node.ID)
	f.connectErr = nil
	f.probeErr = errors.New("remote libvirt domain listing failed")
	if m, _ = test(); m["code"] != "libvirt_unavailable" || m["ssh"].(map[string]any)["ok"] != true || m["libvirt"].(map[string]any)["ok"] != false {
		t.Fatalf("libvirt_unavailable: %v", m)
	}

	f.probeErr = nil
	f.srv.set(newAPISigner(t))
	if m, _ = test(); m["code"] != "host_key_changed" {
		t.Fatalf("changed: %v", m)
	}
}

func TestNodeUpdateDisableDeleteInvalidateAndForgetHostKey(t *testing.T) {
	f := newSSHAPIFixture(t)
	f.setup(t)
	f.pin(t)
	warm := func() {
		if _, err := f.h.libvirtPool.Connector(context.Background(), f.node); err != nil {
			t.Fatal(err)
		}
		if f.h.libvirtPool.Status(f.node.ID).State != libvirt.PoolStateOpen {
			t.Fatal("not open")
		}
	}
	warm()
	if r := f.admin(http.MethodPut, f.nodePath(""), `{"enabled":false}`); r.Code != 200 {
		t.Fatalf("disable: %d %s", r.Code, r.Body.String())
	}
	if f.h.libvirtPool.Status(f.node.ID).State != libvirt.PoolStateClosed {
		t.Fatal("disable must invalidate the pool entry")
	}
	f.node, _ = f.reg.Get(f.node.ID)
	warm()
	if r := f.admin(http.MethodPut, f.nodePath(""), `{"name":"lab1b"}`); r.Code != 200 {
		t.Fatalf("rename: %d", r.Code)
	}
	if f.h.libvirtPool.Status(f.node.ID).State != libvirt.PoolStateClosed {
		t.Fatal("edit must invalidate the pool entry")
	}
	warm()
	if r := f.admin(http.MethodDelete, f.nodePath(""), ""); r.Code != 200 {
		t.Fatalf("delete: %d %s", r.Code, r.Body.String())
	}
	if f.h.libvirtPool.Status(f.node.ID).State != libvirt.PoolStateClosed {
		t.Fatal("delete must invalidate the pool entry")
	}
	if _, ok := f.h.sshKeys.PinnedHostKey(f.srv.host, f.srv.port); ok {
		t.Fatal("known_hosts line survived node deletion")
	}
}

func TestClusterSSHUnwiredHandlerIs503(t *testing.T) {
	h := &Handler{}
	rt := chi.NewRouter()
	h.mountClusterSSH(rt)
	req := httptest.NewRequest(http.MethodGet, "/api/cluster/ssh", nil)
	req.Header.Set("X-Role", models.RoleAdmin)
	res := httptest.NewRecorder()
	rt.ServeHTTP(res, req)
	if res.Code != 503 {
		t.Fatalf("code = %d", res.Code)
	}
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// Authorizing the key and testing again must not be answered from the pool's
// circuit-breaker cache of the previous ssh_auth_denied.
func TestNodeSSHTestBypassesBreakerAfterAuthorize(t *testing.T) {
	f := newSSHAPIFixture(t)
	f.setup(t)
	f.pin(t)
	test := func() map[string]any {
		return bodyJSON(t, f.admin(http.MethodPost, f.nodePath("/ssh/test"), ""))
	}
	f.connectErr = errors.New("ssh: handshake failed: Permission denied (publickey)")
	if m := test(); m["code"] != "ssh_auth_denied" {
		t.Fatalf("denied: %v", m)
	}
	f.connectErr = nil // operator authorized the key meanwhile
	if m := test(); m["ok"] != true {
		t.Fatalf("stale breaker answer: %v", m)
	}
}

// v11 live defect: with the default libssh2 on a libvirt client built without
// it, the test must succeed over libssh and say so (typed, never silent).
func TestNodeSSHTestFallsBackFromLibSSH2(t *testing.T) {
	f := newSSHAPIFixture(t)
	f.setup(t)
	f.pin(t)
	f.noLibSSH2 = true
	m := bodyJSON(t, f.admin(http.MethodPost, f.nodePath("/ssh/test"), ""))
	if m["ok"] != true || m["transport"] != "libssh" || m["transport_configured"] != "libssh2" || m["transport_fallback"] != "libssh2_unsupported" {
		t.Fatalf("fallback test: %v", m)
	}
	ov := bodyJSON(t, f.admin(http.MethodGet, "/api/cluster/ssh", ""))
	if ov["transport"] != "libssh" || ov["transport_fallback"] != "libssh2_unsupported" {
		t.Fatalf("overview: %v", ov)
	}
}
