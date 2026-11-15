package nodes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Errors returned by the SSH key store. API handlers map them to HTTP codes.
var (
	ErrKeyExists           = errors.New("cluster SSH key already exists")
	ErrNoKey               = errors.New("no cluster SSH key configured")
	ErrNoPending           = errors.New("no pending SSH key rotation")
	ErrNoPrevious          = errors.New("no previous SSH key to roll back to")
	ErrFingerprintMismatch = errors.New("host key fingerprint does not match the confirmed one")
	ErrHostKeyUnpinned     = errors.New("host key is not pinned")
	ErrHostKeyChanged      = errors.New("host key differs from the pinned one")
	ErrHostUnreachable     = errors.New("host unreachable")
	ErrHostTimeout         = errors.New("host timed out")
)

// Host key states reported by HostKeyState.
const (
	HostKeyUnknown = "unknown"
	HostKeyPinned  = "pinned"
	HostKeyChanged = "changed"
)

const (
	sshKeyDirName    = "cluster-ssh"
	sshKeyFile       = "id_ed25519"
	sshPubFile       = "id_ed25519.pub"
	sshKnownHosts    = "known_hosts"
	sshMetaFile      = "meta.json"
	sshPendingKey    = "id_ed25519.pending"
	sshPendingPub    = "id_ed25519.pending.pub"
	sshPrevKey       = "id_ed25519.prev"
	sshPrevPub       = "id_ed25519.prev.pub"
	hostKeyCaptureTO = 10 * time.Second
)

// SSHKeyPending is a rotation key that is generated but not yet active.
type SSHKeyPending struct {
	Fingerprint string    `json:"fingerprint"`
	PublicKey   string    `json:"public_key"`
	CreatedAt   time.Time `json:"created_at"`
}

// SSHKeyPrevious is the key that was active before the last committed
// rotation, kept only for rollback.
type SSHKeyPrevious struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
}

// SSHKeyInfo describes the cluster SSH key. It never contains private material.
type SSHKeyInfo struct {
	Configured  bool            `json:"configured"`
	Algorithm   string          `json:"algorithm,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	PublicKey   string          `json:"public_key,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	Pending     *SSHKeyPending  `json:"pending,omitempty"`
	Previous    *SSHKeyPrevious `json:"previous,omitempty"`
}

// HostKey is a server host key captured (never authenticated) from a node.
type HostKey struct {
	Type        string
	Fingerprint string
	Raw         []byte
}

type sshMeta struct {
	ID               string    `json:"id"`
	CreatedAt        time.Time `json:"created_at"`
	PendingID        string    `json:"pending_id,omitempty"`
	PendingCreatedAt time.Time `json:"pending_created_at,omitempty"`
}

// SSHKeyStore keeps the ONE cluster SSH key pair and the pinned host keys
// under DATA_DIR/cluster-ssh (dir 0700, secrets 0600). WebKVM never pushes
// the public key anywhere: the operator authorizes it on each node.
type SSHKeyStore struct {
	dir string
	mu  sync.Mutex
	// dial opens the TCP connection for host key capture. Tests may replace it.
	dial func(ctx context.Context, network, addr string) (net.Conn, error)
}

func NewSSHKeyStore(dataDir string) *SSHKeyStore {
	d := &net.Dialer{}
	return &SSHKeyStore{dir: filepath.Join(dataDir, sshKeyDirName), dial: d.DialContext}
}

var sharedSSHStores sync.Map // dataDir -> *SSHKeyStore

// SharedSSHKeyStore returns the process-wide store for dataDir, so the
// connection pool and the API handlers serialize on the same mutex.
func SharedSSHKeyStore(dataDir string) *SSHKeyStore {
	if v, ok := sharedSSHStores.Load(dataDir); ok {
		return v.(*SSHKeyStore)
	}
	v, _ := sharedSSHStores.LoadOrStore(dataDir, NewSSHKeyStore(dataDir))
	return v.(*SSHKeyStore)
}

