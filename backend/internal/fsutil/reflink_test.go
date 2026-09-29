package fsutil

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFileFast(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.dat")
	dst := filepath.Join(dir, "dst.dat")

	content := []byte("Hello WebKVM CoW Reflink and Fast Copy Test!")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := CopyFileFast(src, dst); err != nil {
		t.Fatalf("CopyFileFast: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile dst: %v", err)
	}

	if !bytes.Equal(got, content) {
		t.Errorf("Copied content mismatch: expected %q, got %q", content, got)
	}
}

func TestCopyFileFast_RejectsTraversal(t *testing.T) {
	if err := CopyFileFast("../foo", "bar"); err == nil {
		t.Error("Expected error for traversal path, got nil")
	}
}
