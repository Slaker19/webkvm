package libvirt

import (
	"strings"
	"testing"

	"webkvm/internal/config"
)

func TestEnsureNoAppArmorLabel(t *testing.T) {
	in := "<domain type='kvm'>\n  <name>vm</name>\n</domain>"
	out := ensureNoAppArmorLabel(in, true)
	if !strings.Contains(out, "<seclabel type='none' model='apparmor'/>") {
		t.Fatalf("seclabel missing:\n%s", out)
	}
	if got := ensureNoAppArmorLabel(in, false); got != in {
		t.Fatalf("seclabel added while disabled:\n%s", got)
	}
	already := "<domain type='kvm'><seclabel type='none'/><name>vm</name></domain>"
	if got := ensureNoAppArmorLabel(already, true); got != already {
		t.Fatalf("existing seclabel was modified: %s", got)
	}
}

func TestDisableAppArmorFor(t *testing.T) {
	cases := []struct {
		name     string
		cfg      *config.Config
		active   bool
		expected bool
	}{
		{"apparmor inactive", &config.Config{DisableAppArmor: true}, false, false},
		{"nil config with apparmor", nil, true, true},
		{"explicit disable", &config.Config{DisableAppArmor: true}, true, true},
		{"explicit keep", &config.Config{DisableAppArmor: false}, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := disableAppArmorFor(tc.cfg, tc.active); got != tc.expected {
				t.Fatalf("disableAppArmorFor() = %v, want %v", got, tc.expected)
			}
		})
	}
}
