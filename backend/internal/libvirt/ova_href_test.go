package libvirt

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRewriteOVFDisks_RejectsBackingFile(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not found in PATH")
	}
	dir := t.TempDir()
	host := filepath.Join(dir, "hostsecret")
	os.WriteFile(host, make([]byte, 4096), 0o600)
	bad := filepath.Join(dir, "disk1.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", "-b", host, "-F", "raw", bad).CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create: %v (%s)", err, out)
	}
	pool := t.TempDir()
	ovf := `<Envelope><References><File ovf:id="f1" ovf:href="disks/disk1.qcow2"/></References></Envelope>`
	_, converted, err := rewriteOVFDisks(ovf, map[string][]byte{"disks/disk1.qcow2": []byte(bad)}, pool, "vm")
	if err == nil {
		t.Fatal("backing-file image accepted")
	}
	if len(converted) != 0 {
		t.Fatalf("converted = %v", converted)
	}
	if ents, _ := os.ReadDir(pool); len(ents) != 0 {
		t.Fatalf("output left behind: %v", ents)
	}
}

// An OVF must not be able to reference a file that already sits in the pool
// (another VM's disk) via a plain or foreign-namespace href.
func TestOVFToLibvirtXML_RejectsForeignDiskHref(t *testing.T) {
	ovf := `<Envelope xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1"><References>
<File ovf:id="f1" href="victim.qcow2"/></References>
<VirtualSystem><Name>x</Name></VirtualSystem></Envelope>`
	if _, err := ovfToLibvirtXML(ovf, "/pool", map[string]bool{"mine.qcow2": true}); err == nil {
		t.Fatal("foreign disk href accepted")
	}
}

func TestRewriteOVFDisks_MissingDiskInArchive(t *testing.T) {
	ovf := `<Envelope><References><File ovf:id="f1" ovf:href="disks/ghost.vmdk"/></References></Envelope>`
	if _, _, err := rewriteOVFDisks(ovf, map[string][]byte{}, t.TempDir(), "vm"); err == nil {
		t.Fatal("href without extracted disk accepted")
	}
}
