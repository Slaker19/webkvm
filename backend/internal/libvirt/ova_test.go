package libvirt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sampleEntry returns a diskEntry with a meaningful mix of size
// (on-disk VMDK) and virtualSize (what the guest sees) so the OVF
// tests can verify the capacity attribute is the virtual size, not
// the compressed on-disk size.
func sampleEntry(arcPath string) diskEntry {
	return diskEntry{
		srcPath:     "/pool/" + arcPath,
		base:        strings.TrimPrefix(arcPath, "disks/"),
		arcPath:     arcPath,
		size:        5176065536,  // 5 GB on-disk (VMDK streamOptimized)
		virtualSize: 32212254720, // 30 GB virtual
	}
}

func TestBuildOVF_VmwareDiskHrefHasVMDKExtension(t *testing.T) {
	ovf := buildOVF("test", []diskEntry{sampleEntry("disks/ubuntu-1.1782283212.vmdk")},
		1048576, 2, "linux", string(OVATargetVMware))
	if !strings.Contains(ovf, `ovf:href="disks/ubuntu-1.1782283212.vmdk"`) {
		t.Errorf("vmware OVF must reference disk with .vmdk extension:\n%s", ovf)
	}
	// Must NOT have a no-extension href (the original bug we just fixed).
	if strings.Contains(ovf, `ovf:href="disks/ubuntu-1.1782283212"`) {
		t.Errorf("vmware OVF still has the no-extension href (regression):\n%s", ovf)
	}
}

func TestBuildOVF_LibvirtDiskHrefHasQCOW2Extension(t *testing.T) {
	ovf := buildOVF("test", []diskEntry{sampleEntry("disks/ubuntu-1.qcow2")},
		1048576, 2, "linux", string(OVATargetLibvirt))
	if !strings.Contains(ovf, `ovf:href="disks/ubuntu-1.qcow2"`) {
		t.Errorf("libvirt OVF must reference disk with .qcow2 extension:\n%s", ovf)
	}
}

func TestBuildOVF_CapacityIsVirtualSize(t *testing.T) {
	ovf := buildOVF("test", []diskEntry{sampleEntry("disks/foo.vmdk")},
		1048576, 2, "linux", string(OVATargetVMware))
	// 32212254720 = 30 GB virtual size. If the OVF carries the
	// on-disk size (5176065536 = 5 GB) instead, the imported VM
	// will end up with a 5 GB disk instead of the original 30 GB.
	if !strings.Contains(ovf, `ovf:capacity="32212254720"`) {
		t.Errorf("ovf:capacity must be the virtual size (30 GB), got:\n%s", ovf)
	}
	if strings.Contains(ovf, `ovf:capacity="5176065536"`) {
		t.Errorf("ovf:capacity is the on-disk size, not virtual size:\n%s", ovf)
	}
}

func TestBuildOVF_FileSizeIsOnDiskSize(t *testing.T) {
	ovf := buildOVF("test", []diskEntry{sampleEntry("disks/foo.vmdk")},
		1048576, 2, "linux", string(OVATargetVMware))
	// <File ovf:size> is the on-disk size of the file in the tar,
	// NOT the virtual size. VBoxManage uses this to know how many
	// bytes to read from the streamOptimized VMDK.
	if !strings.Contains(ovf, `ovf:size="5176065536"`) {
		t.Errorf("ovf:size must be the on-disk size (5 GB), got:\n%s", ovf)
	}
}

func TestBuildOVF_VmwareFormatURL(t *testing.T) {
	ovf := buildOVF("test", []diskEntry{sampleEntry("disks/foo.vmdk")},
		1048576, 2, "linux", string(OVATargetVMware))
	want := "http://www.vmware.com/interfaces/specifications/vmdk.html#streamOptimized"
	if !strings.Contains(ovf, want) {
		t.Errorf("vmware OVF must use the streamOptimized format URL %q:\n%s", want, ovf)
	}
}

func TestBuildOVF_LibvirtFormatURL(t *testing.T) {
	ovf := buildOVF("test", []diskEntry{sampleEntry("disks/foo.qcow2")},
		1048576, 2, "linux", string(OVATargetLibvirt))
	want := "http://libvirt.org/ovf/qcow2.html"
	if !strings.Contains(ovf, want) {
		t.Errorf("libvirt OVF must use the qcow2 format URL %q:\n%s", want, ovf)
	}
}

