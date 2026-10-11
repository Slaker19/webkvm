package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveInRoots(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	pool := filepath.Join(tmp, "pool")
	other := filepath.Join(tmp, "other")
	for _, d := range []string{pool, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	in := filepath.Join(pool, "a.iso")
	out := filepath.Join(other, "secret")
	os.WriteFile(in, []byte("x"), 0o644)
	os.WriteFile(out, []byte("x"), 0o644)
	os.Symlink(out, filepath.Join(pool, "escape.iso"))
	os.Symlink(pool, filepath.Join(tmp, "poollink"))

	if got, err := ResolveInRoots(in, []string{pool}); err != nil || got != in {
		t.Fatalf("inside: %q %v", got, err)
	}
	if got, err := ResolveInRoots(filepath.Join(tmp, "poollink", "a.iso"), []string{pool}); err != nil || got != in {
		t.Fatalf("via symlinked dir: %q %v", got, err)
	}
	if _, err := ResolveInRoots(out, []string{pool}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("outside: %v", err)
	}
	if _, err := ResolveInRoots(filepath.Join(pool, "escape.iso"), []string{pool}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("symlink escape: %v", err)
	}
	if _, err := ResolveInRoots(pool, []string{tmp}); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("dir: %v", err)
	}
	if _, err := ResolveInRoots("/dev/null", []string{"/dev"}); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("device: %v", err)
	}
	if _, err := ResolveInRoots("relative.iso", []string{pool}); err == nil {
		t.Fatal("relative path accepted")
	}
	sib := filepath.Join(tmp, "pool2")
	os.MkdirAll(sib, 0o755)
	os.WriteFile(filepath.Join(sib, "b.iso"), []byte("x"), 0o644)
	if _, err := ResolveInRoots(filepath.Join(sib, "b.iso"), []string{pool}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("sibling prefix: %v", err)
	}
}

func TestCreateExclusive(t *testing.T) {
	dir := t.TempDir()
	f, err := CreateExclusive(dir, "x.iso", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := CreateExclusive(dir, "x.iso", 0o600); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite not refused: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(outside, []byte("keep"), 0o600)
	os.Symlink(outside, filepath.Join(dir, "link"))
	if _, err := CreateExclusive(dir, "link", 0o600); err == nil {
		t.Fatal("followed symlink")
	}
	if b, _ := os.ReadFile(outside); string(b) != "keep" {
		t.Fatal("victim modified")
	}
	for _, n := range []string{"", ".", "..", "a/b", "../x"} {
		if _, err := CreateExclusive(dir, n, 0o600); err == nil {
			t.Fatalf("name %q accepted", n)
		}
	}
}