// KeyPath and KnownHostsPath are the on-disk locations handed to libvirt.
func (s *SSHKeyStore) KeyPath() string        { return filepath.Join(s.dir, sshKeyFile) }
func (s *SSHKeyStore) KnownHostsPath() string { return filepath.Join(s.dir, sshKnownHosts) }

// PendingKeyPath is where the not-yet-active rotation key lives.
func (s *SSHKeyStore) PendingKeyPath() string { return filepath.Join(s.dir, sshPendingKey) }

// checkDir validates the store directory the same way the Incus credential
// store does: a real directory, never a symlink, no group/world access.
func (s *SSHKeyStore) checkDir() error {
	info, err := os.Lstat(s.dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return errors.New("cluster SSH directory permissions are unsafe")
	}
	return nil
}

func (s *SSHKeyStore) ensureDir() error {
	if info, err := os.Lstat(s.dir); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("cluster SSH directory is a symlink")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		return err
	}
	return s.checkDir()
}

// readFile reads a regular file after Lstat. secret files must be 0600-ish;
// public ones must not be group/world writable.
func (s *SSHKeyStore) readFile(name string, secret bool) ([]byte, error) {
	if err := s.checkDir(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	mask := os.FileMode(0o022)
	if secret {
		mask = 0o077
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&mask != 0 {
		return nil, fmt.Errorf("cluster SSH file %q has unsafe type or permissions", name)
	}
	return os.ReadFile(path)
}

// writeAtomic writes via a temp file in the same directory and renames it.
func (s *SSHKeyStore) writeAtomic(name string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(s.dir, ".tmp-"+name+"-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, filepath.Join(s.dir, name)); err != nil {
		return err
	}
	ok = true
	return nil
}

func randomID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// newKeyPair returns the OpenSSH-format private key, the public key line
// (with comment) and its fingerprint.
func newKeyPair(id string) (priv []byte, pub string, fp string, err error) {
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", "", err
	}
	comment := "webkvm-cluster-" + id
	block, err := ssh.MarshalPrivateKey(sk, comment)
	if err != nil {
		return nil, "", "", err
	}
	sp, err := ssh.NewPublicKey(pk)
	if err != nil {
		return nil, "", "", err
	}
	line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment
	return pem.EncodeToMemory(block), line, ssh.FingerprintSHA256(sp), nil
}

func (s *SSHKeyStore) readMeta() (sshMeta, error) {
	var m sshMeta
	b, err := s.readFile(sshMetaFile, true)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(b, &m)
	return m, err
}

func (s *SSHKeyStore) writeMeta(m sshMeta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.writeAtomic(sshMetaFile, b, 0o600)
}

func parsePub(line string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return k, err
}

// Info reports the active key and any pending rotation.
func (s *SSHKeyStore) Info() (SSHKeyInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.infoLocked()
}

func (s *SSHKeyStore) infoLocked() (SSHKeyInfo, error) {
	if _, err := os.Lstat(s.dir); os.IsNotExist(err) {
		return SSHKeyInfo{}, nil
	}
	if _, err := os.Lstat(s.KeyPath()); os.IsNotExist(err) {
		if err := s.checkDir(); err != nil {
			return SSHKeyInfo{}, err
		}
		return SSHKeyInfo{}, nil
	}
	if _, err := s.readFile(sshKeyFile, true); err != nil {
		return SSHKeyInfo{}, err
	}
	pubRaw, err := s.readFile(sshPubFile, false)
	if err != nil {
		return SSHKeyInfo{}, err
	}
	line := strings.TrimSpace(string(pubRaw))
	pk, err := parsePub(line)
	if err != nil {
		return SSHKeyInfo{}, errors.New("cluster SSH public key is invalid")
	}
	info := SSHKeyInfo{Configured: true, Algorithm: pk.Type(), Fingerprint: ssh.FingerprintSHA256(pk), PublicKey: line}
	meta, err := s.readMeta()
	if err == nil {
		info.CreatedAt = meta.CreatedAt
		if meta.PendingID != "" {
			if praw, perr := s.readFile(sshPendingPub, false); perr == nil {
				pl := strings.TrimSpace(string(praw))
				if ppk, perr := parsePub(pl); perr == nil {
					info.Pending = &SSHKeyPending{Fingerprint: ssh.FingerprintSHA256(ppk), PublicKey: pl, CreatedAt: meta.PendingCreatedAt}
				}
			}
		}
	}
	if praw, perr := s.readFile(sshPrevPub, false); perr == nil {
		pl := strings.TrimSpace(string(praw))
		if ppk, perr := parsePub(pl); perr == nil {
			if _, kerr := s.readFile(sshPrevKey, true); kerr == nil {
				info.Previous = &SSHKeyPrevious{Fingerprint: ssh.FingerprintSHA256(ppk), PublicKey: pl}
			}
		}
	}
	return info, nil
}

// Generate creates the cluster key. ErrKeyExists if one is configured.
func (s *SSHKeyStore) Generate() (SSHKeyInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, err := s.infoLocked(); err != nil {
		return SSHKeyInfo{}, err
	} else if cur.Configured {
		return SSHKeyInfo{}, ErrKeyExists
	}
	if err := s.ensureDir(); err != nil {
		return SSHKeyInfo{}, err
	}
	id, err := randomID()
	if err != nil {
		return SSHKeyInfo{}, err
	}
	priv, pub, _, err := newKeyPair(id)
	if err != nil {
		return SSHKeyInfo{}, err
	}
	// Metadata first: a key without meta is still usable, meta without a
	// key just reads as "not configured".
	if err := s.writeMeta(sshMeta{ID: id, CreatedAt: time.Now().UTC()}); err != nil {
		return SSHKeyInfo{}, err
	}
	if err := s.writeAtomic(sshPubFile, []byte(pub+"\n"), 0o644); err != nil {
		return SSHKeyInfo{}, err
	}
	if err := s.writeAtomic(sshKeyFile, priv, 0o600); err != nil {
		return SSHKeyInfo{}, err
	}
	return s.infoLocked()
}

// StartRotation generates a new key kept as pending until CommitRotation.
// Calling it again replaces the previous pending key.
func (s *SSHKeyStore) StartRotation() (SSHKeyPending, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.infoLocked()
	if err != nil {
		return SSHKeyPending{}, err
	}
	if !cur.Configured {
		return SSHKeyPending{}, ErrNoKey
	}
	meta, err := s.readMeta()
	if err != nil {
		return SSHKeyPending{}, err
	}
	id, err := randomID()
	if err != nil {
		return SSHKeyPending{}, err
	}
	priv, pub, fp, err := newKeyPair(id)
	if err != nil {
		return SSHKeyPending{}, err
	}
	now := time.Now().UTC()
	if err := s.writeAtomic(sshPendingKey, priv, 0o600); err != nil {
		return SSHKeyPending{}, err
	}
	if err := s.writeAtomic(sshPendingPub, []byte(pub+"\n"), 0o644); err != nil {
		return SSHKeyPending{}, err
	}
	meta.PendingID, meta.PendingCreatedAt = id, now
	if err := s.writeMeta(meta); err != nil {
		return SSHKeyPending{}, err
	}
	return SSHKeyPending{Fingerprint: fp, PublicKey: pub, CreatedAt: now}, nil
}

// CommitRotation makes the pending key the active one.
func (s *SSHKeyStore) CommitRotation() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.pendingMeta()
	if err != nil {
		return err
	}
	if _, err := s.readFile(sshPendingKey, true); err != nil {
		return err
	}
	if _, err := s.readFile(sshPendingPub, false); err != nil {
		return err
	}
	// Keep the outgoing key for rollback (until the next rotation commit or
	// an explicit rollback). Best effort ordering: a failed copy aborts the
	// commit so the old key is never lost.
	if err := s.keepPreviousLocked(); err != nil {
		return err
	}
	// Meta first so the active key's id/created_at always describe a key
	// that exists; then the public half, then the private half.
	meta.ID, meta.CreatedAt = meta.PendingID, time.Now().UTC()
	meta.PendingID, meta.PendingCreatedAt = "", time.Time{}
	if err := s.writeMeta(meta); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(s.dir, sshPendingPub), filepath.Join(s.dir, sshPubFile)); err != nil {
		return err
	}
	return os.Rename(filepath.Join(s.dir, sshPendingKey), filepath.Join(s.dir, sshKeyFile))
}

