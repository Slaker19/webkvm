package libvirt

import (
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
	if sha256Hex([]byte("hello")) != sha256Hex([]byte("hello")) {
		t.Errorf("sha256Hex is not deterministic")
	}
}
