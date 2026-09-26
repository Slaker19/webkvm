package remotebrowse

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDirsOnly(t *testing.T) {
	dir := t.TempDir()

	if err := os.Mkdir(filepath.Join(dir, "real_dir1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "real_dir2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "regular_file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Symlink to external directory
	targetDir := t.TempDir()
	if err := os.Symlink(targetDir, filepath.Join(dir, "symlink_dir")); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	got := dirsOnly(entries)
	want := []Entry{
		{Name: "real_dir1"},
		{Name: "real_dir2"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dirsOnly failed:\ngot  %+v\nwant %+v", got, want)
	}
}

func TestResolveSubpathSafely(t *testing.T) {
	mountDir := t.TempDir()
	subDir := filepath.Join(mountDir, "subfolder")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// External target
	outsideDir := t.TempDir()
	symlinkEscape := filepath.Join(mountDir, "escape_link")
	if err := os.Symlink(outsideDir, symlinkEscape); err != nil {
		t.Fatal(err)
	}

	// 1. Valid subpath
	resolved, err := safeNFSListDir(mountDir, "subfolder")
	if err != nil {
		t.Fatalf("expected subfolder to be valid, got err: %v", err)
	}
	if resolved != subDir {
		t.Fatalf("got %q, want %q", resolved, subDir)
	}

	// 2. Traversal attempt via ..
	resolved, err = safeNFSListDir(mountDir, "../../")
	if err != nil {
		t.Fatalf("expected root of mountDir for clean root, got err: %v", err)
	}
	if resolved != mountDir {
		t.Fatalf("got %q, want %q", resolved, mountDir)
	}

	// 3. Symlink escape attempt
	_, err = safeNFSListDir(mountDir, "escape_link")
	if err == nil {
		t.Fatalf("expected error for symlink escaping mountDir, but got nil")
	}
}