// keepPreviousLocked copies the active key pair to the .prev files (0600).
func (s *SSHKeyStore) keepPreviousLocked() error {
	priv, err := s.readFile(sshKeyFile, true)
	if err != nil {
		return err
	}
	pub, err := s.readFile(sshPubFile, false)
	if err != nil {
		return err
	}
	if err := s.writeAtomic(sshPrevPub, pub, 0o644); err != nil {
		return err
	}
	return s.writeAtomic(sshPrevKey, priv, 0o600)
}

// RollbackRotation restores the key that was active before the last commit.
// The key being replaced is discarded (it is not kept anywhere), so a
// rollback cannot be undone: rotate again to get a new key. Only the
// immediately previous key is ever kept.
func (s *SSHKeyStore) RollbackRotation() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.readFile(sshPrevKey, true); err != nil {
		return ErrNoPrevious
	}
	if _, err := s.readFile(sshPrevPub, false); err != nil {
		return ErrNoPrevious
	}
	meta, err := s.readMeta()
	if err != nil {
		return err
	}
	meta.CreatedAt = time.Now().UTC()
	if err := s.writeMeta(meta); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(s.dir, sshPrevPub), filepath.Join(s.dir, sshPubFile)); err != nil {
		return err
	}
	return os.Rename(filepath.Join(s.dir, sshPrevKey), filepath.Join(s.dir, sshKeyFile))
}