func TestBuildManifest_StartsWithOVFEntry(t *testing.T) {
	sums := map[string]string{
		"disks/ubuntu-1.1782283212.vmdk": "abc123",
	}
	mf := buildManifest("domain.ovf", "deadbeef", sums)
	wantPrefix := "domain.ovf(sha256)= deadbeef\n"
	if !strings.HasPrefix(mf, wantPrefix) {
		t.Errorf("manifest must start with the OVF entry %q, got:\n%s", wantPrefix, mf)
	}
}

func TestBuildManifest_IncludesDiskEntries(t *testing.T) {
	sums := map[string]string{
		"disks/ubuntu-1.1782283212.vmdk": "abc123",
		"disks/ubuntu-1.data.vmdk":       "def456",
	}
	mf := buildManifest("domain.ovf", "deadbeef", sums)
	if !strings.Contains(mf, "disks/ubuntu-1.1782283212.vmdk(sha256)= abc123") {
		t.Errorf("manifest missing first disk entry:\n%s", mf)
	}
	if !strings.Contains(mf, "disks/ubuntu-1.data.vmdk(sha256)= def456") {
		t.Errorf("manifest missing second disk entry:\n%s", mf)
	}
}

func TestBuildManifest_OVFEntryBeforeDisks(t *testing.T) {
	sums := map[string]string{
		"disks/foo.vmdk": "abc",
	}
	mf := buildManifest("domain.ovf", "ovfhash", sums)
	ovfIdx := strings.Index(mf, "domain.ovf(sha256)")
	diskIdx := strings.Index(mf, "disks/foo.vmdk(sha256)")
	if ovfIdx < 0 || diskIdx < 0 {
		t.Fatalf("manifest missing one of the entries: %q", mf)
	}
	if ovfIdx >= diskIdx {
		t.Errorf("OVF entry must come before disk entries:\n%s", mf)
	}
}

func TestSha256Hex_Deterministic(t *testing.T) {
	// sha256Hex is used to seed the manifest; it must be
	// deterministic across calls and match the reference value.
	got := sha256Hex([]byte("hello"))
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if got != want {
		t.Errorf("sha256Hex(\"hello\") = %q, want %q", got, want)
	}
	got1 := sha256Hex([]byte("hello"))
	got2 := sha256Hex([]byte("hello"))
	if got1 != got2 {
		t.Errorf("sha256Hex is not deterministic")
	}
}

func TestOVFToLibvirtXML_DisksAndTargets(t *testing.T) {
	ovf := `<?xml version="1.0" encoding="UTF-8"?>
<Envelope xmlns="http://schemas.dmtf.org/ovf/envelope/1" xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1">
  <References>
    <File ovf:id="file1" ovf:href="disks/test-disk1.qcow2"/>
    <File ovf:id="file2" ovf:href="disks/test-disk2.qcow2"/>
  </References>
  <VirtualSystem ovf:id="test-vm">
    <Name>test-vm</Name>
    <OperatingSystemSection ovf:id="102">
      <Description>Ubuntu</Description>
    </OperatingSystemSection>
    <VirtualHardwareSection>
      <Item>
        <ResourceType>3</ResourceType>
        <VirtualQuantity>2</VirtualQuantity>
      </Item>
      <Item>
        <ResourceType>4</ResourceType>
        <VirtualQuantity>2048</VirtualQuantity>
      </Item>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>`

	xml, err := ovfToLibvirtXML(ovf, "/var/lib/libvirt/images")
	if err != nil {
		t.Fatalf("ovfToLibvirtXML failed: %v", err)
	}

	// Must assign distinct target dev (vda, vdb) to avoid collision
	if !strings.Contains(xml, "<target dev='vda' bus='virtio'/>") {
		t.Errorf("missing target dev vda:\n%s", xml)
	}
	if !strings.Contains(xml, "<target dev='vdb' bus='virtio'/>") {
		t.Errorf("missing target dev vdb for second disk:\n%s", xml)
	}
	if strings.Count(xml, "dev='vda'") != 1 {
		t.Errorf("dev='vda' duplicated, expected unique target dev per disk:\n%s", xml)
	}
}

