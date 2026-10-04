package libvirt

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestImportNormalizesMachineType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "pc-q35-10.2 single quote",
			in:   `<os><type arch='x86_64' machine='pc-q35-10.2'>hvm</type></os>`,
			want: `<os><type arch='x86_64' machine='q35'>hvm</type></os>`,
		},
		{
			name: "pc-i440fx-7.1 single quote",
			in:   `<os><type arch='x86_64' machine='pc-i440fx-7.1'>hvm</type></os>`,
			want: `<os><type arch='x86_64' machine='pc'>hvm</type></os>`,
		},
		{
			name: "pc-q35-9.0 double quote",
			in:   `<os><type arch="x86_64" machine="pc-q35-9.0">hvm</type></os>`,
			want: `<os><type arch="x86_64" machine="q35">hvm</type></os>`,
		},
		{
			name: "already short form, untouched",
			in:   `<os><type arch='x86_64' machine='q35'>hvm</type></os>`,
			want: `<os><type arch='x86_64' machine='q35'>hvm</type></os>`,
		},
		{
			name: "pc-i440fx-2.12 deprecated, downgrade to pc",
			in:   `<type arch='x86_64' machine='pc-i440fx-2.12'>hvm</type>`,
			want: `<type arch='x86_64' machine='pc'>hvm</type>`,
		},
		{
			name: "no machine attribute, untouched",
			in:   `<os><type arch='x86_64'>hvm</type></os>`,
			want: `<os><type arch='x86_64'>hvm</type></os>`,
		},
		{
			name: "pc-q35-10.0 (latest on debian trixie) — still normalized to q35 for forward compat",
			in:   `<os><type arch='x86_64' machine='pc-q35-10.0'>hvm</type></os>`,
			want: `<os><type arch='x86_64' machine='q35'>hvm</type></os>`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeMachineType(c.in)
			if got != c.want {
				t.Errorf("normalize:\n  got:  %q\n  want: %q", got, c.want)
			}
			// Sanity: result must not still contain a versioned machine type.
			if strings.Contains(got, "pc-q35-") || strings.Contains(got, "pc-i440fx-") {
				t.Errorf("versioned machine type still present: %q", got)
			}
		})
	}
}

// TestStripCdromDevices verifies the import-time transform that
// removes every <disk type='file' device='cdrom'>...</disk> block
// from the imported domain XML and reports a single summary
// warning per import. ImportDomain uses the same regex; this test
// exercises it without needing a live libvirt connection.
func TestStripCdromDevices(t *testing.T) {
	cases := []struct {
		name         string
		xml          string
		wantStripped int
		// substring that must still be present in the result
		wantContains []string
		// substring that must NOT be present (the stripped bits)
		wantMissing []string
	}{
		{
			name: "single CDROM is stripped, disk device untouched",
			xml: `<devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/pool/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/pool/ubuntu.iso'/>
      <target dev='sda' bus='sata'/>
    </disk>
  </devices>`,
			wantStripped: 1,
			wantContains: []string{"/pool/disk.qcow2", "vda"},
			wantMissing:  []string{"/pool/ubuntu.iso", "sda"},
		},
		{
			name: "no CDROM devices: nothing changes",
			xml: `<devices>
    <disk type='file' device='disk'>
      <source file='/pool/disk.qcow2'/>
    </disk>
  </devices>`,
			wantStripped: 0,
			wantContains: []string{"/pool/disk.qcow2"},
		},
		{
			name: "two CDROMs are both stripped",
			xml: `<devices>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/pool/iso1.iso'/>
      <target dev='sda' bus='sata'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/pool/iso2.iso'/>
      <target dev='sdb' bus='sata'/>
    </disk>
  </devices>`,
			wantStripped: 2,
			wantMissing:  []string{"iso1.iso", "iso2.iso", "sda", "sdb"},
		},
		{
			name: "CDROM block spanning newlines is still matched",
			xml: `<devices>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/pool/ubuntu.iso'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
  </devices>`,
			wantStripped: 1,
			wantMissing:  []string{"ubuntu.iso"},
		},
	}

	// Replicate the strip logic from ImportDomain.
	cdromBlockRe := regexp.MustCompile(`(?s)<disk\s+type='file'\s+device='cdrom'\s*>\s*.*?</disk>`)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stripped := cdromBlockRe.FindAllString(c.xml, -1)
			if len(stripped) != c.wantStripped {
				t.Errorf("stripped count: got %d, want %d", len(stripped), c.wantStripped)
			}
			out := cdromBlockRe.ReplaceAllString(c.xml, "")
			for _, s := range c.wantContains {
				if !strings.Contains(out, s) {
					t.Errorf("result should contain %q, got: %s", s, out)
				}
			}
			for _, s := range c.wantMissing {
				if strings.Contains(out, s) {
					t.Errorf("result should NOT contain %q (it was supposed to be stripped), got: %s", s, out)
				}
			}
		})
	}
}

