package libvirt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// TestPoolPurposeStoreAtomicSave: this was the only store in the tree
// writing straight onto the live path with os.WriteFile, which
// truncates before writing. A crash mid-write left the file empty, and
// on restart every pool fell back to InferPoolPurpose — turning the
// ISO/container/backup/template pools into plain disk pools.
func TestPoolPurposeStoreAtomicSave(t *testing.T) {
	dir := t.TempDir()
	s := NewPoolPurposeStore(dir)
	if err := s.Set("mydisk-isos", PoolPurposeISO); err != nil {
		t.Fatal(err)
	}

	// No .tmp may survive a successful save.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}

	// A reloaded store must see the same mapping.
	fresh := NewPoolPurposeStore(dir)
	if err := fresh.Load(); err != nil {
		t.Fatal(err)
	}
	if got := fresh.Get("mydisk-isos"); got != PoolPurposeISO {
		t.Errorf("after reload Get = %q, want %q", got, PoolPurposeISO)
	}
}

// TestPoolPurposeStoreConcurrentWrites: Set and Delete release the
// lock before calling Save, so several callers could reach the file
// write at once. The init-disk flow does exactly this — it registers
// one pool per selected folder, back to back.
func TestPoolPurposeStoreConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	s := NewPoolPurposeStore(dir)

	const writers, each = 8, 40
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				name := string(rune('a'+i)) + string(rune('0'+j%10))
				if err := s.Set(name, PoolPurposeDisk); err != nil {
					t.Errorf("Set: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	b, err := os.ReadFile(filepath.Join(dir, "pool-purposes.json"))
	if err != nil {
		t.Fatalf("file missing after concurrent saves: %v", err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("corrupt JSON on disk after concurrent saves: %v", err)
	}
	if len(m) != writers*10 {
		t.Errorf("got %d entries, want %d", len(m), writers*10)
	}
}