func TestRewriteOVFDisks_RewritesHrefToConvertedQcow2(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not found in PATH")
	}

	poolDir := t.TempDir()
	srcDir := t.TempDir()

	srcDisk := filepath.Join(srcDir, "disk1.vmdk")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", srcDisk, "1M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create failed: %v (%s)", err, out)
	}

	diskFiles := map[string][]byte{
		"disks/disk1.vmdk": []byte(srcDisk),
	}

	ovfInput := `<Envelope><References><File ovf:id="f1" ovf:href="disks/disk1.vmdk"/></References></Envelope>`
	rewritten, err := rewriteOVFDisks(ovfInput, diskFiles, poolDir, "imported-vm")
	if err != nil {
		t.Fatalf("rewriteOVFDisks failed: %v", err)
	}

	wantHref := `ovf:href="disks/imported-vm.qcow2"`
	if !strings.Contains(rewritten, wantHref) {
		t.Errorf("expected rewritten href %q, got:\n%s", wantHref, rewritten)
	}
}

func TestOVFImport_RewriteDisksAndGenerateXML(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not found in PATH")
	}

	poolDir := t.TempDir()
	srcDir := t.TempDir()

	srcDisk1 := filepath.Join(srcDir, "disk1.vmdk")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", srcDisk1, "1M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create disk1 failed: %v (%s)", err, out)
	}
	srcDisk2 := filepath.Join(srcDir, "disk2.vmdk")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", srcDisk2, "1M").CombinedOutput(); err != nil {
		t.Fatalf("qemu-img create disk2 failed: %v (%s)", err, out)
	}

	diskFiles := map[string][]byte{
		"disks/disk1.vmdk": []byte(srcDisk1),
		"disks/disk2.vmdk": []byte(srcDisk2),
	}

	ovfInput := `<?xml version="1.0" encoding="UTF-8"?>
<Envelope xmlns="http://schemas.dmtf.org/ovf/envelope/1" xmlns:ovf="http://schemas.dmtf.org/ovf/envelope/1">
  <References>
    <File ovf:id="file1" ovf:href="disks/disk1.vmdk"/>
    <File ovf:id="file2" ovf:href="disks/disk2.vmdk"/>
  </References>
  <VirtualSystem ovf:id="my-vm">
    <Name>my-vm</Name>
    <OperatingSystemSection ovf:id="102">
      <Description>Linux</Description>
    </OperatingSystemSection>
    <VirtualHardwareSection>
      <Item>
        <ResourceType>3</ResourceType>
        <VirtualQuantity>2</VirtualQuantity>
      </Item>
      <Item>
        <ResourceType>4</ResourceType>
        <VirtualQuantity>2048</VirtualQuantity>
      </Item>
    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>`

	rewrittenOVF, err := rewriteOVFDisks(ovfInput, diskFiles, poolDir, "my-vm")
	if err != nil {
		t.Fatalf("rewriteOVFDisks: %v", err)
	}

	xml, err := ovfToLibvirtXML(rewrittenOVF, poolDir)
	if err != nil {
		t.Fatalf("ovfToLibvirtXML: %v", err)
	}

	expectedDisk1 := filepath.Join(poolDir, "my-vm.qcow2")
	expectedDisk2 := filepath.Join(poolDir, "my-vm-2.qcow2")
	if _, err := os.Stat(expectedDisk1); err != nil {
		t.Errorf("expected converted disk 1 at %s: %v", expectedDisk1, err)
	}
	if _, err := os.Stat(expectedDisk2); err != nil {
		t.Errorf("expected converted disk 2 at %s: %v", expectedDisk2, err)
	}

	if !strings.Contains(xml, "<source file='"+expectedDisk1+"'/>") {
		t.Errorf("XML missing source file for disk 1 (%s):\n%s", expectedDisk1, xml)
	}
	if !strings.Contains(xml, "<source file='"+expectedDisk2+"'/>") {
		t.Errorf("XML missing source file for disk 2 (%s):\n%s", expectedDisk2, xml)
	}

	if !strings.Contains(xml, "<target dev='vda' bus='virtio'/>") {
		t.Errorf("missing target dev vda:\n%s", xml)
	}
	if !strings.Contains(xml, "<target dev='vdb' bus='virtio'/>") {
		t.Errorf("missing target dev vdb:\n%s", xml)
	}
}
