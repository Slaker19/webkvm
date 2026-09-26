package hostcaps

import (
	"strings"
	"testing"
)

// domCapsModern mirrors what Ubuntu 26.04 / QEMU 10.2 emits: QXL and
// SPICE are absent (the device/module was dropped from the default
// build). This is the exact shape that triggered the reported bug.
const domCapsModern = `<domainCapabilities>
  <path>/usr/bin/qemu-system-x86_64</path>
  <domain>kvm</domain>
  <machine>pc-q35-10.2</machine>
  <devices>
    <disk supported='yes'>
      <enum name='diskDevice'>
        <value>disk</value>
        <value>cdrom</value>
        <value>floppy</value>
        <value>lun</value>
      </enum>
      <enum name='bus'>
        <value>fdc</value>
        <value>scsi</value>
        <value>virtio</value>
        <value>usb</value>
        <value>sata</value>
        <value>nvme</value>
      </enum>
    </disk>
    <graphics supported='yes'>
      <enum name='type'>
        <value>sdl</value>
        <value>vnc</value>
        <value>egl-headless</value>
        <value>dbus</value>
      </enum>
    </graphics>
    <video supported='yes'>
      <enum name='modelType'>
        <value>vga</value>
        <value>cirrus</value>
        <value>vmvga</value>
        <value>virtio</value>
        <value>none</value>
        <value>bochs</value>
        <value>ramfb</value>
      </enum>
    </video>
    <sound supported='yes'>
      <enum name='model'>
        <value>ich9</value>
        <value>ac97</value>
        <value>es1370</value>
      </enum>
    </sound>
    <cpu supported='yes'>
      <enum name='mode'>
        <value>host-passthrough</value>
        <value>host-model</value>
        <value>custom</value>
      </enum>
    </cpu>
  </devices>
</domainCapabilities>`

// domCapsLegacy is an old host that DID ship QXL and SPICE.
const domCapsLegacy = `<domainCapabilities>
  <devices>
    <graphics supported='yes'>
      <enum name='type'>
        <value>vnc</value>
        <value>spice</value>
      </enum>
    </graphics>
    <video supported='yes'>
      <enum name='modelType'>
        <value>vga</value>
        <value>qxl</value>
        <value>virtio</value>
        <value>none</value>
      </enum>
    </video>
  </devices>
</domainCapabilities>`

func TestEnumValuesVideoModern(t *testing.T) {
	got := enumValues(domCapsModern, "video", "modelType")
	want := []string{"bochs", "cirrus", "none", "ramfb", "vga", "virtio", "vmvga"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("video models = %v, want %v", got, want)
	}
	if containsFold(got, "qxl") {
		t.Fatal("qxl must NOT be advertised on a QEMU build without it")
	}
}

func TestEnumValuesVideoLegacyHasQXL(t *testing.T) {
	got := enumValues(domCapsLegacy, "video", "modelType")
	if !containsFold(got, "qxl") {
		t.Fatalf("legacy host should advertise qxl, got %v", got)
	}
}

func TestGraphicsSpiceDetection(t *testing.T) {
	modern := enumValues(domCapsModern, "graphics", "type")
	if containsFold(modern, "spice") {
		t.Fatal("SPICE must not be advertised without the spice module")
	}
	legacy := enumValues(domCapsLegacy, "graphics", "type")
	if !containsFold(legacy, "spice") {
		t.Fatalf("legacy host should advertise spice, got %v", legacy)
	}
}

// TestEnumValuesScopedToParent guards the reason enumValues takes a
// parent: "type" appears in both <disk> and <graphics>, and the disk
// one must not leak.
func TestEnumValuesScopedToParent(t *testing.T) {
	diskBus := enumValues(domCapsModern, "disk", "bus")
	if containsFold(diskBus, "vnc") {
		t.Fatalf("disk bus picked up graphics values: %v", diskBus)
	}
	if !containsFold(diskBus, "sata") || !containsFold(diskBus, "nvme") {
		t.Fatalf("disk bus missing expected values: %v", diskBus)
	}
}

func TestEnumValuesMissingParentIsNil(t *testing.T) {
	if got := enumValues(domCapsModern, "watchdog", "model"); got != nil {
		t.Fatalf("missing parent should yield nil, got %v", got)
	}
}

func TestParentBlockRejectsPrefixCollision(t *testing.T) {
	// <video> must not match <videoX>.
	doc := `<devices><videoX><enum name='modelType'><value>bogus</value></enum></videoX><video><enum name='modelType'><value>virtio</value></enum></video></devices>`
	got := enumValues(doc, "video", "modelType")
	if len(got) != 1 || got[0] != "virtio" {
		t.Fatalf("parent match leaked into sibling element: %v", got)
	}
}

