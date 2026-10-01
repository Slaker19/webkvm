package libvirt

import (
	"strings"
	"testing"

	"webkvm/internal/hostcaps"
)

// requireIn is the core predicate behind every device-model check, so it
// carries the bulk of the risk and gets the exhaustive cases. The
// validate* wrappers are thin capability lookups.
func TestRequireIn(t *testing.T) {
	supported := []string{"vga", "virtio", "bochs"}

	cases := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"exact match", "virtio", false},
		{"case-insensitive match", "VIRTIO", false},
		{"mixed case match", "Vga", false},
		{"unsupported value", "qxl", true},
		{"empty requested value", "", true},
		{"prefix is not a match", "virt", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireIn("video model", tc.want, supported)
			if tc.wantErr && err == nil {
				t.Fatalf("requireIn(%q) = nil, want error", tc.want)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("requireIn(%q) = %v, want nil", tc.want, err)
			}
		})
	}
}

// TestRequireInErrorMessageEnumerates guards the user-facing contract:
// the error must name the offending value AND list what IS available,
// because "qxl is invalid" without alternatives is the exact dead end
// this feature exists to remove.
func TestRequireInErrorMessageEnumerates(t *testing.T) {
	err := requireIn("video model", "qxl", []string{"vga", "virtio", "bochs"})
	if err == nil {
		t.Fatal("expected error for qxl")
	}
	msg := err.Error()
	if !strings.Contains(msg, "qxl") {
		t.Errorf("error should name the offending value, got %q", msg)
	}
	for _, want := range []string{"vga", "virtio", "bochs"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should list supported value %q, got %q", want, msg)
		}
	}
}

// TestRequireInEmptySupportedIsPermissive pins the degradation rule: an
// empty capability list means "unknown", not "nothing is allowed".
func TestRequireInEmptySupportedIsPermissive(t *testing.T) {
	if err := requireIn("video model", "qxl", nil); err != nil {
		t.Fatalf("empty supported list must be permissive, got %v", err)
	}
}

// TestValidatorsSkipWhenUnprobeable ensures the wrappers do not reject
// anything on a host whose capabilities could not be read. On this test
// host there is no libvirt, so Parsed is false and every model passes.
func TestValidatorsSkipWhenUnprobeable(t *testing.T) {
	c := hostcaps.Get()
	if c.Parsed {
		t.Skip("test host has a working libvirt probe; degradation path not exercised")
	}
	if err := validateVideoModel("qxl"); err != nil {
		t.Fatalf("unprobeable host must not reject models, got %v", err)
	}
	if err := validateDiskBus("anything"); err != nil {
		t.Fatalf("unprobeable host must not reject disk bus, got %v", err)
	}
}

// TestValidatorsAllowEmptyAndNone documents the deliberate bypasses:
// "none"/"" mean the device is omitted entirely, so they must never be
// validated against a capability list.
func TestValidatorsAllowEmptyAndNone(t *testing.T) {
	if err := validateVideoModel(""); err != nil {
		t.Errorf("empty video model should be allowed: %v", err)
	}
	if err := validateVideoModel("none"); err != nil {
		t.Errorf("video model 'none' should be allowed: %v", err)
	}
	if err := validateAudioModel(""); err != nil {
		t.Errorf("empty audio model should be allowed: %v", err)
	}
	if err := validateAudioModel("none"); err != nil {
		t.Errorf("audio model 'none' should be allowed: %v", err)
	}
}

// TestValidateCPUFlagsStripsPolicyPrefix ensures the +/- policy markers
// are stripped before matching, since "+aes" and "-aes" both refer to
// the CPUID flag "aes", not a literal supported-list entry.
func TestValidateCPUFlagsStripsPolicyPrefix(t *testing.T) {
	c := hostcaps.Get()
	if len(c.CPUFlags) == 0 {
		t.Skip("test host has no probed CPU flags; degradation path not exercised")
	}
	if err := validateCPUFlags([]string{"+aes", "-hypervisor"}); err != nil {
		t.Fatalf("known flags with policy prefixes should validate, got %v", err)
	}
}

func TestValidateCPUFlagsRejectsUnknown(t *testing.T) {
	c := hostcaps.Get()
	if len(c.CPUFlags) == 0 {
		t.Skip("test host has no probed CPU flags; degradation path not exercised")
	}
	if err := validateCPUFlags([]string{"+definitely-not-a-real-cpu-flag"}); err == nil {
		t.Fatal("expected an error for an unrecognized CPU flag")
	}
}

func TestValidateCPUFlagsEmptySliceIsFine(t *testing.T) {
	if err := validateCPUFlags(nil); err != nil {
		t.Fatalf("nil flags should always validate, got %v", err)
	}
	if err := validateCPUFlags([]string{}); err != nil {
		t.Fatalf("empty flags should always validate, got %v", err)
	}
	// A bare "+" or "-" with no name should be ignored, not rejected.
	if err := validateCPUFlags([]string{"+", "-", "  "}); err != nil {
		t.Fatalf("blank flag entries should be ignored, got %v", err)
	}
}

func TestValidateCPUModelUnknown(t *testing.T) {
	c := hostcaps.Get()
	if len(c.CPUModels) == 0 {
		t.Skip("test host has no probed CPU models; degradation path not exercised")
	}
	if err := validateCPUModel("definitely-not-a-real-cpu-model"); err == nil {
		t.Fatal("expected an error for an unrecognized CPU model")
	}
	if err := validateCPUModel(""); err != nil {
		t.Fatalf("empty CPU model should be allowed (means unset), got %v", err)
	}
}

// TestRequireInTruncatesLongLists guards the CPU flags/models UX: with
// hundreds of entries, the raw error would be unreadable, so it must cap
// the enumeration and say how many more exist.
func TestRequireInTruncatesLongLists(t *testing.T) {
	long := make([]string, 200)
	for i := range long {
		long[i] = strings.Repeat("x", 1) + string(rune('a'+i%26))
	}
	err := requireIn("CPU flag", "totally-bogus", long)
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "more") {
		t.Errorf("long list should be truncated with a remainder count, got %q", msg)
	}
	if strings.Count(msg, ",") > 20 {
		t.Errorf("error message not truncated, has too many entries: %q", msg)
	}
}

func TestResolveGraphicsType(t *testing.T) {
	// "none" always stays "none"
	if got := resolveGraphicsType("none"); got != "none" {
		t.Errorf("resolveGraphicsType('none') = %q, want 'none'", got)
	}
	// "vnc" always stays "vnc"
	if got := resolveGraphicsType("vnc"); got != "vnc" {
		t.Errorf("resolveGraphicsType('vnc') = %q, want 'vnc'", got)
	}
	caps := hostcaps.Get()
	if caps.Parsed && !caps.SPICESupported {
		// When host does not support SPICE, "spice", "both", or "" must degrade to "vnc"
		if got := resolveGraphicsType(""); got != "vnc" {
			t.Errorf("resolveGraphicsType('') = %q, want 'vnc' when SPICE unsupported", got)
		}
		if got := resolveGraphicsType("both"); got != "vnc" {
			t.Errorf("resolveGraphicsType('both') = %q, want 'vnc' when SPICE unsupported", got)
		}
		if got := resolveGraphicsType("spice"); got != "vnc" {
			t.Errorf("resolveGraphicsType('spice') = %q, want 'vnc' when SPICE unsupported", got)
		}
	}
}
