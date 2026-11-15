package api

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"webkvm/internal/libvirt"
	"webkvm/internal/nodes"
)

// keyedSSH is an in-process SSH node that accepts a changeable set of public
// keys (what an operator controls with authorized_keys).
type keyedSSH struct {
	mu      sync.Mutex
	host    string
	port    int
	signer  ssh.Signer
	allowed map[string]bool
}

func (k *keyedSSH) allow(pubLine string) {
	pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pubLine))
	if err != nil {
		panic(err)
	}
	k.mu.Lock()
	k.allowed[string(pk.Marshal())] = true
	k.mu.Unlock()
}

func startKeyedSSH(t *testing.T) *keyedSSH {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	k := &keyedSSH{signer: newAPISigner(t), allowed: map[string]bool{}}
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	k.host = h
	k.port, _ = strconv.Atoi(p)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
					k.mu.Lock()
					ok := k.allowed[string(key.Marshal())]
					k.mu.Unlock()
					if ok {
						return nil, nil
					}
					return nil, os.ErrPermission
				}}
				cfg.AddHostKey(k.signer)
				if sc, chans, reqs, err := ssh.NewServerConn(c, cfg); err == nil {
					go ssh.DiscardRequests(reqs)
					go func() {
						for nc := range chans {
							_ = nc.Reject(ssh.Prohibited, "no")
						}
					}()
					_ = sc.Wait()
				}
			}(c)
		}
	}()
	return k
}

// rotationFixture: a configured key, a pending key, and two real-SSH nodes:
// A authorizes only the NEW key, B only the OLD one.
type rotationFixture struct {
	*sshAPIFixture
	a, b       *keyedSSH
	na, nb     nodes.Node
	oldPub     string
	pendingPub string
	pendingFP  string
}

func newRotationFixture(t *testing.T) *rotationFixture {
	f := newSSHAPIFixture(t)
	f.setup(t)
	if err := f.reg.Delete(f.node.ID); err != nil { // drop the deny-everything node
		t.Fatal(err)
	}
	rf := &rotationFixture{sshAPIFixture: f, a: startKeyedSSH(t), b: startKeyedSSH(t)}
	mk := func(name string, s *keyedSSH) nodes.Node {
		n, err := f.reg.Create(name, "qemu+ssh://root@"+s.host+":"+strconv.Itoa(s.port)+"/system")
		if err != nil {
			t.Fatal(err)
		}
		if err := f.h.sshKeys.PinHostKey(s.host, s.port, ssh.FingerprintSHA256(s.signer.PublicKey()), false); err != nil {
			t.Fatal(err)
		}
		return n
	}
	rf.na, rf.nb = mk("node-a", rf.a), mk("node-b", rf.b)
	info, _ := f.h.sshKeys.Info()
	rf.oldPub = info.PublicKey
	rf.a.allow(rf.oldPub)
	rf.b.allow(rf.oldPub)
	r := f.admin(http.MethodPost, "/api/cluster/ssh/rotate", "")
	if r.Code != 201 {
		t.Fatalf("rotate: %d %s", r.Code, r.Body.String())
	}
	p := bodyJSON(t, r)["pending"].(map[string]any)
	rf.pendingPub, rf.pendingFP = p["public_key"].(string), p["fingerprint"].(string)
	rf.a.allow(rf.pendingPub)
	return rf
}

func (rf *rotationFixture) results(t *testing.T, m map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, x := range m["nodes"].([]any) {
		row := x.(map[string]any)
		out[row["name"].(string)] = row
	}
	return out
}

