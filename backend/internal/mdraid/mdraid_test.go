package mdraid

import (
	"testing"
)

func TestNormalizeLevel(t *testing.T) {
	cases := []struct {
		input       string
		wantLevel   string
		wantMinDevs int
		wantErr     bool
	}{
		{"0", "0", 2, false},
		{"raid0", "0", 2, false},
		{"stripe", "0", 2, false},
		{"1", "1", 2, false},
		{"raid1", "1", 2, false},
		{"mirror", "1", 2, false},
		{"5", "5", 3, false},
		{"raid5", "5", 3, false},
		{"6", "6", 4, false},
		{"raid6", "6", 4, false},
		{"10", "10", 4, false},
		{"raid10", "10", 4, false},
		{"invalid", "", 0, true},
		{"raidZ", "", 0, true},
	}

	for _, c := range cases {
		lvl, minDevs, err := NormalizeLevel(c.input)
		if c.wantErr {
			if err == nil {
				t.Errorf("NormalizeLevel(%q) expected error, got nil", c.input)
			}
		} else {
			if err != nil {
				t.Errorf("NormalizeLevel(%q) unexpected error: %v", c.input, err)
			}
			if lvl != c.wantLevel {
				t.Errorf("NormalizeLevel(%q) level = %q, want %q", c.input, lvl, c.wantLevel)
			}
			if minDevs != c.wantMinDevs {
				t.Errorf("NormalizeLevel(%q) minDevs = %d, want %d", c.input, minDevs, c.wantMinDevs)
			}
		}
	}
}

func TestNextAvailableDevice(t *testing.T) {
	dev, err := NextAvailableDevice()
	if err != nil {
		t.Fatalf("NextAvailableDevice() error: %v", err)
	}
	if dev == "" {
		t.Fatalf("NextAvailableDevice() returned empty string")
	}
	if !safeMDDeviceRE.MatchString(dev) {
		t.Errorf("NextAvailableDevice() returned invalid device path: %q", dev)
	}
}
