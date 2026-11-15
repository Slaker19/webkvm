package nodes

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func rotNode(srv *fwdSSHServer) Node {
	return Node{ID: "n1", URI: "qemu+ssh://root@" + srv.host + ":" + strconv.Itoa(srv.port) + "/system"}
}

// The pending key is tested over a throwaway connection: it must succeed only
// on a node that authorized the NEW key, and never use the active key.
func TestProbePendingKeyUsesOnlyThePendingKey(t *testing.T) {
	store := NewSSHKeyStore(t.TempDir())
	oldInfo, err := store.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ProbePendingKey(context.Background(), Node{URI: "qemu+ssh://root@127.0.0.1:1/system"}); !errors.Is(err, ErrNoPending) {
		t.Fatalf("no pending: %v", err)
	}
	p, err := store.StartRotation()
	if err != nil {
		t.Fatal(err)
	}
	oldPub, _ := parsePub(oldInfo.PublicKey)
	newPub, _ := parsePub(p.PublicKey)
	onlyOld, onlyNew := startFwdSSH(t, oldPub), startFwdSSH(t, newPub)
	for _, s := range []*fwdSSHServer{onlyOld, onlyNew} {
		if err := store.PinHostKey(s.host, s.port, ssh.FingerprintSHA256(s.signer.PublicKey()), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ProbePendingKey(context.Background(), rotNode(onlyNew)); err != nil {
		t.Fatalf("node with new key: %v", err)
	}
	if err := store.ProbePendingKey(context.Background(), rotNode(onlyOld)); !errors.Is(err, ErrSSHAuthDenied) {
		t.Fatalf("node with old key only: %v", err)
	}
	// An unpinned host key fails closed.
	un := startFwdSSH(t, newPub)
	if err := store.ProbePendingKey(context.Background(), rotNode(un)); !errors.Is(err, ErrHostKeyUnpinned) {
		t.Fatalf("unpinned: %v", err)
	}
}

func TestCommitKeepsPreviousKeyAndRollbackRestoresIt(t *testing.T) {
	dir := t.TempDir()
	store := NewSSHKeyStore(dir)
	first, _ := store.Generate()
	if err := store.RollbackRotation(); !errors.Is(err, ErrNoPrevious) {
		t.Fatalf("rollback with nothing: %v", err)
	}
	if _, err := store.StartRotation(); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRotation(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, "cluster-ssh", "id_ed25519.prev"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("prev key: %v %v", st, err)
	}
	second, _ := store.Info()
	if second.Fingerprint == first.Fingerprint || second.Previous == nil || second.Previous.Fingerprint != first.Fingerprint {
		t.Fatalf("info after commit: %+v", second)
	}
	// A second rotation replaces the kept key with the then-active one.
	if _, err := store.StartRotation(); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitRotation(); err != nil {
		t.Fatal(err)
	}
	third, _ := store.Info()
	if third.Previous.Fingerprint != second.Fingerprint {
		t.Fatalf("prev must be the key before the LAST commit: %+v", third)
	}
	if err := store.RollbackRotation(); err != nil {
		t.Fatal(err)
	}
	back, _ := store.Info()
	if back.Fingerprint != second.Fingerprint || back.Previous != nil {
		t.Fatalf("after rollback: %+v", back)
	}
	if err := store.RollbackRotation(); !errors.Is(err, ErrNoPrevious) {
		t.Fatalf("second rollback: %v", err)
	}
}

func TestBuildConnURIWithKeyPath(t *testing.T) {
	store := readyStore(t)
	if _, err := store.StartRotation(); err != nil {
		t.Fatal(err)
	}
	pinForTest(t, store, "node1.example", 22)
	n := Node{URI: "qemu+ssh://root@node1.example/system"}
	u, err := BuildConnURI(n, store, WithKeyPath(store.PendingKeyPath()))
	if err != nil || !strings.Contains(u, "id_ed25519.pending") || strings.Contains(u, "keyfile="+url.QueryEscape(store.KeyPath())+"&") {
		t.Fatalf("uri does not use the pending key: %s %v", u, err)
	}
	if u, _ := BuildConnURI(n, store); !strings.Contains(u, "keyfile="+url.QueryEscape(store.KeyPath())+"&") {
		t.Fatalf("default must stay the active key: %s", u)
	}
	if _, err := BuildConnURI(n, store, WithKeyPath("/tmp/evil key;rm")); err == nil {
		t.Fatal("unsafe key path accepted")
	}
}
