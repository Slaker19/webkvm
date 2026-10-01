package mdraid

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var safeMDDeviceRE = regexp.MustCompile(`^/dev/md[0-9]+$|^/dev/md/[a-zA-Z0-9_\-]+$`)

// IsAvailable reports whether the `mdadm` utility is present on the host.
func IsAvailable() bool {
	_, err := exec.LookPath("mdadm")
	return err == nil
}

// NormalizeLevel maps user inputs (e.g. "mirror", "raid1", "1") to canonical mdadm levels
// and returns the minimum required number of devices.
func NormalizeLevel(level string) (canonical string, minDevs int, err error) {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "0", "raid0", "stripe":
		return "0", 2, nil
	case "1", "raid1", "mirror":
		return "1", 2, nil
	case "5", "raid5":
		return "5", 3, nil
	case "6", "raid6":
		return "6", 4, nil
	case "10", "raid10":
		return "10", 4, nil
	default:
		return "", 0, fmt.Errorf("unsupported RAID level %q (must be 0, 1, 5, 6, 10)", level)
	}
}

// NextAvailableDevice scans /dev/mdN and /proc/mdstat to find an unused MD device path.
func NextAvailableDevice() (string, error) {
	mdstat, _ := os.ReadFile("/proc/mdstat")
	mdstatContent := string(mdstat)

	for i := 0; i <= 127; i++ {
		devName := fmt.Sprintf("md%d", i)
		devPath := fmt.Sprintf("/dev/%s", devName)

		// Check /proc/mdstat
		if strings.Contains(mdstatContent, devName+" :") || strings.Contains(mdstatContent, devName+"[") {
			continue
		}

		// Check if device node exists and is in use
		if _, err := os.Stat(devPath); err == nil {
			// Node exists; check if it's already an active block device in sysfs
			sysPath := fmt.Sprintf("/sys/class/block/%s/md", devName)
			if _, err := os.Stat(sysPath); err == nil {
				continue
			}
		}

		return devPath, nil
	}

	return "", errors.New("no free /dev/md device available (0-127 all in use)")
}

// Create initializes a new Linux software RAID array using mdadm.
func Create(ctx context.Context, mdDevice, level string, devices []string) (string, error) {
	if !IsAvailable() {
		return "", errors.New("mdadm command not found on host")
	}

	lvl, minDevs, err := NormalizeLevel(level)
	if err != nil {
		return "", err
	}

	if len(devices) < minDevs {
		return "", fmt.Errorf("RAID %s requires at least %d devices, got %d", lvl, minDevs, len(devices))
	}

	if mdDevice == "" {
		next, err := NextAvailableDevice()
		if err != nil {
			return "", fmt.Errorf("determine free md device: %w", err)
		}
		mdDevice = next
	} else {
		mdDevice = strings.TrimSpace(mdDevice)
		if !strings.HasPrefix(mdDevice, "/dev/") {
			mdDevice = "/dev/" + mdDevice
		}
		if !safeMDDeviceRE.MatchString(mdDevice) {
			return "", fmt.Errorf("invalid RAID device name %q: must match /dev/mdN or /dev/md/name", mdDevice)
		}
	}

	ensureModules(ctx, lvl)
	ensureDeviceNode(ctx, mdDevice)

	args := []string{
		"--create", mdDevice,
		"--auto=yes",
		fmt.Sprintf("--level=%s", lvl),
		fmt.Sprintf("--raid-devices=%d", len(devices)),
		"--metadata=1.2",
		"--run",
		"--force",
	}
	args = append(args, devices...)

	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "mdadm", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("mdadm create: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	// Settle udev so /dev/mdX node is fully created and available
	_ = exec.CommandContext(cctx, "udevadm", "settle", "--timeout=5").Run()

	// Persist to mdadm.conf best-effort
	persistConfig(cctx)

	return mdDevice, nil
}

func ensureDeviceNode(ctx context.Context, mdDevice string) {
	mdDevice = filepath.Clean(strings.TrimSpace(mdDevice))
	if !safeMDDeviceRE.MatchString(mdDevice) || !strings.HasPrefix(mdDevice, "/dev/md") || strings.Contains(mdDevice, "..") {
		return
	}
	if _, err := os.Stat(mdDevice); err == nil {
		return
	}
	if strings.HasPrefix(mdDevice, "/dev/md") {
		var minor int
		if _, err := fmt.Sscanf(mdDevice, "/dev/md%d", &minor); err == nil {
			_ = exec.CommandContext(ctx, "mknod", "-m", "660", mdDevice, "b", "9", fmt.Sprintf("%d", minor)).Run()
		}
	}
}

func ensureModules(ctx context.Context, level string) {
	_ = exec.CommandContext(ctx, "modprobe", "md_mod").Run()
	switch level {
	case "0":
		_ = exec.CommandContext(ctx, "modprobe", "raid0").Run()
	case "1":
		_ = exec.CommandContext(ctx, "modprobe", "raid1").Run()
	case "5", "6":
		_ = exec.CommandContext(ctx, "modprobe", "raid456").Run()
	case "10":
		_ = exec.CommandContext(ctx, "modprobe", "raid10").Run()
	}
}

// SyncAction initiates or cancels a consistency check or repair on the specified MD RAID device.
// action can be "check", "repair", or "idle".
func SyncAction(ctx context.Context, mdDevice, action string) error {
	clean := filepath.Clean(strings.TrimSpace(mdDevice))
	if !strings.HasPrefix(clean, "/dev/") {
		clean = "/dev/" + clean
	}
	if !safeMDDeviceRE.MatchString(clean) || strings.Contains(clean, "..") {
		return fmt.Errorf("invalid MD device %q", mdDevice)
	}

	devName := filepath.Base(clean)
	syncActionFile := filepath.Join("/sys/block", devName, "md", "sync_action")

	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "check", "repair", "idle":
	default:
		return fmt.Errorf("invalid sync action %q (must be 'check', 'repair', or 'idle')", action)
	}

	if _, err := os.Stat(syncActionFile); err != nil {
		return fmt.Errorf("RAID array %s is not active or sync_action sysfs node not found", clean)
	}

	return os.WriteFile(syncActionFile, []byte(action+"\n"), 0644)
}

func persistConfig(ctx context.Context) {
	out, err := exec.CommandContext(ctx, "mdadm", "--detail", "--scan").Output()
	if err != nil || len(out) == 0 {
		return
	}

	for _, confPath := range []string{"/etc/mdadm/mdadm.conf", "/etc/mdadm.conf"} {
		if fi, err := os.Stat(confPath); err == nil && !fi.IsDir() {
			existing, _ := os.ReadFile(confPath)
			lines := strings.Split(string(existing), "\n")
			var filtered []string
			for _, l := range lines {
				// keep non-ARRAY lines
				if !strings.HasPrefix(strings.TrimSpace(l), "ARRAY ") {
					filtered = append(filtered, l)
				}
			}
			newContent := strings.Join(filtered, "\n")
			if !strings.HasSuffix(newContent, "\n") {
				newContent += "\n"
			}
			newContent += string(out)
			_ = os.WriteFile(confPath, []byte(newContent), 0644)
			break
		}
	}
}
