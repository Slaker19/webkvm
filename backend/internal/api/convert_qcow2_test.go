package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestConvertToQcow2_MissingQemuImg(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err == nil {
		t.Skip("qemu-img is installed on this host; this test only covers the missing-binary path")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.raw")
	if err := os.WriteFile(src, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	dst := filepath.Join(dir, "out.qcow2")
	if convertToQcow2(src, dst) {
		t.Fatal("expected convertToQcow2 to return false when qemu-img is absent")
	}
}

func TestConvertToQcow2_Success(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed on this host")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.raw")
	if err := exec.Command("qemu-img", "create", "-f", "raw", src, "1M").Run(); err != nil {
		t.Fatalf("failed to create test fixture: %v", err)
	}
	dst := filepath.Join(dir, "out.qcow2")
	if !convertToQcow2(src, dst) {
		t.Fatal("expected convertToQcow2 to succeed")
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("expected output file to exist: %v", err)
	}
}

func TestConvertToQcow2_InvalidSource(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed on this host")
	}
	dir := t.TempDir()
	dst := filepath.Join(dir, "out.qcow2")
	if convertToQcow2(filepath.Join(dir, "does-not-exist.raw"), dst) {
		t.Fatal("expected convertToQcow2 to return false for a missing source file")
	}
}
