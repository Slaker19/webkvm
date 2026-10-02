package zvol

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// safeZVolNameRE requires at least pool/volume, allowing alphanumeric,
// underscores, periods, hyphens and colons in each segment.
var safeZVolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.\-:]*(?:/[a-zA-Z0-9_][a-zA-Z0-9_.\-:]*)+$`)

// Info describes a discovered or resolved ZFS volume.
type Info struct {
	Name    string `json:"name"`    // e.g. "tank/vm-disk-1"
	Pool    string `json:"pool"`    // e.g. "tank"
	VolSize int64  `json:"volsize"` // volume size in bytes
	Used    int64  `json:"used"`    // allocated / used bytes
	Device  string `json:"device"`  // block device path: "/dev/zvol/tank/vm-disk-1"
}

// ValidName validates a ZFS volume identifier. It requires at least one
// hierarchy level (pool/vol), rejects snapshots (@), bookmarks (#),
// path traversal (..), spaces and control characters.
func ValidName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("zvol name cannot be empty")
	}
	if len(name) > 255 {
		return errors.New("zvol name exceeds 255 characters")
	}
	if strings.ContainsAny(name, "@# \t\r\n\x00\"'`;|&$<>") {
		return errors.New("zvol name contains forbidden characters (@, #, whitespace or shell metacharacters)")
	}
	if strings.Contains(name, "..") {
		return errors.New("zvol name cannot contain '..'")
	}
	if !safeZVolNameRE.MatchString(name) {
		return fmt.Errorf("invalid zvol name %q: must be formatted as 'pool/volume' with valid characters", name)
	}
	return nil
}

// DevicePath returns the canonical /dev/zvol device path for a zvol name.
func DevicePath(name string) string {
	clean := strings.TrimPrefix(name, "/dev/zvol/")
	return "/dev/zvol/" + clean
}

// IsAvailable reports whether the `zfs` userland utility is present on the host.
func IsAvailable() bool {
	_, err := exec.LookPath("zfs")
	return err == nil
}

// Resolve queries ZFS for the given volume name, verifying that it exists,
// is of type 'volume', and has a corresponding block device under /dev/zvol.
func Resolve(ctx context.Context, name string) (Info, error) {
	name = strings.TrimPrefix(strings.TrimSpace(name), "/dev/zvol/")
	if err := ValidName(name); err != nil {
		return Info{}, err
	}
	if !IsAvailable() {
		return Info{}, errors.New("zfs command not found on host")
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "zfs", "get", "-Hp", "-o", "property,value", "type,volsize,used", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Info{}, fmt.Errorf("zfs get %s: %s (%w)", name, strings.TrimSpace(string(out)), err)
	}

	info, err := parseZfsGetOutput(name, out)
	if err != nil {
		return Info{}, err
	}

	// Verify the /dev/zvol block device node exists safely within /dev/zvol
	devPath := filepath.Clean(DevicePath(name))
	if !strings.HasPrefix(devPath, "/dev/zvol/") || strings.Contains(devPath, "..") {
		return Info{}, fmt.Errorf("invalid zvol device path %q", devPath)
	}
	if !safeZVolNameRE.MatchString(strings.TrimPrefix(devPath, "/dev/zvol/")) {
		return Info{}, fmt.Errorf("invalid zvol name format in %q", devPath)
	}
	var fi os.FileInfo
	for attempt := 0; attempt < 10; attempt++ {
		fi, err = os.Stat(devPath)
		if err == nil {
			break
		}
		_ = exec.CommandContext(cctx, "udevadm", "settle", "--timeout=1").Run()
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return Info{}, fmt.Errorf("zvol block device %s not found: %w", devPath, err)
	}
	if fi.Mode()&os.ModeDevice == 0 {
		return Info{}, fmt.Errorf("zvol path %s is not a device node", devPath)
	}

	// Validate symlink destination stays within /dev
	if realPath, err := filepath.EvalSymlinks(devPath); err == nil {
		if !strings.HasPrefix(realPath, "/dev/") {
			return Info{}, fmt.Errorf("zvol device %s resolves outside /dev: %s", devPath, realPath)
		}
	}

	return info, nil
}

func parseZfsGetOutput(name string, out []byte) (Info, error) {
	parts := strings.Split(name, "/")
	info := Info{
		Name:   name,
		Pool:   parts[0],
		Device: DevicePath(name),
	}

	var dsType string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			continue
		}
		prop, val := fields[0], fields[1]
		switch prop {
		case "type":
			dsType = val
		case "volsize":
			if sz, err := strconv.ParseInt(val, 10, 64); err == nil {
				info.VolSize = sz
			}
		case "used":
			if sz, err := strconv.ParseInt(val, 10, 64); err == nil {
				info.Used = sz
			}
		}
	}

	if dsType == "" {
		return Info{}, fmt.Errorf("zfs dataset %q not found", name)
	}
	if dsType != "volume" {
		return Info{}, fmt.Errorf("zfs dataset %q is a %s, not a volume (zvol)", name, dsType)
	}

	return info, nil
}

// List enumerates all ZFS volumes on the host. Returns an empty slice without
// error if ZFS is not installed or if no pools/volumes exist.
func List(ctx context.Context) ([]Info, error) {
	if !IsAvailable() {
		return []Info{}, nil
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "zfs", "list", "-Hp", "-t", "volume", "-o", "name,volsize,used")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// If "no datasets available", "no pools available", or running unprivileged without permissions,
		// return empty list cleanly.
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "no datasets available") || strings.Contains(msg, "no pools available") || strings.Contains(msg, "Permission denied") || strings.Contains(msg, "must be run as root") {
			return []Info{}, nil
		}
		return nil, fmt.Errorf("zfs list: %s (%w)", msg, err)
	}

	return parseZfsListOutput(out), nil
}

func parseZfsListOutput(out []byte) []Info {
	zvols := make([]Info, 0)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 3 {
			continue
		}
		name := fields[0]
		if err := ValidName(name); err != nil {
			continue
		}
		volsize, _ := strconv.ParseInt(fields[1], 10, 64)
		used, _ := strconv.ParseInt(fields[2], 10, 64)
		parts := strings.Split(name, "/")
		zvols = append(zvols, Info{
			Name:    name,
			Pool:    parts[0],
			VolSize: volsize,
			Used:    used,
			Device:  DevicePath(name),
		})
	}
	return zvols
}
