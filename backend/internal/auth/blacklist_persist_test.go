package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestBlacklistPersistsAcrossInstances: revoke → fichero válido → nueva
// instancia cargada del fichero sigue viendo el token revocado.
func TestBlacklistPersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revoked.json")
	bl := NewTokenBlacklistWithPath(path)
	defer bl.Close()
	bl.Revoke("jti-abc", "", time.Now().Add(1*time.Hour))
	if !bl.IsRevoked("jti-abc", "") {
		t.Fatalf("just-revoked token reports not revoked")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("revoked.json not written: %v", err)
	}
	var rf revokedFile
	if err := json.Unmarshal(raw, &rf); err != nil {
		t.Fatalf("revoked.json not valid json: %v", err)
	}
	if rf.Version != revokedFileVersion {
		t.Fatalf("version = %d, want %d", rf.Version, revokedFileVersion)
	}
	bl2 := NewTokenBlacklistWithPath(path)
	defer bl2.Close()
	if !bl2.IsRevoked("jti-abc", "") {
		t.Fatalf("fresh instance loaded from disk does not see revoked token")
	}
}

// Entradas expiradas NO reviven al cargar.
func TestBlacklistLoadDropsExpired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revoked.json")
	bl := NewTokenBlacklistWithPath(path)
	defer bl.Close()
	bl.Revoke("old", "", time.Now().Add(-1*time.Minute))
	if bl.IsRevoked("old", "") {
		t.Fatalf("expired entry reported as revoked")
	}
	bl2 := NewTokenBlacklistWithPath(path)
	defer bl2.Close()
	if bl2.IsRevoked("old", "") {
		t.Fatalf("expired entry resurrected by load")
	}
}

func readTmpFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return b
}

// Corrupción → fail-open, custodia .corrupt-<ts>, sin abortar arranque.
func TestBlacklistCorruptFileQuarantined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revoked.json")
	if err := os.WriteFile(path, []byte("{not valid json!!!"), 0600); err != nil {
		t.Fatal(err)
	}
	bl := NewTokenBlacklistWithPath(path)
	defer bl.Close()
	if bl.IsRevoked("anything", "") {
		t.Fatalf("corrupt file must load as empty set")
	}
	entries, err := filepath.Glob(path + ".corrupt-*")
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one .corrupt-* backup, got err=%v n=%d", err, len(entries))
	}
	if len(readTmpFile(t, entries[0])) == 0 {
		t.Fatalf("quarantined copy is empty")
	}
	bl.Revoke("again", "", time.Now().Add(time.Hour))
	bl2 := NewTokenBlacklistWithPath(path)
	defer bl2.Close()
	if !bl2.IsRevoked("again", "") {
		t.Fatalf("blacklist did not recover after corruption")
	}
	after, _ := filepath.Glob(path + ".corrupt-*")
	if len(after) != 1 || after[0] != entries[0] {
		t.Fatalf("original corrupt backup changed: %v -> %v", entries, after)
	}
}

// Atomicidad: sin .tmp residual y permisos 0600.
func TestBlacklistSaveIsAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revoked.json")
	bl := NewTokenBlacklistWithPath(path)
	defer bl.Close()
	bl.Revoke("x1", "", time.Now().Add(time.Hour))
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file left behind after save")
	}
	fi, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("revoked.json missing: %v", statErr)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("perms = %v, want 0600", fi.Mode().Perm())
	}
}

// Close idempotente.
func TestBlacklistCloseIdempotent(t *testing.T) {
	bl := NewTokenBlacklistWithPath(filepath.Join(t.TempDir(), "revoked.json"))
	bl.Close()
	bl.Close()
}

// 100 revocaciones simultáneas: JSON válido, ninguna clave perdida.
func TestBlacklistConcurrentRevocations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revoked.json")
	bl := NewTokenBlacklistWithPath(path)
	defer bl.Close()
	const n = 100
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bl.Revoke(fmt.Sprintf("c%d", i), "", time.Now().Add(time.Hour))
		}(i)
	}
	wg.Wait()
	var rf revokedFile
	if err := json.Unmarshal(readTmpFile(t, path), &rf); err != nil {
		t.Fatalf("file corrupted under concurrency: %v", err)
	}
	bl2 := NewTokenBlacklistWithPath(path)
	defer bl2.Close()
	for _, k := range []string{"c0", "c50", "c99"} {
		if !bl2.IsRevoked(k, "") {
			t.Fatalf("revoked key %s lost across persist", k)
		}
	}
}

// Lectores concurrentes durante 50 revocations: sin deadlock ni carreras
// (el race detector corrobora).
func TestBlacklistReadersDuringWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "revoked.json")
	bl := NewTokenBlacklistWithPath(path)
	defer bl.Close()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bl.Revoke(fmt.Sprintf("w%d", i), "", time.Now().Add(2*time.Minute))
		}(i)
	}
	var readers sync.WaitGroup
	stop := make(chan struct{})
	for r := 0; r < 4; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := 0; i < 300; i++ {
				select {
				case <-stop:
					return
				default:
				}
				_ = bl.IsRevoked("nope", "")
			}
		}()
	}
	wg.Wait()
	close(stop)
	readers.Wait()
}

// Compatible: blacklist sin path funciona solo en memoria.
func TestBlacklistNoPathWorks(t *testing.T) {
	bl := NewTokenBlacklist()
	defer bl.Close()
	bl.Revoke("x", "", time.Now().Add(time.Hour))
	if !bl.IsRevoked("x", "") {
		t.Fatalf("in-memory revocation lost")
	}
}
