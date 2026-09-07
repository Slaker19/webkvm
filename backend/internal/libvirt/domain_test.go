package libvirt

import (
	"os"
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
		name        string
		xml         string
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

	// Non-bridge network name -> libvirt virtual network interface.
	linuxBridgeCheck = func(string) bool { return false }
	mainBridgeCheck = func() string { return "" }
	out := interfaceXML("default", "virtio")
	if !strings.Contains(out, "<interface type='network'>") ||
		!strings.Contains(out, "<source network='default'/>") ||
		!strings.Contains(out, "<model type='virtio'/>") {
		t.Errorf("network interface XML wrong:\n%s", out)
	}
	// Empty name with no host bridge falls back to the NAT default network.
	out = interfaceXML("", "virtio")
	if !strings.Contains(out, "<source network='default'/>") {
		t.Errorf("empty network should fall back to 'default':\n%s", out)
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
}