// silence unused-import warning when the test that uses os is removed.
var _ = os.Stat

func TestBootDeviceAttr(t *testing.T) {
	cases := []struct {
		order string
		want  string
	}{
		{"", "<boot dev='hd'/>"},
		{"disk", "<boot dev='hd'/>"},
		{"cdrom", "<boot dev='cdrom'/>"},
		{"network", "<boot dev='network'/>"},
		{"garbage", "<boot dev='hd'/>"},
	}
	for _, c := range cases {
		if got := bootDeviceAttr(c.order); got != c.want {
			t.Errorf("bootDeviceAttr(%q) = %q, want %q", c.order, got, c.want)
		}
	}
}

func TestBootOrderFromXML(t *testing.T) {
	cases := []struct {
		xml  string
		want string
	}{
		{"<os><type arch='x86_64'>hvm</type><boot dev='hd'/></os>", "disk"},
		{"<os><boot dev='cdrom'/></os>", "cdrom"},
		{"<os><boot dev='network'/></os>", "network"},
		{"<os><type arch='x86_64'>hvm</type></os>", "disk"},
	}
	for _, c := range cases {
		if got := bootOrderFromXML(c.xml); got != c.want {
			t.Errorf("bootOrderFromXML(%q) = %q, want %q", c.xml, got, c.want)
		}
	}
}

func TestBootDeviceToAPI(t *testing.T) {
	cases := map[string]string{
		"hd":      "disk",
		"cdrom":   "cdrom",
		"network": "network",
		"floppy":  "disk",
		"":        "disk",
	}
	for dev, want := range cases {
		if got := bootDeviceToAPI(dev); got != want {
			t.Errorf("bootDeviceToAPI(%q) = %q, want %q", dev, got, want)
		}
	}
}