func TestRotateTestReportsPerNodeAndGatesCommit(t *testing.T) {
	rf := newRotationFixture(t)
	if rf.h.libvirtPool == nil {
		t.Fatal("pool")
	}
	connects := rf.connects

	// Nothing tested yet: commit refuses and says why per node.
	r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", "")
	m := bodyJSON(t, r)
	if r.Code != 409 || m["code"] != "rotation_unverified" || len(m["failing"].([]any)) != 2 {
		t.Fatalf("untested commit: %d %s", r.Code, r.Body.String())
	}
	if c := m["failing"].([]any)[0].(map[string]any)["code"]; c != "not_tested" {
		t.Fatalf("code = %v", c)
	}

	r = rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/test", "")
	m = bodyJSON(t, r)
	res := rf.results(t, m)
	if r.Code != 200 || m["ok"] != false || res["node-a"]["ok"] != true || res["node-b"]["ok"] != false || res["node-b"]["code"] != "ssh_auth_denied" {
		t.Fatalf("test: %d %s", r.Code, r.Body.String())
	}
	if rf.connects != connects {
		t.Fatal("testing the pending key must not touch the active pool connections")
	}

	r = rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", "")
	m = bodyJSON(t, r)
	f0 := m["failing"].([]any)[0].(map[string]any)
	if r.Code != 409 || len(m["failing"].([]any)) != 1 || f0["name"] != "node-b" || f0["code"] != "ssh_auth_denied" {
		t.Fatalf("commit with B failing: %d %s", r.Code, r.Body.String())
	}
	if info, _ := rf.h.sshKeys.Info(); info.Pending == nil {
		t.Fatal("a refused commit must keep the pending key")
	}

	// The operator authorizes the new key on B; test again, then commit.
	rf.b.allow(rf.pendingPub)
	m = bodyJSON(t, rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/test", ""))
	if m["ok"] != true {
		t.Fatalf("retest: %v", m)
	}
	ov := bodyJSON(t, rf.admin(http.MethodGet, "/api/cluster/ssh", ""))
	for _, n := range ov["nodes"].([]any) {
		if n.(map[string]any)["pending_verified"] != true {
			t.Fatalf("overview must show verified nodes: %v", n)
		}
	}
	r = rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", "")
	if r.Code != 200 || bodyJSON(t, r)["forced"] != false {
		t.Fatalf("commit: %d %s", r.Code, r.Body.String())
	}
	if len(rf.actions(t, "cluster_ssh.rotate_commit")) != 1 || len(rf.actions(t, "cluster_ssh.rotate_commit_forced")) != 0 {
		t.Fatal("audit")
	}
	info, _ := rf.h.sshKeys.Info()
	if info.Fingerprint != rf.pendingFP || info.Previous == nil {
		t.Fatalf("info after commit: %+v", info)
	}
	if st, err := os.Stat(filepath.Join(rf.dir, "cluster-ssh", "id_ed25519.prev")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("prev key file: %v %v", st, err)
	}
}

func TestRotateCommitForceIsAuditedWithUnverifiedNodes(t *testing.T) {
	rf := newRotationFixture(t)
	r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", `{"force":true}`)
	if r.Code != 200 || bodyJSON(t, r)["forced"] != true {
		t.Fatalf("forced commit: %d %s", r.Code, r.Body.String())
	}
	e := rf.actions(t, "cluster_ssh.rotate_commit_forced")
	if len(e) != 1 {
		t.Fatalf("audit entries: %d", len(e))
	}
	if u, _ := e[0].Detail["unverified"].([]any); len(u) != 2 {
		t.Fatalf("audit detail: %+v", e[0].Detail)
	}
	if r := rf.do(http.MethodPost, "/api/cluster/ssh/rotate/commit", `{"force":true}`, "operator"); r.Code != http.StatusForbidden {
		t.Fatalf("force by non-admin: %d", r.Code)
	}
}

func TestRotateVerificationExpiresAndIsClearedByNewRotation(t *testing.T) {
	rf := newRotationFixture(t)
	rf.b.allow(rf.pendingPub)
	now := time.Now()
	prev := rotationNow
	rotationNow = func() time.Time { return now }
	t.Cleanup(func() { rotationNow = prev })
	if m := bodyJSON(t, rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/test", "")); m["ok"] != true {
		t.Fatalf("test: %v", m)
	}
	now = now.Add(rotationVerifyWindow + time.Second)
	if r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", ""); r.Code != 409 {
		t.Fatalf("expired verification must not allow commit: %d", r.Code)
	}
	now = now.Add(-2 * time.Second)
	if r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", ""); r.Code != 200 {
		t.Fatalf("inside the window: %d %s", r.Code, r.Body.String())
	}
}

func TestRotateNewRotationAndAbortClearVerification(t *testing.T) {
	rf := newRotationFixture(t)
	rf.b.allow(rf.pendingPub)
	rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/test", "")
	rf.admin(http.MethodPost, "/api/cluster/ssh/rotate", "") // a NEW pending key
	if r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", ""); r.Code != 409 {
		t.Fatalf("verification of the old pending key must not carry over: %d", r.Code)
	}
	rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/abort", "")
	if r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/test", ""); r.Code != 409 || bodyJSON(t, r)["code"] != "rotation_no_pending" {
		t.Fatalf("test without pending: %d %s", r.Code, r.Body.String())
	}
}

func TestRotateDisabledNodeDoesNotBlockCommit(t *testing.T) {
	rf := newRotationFixture(t)
	rf.b.allow(rf.pendingPub)
	off := false
	if _, err := rf.reg.Update(rf.nb.ID, "", "", &off); err != nil {
		t.Fatal(err)
	}
	m := bodyJSON(t, rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/test", ""))
	if len(m["nodes"].([]any)) != 1 {
		t.Fatalf("disabled node must not be tested: %v", m)
	}
	if r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", ""); r.Code != 200 {
		t.Fatalf("commit: %d %s", r.Code, r.Body.String())
	}
}

func TestRotateRollbackRestoresPreviousKeyAndInvalidatesPool(t *testing.T) {
	rf := newRotationFixture(t)
	before, _ := rf.h.sshKeys.Info()
	if r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/rollback", ""); r.Code != 409 || bodyJSON(t, r)["code"] != "rotation_no_previous" {
		t.Fatalf("rollback before any commit: %d %s", r.Code, r.Body.String())
	}
	rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/commit", `{"force":true}`)
	if _, err := rf.h.libvirtPool.Connector(t.Context(), rf.na); err != nil {
		t.Fatal(err)
	}
	if r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/rollback", ""); r.Code != 200 {
		t.Fatalf("rollback: %d %s", r.Code, r.Body.String())
	}
	after, _ := rf.h.sshKeys.Info()
	if after.Fingerprint != before.Fingerprint || after.Previous != nil {
		t.Fatalf("rollback did not restore the old key: %+v", after)
	}
	if rf.h.libvirtPool.Status(rf.na.ID).State != libvirt.PoolStateClosed {
		t.Fatal("rollback must invalidate pooled connections")
	}
	if len(rf.actions(t, "cluster_ssh.rotate_rollback")) != 1 {
		t.Fatal("rollback not audited")
	}
	if r := rf.admin(http.MethodPost, "/api/cluster/ssh/rotate/rollback", ""); r.Code != 409 {
		t.Fatalf("second rollback: %d", r.Code)
	}
}
