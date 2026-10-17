package mdraid

import (
	"context"
	"strings"
	"testing"

	"webkvm/internal/sysutil"
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

func TestCreate_MockRunner(t *testing.T) {
	var executed [][]string
	mock := &sysutil.MockCommandRunner{
		Handler: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			executed = append(executed, append([]string{name}, args...))
			return []byte("mdadm: array /dev/md0 started.\n"), nil
		},
	}

	SetRunner(mock)
	defer SetRunner(nil)

	if !IsAvailable() {
		t.Errorf("IsAvailable() should be true when using MockCommandRunner")
	}

	dev, err := Create(context.Background(), "md0", "1", []string{"/dev/sdb", "/dev/sdc"})
	if err != nil {
		t.Fatalf("Create() unexpected error: %v", err)
	}
	if dev != "/dev/md0" {
		t.Errorf("Create() dev = %q, want /dev/md0", dev)
	}

	var foundMdadm bool
	for _, cmd := range executed {
		if len(cmd) > 0 && cmd[0] == "mdadm" && strings.Contains(strings.Join(cmd, " "), "--create") {
			foundMdadm = true
			break
		}
	}
	if !foundMdadm {
		t.Errorf("expected mdadm --create command to be executed, got: %+v", executed)
	}
}

func TestStop_MockRunner(t *testing.T) {
	var stoppedDev string
	mock := &sysutil.MockCommandRunner{
		Handler: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "mdadm" && len(args) >= 2 && args[0] == "--stop" {
				stoppedDev = args[1]
				return []byte("mdadm: stopped /dev/md0\n"), nil
			}
			return nil, nil
		},
	}

	SetRunner(mock)
	defer SetRunner(nil)

	err := Stop(context.Background(), "/dev/md0")
	if err != nil {
		t.Fatalf("Stop() unexpected error: %v", err)
	}
	if stoppedDev != "/dev/md0" {
		t.Errorf("Stop() called with device %q, want /dev/md0", stoppedDev)
	}
}