func TestNICModelsFromDeviceHelp(t *testing.T) {
	help := `
name "e1000", bus PCI, alias "e1000-82540em", desc "Intel Gigabit Ethernet"
name "e1000e", bus PCI, desc "Intel 82574L GbE Controller"
name "pcnet", bus PCI
name "rtl8139", bus PCI
name "virtio-net-pci", bus PCI, alias "virtio-net"
name "vmxnet3", bus PCI, desc "VMWare Paravirtualized Ethernet v3"
name "VGA", bus PCI
name "some-other-device", bus PCI
`
	got := nicModels(help)
	want := "e1000,e1000e,pcnet,rtl8139,virtio,vmxnet3"
	if strings.Join(got, ",") != want {
		t.Fatalf("NIC models = %v, want %s", got, want)
	}
}

func TestNICModelsNormalizesVirtio(t *testing.T) {
	got := nicModels(`name "virtio-net-pci", bus PCI`)
	if len(got) != 1 || got[0] != "virtio" {
		t.Fatalf("virtio-net-pci should normalize to virtio, got %v", got)
	}
}

func TestNormalizeSound(t *testing.T) {
	got := normalizeSound([]string{"ich9", "ac97", "es1370", "usb", "bogus"})
	want := "ac97,es1370,ich9,usb"
	if strings.Join(got, ",") != want {
		t.Fatalf("normalizeSound = %v, want %s", got, want)
	}
}

func TestParseQEMUVersion(t *testing.T) {
	cases := map[string]string{
		"QEMU emulator version 10.2.1 (Debian 1:10.2.1+ds-1ubuntu3.2)\nCopyright": "10.2.1",
		"QEMU emulator version 8.2.0\n":                                           "8.2.0",
		"no version here":                                                         "",
	}
	for in, want := range cases {
		if got := parseQEMUVersion(in); got != want {
			t.Errorf("parseQEMUVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestCuratedFallbackOmitsQXL pins the safe-fallback decision: an
// unprobeable host must not be offered QXL, because modern QEMU is far
// more common than QEMU-5-era hosts that need it.
func TestCuratedFallbackOmitsQXL(t *testing.T) {
	if containsFold(curatedVideoModels, "qxl") {
		t.Fatal("curated fallback must not include qxl")
	}
	if !containsFold(curatedVideoModels, "virtio") {
		t.Fatal("curated fallback must include virtio")
	}
}

func TestParseLines(t *testing.T) {
	in := "qemu64\nkvm64\n\n  EPYC-Genoa  \nkvm64\nathlon\n"
	got := parseLines(in)
	want := "qemu64,kvm64,EPYC-Genoa,athlon"
	if strings.Join(got, ",") != want {
		t.Fatalf("parseLines = %v, want %s", got, want)
	}
}

func TestParseLinesEmpty(t *testing.T) {
	if got := parseLines("\n  \n"); len(got) != 0 {
		t.Fatalf("expected no entries, got %v", got)
	}
}

// TestCPUModesAlwaysPresent ensures the fixed libvirt modes are reported
// regardless of probe outcome, since the UI renders them unconditionally.
func TestCPUModesAlwaysPresent(t *testing.T) {
	for _, want := range []string{"host-passthrough", "host-model", "custom"} {
		if !containsFold(curatedCPUModes, want) {
			t.Errorf("curated CPU modes missing %q", want)
		}
	}
}

func TestCPUFlagsFromQEMUHelp(t *testing.T) {
	help := `x86 base                Base CPU
x86 qemu64              QEMU Virtual CPU

Recognized CPUID flags:
  3dnow 3dnowext 3dnowprefetch abm ace2 ace2-en acpi adx aes amd-no-ssb
  amd-psfd amd-ssbd amd-stibp
`
	got := cpuFlags(help)
	for _, want := range []string{"aes", "adx", "abm", "amd-stibp"} {
		if !containsFold(got, want) {
			t.Errorf("cpuFlags missing %q, got %v", want, got)
		}
	}
	// Model names printed before the header must not leak in as flags.
	if containsFold(got, "base") || containsFold(got, "qemu64") {
		t.Errorf("cpuFlags picked up CPU model names: %v", got)
	}
}

func TestCPUFlagsMissingHeaderIsNil(t *testing.T) {
	if got := cpuFlags("no matching section here"); got != nil {
		t.Fatalf("expected nil, got %v", got)
	}
}

func TestCPUFlagsDeduplicatesAndSorts(t *testing.T) {
	got := cpuFlags("Recognized CPUID flags:\n  zeta aes aes zeta abm\n")
	want := "abm,aes,zeta"
	if strings.Join(got, ",") != want {
		t.Fatalf("cpuFlags = %v, want %s", got, want)
	}
}