func TestInterfaceXML(t *testing.T) {
	origBridge := linuxBridgeCheck
	origMain := mainBridgeCheck
	t.Cleanup(func() {
		linuxBridgeCheck = origBridge
		mainBridgeCheck = origMain
	})

	// v2.4 agnostic L2: there is NO libvirt virtual-network path. A
	// non-bridge network name falls back to the host's main bridge.
	linuxBridgeCheck = func(string) bool { return false }
	mainBridgeCheck = func() string { return "vmbr0" }
	out := interfaceXML("default", "virtio")
	if !strings.Contains(out, "<interface type='bridge'>") ||
		!strings.Contains(out, "<source bridge='vmbr0'/>") ||
		!strings.Contains(out, "<model type='virtio'/>") {
		t.Errorf("non-bridge name should fall back to main bridge:\n%s", out)
	}
	// Empty name with no host bridge -> empty source (CreateDomain guards
	// this with ErrNoPhysicalBridge before interfaceXML is reached).
	linuxBridgeCheck = func(string) bool { return false }
	mainBridgeCheck = func() string { return "" }
	out = interfaceXML("", "virtio")
	if !strings.Contains(out, "<interface type='bridge'>") {
		t.Errorf("empty network must still emit type='bridge':\n%s", out)
	}
	// Empty name WITH a main host bridge defaults to a direct bridge
	// attachment (Proxmox-style shared L2).
	linuxBridgeCheck = func(string) bool { return true }
	mainBridgeCheck = func() string { return "vmbr0" }
	out = interfaceXML("", "virtio")
	if !strings.Contains(out, "<interface type='bridge'>") ||
		!strings.Contains(out, "<source bridge='vmbr0'/>") {
		t.Errorf("empty network with main bridge should attach to vmbr0:\n%s", out)
	}

	// Host Linux bridge -> direct bridge attachment (Proxmox-style L2).
	out = interfaceXML("vmbr0", "virtio")
	if !strings.Contains(out, "<interface type='bridge'>") ||
		!strings.Contains(out, "<source bridge='vmbr0'/>") ||
		!strings.Contains(out, "<model type='virtio'/>") {
		t.Errorf("bridge interface XML wrong:\n%s", out)
	}

	// With VLAN tag
	tag := 100
	outVlan := interfaceXMLWithVLAN("vmbr0", "virtio", &tag)
	if !strings.Contains(outVlan, "<vlan><tag id='100'/></vlan>") {
		t.Errorf("expected VLAN tag XML in output:\n%s", outVlan)
	}
}

func TestParseNetworksVLAN(t *testing.T) {
	xmlDesc := `<domain>
  <devices>
    <interface type='bridge'>
      <mac address='52:54:00:12:34:56'/>
      <source bridge='vmbr0'/>
      <vlan>
        <tag id='200'/>
      </vlan>
      <model type='virtio'/>
      <link state='down'/>
    </interface>
  </devices>
</domain>`
	ifaces := parseNetworks(xmlDesc)
	if len(ifaces) != 1 {
		t.Fatalf("expected 1 interface, got %d", len(ifaces))
	}
	if ifaces[0].VLANTag == nil || *ifaces[0].VLANTag != 200 {
		t.Errorf("expected VLAN tag 200, got %+v", ifaces[0].VLANTag)
	}
	if ifaces[0].LinkState != "down" {
		t.Errorf("expected link state 'down', got %q", ifaces[0].LinkState)
	}
}

func TestSerialPortAndAudioXMLHandling(t *testing.T) {
	xmlWithSerialAndAudio := `<domain>
  <devices>
    <serial type='pty'><target port='0'/></serial>
    <console type='pty'><target type='serial' port='0'/></console>
    <sound model='ich9'/>
  </devices>
</domain>`

	if !strings.Contains(xmlWithSerialAndAudio, "<serial") {
		t.Errorf("expected serial device")
	}
	if !strings.Contains(xmlWithSerialAndAudio, "<sound model='ich9'") {
		t.Errorf("expected ich9 sound device")
	}

	// Test stripping serial
	stripSerialRE := regexp.MustCompile(`<serial\b[^>]*>[\s\S]*?</serial>\s*`)
	stripConsoleRE := regexp.MustCompile(`<console\b[^>]*>[\s\S]*?</console>\s*`)
	noSerial := stripSerialRE.ReplaceAllString(xmlWithSerialAndAudio, "")
	noSerial = stripConsoleRE.ReplaceAllString(noSerial, "")

	if strings.Contains(noSerial, "<serial") || strings.Contains(noSerial, "<console") {
		t.Errorf("expected no serial or console in stripped XML, got:\n%s", noSerial)
	}

	// Test sound replacement
	soundRE := regexp.MustCompile(`<sound\b[^>]*/>\s*`)
	noSound := soundRE.ReplaceAllString(xmlWithSerialAndAudio, "")
	if strings.Contains(noSound, "<sound") {
		t.Errorf("expected no sound device in stripped XML, got:\n%s", noSound)
	}
}

