package api

import (
	"archive/tar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func needQemuImg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed")
	}
}

// realQcow2 returns the bytes of a small, plain qcow2 image (or a stand-in
// when qemu-img is missing; callers that need a valid image skip first).
func realQcow2(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("qemu-img"); err != nil {
		return "bogus"
	}
	p := filepath.Join(t.TempDir(), "plain.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", p, "1M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v (%s)", err, out)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func backedQcow2(t *testing.T, backing string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "bad.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", "-b", backing, "-F", "raw", p).CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v (%s)", err, out)
	}
	b, _ := os.ReadFile(p)
	return string(b)
}

func TestExtractOVA_RejectsBackingFileQcow2(t *testing.T) {
	needQemuImg(t)
	host := filepath.Join(t.TempDir(), "hostsecret")
	os.WriteFile(host, make([]byte, 4096), 0o600)
	parent := t.TempDir()
	pool := filepath.Join(parent, "pool")
	os.MkdirAll(pool, 0o755)
	ova := filepath.Join(parent, "evil.ova")
	writeOVA(t, ova, map[string]ovaEntry{"disk.qcow2": reg(backedQcow2(t, host))})
	if _, _, _, err := extractAndConvertOVA(ova, pool, "evil.ova"); err == nil {
		t.Fatal("qcow2 with backing file accepted from OVA")
	}
	if ents, _ := os.ReadDir(pool); len(ents) != 0 {
		t.Fatalf("files left in pool: %v", ents)
	}
}

func TestExtractOVA_RejectsVMDKExtentOutside(t *testing.T) {
	needQemuImg(t)
	victim := filepath.Join(t.TempDir(), "victim")
	os.WriteFile(victim, make([]byte, 1<<20), 0o600)
	desc := "# Disk DescriptorFile\nversion=1\nCID=ffffffff\nparentCID=ffffffff\ncreateType=\"monolithicFlat\"\n\nRW 2048 FLAT \"" + victim + "\" 0\n"
	parent := t.TempDir()
	pool := filepath.Join(parent, "pool")
	os.MkdirAll(pool, 0o755)
	ova := filepath.Join(parent, "evil.ova")
	writeOVA(t, ova, map[string]ovaEntry{"disk.vmdk": {body: desc, typeflag: tar.TypeReg}})
	if _, _, _, err := extractAndConvertOVA(ova, pool, "evil.ova"); err == nil {
		t.Fatal("vmdk with external extent accepted from OVA")
	}
	if ents, _ := os.ReadDir(pool); len(ents) != 0 {
		t.Fatalf("files left in pool: %v", ents)
	}
}

func TestValidateUploadedImage(t *testing.T) {
	needQemuImg(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.qcow2")
	os.WriteFile(good, []byte(realQcow2(t)), 0o600)
	if err := validateUploadedImage(good); err != nil {
		t.Fatalf("plain qcow2 rejected: %v", err)
	}
	host := filepath.Join(dir, "host")
	os.WriteFile(host, make([]byte, 4096), 0o600)
	bad := filepath.Join(dir, "bad.qcow2")
	os.WriteFile(bad, []byte(backedQcow2(t, host)), 0o600)
	if err := validateUploadedImage(bad); err == nil {
		t.Fatal("backing-file qcow2 accepted")
	}
	// A qcow2 smuggled under a raw-looking name is just raw bytes: fine.
	// A raw-looking payload under a qcow2 name must fail the format check.
	fake := filepath.Join(dir, "fake.qcow2")
	os.WriteFile(fake, []byte(strings.Repeat("A", 4096)), 0o600)
	if err := validateUploadedImage(fake); err == nil {
		t.Fatal("non-qcow2 payload accepted as qcow2")
	}
	if err := validateUploadedImage(filepath.Join(dir, "x.exe")); err == nil {
		t.Fatal("unsupported extension accepted")
	}
}

func TestConvertToQcow2_RefusesBackingAndOverwrite(t *testing.T) {
	needQemuImg(t)
	dir := t.TempDir()
	host := filepath.Join(dir, "host")
	os.WriteFile(host, make([]byte, 4096), 0o600)
	bad := filepath.Join(dir, "bad.qcow2")
	os.WriteFile(bad, []byte(backedQcow2(t, host)), 0o600)
	if convertToQcow2(bad, filepath.Join(dir, "out.qcow2")) {
		t.Fatal("converted an image with a backing file")
	}
	src := filepath.Join(dir, "in.raw")
	exec.Command("qemu-img", "create", "-f", "raw", src, "1M").Run()
	existing := filepath.Join(dir, "taken.qcow2")
	os.WriteFile(existing, []byte("precious"), 0o600)
	if convertToQcow2(src, existing) {
		t.Fatal("overwrote an existing file")
	}
	if b, _ := os.ReadFile(existing); string(b) != "precious" {
		t.Fatal("existing file modified")
	}
}
