// Package diskprobe inspects a VM disk image to answer one question
// before it is attached to (or formatted for) a VM: does this disk
// already contain data or an operating system?
//
// The motivating problem: WebKVM let an operator attach any volume from
// a pool as an "existing disk" with no idea whether it was empty, a
// previously-installed guest OS, or another VM's data. Attaching a disk
// that already holds an OS silently produces a VM that will not boot, or
// worse, a disk that two VMs write to.
//
// Two levels of inspection are offered:
//
//   - Basic (always available): `qemu-img info` reports the real image
//     format and, crucially, how many bytes are actually allocated. A
//     freshly created qcow2 has allocation 0; anything with data does
//     not. This is fast and dependency-free.
//   - Deep (only when libguestfs is installed): `virt-inspector` boots a
//     minimal appliance to read the guest's partitions and filesystems,
//     identifying the installed OS. This is slow (seconds) and heavy, so
//     it only runs when explicitly requested.
//
// Every probe is read-only and timeout-bounded. It never panics and
// never returns an error that would block an attach by itself — callers
// decide the policy; this package only reports facts.
package diskprobe

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Result is the outcome of probing a disk image.
type Result struct {
	// Path is the probed image path.
	Path string `json:"path"`
	// Format is the real qemu image format (qcow2, raw, ...).
	Format string `json:"format"`
	// VirtualSize is the advertised disk size in bytes.
	VirtualSize int64 `json:"virtual_size"`
	// Allocated is the number of bytes actually written to the image
	// file. Zero means the image is empty (no data ever written); a
	// non-zero value means the disk contains data.
	Allocated int64 `json:"allocated"`
	// HasData is true when the image appears to already contain data
	// (allocated bytes beyond an empty image's metadata). This is the
	// flag the attach guard and the UI key off.
	HasData bool `json:"has_data"`
	// BackingFile is set when the image is a qcow2 overlay backed by
	// another file (a snapshot/linked clone), which by itself implies
	// the disk is not a blank, independent disk.
	BackingFile string `json:"backing_file,omitempty"`
	// Deep is true when the libguestfs inspection also ran successfully.
	Deep bool `json:"deep"`
	// OS is the detected operating system name (deep probe only).
	OS string `json:"os,omitempty"`
	// Distro is the detected distribution (deep probe only).
	Distro string `json:"distro,omitempty"`
	// Filesystems lists detected mounted filesystems/partitions (deep
	// probe only), e.g. ["/dev/sda1 (ext4)"].
	Filesystems []string `json:"filesystems,omitempty"`
	// Warning carries a non-fatal note (e.g. deep probe unavailable).
	Warning string `json:"warning,omitempty"`
}

// emptyImageAllocationSlack is how many bytes of allocated data are
// tolerated before an image is considered "has data". A freshly created
// qcow2 allocates 0; some operations (preallocation=metadata) allocate a
// small header-like amount without user data. Keep this conservative:
// better to flag a near-empty disk than to miss real data.
const emptyImageAllocationSlack = 1 << 20 // 1 MiB

type qemuImgInfo struct {
	Format      string `json:"format"`
	VirtualSize int64  `json:"virtual-size"`
	ActualSize  int64  `json:"actual-size"`
	BackingFile string `json:"backing-filename"`
}

// Basic inspects the image with qemu-img only. Never runs a guest:
// fast, always safe. Returns an error only if qemu-img itself fails to
// read the image (e.g. the path does not exist or is not an image).
func Basic(ctx context.Context, path string) (Result, error) {
	res := Result{Path: path}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	out, err := exec.CommandContext(cctx, "qemu-img", "info", "--output=json", path).Output()
	if err != nil {
		return res, fmt.Errorf("qemu-img info %s: %w", path, err)
	}
	var info qemuImgInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return res, fmt.Errorf("parse qemu-img output: %w", err)
	}
	res.Format = info.Format
	res.VirtualSize = info.VirtualSize
	res.Allocated = info.ActualSize
	res.BackingFile = info.BackingFile
	// A backing file means this is an overlay of another image: never a
	// blank disk. An actual-size beyond the empty slack means data was
	// written. Either way the disk is not empty.
	res.HasData = info.ActualSize > emptyImageAllocationSlack || info.BackingFile != ""
	return res, nil
}

// Deep runs the basic probe and, when libguestfs is available, an
// os-info inspection. inspectorPath is the resolved virt-inspector
// binary (empty disables the deep step). A failed deep probe is
// reported in Warning, not as a hard error, so the basic facts are
// still returned.
func Deep(ctx context.Context, path, inspectorPath string) Result {
	res, err := Basic(ctx, path)
	if err != nil {
		return Result{Path: path, Warning: err.Error()}
	}
	if inspectorPath == "" {
		res.Warning = "libguestfs not installed; only basic image inspection available"
		return res
	}

	// virt-inspector boots a minimal appliance to read the guest. Bound
	// it tightly: a hung or huge image must not hang the request.
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	out, err := exec.CommandContext(cctx, inspectorPath, "--no-applications", path).Output()
	if err != nil {
		res.Warning = fmt.Sprintf("deep inspection failed (image may be raw/unpartitioned or guestfs unavailable): %v", err)
		return res
	}
	osName, distro, fs := parseVirtInspector(out)
	res.Deep = true
	res.OS = osName
	res.Distro = distro
	res.Filesystems = fs
	if osName != "" {
		res.HasData = true
	}
	return res
}

// virtInspectorXML is the subset of virt-inspector's XML output we need.
type virtInspectorXML struct {
	Operatingsystems []struct {
		Name      string `xml:"name"`
		Distro    string `xml:"distro"`
		Mountpoints []struct {
			MountPoint string `xml:"mountpoint,attr"`
			Device     string `xml:"dev,attr"`
			Type       string `xml:"type,attr"`
		} `xml:"mountpoints>mountpoint"`
	} `xml:"operatingsystem"`
}

// parseVirtInspector extracts the primary OS name, distro and a list of
// mountpoints from virt-inspector XML. Returns empty strings when the
// output has no recognizable operating system (e.g. a data disk).
func parseVirtInspector(out []byte) (osName, distro string, filesystems []string) {
	var v virtInspectorXML
	if err := xml.Unmarshal(out, &v); err != nil {
		return "", "", nil
	}
	if len(v.Operatingsystems) == 0 {
		return "", "", nil
	}
	primary := v.Operatingsystems[0]
	osName = primary.Name
	distro = primary.Distro
	for _, mp := range primary.Mountpoints {
		if mp.Device == "" {
			continue
		}
		var label strings.Builder
		label.WriteString(mp.Device)
		if mp.Type != "" {
			label.WriteString(" (" + mp.Type + ")")
		}
		if mp.MountPoint != "" {
			label.WriteString(" @ " + mp.MountPoint)
		}
		filesystems = append(filesystems, label.String())
	}
	return osName, distro, filesystems
}

// InspectorAvailable reports whether a usable virt-inspector binary is
// present. Kept here so callers do not need to know about hostcaps.
func InspectorAvailable() (string, bool) {
	p, err := exec.LookPath("virt-inspector")
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}