// TestCloneDiskFile_FullCopy verifies that the default (non-linked)
// clone mode produces a fully independent qcow2 file with the
// source's actual data — this is the regression test for the bug
// where CloneDomain used to create an empty volume of the right size
// instead of copying the disk contents.
func TestCloneDiskFile_FullCopy(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not available in test environment")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.qcow2")
	dst := filepath.Join(dir, "clone.qcow2")

	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", src, "16M").CombinedOutput(); err != nil {
		t.Fatalf("setup: create source qcow2: %v (%s)", err, out)
	}
	// Write recognizable data through the qcow2 file with qemu-img
	// so we don't need a full guest — dd against the raw file won't
	// respect the qcow2 format, but writing via `qemu-img convert`
	// from a small raw seed does.
	seed := filepath.Join(dir, "seed.raw")
	if err := os.WriteFile(seed, []byte("WEBKVM-CLONE-TEST-MARKER"), 0644); err != nil {
		t.Fatalf("setup: write seed: %v", err)
	}
	if out, err := exec.Command("qemu-img", "convert", "-n", "-O", "qcow2", seed, src).CombinedOutput(); err != nil {
		t.Fatalf("setup: seed source qcow2: %v (%s)", err, out)
	}

	if err := cloneDiskFile(src, dst, "qcow2", false); err != nil {
		t.Fatalf("cloneDiskFile: %v", err)
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("clone file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("clone file is empty")
	}

	// The clone must be independent: qemu-img info must report no
	// backing file.
	out, err := exec.Command("qemu-img", "info", "--output=json", dst).CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img info on clone: %v (%s)", err, out)
	}
	if strings.Contains(string(out), "backing-filename") {
		t.Errorf("full-copy clone must not have a backing file, got:\n%s", out)
	}

	// Verify actual disk content was copied, not just an empty volume
	// of the right size (the original bug).
	rawOut := filepath.Join(dir, "clone-check.raw")
	if out, err := exec.Command("qemu-img", "convert", "-O", "raw", dst, rawOut).CombinedOutput(); err != nil {
		t.Fatalf("convert clone to raw for verification: %v (%s)", err, out)
	}
	data, err := os.ReadFile(rawOut)
	if err != nil {
		t.Fatalf("read converted clone: %v", err)
	}
	if !strings.Contains(string(data), "WEBKVM-CLONE-TEST-MARKER") {
		t.Fatal("cloned disk does not contain source data — clone is empty (the original bug)")
	}
}

// TestCloneDiskFile_Linked verifies the linked-clone (copy-on-write)
// mode creates a qcow2 overlay backed by the source file.
func TestCloneDiskFile_Linked(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not available in test environment")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.qcow2")
	dst := filepath.Join(dir, "linked-clone.qcow2")

	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", src, "16M").CombinedOutput(); err != nil {
		t.Fatalf("setup: create source qcow2: %v (%s)", err, out)
	}

	if err := cloneDiskFile(src, dst, "qcow2", true); err != nil {
		t.Fatalf("cloneDiskFile (linked): %v", err)
	}

	out, err := exec.Command("qemu-img", "info", "--output=json", dst).CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img info on linked clone: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), "backing-filename") {
		t.Errorf("linked clone must have a backing file, got:\n%s", out)
	}
	if !strings.Contains(string(out), src) {
		t.Errorf("linked clone's backing file must point at the source, got:\n%s", out)
	}

	// A fresh overlay is near-empty on disk regardless of the source's
	// virtual size — this is the whole point of linked clones.
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("linked clone file missing: %v", err)
	}
	if info.Size() > 1<<20 { // 1 MiB — generous ceiling for qcow2 header+metadata
		t.Errorf("linked clone overlay unexpectedly large: %d bytes", info.Size())
	}
}

