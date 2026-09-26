package hostcaps

import (
	"os"
	"strings"
	"testing"
)

// TestAgainstLiveDomCapabilities parses a real `virsh domcapabilities`
// dump captured from a QEMU 10.2 host (Ubuntu 26.04) if present at
// /tmp/live_domcaps.xml. Skipped when absent so the suite stays
// hermetic in CI; run after capturing the file to validate the parser
// against reality rather than a hand-written fixture.
func TestAgainstLiveDomCapabilities(t *testing.T) {
	b, err := os.ReadFile("/tmp/live_domcaps.xml")
	if err != nil {
		t.Skip("no live domcapabilities dump available")
	}
	doc := string(b)

	video := enumValues(doc, "video", "modelType")
	t.Logf("video models from live host: %v", video)
	if strings.Contains(strings.ToLower(strings.Join(video, ",")), "qxl") {
		t.Errorf("live QEMU 10 host unexpectedly advertises qxl: %v", video)
	}
	if !containsFold(video, "virtio") {
		t.Errorf("live host should still advertise virtio, got %v", video)
	}

	graphics := enumValues(doc, "graphics", "type")
	t.Logf("graphics types from live host: %v", graphics)
	if containsFold(graphics, "spice") {
		t.Errorf("live host has no spice module, should not advertise it: %v", graphics)
	}

	bus := enumValues(doc, "disk", "bus")
	t.Logf("disk buses from live host: %v", bus)

	devices := enumValues(doc, "disk", "diskDevice")
	t.Logf("disk devices from live host: %v", devices)
	if !containsFold(devices, "cdrom") {
		t.Errorf("diskDevice enum not parsed; got %v", devices)
	}

	sound := normalizeSound(enumValues(doc, "sound", "model"))
	t.Logf("sound models from live host: %v", sound)
}

// TestAgainstLiveCPUModels parses a real `virsh cpu-models x86_64` dump
// captured alongside the domcapabilities one. This is the list the CPU
// model picker filters against, and it is NOT available from
// domcapabilities on this libvirt, so it has its own capture.
func TestAgainstLiveCPUModels(t *testing.T) {
	b, err := os.ReadFile("/tmp/live_cpumodels.txt")
	if err != nil {
		t.Skip("no live cpu-models dump available")
	}
	models := parseLines(string(b))
	t.Logf("parsed %d CPU models from live host", len(models))
	if len(models) == 0 {
		t.Fatal("expected a non-empty CPU model list")
	}
	// The UI's preset list must be representable: every model it offers
	// that the host lacks is fine, but the common ones should exist.
	for _, want := range []string{"qemu64", "kvm64"} {
		if !containsFold(models, want) {
			t.Errorf("live host should provide %q", want)
		}
	}
}

// TestAgainstLiveCPUFlags parses a real `qemu -cpu help` dump. This is
// the on/auto/off flag picker's authoritative list.
func TestAgainstLiveCPUFlags(t *testing.T) {
	b, err := os.ReadFile("/tmp/live_cpu_help_full.txt")
	if err != nil {
		t.Skip("no live -cpu help dump available")
	}
	flags := cpuFlags(string(b))
	t.Logf("parsed %d CPU flags from live host", len(flags))
	// These are the flags the UI hardcoded before this feature existed;
	// they must still resolve on a real host or every preset breaks.
	for _, want := range []string{"aes", "avx2", "topoext", "pdpe1gb", "pcid", "hypervisor"} {
		if !containsFold(flags, want) {
			t.Errorf("live host should recognize CPU flag %q", want)
		}
	}
	if len(flags) < 100 {
		t.Errorf("expected hundreds of CPUID flags on a modern host, got %d", len(flags))
	}
}