// AbortRotation discards the pending key.
func (s *SSHKeyStore) AbortRotation() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	meta, err := s.pendingMeta()
	if err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(s.dir, sshPendingKey))
	_ = os.Remove(filepath.Join(s.dir, sshPendingPub))
	meta.PendingID, meta.PendingCreatedAt = "", time.Time{}
	return s.writeMeta(meta)
}

func (s *SSHKeyStore) pendingMeta() (sshMeta, error) {
	if _, err := os.Lstat(s.dir); os.IsNotExist(err) {
		return sshMeta{}, ErrNoPending
	}
	meta, err := s.readMeta()
	if err != nil {
		if os.IsNotExist(err) {
			return sshMeta{}, ErrNoPending
		}
		return sshMeta{}, err
	}
	if meta.PendingID == "" {
		return sshMeta{}, ErrNoPending
	}
	return meta, nil
}

// sanitizeKeyToken keeps only characters that can appear in an OpenSSH
// public key token (type, base64, a plain comment).
func sanitizeKeyToken(s string, extra string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case strings.ContainsRune("+/=@._:-"+extra, r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// AuthorizedKeysLine returns the restricted authorized_keys entry for pub.
// pub is reduced to "type base64 comment" with a conservative alphabet, so
// no newline, quote or shell metacharacter can ever survive into the line.
func AuthorizedKeysLine(pub string) string {
	f := strings.Fields(pub)
	if len(f) < 2 {
		return ""
	}
	typ, key := sanitizeKeyToken(f[0], ""), sanitizeKeyToken(f[1], "")
	if typ == "" || key == "" {
		return ""
	}
	out := `restrict,port-forwarding,permitopen="127.0.0.1:*" ` + typ + " " + key
	if len(f) > 2 {
		if c := sanitizeKeyToken(strings.Join(f[2:], "-"), ""); c != "" {
			out += " " + c
		}
	}
	return out
}

// AuthorizedKeysLine is the method form of the package function.
func (s *SSHKeyStore) AuthorizedKeysLine(pub string) string { return AuthorizedKeysLine(pub) }

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// AuthorizeCommand returns a copy-paste shell command the operator runs,
// logged in as user, on the node to authorize the cluster key. WebKVM never
// runs it. Empty when no key is configured or user is not a valid account.
func (s *SSHKeyStore) AuthorizeCommand(user string) string {
	if !nodeSSHUserRE.MatchString(user) {
		return ""
	}
	info, err := s.Info()
	if err != nil || !info.Configured {
		return ""
	}
	return authorizeCommandFor(info.PublicKey)
}

// AuthorizeCommandForKey builds the command for an arbitrary public key
// (used to authorize a pending rotation key).
func AuthorizeCommandForKey(pub string) string { return authorizeCommandFor(pub) }

func authorizeCommandFor(pub string) string {
	line := AuthorizedKeysLine(pub)
	if line == "" {
		return ""
	}
	q := shellQuote(line)
	return "install -d -m 700 ~/.ssh && { grep -qxF -- " + q + " ~/.ssh/authorized_keys 2>/dev/null || echo " + q +
		" >> ~/.ssh/authorized_keys; } && chmod 600 ~/.ssh/authorized_keys"
}

// knownHostsName is the host token OpenSSH/libssh look up: the bare host
// for port 22, "[host]:port" otherwise.
func knownHostsName(host string, port int) string {
	if port == 22 {
		return host
	}
	return "[" + host + "]:" + strconv.Itoa(port)
}

func hostMatches(field, host string, port int) bool {
	for _, h := range strings.Split(field, ",") {
		if h == knownHostsName(host, port) || h == "["+host+"]:"+strconv.Itoa(port) {
			return true
		}
	}
	return false
}

type knownEntry struct {
	raw  string
	host bool // matches the requested host
	typ  string
	key  []byte
}

func (s *SSHKeyStore) readKnownHosts(host string, port int) ([]knownEntry, error) {
	b, err := s.readFile(sshKnownHosts, true)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []knownEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := sc.Text()
		e := knownEntry{raw: line}
		f := strings.Fields(line)
		if len(f) >= 3 && !strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "@") && hostMatches(f[0], host, port) {
			if kb, err := base64.StdEncoding.DecodeString(f[2]); err == nil {
				e.host, e.typ, e.key = true, f[1], kb
			}
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// PinnedHostKey reports the pinned fingerprint of host:port without any
// network access.
func (s *SSHKeyStore) PinnedHostKey(host string, port int) (fingerprint string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.readKnownHosts(host, port)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if e.host {
			if pk, err := ssh.ParsePublicKey(e.key); err == nil {
				return ssh.FingerprintSHA256(pk), true
			}
		}
	}
	return "", false
}

type errHostKeyCaptured struct{}

func (errHostKeyCaptured) Error() string { return "host key captured" }

// CaptureHostKey connects, records the server host key and aborts. It never
// authenticates and never trusts anything.
func (s *SSHKeyStore) CaptureHostKey(ctx context.Context, host string, port int) (HostKey, error) {
	if !validCaptureTarget(host, port) {
		return HostKey{}, ErrHostUnreachable
	}
	ctx, cancel := context.WithTimeout(ctx, hostKeyCaptureTO)
	defer cancel()
	conn, err := s.dial(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return HostKey{}, classifyDialErr(ctx, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	var got HostKey
	cfg := &ssh.ClientConfig{
		User: "webkvm-hostkey-probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = HostKey{Type: key.Type(), Fingerprint: ssh.FingerprintSHA256(key), Raw: key.Marshal()}
			return errHostKeyCaptured{}
		},
		Timeout: hostKeyCaptureTO,
	}
	// Abort when the context ends even if the handshake blocks.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	c, chans, reqs, err := ssh.NewClientConn(conn, net.JoinHostPort(host, strconv.Itoa(port)), cfg)
	if err == nil { // cannot happen: callback always errors
		_ = c.Close()
		_ = chans
		_ = reqs
	}
	if got.Raw != nil {
		return got, nil
	}
	return HostKey{}, classifyDialErr(ctx, err)
}

func validCaptureTarget(host string, port int) bool {
	return host != "" && port > 0 && port < 65536 && !strings.ContainsAny(host, " \t\r\n/@?#")
}

func classifyDialErr(ctx context.Context, err error) error {
	var ne net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return ErrHostTimeout
	}
	return ErrHostUnreachable
}

// HostKeyState recaptures the live host key and compares it with the pinned
// one: "unknown" (nothing pinned, or the host cannot be reached), "pinned"
// (same key) or "changed" (different key). The fingerprint is the live one,
// or the pinned one when the host could not be reached.
func (s *SSHKeyStore) HostKeyState(host string, port int) (state string, fingerprint string) {
	pinnedFP, pinned := s.PinnedHostKey(host, port)
	ctx, cancel := context.WithTimeout(context.Background(), hostKeyCaptureTO)
	defer cancel()
	hk, err := s.CaptureHostKey(ctx, host, port)
	if err != nil {
		if pinned {
			return HostKeyPinned, pinnedFP
		}
		return HostKeyUnknown, ""
	}
	if !pinned {
		return HostKeyUnknown, hk.Fingerprint
	}
	if subtle.ConstantTimeCompare([]byte(hk.Fingerprint), []byte(pinnedFP)) == 1 {
		return HostKeyPinned, hk.Fingerprint
	}
	return HostKeyChanged, hk.Fingerprint
}

// PinHostKey re-captures the host key and pins it only if it matches the
// fingerprint the operator confirmed. A different already-pinned key is
// never overwritten unless replace is set.
func (s *SSHKeyStore) PinHostKey(host string, port int, fingerprint string, replace bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), hostKeyCaptureTO)
	defer cancel()
	hk, err := s.CaptureHostKey(ctx, host, port)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(fingerprint)), []byte(hk.Fingerprint)) != 1 {
		return ErrFingerprintMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	entries, err := s.readKnownHosts(host, port)
	if err != nil {
		return err
	}
	var kept []string
	already := false
	for _, e := range entries {
		if !e.host {
			kept = append(kept, e.raw)
			continue
		}
		if e.typ == hk.Type && bytes.Equal(e.key, hk.Raw) {
			already = true
			kept = append(kept, e.raw)
			continue
		}
		if !replace {
			return ErrHostKeyChanged
		}
	}
	if !already {
		kept = append(kept, knownHostsName(host, port)+" "+hk.Type+" "+base64.StdEncoding.EncodeToString(hk.Raw))
	}
	return s.writeAtomic(sshKnownHosts, []byte(strings.Join(kept, "\n")+"\n"), 0o600)
}