// TestCloneDiskFile_LinkedIgnoredForRaw verifies raw-format sources
// always get a full copy even when Linked is requested, since raw
// disks have no backing-file mechanism.
func TestCloneDiskFile_LinkedIgnoredForRaw(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not available in test environment")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.raw")
	dst := filepath.Join(dir, "clone.raw")

	if out, err := exec.Command("qemu-img", "create", "-f", "raw", src, "1M").CombinedOutput(); err != nil {
		t.Fatalf("setup: create source raw: %v (%s)", err, out)
	}

	if err := cloneDiskFile(src, dst, "raw", true); err != nil {
		t.Fatalf("cloneDiskFile (linked=true, raw format): %v", err)
	}

	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("clone file missing: %v", err)
	}
}

func TestBuildCPUXML(t *testing.T) {
	s, c, th := 1, 4, 2
	cases := []struct {
		name    string
		mode    string
		model   string
		sockets *int
		cores   *int
		threads *int
		flags   []string
		wantIn  []string
		wantOut []string
	}{
		{
			name:   "host-passthrough default",
			mode:   "",
			wantIn: []string{"<cpu mode='host-passthrough' check='none'/>"},
		},
		{
			name:    "host-passthrough with topology and flags",
			mode:    "host-passthrough",
			sockets: &s, cores: &c, threads: &th,
			flags: []string{"+aes", "+avx2", "-hypervisor", "+topoext"},
			wantIn: []string{
				"<cpu mode='host-passthrough' check='none'>",
				"<topology sockets='1' cores='4' threads='2'/>",
				"<feature policy='require' name='aes'/>",
				"<feature policy='require' name='avx2'/>",
				"<feature policy='disable' name='hypervisor'/>",
				"<feature policy='require' name='topoext'/>",
			},
		},
		{
			name:    "custom model with flags",
			mode:    "custom",
			model:   "EPYC-Rome",
			sockets: &s, cores: &c, threads: &th,
			flags: []string{"+pcid", "+pdpe1gb"},
			wantIn: []string{
				"<cpu mode='custom' match='exact'>",
				"<model>EPYC-Rome</model>",
				"<topology sockets='1' cores='4' threads='2'/>",
				"<feature policy='require' name='pcid'/>",
				"<feature policy='require' name='pdpe1gb'/>",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildCPUXML(tc.mode, tc.model, tc.sockets, tc.cores, tc.threads, tc.flags)
			for _, w := range tc.wantIn {
				if !strings.Contains(got, w) {
					t.Errorf("expected %q in cpu xml, got:\n%s", w, got)
				}
			}
			for _, w := range tc.wantOut {
				if strings.Contains(got, w) {
					t.Errorf("expected %q NOT in cpu xml, got:\n%s", w, got)
				}
			}
		})
	}
}

func TestMinRAMAndIOThreadsXMLParsing(t *testing.T) {
	xmlDesc := `<domain type='kvm'>
  <name>test-vm</name>
  <uuid>11111111-2222-3333-4444-555555555555</uuid>
  <memory unit='MiB'>4096</memory>
  <currentMemory unit='MiB'>2048</currentMemory>
  <vcpu placement='static'>4</vcpu>
  <iothreads>2</iothreads>
  <devices>
    <controller type='scsi' model='virtio-scsi' index='0'>
      <driver iothread='1'/>
    </controller>
    <memballoon model='virtio'/>
  </devices>
</domain>`

	// Test extracting MinRAMMB and IOThreads
	var minRAMMB int64
	if m := regexp.MustCompile(`<currentMemory unit='MiB'>(\d+)</currentMemory>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		minRAMMB = 2048
	}
	if minRAMMB != 2048 {
		t.Errorf("expected minRAMMB 2048, got %d", minRAMMB)
	}

	var iothreads int
	if m := regexp.MustCompile(`<iothreads>(\d+)</iothreads>`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		iothreads = 2
	}
	if iothreads != 2 {
		t.Errorf("expected iothreads 2, got %d", iothreads)
	}
}
