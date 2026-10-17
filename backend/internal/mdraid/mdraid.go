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

	"webkvm/internal/sysutil"
)

var (
	safeMDDeviceRE = regexp.MustCompile(`^/dev/md[0-9]+$|^/dev/md/[a-zA-Z0-9_\-]+$`)
	runner         sysutil.CommandRunner = sysutil.DefaultRunner
)

// SetRunner overrides the command runner for testing or custom environments.
func SetRunner(r sysutil.CommandRunner) {
	if r == nil {
		runner = sysutil.DefaultRunner
	} else {
		runner = r
	}
}

// IsAvailable reports whether the `mdadm` utility is present on the host.
func IsAvailable() bool {
	if _, ok := runner.(*sysutil.OSCommandRunner); !ok {
		return true
	}
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

	out, err := runner.Run(cctx, "mdadm", args...)
	if err != nil {
		return "", fmt.Errorf("mdadm create: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	// Settle udev so /dev/mdX node is fully created and available
	_, _ = runner.Run(cctx, "udevadm", "settle", "--timeout=5")

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
			_, _ = runner.Run(ctx, "mknod", "-m", "660", mdDevice, "b", "9", fmt.Sprintf("%d", minor))
		}
	}
}

func ensureModules(ctx context.Context, level string) {
	_, _ = runner.Run(ctx, "modprobe", "md_mod")
	switch level {
	case "0":
		_, _ = runner.Run(ctx, "modprobe", "raid0")
	case "1":
		_, _ = runner.Run(ctx, "modprobe", "raid1")
	case "5", "6":
		_, _ = runner.Run(ctx, "modprobe", "raid456")
	case "10":
		_, _ = runner.Run(ctx, "modprobe", "raid10")
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

// Array represents a Linux software RAID array parsed directly from kernel sysfs.
type Array struct {
	Name        string   `json:"name"`        // "md0"
	Device      string   `json:"device"`      // "/dev/md0"
	Level       string   `json:"level"`       // "raid1"
	State       string   `json:"state"`       // "clean", "active", "degraded"
	Degraded    bool     `json:"degraded"`    // true if degraded
	SyncAction  string   `json:"sync_action"` // "idle", "resync", "check", "repair"
	DisksTotal  int      `json:"disks_total"`
	DisksActive int      `json:"disks_active"`
	Devices     []string `json:"devices"` // ["/dev/sdb", "/dev/sdc"]
	Size        int64    `json:"size"`
	SizeHuman   string   `json:"size_human"`
	MismatchCnt int64    `json:"mismatch_cnt"`
	FSType      string   `json:"fstype,omitempty"`
	MountPoints []string `json:"mountpoints,omitempty"`
	Mounted     bool     `json:"mounted"`
}

// List discovers all active MD RAID arrays directly from /sys/block/md*/md.
func List() ([]Array, error) {
	matches, err := filepath.Glob("/sys/block/md*/md")
	if err != nil {
		return nil, err
	}

	arrays := make([]Array, 0, len(matches))
	for _, mdDir := range matches {
		blockDir := filepath.Dir(mdDir)
		devName := filepath.Base(blockDir)

		readSys := func(name string) string {
			b, err := os.ReadFile(filepath.Join(mdDir, name))
			if err != nil {
				return ""
			}
			return strings.TrimSpace(string(b))
		}

		level := readSys("level")
		if level == "" {
			continue
		}

		state := readSys("array_state")
		syncAction := readSys("sync_action")
		if syncAction == "" {
			syncAction = "idle"
		}
		degraded := readSys("degraded") == "1"

		var disksTotal, disksActive int
		_, _ = fmt.Sscanf(readSys("raid_disks"), "%d", &disksTotal)

		var mismatchCnt int64
		_, _ = fmt.Sscanf(readSys("mismatch_cnt"), "%d", &mismatchCnt)

		devMatches, _ := filepath.Glob(filepath.Join(mdDir, "dev-*"))
		memberDevs := make([]string, 0, len(devMatches))
		for _, dm := range devMatches {
			base := filepath.Base(dm)
			memberDev := "/dev/" + strings.TrimPrefix(base, "dev-")
			memberDevs = append(memberDevs, memberDev)
			rdevState, _ := os.ReadFile(filepath.Join(dm, "state"))
			if strings.Contains(string(rdevState), "in_sync") {
				disksActive++
			}
		}

		var sizeBytes int64
		if sizeSecStr, err := os.ReadFile(filepath.Join(blockDir, "size")); err == nil {
			var sec int64
			if _, err := fmt.Sscanf(strings.TrimSpace(string(sizeSecStr)), "%d", &sec); err == nil {
				sizeBytes = sec * 512
			}
		}

		sizeHuman := fmt.Sprintf("%.1f GB", float64(sizeBytes)/(1024*1024*1024))
		if sizeBytes < 1024*1024*1024 {
			sizeHuman = fmt.Sprintf("%.1f MB", float64(sizeBytes)/(1024*1024))
		}

		arrays = append(arrays, Array{
			Name:        devName,
			Device:      "/dev/" + devName,
			Level:       level,
			State:       state,
			Degraded:    degraded,
			SyncAction:  syncAction,
			DisksTotal:  disksTotal,
			DisksActive: disksActive,
			Devices:     memberDevs,
			Size:        sizeBytes,
			SizeHuman:   sizeHuman,
			MismatchCnt: mismatchCnt,
		})
	}

	return arrays, nil
}

// Stop halts an MD RAID array using mdadm --stop and updates mdadm.conf.
func Stop(ctx context.Context, mdDevice string) error {
	clean := filepath.Clean(strings.TrimSpace(mdDevice))
	if !strings.HasPrefix(clean, "/dev/") {
		clean = "/dev/" + clean
	}
	if !safeMDDeviceRE.MatchString(clean) {
		return fmt.Errorf("invalid MD device %q", mdDevice)
	}

	out, err := runner.Run(ctx, "mdadm", "--stop", clean)
	if err != nil {
		return fmt.Errorf("stop raid %s: %v (%s)", clean, err, strings.TrimSpace(string(out)))
	}

	persistConfig(ctx)
	return nil
}

func persistConfig(ctx context.Context) {
	out, err := runner.Run(ctx, "mdadm", "--detail", "--scan")
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
