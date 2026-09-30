// Package zvol lets a VM use an existing ZFS volume (zvol) as a raw
// block disk.
//
// WebKVM's libvirt storage pools are file based (dir/netfs): every disk
// it creates is a qcow2/raw file. A zvol is different — it is a block
// device that ZFS exposes at /dev/zvol/<pool>/<dataset>, created and
// managed with the zfs(8) tooling, and attached to the guest as
// <disk type='block'><source dev='/dev/zvol/...'/>.
//
// This package only reads ZFS state; it never creates, resizes or
// destroys a zvol. Lifecycle (zfs create -V, snapshots, send/receive)
// stays with the operator. Callers identify a zvol by its ZFS name
// (tank/vms/web01), never by a raw /dev path, so an API request can only
// ever reach a device that ZFS itself reports as a volume.
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

// DevRoot is where the ZFS udev rules publish zvol device links.
const DevRoot = "/dev/zvol/"

// dataSlack is how many logically written bytes a zvol may hold before
// it is considered to already contain data. A freshly created volume
// reports a few KiB of metadata; anything past 1 MiB is real content
// (mirrors diskprobe's emptyImageAllocationSlack).
const dataSlack = 1 << 20

const cmdTimeout = 15 * time.Second

// ErrNotVolume is returned when the named dataset exists but is not a
// volume (a filesystem or snapshot).
var ErrNotVolume = errors.New("dataset is not a ZFS volume")

// ErrZFSUnavailable is returned when the zfs binary is not installed.
var ErrZFSUnavailable = errors.New("zfs command not found (is ZFS installed?)")

// Component rules follow zfs(8): alphanumerics plus _ - : . and the
// first component (the pool) must start with a letter. At least two
// components are required — a pool root is never a volume. '@' (snapshot)
// and '#' (bookmark) are rejected outright, as is any '.'/'..' component
// that could walk out of /dev/zvol once joined into a path.
var (
	poolRE      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.:-]*$`)
	componentRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
)

// Volume describes one ZFS volume.
type Volume struct {
	Name string `json:"name"` // tank/vms/web01
	Dev  string `json:"dev"`  // /dev/zvol/tank/vms/web01
	// SizeBytes is the volume's volsize (the guest-visible disk size).
	SizeBytes int64 `json:"size_bytes"`
	// WrittenBytes is logicalreferenced: data actually written to it.
	WrittenBytes int64 `json:"written_bytes"`
	// HasData is true once the volume holds more than metadata.
	HasData bool `json:"has_data"`
}

// ValidateName checks that name is a syntactically valid ZFS volume name
// safe to pass to zfs(8) and to join under DevRoot.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("zvol name is required")
	}
	if len(name) > 255 {
		return fmt.Errorf("zvol name too long")
	}
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return fmt.Errorf("invalid zvol name %q: expected <pool>/<volume>", name)
	}
	if !poolRE.MatchString(parts[0]) {
		return fmt.Errorf("invalid zvol name %q: bad pool name", name)
	}
	for _, p := range parts[1:] {
		if p == "." || p == ".." || !componentRE.MatchString(p) {
			return fmt.Errorf("invalid zvol name %q", name)
		}
	}
	return nil
}

// DevPath returns the /dev/zvol link for a (validated) volume name.
func DevPath(name string) string {
	return DevRoot + name
}

// NameFromDev maps a /dev/zvol/... path back to its ZFS name. ok is
// false for any other path.
func NameFromDev(dev string) (string, bool) {
	if !strings.HasPrefix(dev, DevRoot) {
		return "", false
	}
	name := strings.TrimPrefix(dev, DevRoot)
	if ValidateName(name) != nil {
		return "", false
	}
	return name, true
}

// runZFS executes zfs(8). A package variable so tests can stub it.
var runZFS = func(ctx context.Context, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("zfs"); err != nil {
		return nil, ErrZFSUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, cmdTimeout)
	defer cancel()
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "zfs", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("zfs %s: %s", args[0], msg)
		}
		return nil, fmt.Errorf("zfs %s: %w", args[0], err)
	}
	return out, nil
}

// statDev reports whether path resolves to a block device. A package
// variable so tests can stub it.
var statDev = func(path string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("zvol device %s not found (udev link missing?): %w", path, err)
	}
	if !strings.HasPrefix(real, "/dev/") {
		return fmt.Errorf("zvol device %s resolves outside /dev", path)
	}
	fi, err := os.Stat(real)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeDevice == 0 || fi.Mode()&os.ModeCharDevice != 0 {
		return fmt.Errorf("%s is not a block device", real)
	}
	return nil
}

// Get resolves one volume by name: it must exist, be of type volume, and
// have its block device present.
func Get(ctx context.Context, name string) (Volume, error) {
	if err := ValidateName(name); err != nil {
		return Volume{}, err
	}
	out, err := runZFS(ctx, "get", "-H", "-p", "-o", "property,value",
		"type,volsize,logicalreferenced", name)
	if err != nil {
		return Volume{}, err
	}
	props := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.SplitN(sc.Text(), "\t", 2)
		if len(f) == 2 {
			props[f[0]] = strings.TrimSpace(f[1])
		}
	}
	if props["type"] != "volume" {
		return Volume{}, fmt.Errorf("%s: %w", name, ErrNotVolume)
	}
	v := volumeFrom(name, props["volsize"], props["logicalreferenced"])
	if err := statDev(v.Dev); err != nil {
		return Volume{}, err
	}
	return v, nil
}

// List returns every ZFS volume on the host. An empty list (no error) is
// returned when zfs is not installed, since that simply means there is
// nothing to offer.
func List(ctx context.Context) ([]Volume, error) {
	out, err := runZFS(ctx, "list", "-H", "-p", "-t", "volume",
		"-o", "name,volsize,logicalreferenced")
	if errors.Is(err, ErrZFSUnavailable) {
		return []Volume{}, nil
	}
	if err != nil {
		return nil, err
	}
	vols := []Volume{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) != 3 || ValidateName(f[0]) != nil {
			continue
		}
		vols = append(vols, volumeFrom(f[0], f[1], f[2]))
	}
	return vols, nil
}

func volumeFrom(name, volsize, written string) Volume {
	size, _ := strconv.ParseInt(volsize, 10, 64)
	used, _ := strconv.ParseInt(written, 10, 64)
	return Volume{
		Name:         name,
		Dev:          DevPath(name),
		SizeBytes:    size,
		WrittenBytes: used,
		HasData:      used > dataSlack,
	}
}