// RemoveHostKey drops every pinned key of host:port (used on node delete).
func (s *SSHKeyStore) RemoveHostKey(host string, port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Lstat(s.dir); os.IsNotExist(err) {
		return nil
	}
	entries, err := s.readKnownHosts(host, port)
	if err != nil {
		return err
	}
	var kept []string
	removed := false
	for _, e := range entries {
		if e.host {
			removed = true
			continue
		}
		kept = append(kept, e.raw)
	}
	if !removed {
		return nil
	}
	data := ""
	if len(kept) > 0 {
		data = strings.Join(kept, "\n") + "\n"
	}
	return s.writeAtomic(sshKnownHosts, []byte(data), 0o600)
}

// SSHLastTest is the outcome of the last managed-SSH connection test of one
// node. It carries no secrets: only the verdict, a stable error code and when.
type SSHLastTest struct {
	OK   bool      `json:"ok"`
	Code string    `json:"code,omitempty"`
	At   time.Time `json:"at"`
}

const sshLastTestFile = "last_test.json"

// LoadLastTests reads DATA_DIR/cluster-ssh/last_test.json. A missing,
// unsafe or corrupt file yields an empty map: the verdicts are a cache that a
// new test rebuilds, never a reason to fail.
func (s *SSHKeyStore) LoadLastTests() map[string]SSHLastTest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]SSHLastTest{}
	data, err := s.readFile(sshLastTestFile, true)
	if err != nil {
		return out
	}
	var m map[string]SSHLastTest
	if json.Unmarshal(data, &m) != nil {
		return out
	}
	for id, v := range m {
		if id != "" && !v.At.IsZero() {
			out[id] = v
		}
	}
	return out
}

// SaveLastTests atomically replaces last_test.json (0600).
func (s *SSHKeyStore) SaveLastTests(m map[string]SSHLastTest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return s.writeAtomic(sshLastTestFile, b, 0o600)
}
