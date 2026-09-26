package api

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveFile(t *testing.T) {
	t.Run("same directory move", func(t *testing.T) {
		tmpDir := t.TempDir()
		src := filepath.Join(tmpDir, "src.txt")
		dst := filepath.Join(tmpDir, "dst.txt")

		content := []byte("hello world same dir")
		if err := os.WriteFile(src, content, 0o644); err != nil {
			t.Fatalf("write src: %v", err)
		}

		if err := moveFile(src, dst); err != nil {
			t.Fatalf("moveFile failed: %v", err)
		}

		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Errorf("src still exists after move")
		}

		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("read dst: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("content mismatch: got %q, want %q", string(got), string(content))
		}
	})

	t.Run("different directory move", func(t *testing.T) {
		dir1 := t.TempDir()
		dir2 := t.TempDir()
		src := filepath.Join(dir1, "source.qcow2")
		dst := filepath.Join(dir2, "target.qcow2")

		content := []byte("qcow2 image simulated payload bytes")
		if err := os.WriteFile(src, content, 0o600); err != nil {
			t.Fatalf("write src: %v", err)
		}

		if err := moveFile(src, dst); err != nil {
			t.Fatalf("moveFile failed: %v", err)
		}

		if _, err := os.Stat(src); !os.IsNotExist(err) {
			t.Errorf("src still exists after move")
		}

		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("read dst: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("content mismatch: got %q, want %q", string(got), string(content))
		}
	})
}

// TestPoolStateUsable pins the cross-backend vocabulary. libvirt calls a
// healthy pool "active" and Incus calls it "created"; an earlier literal
// comparison against "active" refused every working Incus pool with
// "storage pool is not active", which no operator could have acted on.
func TestPoolStateUsable(t *testing.T) {
	for state, want := range map[string]bool{
		"active":     true,
		"created":    true,
		"ACTIVE":     true,
		"  created ": true,
		"inactive":   false,
		"pending":    false,
		"errored":    false,
		"unknown":    false,
		"":           false,
	} {
		if got := poolStateUsable(state); got != want {
			t.Errorf("poolStateUsable(%q) = %v, want %v", state, got, want)
		}
	}
}
