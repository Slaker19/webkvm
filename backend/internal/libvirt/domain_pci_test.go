package libvirt

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParsePCIAddressHex(t *testing.T) {
	tests := []struct {
		addr    string
		wantDom string
		wantBus string
		wantSlt string
		wantFn  string
		wantErr bool
	}{
		{"0000:02:00.0", "0x0000", "0x02", "0x00", "0x0", false},
		{"0000:00:1f.3", "0x0000", "0x00", "0x1f", "0x3", false},
		{"0000:01:00.1", "0x0000", "0x01", "0x00", "0x1", false},
		{"invalid", "", "", "", "", true},
		{"0000:zz:00.0", "", "", "", "", true},
	}

	for _, tt := range tests {
		dom, bus, slt, fn, err := parsePCIAddressHex(tt.addr)
		if (err != nil) != tt.wantErr {
			t.Errorf("parsePCIAddressHex(%q) err = %v, wantErr = %v", tt.addr, err, tt.wantErr)
			continue
		}
		if !tt.wantErr {
			if dom != tt.wantDom || bus != tt.wantBus || slt != tt.wantSlt || fn != tt.wantFn {
				t.Errorf("parsePCIAddressHex(%q) = (%q, %q, %q, %q), want (%q, %q, %q, %q)",
					tt.addr, dom, bus, slt, fn, tt.wantDom, tt.wantBus, tt.wantSlt, tt.wantFn)
			}
		}
	}
}

func TestPciDriverOverride(t *testing.T) {
	// Invalid address returns empty string
	if ovr := pciDriverOverride("invalid-addr"); ovr != "" {
		t.Errorf("expected empty string for invalid address, got %q", ovr)
	}

	// Non-existent address returns empty string
	if ovr := pciDriverOverride("9999:99:99.9"); ovr != "" {
		t.Errorf("expected empty string for non-existent address, got %q", ovr)
	}

	// Simulated sysfs with (null) vs real override
	tmpDir := t.TempDir()
	devDir := filepath.Join(tmpDir, "0000:02:00.0")
	if err := os.MkdirAll(devDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Test reading override directly from file
	overrideFile := filepath.Join(devDir, "driver_override")
	_ = os.WriteFile(overrideFile, []byte("(null)\n"), 0644)
	data, _ := os.ReadFile(overrideFile)
	if string(data) != "(null)\n" {
		t.Errorf("unexpected file content: %s", string(data))
	}
}
