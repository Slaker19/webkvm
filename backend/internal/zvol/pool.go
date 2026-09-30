package zvol

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var safePoolNameRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.\-:]*$`)
var safeVolNameRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.\-:]*$`)

var reservedPoolNames = map[string]bool{
	"mirror":  true,
	"raidz":   true,
	"raidz1":  true,
	"raidz2":  true,
	"raidz3":  true,
	"spare":   true,
	"log":     true,
	"cache":   true,
	"special": true,
	"dedup":   true,
}

// PoolInfo describes a discovered ZFS pool.
type PoolInfo struct {
	Name       string   `json:"name"`
	Size       int64    `json:"size_bytes"`
	SizeHuman  string   `json:"size_human"`
	Allocated  int64    `json:"allocated_bytes"`
	AllocHuman string   `json:"alloc_human"`
	Free       int64    `json:"free_bytes"`
	FreeHuman  string   `json:"free_human"`
	Health     string   `json:"health"`
	Devices    []string `json:"devices,omitempty"`
}

// ValidPoolName validates a ZFS pool identifier.
func ValidPoolName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("pool name cannot be empty")
	}
	if len(name) > 64 {
		return errors.New("pool name exceeds 64 characters")
	}
	if strings.ContainsAny(name, "/@# \t\r\n\x00\"'`;|&$<>") {
		return errors.New("pool name contains invalid characters")
	}
	if strings.Contains(name, "..") {
		return errors.New("pool name cannot contain '..'")
	}
	if !safePoolNameRE.MatchString(name) {
		return fmt.Errorf("invalid pool name %q: must begin with a letter and contain only alphanumeric, '_', '-', '.', ':'", name)
	}
	if reservedPoolNames[strings.ToLower(name)] {
		return fmt.Errorf("pool name %q is a reserved ZFS keyword", name)
	}
	return nil
}

// ValidVolumeName validates a single dataset volume name (without pool prefix).
func ValidVolumeName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("volume name cannot be empty")
	}
	if len(name) > 255 {
		return errors.New("volume name exceeds 255 characters")
	}
	if strings.ContainsAny(name, "/@# \t\r\n\x00\"'`;|&$<>") {
		return errors.New("volume name contains invalid characters")
	}
	if strings.Contains(name, "..") {
		return errors.New("volume name cannot contain '..'")
	}
	if !safeVolNameRE.MatchString(name) {
		return fmt.Errorf("invalid volume name %q", name)
	}
	return nil
}

// IsPoolAvailable reports whether the `zpool` userland utility is present on the host.
func IsPoolAvailable() bool {
	_, err := exec.LookPath("zpool")
	return err == nil
}

// ListPools enumerates all ZFS storage pools on the host.
func ListPools(ctx context.Context) ([]PoolInfo, error) {
	if !IsPoolAvailable() {
		return []PoolInfo{}, nil
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "zpool", "list", "-Hp", "-o", "name,size,alloc,free,health")
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "no pools available") || strings.Contains(msg, "Permission denied") || strings.Contains(msg, "must be run as root") {
			return []PoolInfo{}, nil
		}
		return nil, fmt.Errorf("zpool list: %s (%w)", msg, err)
	}

	pools := parseZpoolListOutput(out)

	// Fetch devices for each pool using zpool status
	for i := range pools {
		pools[i].Devices = getPoolDevices(cctx, pools[i].Name)
	}

	return pools, nil
}

func parseZpoolListOutput(out []byte) []PoolInfo {
	pools := make([]PoolInfo, 0)
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 5 {
			continue
		}
		name := fields[0]
		if err := ValidPoolName(name); err != nil {
			continue
		}
		size, _ := strconv.ParseInt(fields[1], 10, 64)
		alloc, _ := strconv.ParseInt(fields[2], 10, 64)
		free, _ := strconv.ParseInt(fields[3], 10, 64)
		health := fields[4]

		pools = append(pools, PoolInfo{
			Name:       name,
			Size:       size,
			SizeHuman:  formatBytes(size),
			Allocated:  alloc,
			AllocHuman: formatBytes(alloc),
			Free:       free,
			FreeHuman:  formatBytes(free),
			Health:     health,
		})
	}
	return pools
}

func getPoolDevices(ctx context.Context, pool string) []string {
	cmd := exec.CommandContext(ctx, "zpool", "status", "-P", "-v", pool)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil
	}

	var devices []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	inConfig := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "NAME") {
			inConfig = true
			continue
		}
		if !inConfig || line == "" || strings.HasPrefix(line, "errors:") {
			if strings.HasPrefix(line, "errors:") {
				break
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			dev := fields[0]
			if strings.HasPrefix(dev, "/dev/") {
				devices = append(devices, dev)
			}
		}
	}
	return devices
}

// CreatePool creates a new ZFS storage pool.
func CreatePool(ctx context.Context, name, topology string, devices []string) error {
	if err := ValidPoolName(name); err != nil {
		return err
	}
	if !IsPoolAvailable() {
		return errors.New("zpool command not found on host")
	}

	topo := strings.ToLower(strings.TrimSpace(topology))
	if topo == "" {
		topo = "stripe"
	}

	switch topo {
	case "stripe":
		if len(devices) < 1 {
			return errors.New("stripe pool requires at least 1 disk")
		}
	case "mirror":
		if len(devices) < 2 {
			return errors.New("mirror pool requires at least 2 disks")
		}
	case "raidz", "raidz1":
		topo = "raidz1"
		if len(devices) < 3 {
			return errors.New("raidz1 pool requires at least 3 disks")
		}
	case "raidz2":
		if len(devices) < 4 {
			return errors.New("raidz2 pool requires at least 4 disks")
		}
	default:
		return fmt.Errorf("unsupported topology %q (must be stripe, mirror, raidz1, raidz2)", topology)
	}

	args := []string{"create", "-f", name}
	if topo != "stripe" {
		args = append(args, topo)
	}
	args = append(args, devices...)

	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "zpool", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("zpool create: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	return nil
}

// CreateVolume creates a new ZFS volume (zvol).
func CreateVolume(ctx context.Context, pool, name string, sizeBytes int64, sparse bool) (Info, error) {
	if err := ValidPoolName(pool); err != nil {
		return Info{}, fmt.Errorf("invalid pool: %w", err)
	}
	if err := ValidVolumeName(name); err != nil {
		return Info{}, fmt.Errorf("invalid volume name: %w", err)
	}
	if sizeBytes <= 0 {
		return Info{}, errors.New("volume size must be greater than 0")
	}
	if !IsAvailable() {
		return Info{}, errors.New("zfs command not found on host")
	}

	fullName := pool + "/" + name
	sizeStr := fmt.Sprintf("%d", sizeBytes)

	args := []string{"create"}
	if sparse {
		args = append(args, "-s")
	}
	args = append(args, "-V", sizeStr, fullName)

	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "zfs", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Info{}, fmt.Errorf("zfs create: %s (%w)", strings.TrimSpace(string(out)), err)
	}

	// Give udev / devfs a moment to create the /dev/zvol symlink if needed
	time.Sleep(200 * time.Millisecond)

	return Resolve(ctx, fullName)
}

func formatBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
