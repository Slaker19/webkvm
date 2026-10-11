package imgsafe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func needQemu(t *testing.T) {
	t.Helper()
	if !Available() {
		t.Skip("qemu-img not installed")
	}
}

func mk(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("qemu-img", args...).CombinedOutput(); err != nil {
		t.Fatalf("qemu-img %v: %v (%s)", args, err, out)
	}
}

func TestInspect_AcceptsPlainImages(t *testing.T) {
	needQemu(t)
	dir := t.TempDir()
	q := filepath.Join(dir, "ok.qcow2")
	mk(t, "create", "-f", "qcow2", q, "1M")
	if _, err := Inspect(context.Background(), q, "qcow2"); err != nil {
		t.Fatalf("plain qcow2 rejected: %v", err)
	}
	r := filepath.Join(dir, "ok.raw")
	mk(t, "create", "-f", "raw", r, "1M")
	if _, err := Inspect(context.Background(), r, "raw"); err != nil {
		t.Fatalf("plain raw rejected: %v", err)
	}
	v := filepath.Join(dir, "ok.vmdk")
	mk(t, "create", "-f", "vmdk", "-o", "subformat=streamOptimized", v, "1M")
	if _, err := Inspect(context.Background(), v, "vmdk"); err != nil {
		t.Fatalf("plain streamOptimized vmdk rejected: %v", err)
	}
}

func TestInspect_RejectsBackingFile(t *testing.T) {
	needQemu(t)
	dir := t.TempDir()
	host := filepath.Join(dir, "hostfile")
	os.WriteFile(host, []byte("secret"), 0o600)
	bad := filepath.Join(dir, "bad.qcow2")
	mk(t, "create", "-f", "qcow2", "-b", host, "-F", "raw", bad)
	if _, err := Inspect(context.Background(), bad, "qcow2"); err == nil {
		t.Fatal("backing file accepted")
	}
	// Convert must refuse it too and must not leave an output behind.
	dst := filepath.Join(dir, "out.qcow2")
	if err := Convert(context.Background(), bad, dst, "qcow2", time.Minute); err == nil {
		t.Fatal("Convert accepted a backing file")
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("output file left behind")
	}
}

func TestInspect_RejectsDataFile(t *testing.T) {
	needQemu(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.qcow2")
	mk(t, "create", "-f", "qcow2", "-o", "data_file="+filepath.Join(dir, "x.raw"), bad, "1M")
	if _, err := Inspect(context.Background(), bad, "qcow2"); err == nil {
		t.Fatal("data-file accepted")
	}
}

func TestInspect_RejectsVMDKExtentOutside(t *testing.T) {
	needQemu(t)
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.bin")
	os.WriteFile(victim, make([]byte, 1024*1024), 0o600)
	bad := filepath.Join(dir, "bad.vmdk")
	desc := "# Disk DescriptorFile\nversion=1\nCID=ffffffff\nparentCID=ffffffff\ncreateType=\"monolithicFlat\"\n\n" +
		"RW 2048 FLAT \"" + victim + "\" 0\n"
	os.WriteFile(bad, []byte(desc), 0o600)
	if _, err := Inspect(context.Background(), bad, "vmdk"); err == nil {
		t.Fatal("vmdk extent outside the image accepted")
	}
}

func TestInspect_FormatMismatchAndProbingDisabled(t *testing.T) {
	needQemu(t)
	dir := t.TempDir()
	q := filepath.Join(dir, "really-qcow2.raw")
	mk(t, "create", "-f", "qcow2", q, "1M")
	// Declared raw: qemu-img treats it as raw (no probing), the format
	// matches the declaration, so it is just opaque bytes to the guest.
	if i, err := Inspect(context.Background(), q, "raw"); err != nil || i.Format != "raw" {
		t.Fatalf("declared raw: %+v %v", i, err)
	}
	// Declared vmdk on a qcow2: must fail.
	if _, err := Inspect(context.Background(), q, "vmdk"); err == nil {
		t.Fatal("qcow2 accepted as vmdk")
	}
	if _, err := Inspect(context.Background(), q, "luks"); err == nil {
		t.Fatal("unsupported format accepted")
	}
}

func TestConvert_NeverOverwrites(t *testing.T) {
	needQemu(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "in.raw")
	mk(t, "create", "-f", "raw", src, "1M")
	dst := filepath.Join(dir, "out.qcow2")
	os.WriteFile(dst, []byte("precious"), 0o600)
	err := Convert(context.Background(), src, dst, "raw", time.Minute)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected ErrExist, got %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "precious" {
		t.Fatal("existing file overwritten")
	}
	dst2 := filepath.Join(dir, "new.qcow2")
	if err := Convert(context.Background(), src, dst2, "raw", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), dst2, "qcow2"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckChainInside(t *testing.T) {
	needQemu(t)
	pool := t.TempDir()
	outside := t.TempDir()
	base := filepath.Join(pool, "vm.base")
	mk(t, "create", "-f", "qcow2", base, "1M")
	ov := filepath.Join(pool, "vm.qcow2")
	mk(t, "create", "-f", "qcow2", "-b", base, "-F", "qcow2", ov)
	if err := CheckChainInside(context.Background(), ov, "qcow2", pool); err != nil {
		t.Fatalf("legit chain rejected: %v", err)
	}
	hostfile := filepath.Join(outside, "secret")
	os.WriteFile(hostfile, make([]byte, 4096), 0o600)
	evil := filepath.Join(pool, "evil.qcow2")
	mk(t, "create", "-f", "qcow2", "-b", hostfile, "-F", "raw", evil)
	if err := CheckChainInside(context.Background(), evil, "qcow2", pool); err == nil {
		t.Fatal("chain leaving the pool accepted")
	}
}
