package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"webkvm/internal/backupstore"
	"webkvm/internal/diskstate"
	"webkvm/internal/mdraid"
	"webkvm/internal/models"
	"webkvm/internal/smart"
	"webkvm/internal/zvol"

	"github.com/go-chi/chi/v5"
)

// containsString reports whether list already holds want.
func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// partitionHasMount reports whether any partition already shows this
// mountpoint, so the parent disk does not duplicate it in its own list.
func partitionHasMount(parts []models.HostPartition, mp string) bool {
	for _, p := range parts {
		if containsString(p.MountPoints, mp) {
			return true
		}
	}
	return false
}

// mountDetail renders the explanation shown next to a disk, but only
// when the kernel view alone would not explain it. A plainly mounted
// disk needs no extra prose — the mountpoint column already says it.
func mountDetail(s diskstate.State) string {
	if s.Status() == diskstate.StatusConfigured {
		return s.Reason()
	}
	return ""
}

// probeDiskSMART retrieves SMART telemetry for physical disks.
func probeDiskSMART(ctx context.Context, devPath, devType string) *models.HostDiskSMART {
	if devType != "disk" {
		return nil
	}
	info, err := smart.Probe(ctx, devPath)
	if err != nil {
		if !smart.IsAvailable() {
			return nil
		}
		return &models.HostDiskSMART{
			Available: false,
			Healthy:   false,
			Status:    "UNKNOWN",
		}
	}

	return &models.HostDiskSMART{
		Available:          info.Available,
		Healthy:            info.Healthy,
		Status:             info.Status,
		TemperatureC:       info.TemperatureC,
		PowerOnHours:       info.PowerOnHours,
		PowerCycles:        info.PowerCycles,
		WearPercentage:     info.WearPercentage,
		DataWrittenBytes:   info.DataWrittenBytes,
		ReallocatedSectors: info.ReallocatedSectors,
		PendingSectors:     info.PendingSectors,
		CriticalWarning:    info.CriticalWarning,
	}
}

// errDiskProbeUnavailable signals that a safety probe (lsblk) could not
// run, so a destructive operation cannot be verified as safe. Handlers
// map this to HTTP 503 (service cannot currently guarantee safety),
// distinct from a 403/409 where the disk is known to be unsafe.
var errDiskProbeUnavailable = errors.New("disk safety probe unavailable")

// diskGuardErr writes the right status for a guard failure: 503 when
// the probe itself failed (fail-closed), 403/409 for a known refusal.
func diskGuardErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errDiskProbeUnavailable) {
		jsonErr(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	jsonErr(w, http.StatusConflict, err.Error())
}

type lsblkOutput struct {
	Blockdevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name        string        `json:"name"`
	Path        string        `json:"path"`
	Size        any           `json:"size"`
	Type        string        `json:"type"`
	FSType      *string       `json:"fstype"`
	MountPoints []string      `json:"mountpoints"`
	Model       *string       `json:"model"`
	Serial      *string       `json:"serial"`
	Rota        any           `json:"rota"`
	Tran        *string       `json:"tran"`
	RO          any           `json:"ro"`
	Children    []lsblkDevice `json:"children,omitempty"`
}

var safeDiskNameRE = regexp.MustCompile(`^[a-zA-Z0-9_\-\.]+$`)

// safeMountOptionRE matches a single fstab mount option such as
// "compress=zstd", "subvol=@data" or "uid=1000". Anything with spaces,
// commas, quotes, newlines or shell metacharacters is rejected so a
// caller can never inject extra options or break out of the Options=
// line in the generated systemd unit.
var safeMountOptionRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_\-\.=/:@\+]*$`)

// safeDiskPathRE accepts whole block devices and their partitions:
//
//	/dev/sda, /dev/sda1
//	/dev/nvme0n1, /dev/nvme0n1p1
//	/dev/vda, /dev/vda1
//	/dev/hda, /dev/loop0, /dev/loop0p1
//	/dev/md0, /dev/md127, /dev/md/data
//
// It deliberately does NOT accept arbitrary /dev nodes (e.g. /dev/mem,
// /dev/console) nor any path containing shell metacharacters, since the
// value is passed to wipefs/mkfs/parted/mount.
var safeDiskPathRE = regexp.MustCompile(`^/dev/(sd[a-z]+[0-9]*|nvme[0-9]+n[0-9]+(p[0-9]+)?|vd[a-z]+[0-9]*|hd[a-z]+[0-9]*|loop[0-9]+(p[0-9]+)?|md[0-9]+(p[0-9]+)?|md/[a-zA-Z0-9_\-]+)$`)

// filesystemSpec describes how to format a device with a given
// filesystem: which mkfs binary to invoke and the flags that make it
// non-interactive (force overwrite, no prompts).
type filesystemSpec struct {
	// ID is the value accepted in InitHostDiskRequest.Filesystem.
	ID string
	// Label is a short human name shown in the UI/CLI.
	Label string
	// Bin is the mkfs binary (looked up on PATH; e.g. "mkfs.ext4").
	Bin string
	// Args are passed before the target device, e.g. ["-F"] for ext4.
	Args []string
}

// supportedFilesystems is the fixed set of filesystems WebKVM can format
// a disk with. Each one is only offered to the frontend/CLI if its mkfs
// binary is actually present on the host (see AvailableFilesystems).
//
// Deliberately excluded, with rationale:
//   - vfat/exfat/ntfs: no POSIX permissions/ownership, unsuitable as a
//     backing store for VM disk images or container rootfs.
//   - ext2/ext3: superseded by ext4 in every respect relevant here.
//   - jfs: unmaintained, no advantage over ext4/xfs for this use case.
//   - nilfs2: niche log-structured FS for embedded flash; its continuous
//     snapshot model isn't leveraged by WebKVM's own snapshot feature.
//   - minix/udf: not general-purpose server filesystems.
var supportedFilesystems = []filesystemSpec{
	{ID: "ext4", Label: "ext4", Bin: "mkfs.ext4", Args: []string{"-F"}},
	{ID: "xfs", Label: "XFS", Bin: "mkfs.xfs", Args: []string{"-f"}},
	{ID: "btrfs", Label: "Btrfs", Bin: "mkfs.btrfs", Args: []string{"-f"}},
	{ID: "f2fs", Label: "F2FS", Bin: "mkfs.f2fs", Args: []string{"-f"}},
}

// allowedMountRoots are the only directory trees a WebKVM-managed
// persistent mount may be created under. Restricting to /mnt and /srv
// keeps a caller from mounting over /etc, /boot, /usr, / or another
// live path (the mount point is written verbatim into a systemd .mount
// unit's Where= and Description=, so it must also be free of the
// newlines/control characters that would break or inject directives).
var allowedMountRoots = []string{"/mnt/", "/srv/"}

// validateMountPoint rejects mount points that are not absolute, not
// under an allowed root, contain a path traversal or control/newline
// characters, or are a bare allowed root. Pure (no I/O) so it is
// unit-testable.
func validateMountPoint(mp string) error {
	if !filepath.IsAbs(mp) {
		return fmt.Errorf("mount point must be an absolute path")
	}
	if strings.ContainsAny(mp, "\n\r\t\x00") {
		return fmt.Errorf("mount point must not contain control characters")
	}
	// filepath.IsAbs alone would accept "/mnt/../etc": reject any ".."
	// component and clean first, then re-check containment.
	cleaned := filepath.Clean(mp)
	if cleaned != mp {
		return fmt.Errorf("mount point must be a clean path (no '.' or '..' components or duplicate slashes)")
	}
	if cleaned == "/" {
		return fmt.Errorf("refusing to use / as a mount point")
	}
	ok := false
	for _, root := range allowedMountRoots {
		if strings.HasPrefix(cleaned+"/", root) && cleaned != strings.TrimSuffix(root, "/") {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Errorf("mount point must live under /mnt/ or /srv/ (got %q)", mp)
	}
	return nil
}

// filesystemByID looks up a filesystemSpec by its ID, returning ok=false
// if the ID isn't in supportedFilesystems.
func filesystemByID(id string) (filesystemSpec, bool) {
	for _, fs := range supportedFilesystems {
		if fs.ID == id {
			return fs, true
		}
	}
	return filesystemSpec{}, false
}

// ListFilesystems reports which filesystems WebKVM can format a disk
// with, and whether each one's mkfs binary is actually installed on
// this host. The frontend uses this to only offer choices that will
// succeed, instead of hardcoding ext4/xfs.
func (h *Handler) ListFilesystems(w http.ResponseWriter, r *http.Request) {
	type fsInfo struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		Available bool   `json:"available"`
	}
	out := make([]fsInfo, 0, len(supportedFilesystems))
	for _, fs := range supportedFilesystems {
		_, err := exec.LookPath(fs.Bin)
		out = append(out, fsInfo{ID: fs.ID, Label: fs.Label, Available: err == nil})
	}
	jsonResp(w, http.StatusOK, out)
}

// ListOrphanMounts reports systemd automount units whose paired .mount
// unit no longer exists.
//
// Such a unit stays enabled and re-arms at every boot, laying an autofs
// over its mountpoint: the directory's real contents become invisible
// and every access fails after a timeout. A folder holding live VM
// disks can look empty, which is indistinguishable from data loss for
// the operator. Nothing else surfaces this — the unit declares no
// device, so it never reaches the mount-intent table either.
func (h *Handler) ListOrphanMounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), diskstate.DefaultTimeout)
	defer cancel()

	orphans := diskstate.New().Orphans(ctx)
	if orphans == nil {
		orphans = []diskstate.Orphan{}
	}
	jsonResp(w, http.StatusOK, orphans)
}

// ListHostDisks returns the physical disks and partitions on the host.
func (h *Handler) ListHostDisks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,SIZE,TYPE,FSTYPE,MOUNTPOINTS,MODEL,SERIAL,ROTA,TRAN,RO")
	out, err := cmd.Output()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to query host disks: "+err.Error())
		return
	}

	var raw lsblkOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to parse disk list: "+err.Error())
		return
	}

	// Intent-aware mount table. Resolved once for the whole listing so
	// a host with a dozen disks does not re-run systemctl per device.
	// A failure here degrades to an empty table: every disk then falls
	// back to the kernel-only view, which is what this endpoint did
	// before, so the listing never breaks because systemd is absent.
	mountTable := diskstate.New().Resolve(ctx)

	disks := make([]models.HostDisk, 0, len(raw.Blockdevices))
	for _, dev := range raw.Blockdevices {
		isDisk := dev.Type == "disk" || dev.Type == "loop" || dev.Type == "mpath" || dev.Type == "md" || dev.Type == "raid" || strings.HasPrefix(dev.Type, "raid") || strings.HasPrefix(dev.Name, "md")
		if !isDisk {
			continue
		}
		// Ignore zram
		if strings.HasPrefix(dev.Name, "zram") {
			continue
		}

		// Filter out garbage loop devices (squashfs, iso9660, readonly, snap/temp mounts)
		if dev.Type == "loop" {
			if dev.FSType != nil && (*dev.FSType == "squashfs" || *dev.FSType == "iso9660") {
				continue
			}
			if parseBool(dev.RO) {
				continue
			}
			isSnapOrTemp := false
			for _, m := range dev.MountPoints {
				if strings.HasPrefix(m, "/snap") || strings.HasPrefix(m, "/var/lib/snapd") || strings.HasPrefix(m, "/tmp") {
					isSnapOrTemp = true
					break
				}
			}
			if isSnapOrTemp {
				continue
			}
		}

		sizeBytes := parseSize(dev.Size)
		isSystem := false

		// Check top-level mountpoints
		for _, m := range dev.MountPoints {
			if m == "/" || m == "/boot" || strings.HasPrefix(m, "/boot/") || m == "/etc" {
				isSystem = true
			}
		}

		var children []models.HostPartition
		partPaths := make([]string, 0, len(dev.Children))
		for _, child := range dev.Children {
			childSize := parseSize(child.Size)
			var childFSType string
			if child.FSType != nil {
				childFSType = *child.FSType
			}
			if child.Path != "" {
				partPaths = append(partPaths, child.Path)
			}
			for _, m := range child.MountPoints {
				if m == "/" || m == "/boot" || strings.HasPrefix(m, "/boot/") || m == "/etc" {
					isSystem = true
				}
			}
			children = append(children, models.HostPartition{
				Name:        child.Name,
				Path:        child.Path,
				Size:        childSize,
				SizeHuman:   formatBytesHuman(childSize),
				FSType:      childFSType,
				MountPoints: child.MountPoints,
			})
		}

		var fsType, model, serial, tran string
		if dev.FSType != nil {
			fsType = *dev.FSType
		}
		if dev.Model != nil {
			model = strings.TrimSpace(*dev.Model)
		}
		if dev.Serial != nil {
			serial = strings.TrimSpace(*dev.Serial)
		}
		if dev.Tran != nil {
			tran = *dev.Tran
		}

		// Declared mount intent for the disk and all of its partitions.
		// This is what catches the armed-but-unmounted automount that
		// lsblk alone reports as an empty mountpoint list.
		ms := mountTable.ForDisk(dev.Path, partPaths)
		if ms.HasSystemMount() {
			isSystem = true
		}
		// Surface the paths systemd/fstab would mount even though the
		// kernel shows none, otherwise the UI still renders "no mounts"
		// next to an "in use" badge.
		mountPoints := dev.MountPoints
		for _, mp := range ms.MountPoints {
			if mp != "" && !containsString(mountPoints, mp) && !partitionHasMount(children, mp) {
				mountPoints = append(mountPoints, mp)
			}
		}

		disks = append(disks, models.HostDisk{
			Name:         dev.Name,
			Path:         dev.Path,
			Size:         sizeBytes,
			SizeHuman:    formatBytesHuman(sizeBytes),
			Type:         dev.Type,
			FSType:       fsType,
			MountPoints:  mountPoints,
			Model:        model,
			Serial:       serial,
			Rotational:   parseBool(dev.Rota),
			Transport:    tran,
			IsSystem:     isSystem,
			Children:     children,
			MountState:   string(ms.Status()),
			MountSources: ms.SourceStrings(),
			MountUnits:   ms.Units,
			MountPools:   ms.Pools,
			MountDetail:  mountDetail(ms),
		})
	}

	// Also surface active Linux MD RAID arrays as top-level storage devices
	if mdArrays, err := mdraid.List(); err == nil {
		for _, arr := range mdArrays {
			alreadyPresent := false
			for _, d := range disks {
				if d.Path == arr.Device {
					alreadyPresent = true
					break
				}
			}
			if alreadyPresent {
				continue
			}

			ms := mountTable.ForDisk(arr.Device, nil)
			mountPoints := []string{}
			var fsType string
			if out, err := exec.CommandContext(ctx, "blkid", "-s", "TYPE", "-o", "value", arr.Device).Output(); err == nil {
				fsType = strings.TrimSpace(string(out))
			}
			for _, mp := range ms.MountPoints {
				if mp != "" && !containsString(mountPoints, mp) {
					mountPoints = append(mountPoints, mp)
				}
			}

			disks = append(disks, models.HostDisk{
				Name:         arr.Name,
				Path:         arr.Device,
				Size:         arr.Size,
				SizeHuman:    arr.SizeHuman,
				Type:         "raid",
				FSType:       fsType,
				MountPoints:  mountPoints,
				Model:        fmt.Sprintf("Linux Software RAID (%s)", arr.Level),
				Serial:       fmt.Sprintf("md-%s", arr.State),
				Rotational:   false,
				Transport:    "md",
				IsSystem:     ms.HasSystemMount(),
				Children:     nil,
				MountState:   string(ms.Status()),
				MountSources: ms.SourceStrings(),
				MountUnits:   ms.Units,
				MountPools:   ms.Pools,
				MountDetail:  mountDetail(ms),
			})
		}
	}

	// Probe SMART telemetry concurrently across all disks to minimize response latency
	var smartWG sync.WaitGroup
	for i := range disks {
		if disks[i].Type == "disk" {
			smartWG.Add(1)
			go func(idx int) {
				defer smartWG.Done()
				disks[idx].SMART = probeDiskSMART(ctx, disks[idx].Path, disks[idx].Type)
			}(i)
		}
	}
	smartWG.Wait()

	jsonResp(w, http.StatusOK, disks)
}

// WipeHostDisk clears all partition signatures and filesystem headers
// from a non-system disk.
func (h *Handler) WipeHostDisk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DiskPath string `json:"disk_path"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.DiskPath = strings.TrimSpace(req.DiskPath)
	if !safeDiskPathRE.MatchString(req.DiskPath) {
		jsonErr(w, http.StatusBadRequest, "invalid or unsafe disk path: "+req.DiskPath)
		return
	}

	if err := h.assertNotSystemDisk(r.Context(), req.DiskPath); err != nil {
		diskGuardErr(w, err)
		return
	}
	// A disk that's mounted anywhere is live data, not just "not the
	// system disk". Refuse to wipe it out from under whatever pool or
	// manual mount is using it — see the doc comment on
	// assertDiskNotMounted for the incident that motivated this check.
	if err := h.assertDiskNotMounted(r.Context(), req.DiskPath); err != nil {
		diskGuardErr(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// 1. wipefs
	if out, err := exec.CommandContext(ctx, "wipefs", "-a", "-f", req.DiskPath).CombinedOutput(); err != nil {
		jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("wipefs failed: %v (%s)", err, strings.TrimSpace(string(out))))
		return
	}

	// 2. sgdisk --zap-all
	if _, err := exec.LookPath("sgdisk"); err == nil {
		_ = exec.CommandContext(ctx, "sgdisk", "--zap-all", req.DiskPath).Run()
	}

	_ = exec.CommandContext(ctx, "partprobe", req.DiskPath).Run()

	h.audit.Log(auditFor(r, "host.disk_wipe", req.DiskPath, nil))
	jsonResp(w, http.StatusOK, map[string]string{"status": "wiped", "disk": req.DiskPath})
}

// InitHostDiskDirectory formats a disk with ext4/xfs, mounts it persistently at
// /mnt/{ID_NOMBRE} via a systemd mount unit, and optionally creates a Storage Pool.
func (h *Handler) InitHostDiskDirectory(w http.ResponseWriter, r *http.Request) {
	var req models.InitHostDiskRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.DiskPath = strings.TrimSpace(req.DiskPath)
	req.VolumeName = strings.TrimSpace(req.VolumeName)
	req.MountPoint = strings.TrimSpace(req.MountPoint)
	req.Filesystem = strings.ToLower(strings.TrimSpace(req.Filesystem))

	if !safeDiskPathRE.MatchString(req.DiskPath) {
		jsonErr(w, http.StatusBadRequest, "invalid or unsafe disk path: "+req.DiskPath)
		return
	}
	if !safeDiskNameRE.MatchString(req.VolumeName) {
		jsonErr(w, http.StatusBadRequest, "invalid volume name (use letters, numbers, '-', '_')")
		return
	}
	if req.MountPoint == "" {
		req.MountPoint = "/mnt/" + req.VolumeName
	}
	if err := validateMountPoint(req.MountPoint); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.assertNotSystemDisk(r.Context(), req.DiskPath); err != nil {
		diskGuardErr(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	out, isGuard, err := h.executeInitHostDisk(ctx, req)
	if err != nil {
		if isGuard {
			diskGuardErr(w, err)
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(auditFor(r, "host.disk_init_directory", req.DiskPath, map[string]any{
		"mode":            req.Mode,
		"mount_point":     out.MountPoint,
		"device":          out.TargetDevice,
		"filesystem":      out.Filesystem,
		"volume_name":     req.VolumeName,
		"mount_options":   out.MountOptions,
		"mounted":         out.Mounted,
		"mount_deferred":  out.MountDeferred,
		"create_pool":     req.CreatePool,
		"pools":           out.Pools,
		"subfolders":      out.Subfolders,
		"register_backup": req.RegisterBackupTarget,
	}))

	jsonResp(w, http.StatusOK, map[string]any{
		"status":         "initialized",
		"mode":           req.Mode,
		"disk":           req.DiskPath,
		"device":         out.TargetDevice,
		"mount_point":    out.MountPoint,
		"filesystem":     out.Filesystem,
		"mount_options":  out.MountOptions,
		"mounted":        out.Mounted,
		"mount_deferred": out.MountDeferred,
		"pools":          out.Pools,
		"subfolders":     out.Subfolders,
	})
}

type initHostDiskOutput struct {
	TargetDevice  string       `json:"device"`
	MountPoint    string       `json:"mount_point"`
	Filesystem    string       `json:"filesystem"`
	MountOptions  string       `json:"mount_options"`
	Mounted       bool         `json:"mounted"`
	MountDeferred bool         `json:"mount_deferred"`
	Pools         []PoolResult `json:"pools"`
	Subfolders    []string     `json:"subfolders"`
}

func (h *Handler) executeInitHostDisk(ctx context.Context, req models.InitHostDiskRequest) (*initHostDiskOutput, bool, error) {
	req.Mode = strings.ToLower(strings.TrimSpace(req.Mode))
	if req.Mode != "format" && req.Mode != "mount" {
		req.Mode = "format"
	}
	if _, ok := filesystemByID(req.Filesystem); !ok {
		req.Filesystem = ""
	}

	var targetDevice string

	if req.Mode == "mount" {
		dev, fstype, err := h.findExistingFilesystem(ctx, req.DiskPath, req.Device)
		if err != nil {
			return nil, false, err
		}
		if err := h.assertDeviceNotMountedElsewhere(ctx, dev, req.MountPoint); err != nil {
			return nil, true, err
		}
		targetDevice = dev
		req.Filesystem = fstype
	} else {
		if err := h.assertDiskNotMounted(ctx, req.DiskPath); err != nil {
			return nil, true, err
		}
		if out, err := exec.CommandContext(ctx, "wipefs", "-a", "-f", req.DiskPath).CombinedOutput(); err != nil {
			return nil, false, fmt.Errorf("wipefs failed: %v (%s)", err, strings.TrimSpace(string(out)))
		}

		isMD := strings.HasPrefix(req.DiskPath, "/dev/md")
		if isMD {
			targetDevice = filepath.Clean(req.DiskPath)
		} else {
			cleanDisk := filepath.Clean(req.DiskPath)
			partitionPath := cleanDisk + "1"
			if strings.Contains(cleanDisk, "nvme") || strings.Contains(cleanDisk, "loop") {
				partitionPath = cleanDisk + "p1"
			}
			cleanPartition := filepath.Clean(partitionPath)
			if !safeDiskPathRE.MatchString(cleanPartition) {
				return nil, false, fmt.Errorf("invalid partition path: %s", cleanPartition)
			}

			if _, err := exec.LookPath("sgdisk"); err == nil {
				cmd := exec.CommandContext(ctx, "sgdisk", "--zap-all", cleanDisk)
				_ = cmd.Run()
				cmdPart := exec.CommandContext(ctx, "sgdisk", "-n", "1:0:0", "-t", "1:8300", cleanDisk)
				if out, err := cmdPart.CombinedOutput(); err != nil {
					return nil, false, fmt.Errorf("partition disk with sgdisk: %v (%s)", err, strings.TrimSpace(string(out)))
				}
			} else {
				cmdPart := exec.CommandContext(ctx, "parted", "-s", cleanDisk, "mklabel", "gpt", "mkpart", "primary", "ext4", "1MiB", "100%")
				if out, err := cmdPart.CombinedOutput(); err != nil {
					return nil, false, fmt.Errorf("partition disk with parted: %v (%s)", err, strings.TrimSpace(string(out)))
				}
			}

			_ = exec.CommandContext(ctx, "partprobe", cleanDisk).Run()
			select {
			case <-ctx.Done():
				return nil, false, errors.New("operation cancelled")
			case <-time.After(500 * time.Millisecond):
			}

			targetDevice = cleanPartition
			if _, err := os.Stat(cleanPartition); err != nil {
				targetDevice = cleanDisk
			}
		}

		if req.Filesystem == "" {
			req.Filesystem = "ext4"
		}

		fs, ok := filesystemByID(req.Filesystem)
		if !ok {
			return nil, false, fmt.Errorf("unsupported filesystem: %s", req.Filesystem)
		}
		if _, err := exec.LookPath(fs.Bin); err != nil {
			return nil, false, fmt.Errorf("%s is not installed on this host (%s)", fs.Bin, fs.Label)
		}
		mkfsArgs := append(append([]string{}, fs.Args...), targetDevice)
		mkfsCmd := exec.CommandContext(ctx, fs.Bin, mkfsArgs...)
		if out, err := mkfsCmd.CombinedOutput(); err != nil {
			return nil, false, fmt.Errorf("format filesystem %s: %v (%s)", req.Filesystem, err, strings.TrimSpace(string(out)))
		}
	}

	if strings.Contains(req.MountPoint, "..") {
		return nil, false, errors.New("invalid mount point: traversal not allowed")
	}
	if err := os.MkdirAll(req.MountPoint, 0755); err != nil {
		return nil, false, fmt.Errorf("create mount point: %w", err)
	}

	uuidOut, _ := exec.CommandContext(ctx, "blkid", "-s", "UUID", "-o", "value", targetDevice).Output()
	uuidStr := strings.TrimSpace(string(uuidOut))

	deviceToMount := targetDevice
	if uuidStr != "" {
		deviceToMount = "/dev/disk/by-uuid/" + uuidStr
	}
	if req.Filesystem == "" {
		req.Filesystem = "auto"
	}

	mountOpts := "defaults,noatime"
	if req.NoFail == nil || *req.NoFail {
		mountOpts = "defaults,noatime,nofail,x-systemd.device-timeout=10,x-systemd.default-timeout=10"
	}
	if req.Automount != nil && *req.Automount {
		mountOpts += ",x-systemd.automount,x-systemd.idle-timeout=60"
	}
	if req.ReadOnly != nil && *req.ReadOnly {
		mountOpts += ",ro"
	}
	for _, opt := range req.MountOptions {
		opt = strings.TrimSpace(opt)
		if opt == "" || !safeMountOptionRE.MatchString(opt) {
			continue
		}
		mountOpts += "," + opt
	}

	doAutomount := req.Automount == nil || *req.Automount

	unitNameOut, err := exec.CommandContext(ctx, "systemd-escape", "--path", "--suffix=mount", req.MountPoint).Output()
	if err == nil && len(unitNameOut) > 0 {
		unitName := strings.TrimSpace(string(unitNameOut))
		automountName := strings.TrimSuffix(unitName, ".mount") + ".automount"

		unitContent := fmt.Sprintf(`[Unit]
Description=WebKVM Mount for %s (%s)
After=local-fs.target
DefaultDependencies=no
StartLimitIntervalSec=0

[Mount]
What=%s
Where=%s
Type=%s
Options=%s

[Install]
WantedBy=local-fs.target
`, req.VolumeName, req.MountPoint, deviceToMount, req.MountPoint, req.Filesystem, mountOpts)

		unitPath := filepath.Join("/etc/systemd/system", unitName)
		_ = os.WriteFile(unitPath, []byte(unitContent), 0644)

		automountPath := filepath.Join("/etc/systemd/system", automountName)
		if doAutomount {
			automountContent := fmt.Sprintf(`[Unit]
Description=WebKVM Automount for %s (%s)
After=local-fs.target
StartLimitIntervalSec=0

[Automount]
Where=%s
TimeoutIdleSec=0

[Install]
WantedBy=local-fs.target
`, req.VolumeName, req.MountPoint, req.MountPoint)
			_ = os.WriteFile(automountPath, []byte(automountContent), 0644)
		} else {
			_ = os.Remove(automountPath)
		}

		_ = exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()

		if doAutomount {
			_ = exec.CommandContext(ctx, "systemctl", "enable", automountName).Run()
			_, _ = exec.CommandContext(ctx, "systemctl", "start", automountName).CombinedOutput()
			_, _ = exec.CommandContext(ctx, "ls", req.MountPoint).CombinedOutput()
		} else {
			_ = exec.CommandContext(ctx, "systemctl", "enable", unitName).Run()
			_, _ = exec.CommandContext(ctx, "systemctl", "start", unitName).CombinedOutput()
		}
	}

	if !isMountpoint(req.MountPoint) {
		_ = exec.CommandContext(ctx, "mount", targetDevice, req.MountPoint).Run()
	}

	targetDevice = filepath.Clean(targetDevice)
	if !safeDiskPathRE.MatchString(targetDevice) {
		return nil, false, fmt.Errorf("invalid target device path: %s", targetDevice)
	}

	mounted := isMountpoint(req.MountPoint)
	mountDeferred := false
	if !mounted {
		if _, statErr := os.Stat(targetDevice); statErr != nil {
			mountDeferred = true
		}
	}
	if !mounted && !mountDeferred {
		return nil, false, fmt.Errorf("device %s could not be mounted at %s", targetDevice, req.MountPoint)
	}

	createdSubfolders := []string{}
	seen := map[string]bool{}
	for _, raw := range req.Subfolders {
		sub, ok := allowedSubfolder(raw)
		if !ok || seen[sub] {
			continue
		}
		var safeSub string
		switch sub {
		case "discos":
			safeSub = "discos"
		case "contenedores":
			safeSub = "contenedores"
		case "isos":
			safeSub = "isos"
		case "backups":
			safeSub = "backups"
		case "plantillas":
			safeSub = "plantillas"
		default:
			continue
		}
		subPath := filepath.Join(req.MountPoint, safeSub)
		if rel, rerr := filepath.Rel(req.MountPoint, subPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			continue
		}
		if err := os.MkdirAll(subPath, 0755); err == nil {
			createdSubfolders = append(createdSubfolders, safeSub)
			seen[sub] = true
		}
	}

	if req.RegisterBackupTarget && h.backupStore != nil {
		backupPath := filepath.Join(req.MountPoint, "backups")
		if rel, rerr := filepath.Rel(req.MountPoint, backupPath); rerr == nil && !strings.HasPrefix(rel, "..") && rel != ".." {
			if err := os.MkdirAll(backupPath, 0755); err == nil {
				if !seen["backups"] {
					createdSubfolders = append(createdSubfolders, backupPath)
				}
				_, _ = h.backupStore.CreateTargetOpts(
					req.VolumeName+"-backups", backupPath,
					backupstore.TargetLocal, "all", nil, backupstore.TargetOptions{},
				)
			}
		}
	}

	poolResults := []PoolResult{}
	if req.CreatePool {
		poolResults = h.createPoolsForSubfolders(ctx, req.VolumeName, req.MountPoint, req.Subfolders, req.PoolPurpose)
	}

	return &initHostDiskOutput{
		TargetDevice:  targetDevice,
		MountPoint:    req.MountPoint,
		Filesystem:    req.Filesystem,
		MountOptions:  mountOpts,
		Mounted:       mounted,
		MountDeferred: mountDeferred,
		Pools:         poolResults,
		Subfolders:    createdSubfolders,
	}, false, nil
}

// findExistingFilesystem locates a usable filesystem on diskPath (or an
// explicit partition in preferredDevice) without writing to the device.
// It returns the device node and its detected fstype.
func (h *Handler) findExistingFilesystem(ctx context.Context, diskPath, preferredDevice string) (string, string, error) {
	candidates := []string{}
	if preferredDevice != "" {
		if !safeDiskPathRE.MatchString(preferredDevice) {
			return "", "", fmt.Errorf("invalid device path: %s", preferredDevice)
		}
		candidates = append(candidates, preferredDevice)
	}
	candidates = append(candidates, diskPath)

	// Enumerate partitions via lsblk so /dev/sdb1, nvme0n1p1, etc. are covered.
	cmd := exec.CommandContext(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,FSTYPE,TYPE", diskPath)
	if out, err := cmd.Output(); err == nil {
		var raw lsblkOutput
		if json.Unmarshal(out, &raw) == nil {
			for _, dev := range raw.Blockdevices {
				if dev.FSType != nil && *dev.FSType != "" && !strings.HasPrefix(*dev.FSType, "linux_raid") {
					candidates = append(candidates, dev.Path)
				}
				for _, c := range dev.Children {
					if c.FSType != nil && *c.FSType != "" && !strings.HasPrefix(*c.FSType, "linux_raid") {
						candidates = append(candidates, c.Path)
					}
				}
			}
		}
	}

	seen := map[string]bool{}
	for _, dev := range candidates {
		if dev == "" || seen[dev] {
			continue
		}
		seen[dev] = true
		out, err := exec.CommandContext(ctx, "blkid", "-s", "TYPE", "-o", "value", dev).Output()
		if err != nil {
			continue
		}
		fstype := strings.TrimSpace(string(out))
		if fstype == "" {
			continue
		}
		switch fstype {
		case "ext4", "ext3", "ext2", "xfs", "btrfs", "f2fs", "vfat":
			return dev, fstype, nil
		default:
			return dev, fstype, nil
		}
	}
	return "", "", fmt.Errorf("no existing filesystem found on %s; format the disk first or pass an explicit device", diskPath)
}

// mountedPaths returns every non-empty mountpoint currently active for
// diskPath itself and, if diskPath is a whole disk, for each of its
// partitions. Returns (nil, nil) if lsblk can't describe the device —
// callers should not block on that, since the operation that follows
// will fail with a clearer error anyway.
func (h *Handler) mountedPaths(ctx context.Context, diskPath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,MOUNTPOINTS", diskPath)
	out, err := cmd.Output()
	if err != nil {
		// Fail CLOSED: a probe that could not run must not be read as
		// "nothing is mounted". These guards protect against wiping a
		// live disk; returning nil here (the old behaviour) turned a
		// transient lsblk failure into permission to destroy data.
		return nil, fmt.Errorf("%w: lsblk failed, cannot verify disk is unmounted: %v", errDiskProbeUnavailable, err)
	}
	var raw lsblkOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("%w: could not parse lsblk output, cannot verify disk is unmounted: %v", errDiskProbeUnavailable, err)
	}
	var mounts []string
	for _, dev := range raw.Blockdevices {
		mounts = append(mounts, nonEmpty(dev.MountPoints)...)
		for _, c := range dev.Children {
			mounts = append(mounts, nonEmpty(c.MountPoints)...)
		}
	}
	return mounts, nil
}

func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// firstConflictingMount returns the first entry in mounts that isn't
// equal to allowed. Pure function so the guard logic below is unit
// testable without shelling out to lsblk. Passing allowed="" rejects
// any real mountpoint — used by the destructive wipe/format paths,
// which must never run against a device serving live data. Passing
// the requested mount point lets the non-destructive "mount" flow
// re-run idempotently against an already-correctly-mounted device
// while still refusing to silently create a second mountpoint for the
// same filesystem (the root cause of a real incident where /dev/vda
// ended up mounted at two paths and was then wiped while still live).
func firstConflictingMount(mounts []string, allowed string) (string, bool) {
	for _, m := range mounts {
		if m != allowed {
			return m, true
		}
	}
	return "", false
}

// assertDeviceNotMountedElsewhere refuses the operation if device is
// currently mounted anywhere other than allowedMountPoint, OR if
// anything declares that it should be mounted.
//
// The second half matters more than the first. systemd automount units
// — the very thing InitHostDiskDirectory writes — idle in "active
// (waiting)" with the paired .mount unit dead whenever the path has not
// been touched. In that state the kernel reports no mountpoint at all,
// so a check built only on lsblk/proc sees a free disk and green-lights
// a format. systemd then remounts the device on the next access, on top
// of the filesystem that was just destroyed. Declared intent is
// therefore treated exactly like an active mount.
func (h *Handler) assertDeviceNotMountedElsewhere(ctx context.Context, device, allowedMountPoint string) error {
	mounts, err := h.mountedPaths(ctx, device)
	if err != nil {
		return err
	}
	if m, conflict := firstConflictingMount(mounts, allowedMountPoint); conflict {
		if allowedMountPoint == "" {
			return fmt.Errorf("refusing operation: %s is currently mounted at %s; unmount it (or delete the existing storage pool) before wiping/formatting", device, m)
		}
		return fmt.Errorf("refusing to mount %s at %s: it is already mounted at %s — use the existing storage pool instead of creating a duplicate mount", device, allowedMountPoint, m)
	}

	return h.assertNoDeclaredMount(ctx, device, allowedMountPoint)
}

// assertNoDeclaredMount refuses the operation when a systemd unit,
// fstab entry or libvirt pool claims the device even though nothing is
// mounted right now.
func (h *Handler) assertNoDeclaredMount(ctx context.Context, device, allowedMountPoint string) error {
	state := diskstate.New().Resolve(ctx).ForDisk(device, h.partitionPaths(ctx, device))
	if !state.Configured {
		return nil
	}
	// Re-running the mount flow against the device that is already
	// declared for exactly this mount point is idempotent, not a
	// conflict — that is the "mount an existing filesystem" path.
	if allowedMountPoint != "" && onlyMountPoint(state.MountPoints, allowedMountPoint) {
		return nil
	}
	return fmt.Errorf("refusing operation: %s is not mounted right now, but %s — it would be remounted on top of any new filesystem, destroying the data written in between",
		device, state.Reason())
}

// onlyMountPoint reports whether every declared mountpoint equals
// allowed (an empty list counts as a match).
func onlyMountPoint(mounts []string, allowed string) bool {
	for _, m := range mounts {
		if m != allowed {
			return false
		}
	}
	return true
}

// partitionPaths lists the partition device paths of a disk so mount
// intent declared against /dev/sda1 blocks an operation on /dev/sda.
// Returns nil on failure: the caller's own lsblk probe already failed
// closed by that point.
func (h *Handler) partitionPaths(ctx context.Context, diskPath string) []string {
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,TYPE", diskPath).Output()
	if err != nil {
		return nil
	}
	var raw lsblkOutput
	if json.Unmarshal(out, &raw) != nil {
		return nil
	}
	var paths []string
	for _, dev := range raw.Blockdevices {
		for _, c := range dev.Children {
			if c.Path != "" {
				paths = append(paths, c.Path)
			}
		}
	}
	return paths
}

// assertDiskNotMounted refuses destructive operations (wipe, format)
// against diskPath if it, or any of its partitions, is mounted
// anywhere at all.
func (h *Handler) assertDiskNotMounted(ctx context.Context, diskPath string) error {
	return h.assertDeviceNotMountedElsewhere(ctx, diskPath, "")
}

func (h *Handler) assertNotSystemDisk(ctx context.Context, diskPath string) error {
	cmd := exec.CommandContext(ctx, "lsblk", "-J", "-b", "-o", "NAME,PATH,MOUNTPOINTS", diskPath)
	out, err := cmd.Output()
	if err != nil {
		// Fail CLOSED: without lsblk we cannot prove this is not the OS
		// disk, so refuse rather than risk wiping the running system.
		return fmt.Errorf("%w: lsblk failed, cannot verify disk is not the system disk: %v", errDiskProbeUnavailable, err)
	}
	var raw lsblkOutput
	if err := json.Unmarshal(out, &raw); err != nil {
		return fmt.Errorf("%w: could not parse lsblk output, cannot verify disk is not the system disk: %v", errDiskProbeUnavailable, err)
	}
	for _, dev := range raw.Blockdevices {
		for _, m := range dev.MountPoints {
			if m == "/" || m == "/boot" || strings.HasPrefix(m, "/boot/") || m == "/etc" {
				return fmt.Errorf("refusing operation: %s is a system root/boot disk mounted at %s", diskPath, m)
			}
		}
		for _, c := range dev.Children {
			for _, m := range c.MountPoints {
				if m == "/" || m == "/boot" || strings.HasPrefix(m, "/boot/") || m == "/etc" {
					return fmt.Errorf("refusing operation: %s contains system partition %s mounted at %s", diskPath, c.Path, m)
				}
			}
		}
	}
	return nil
}

func parseSize(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		i, _ := strconv.ParseInt(n, 10, 64)
		return i
	default:
		return 0
	}
}

func parseBool(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "1" || strings.EqualFold(b, "true")
	case float64:
		return b == 1
	default:
		return false
	}
}

func formatBytesHuman(b int64) string {
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
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// ListHostZVols returns all ZFS volumes discovered on the host and cross-references
// which VM currently attaches each one. Restricted to administrators.
func (h *Handler) ListHostZVols(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	zvols, err := zvol.List(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to list ZFS volumes: "+err.Error())
		return
	}

	out := make([]models.HostZVol, 0, len(zvols))
	for _, z := range zvols {
		hz := models.HostZVol{
			Name:    z.Name,
			Pool:    z.Pool,
			VolSize: z.VolSize,
			Used:    z.Used,
			Device:  z.Device,
		}
		if h.compute != nil {
			if atts, aerr := h.compute.FindZVolAttachments(z.Name); aerr == nil && len(atts) > 0 {
				hz.UsedBy = &atts[0]
			}
		}
		out = append(out, hz)
	}

	jsonResp(w, http.StatusOK, out)
}

// ListHostZPools returns all ZFS storage pools discovered on the host.
// Restricted to administrators.
func (h *Handler) ListHostZPools(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	pools, err := zvol.ListPools(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to list ZFS pools: "+err.Error())
		return
	}

	out := make([]models.HostZPool, 0, len(pools))
	for _, p := range pools {
		out = append(out, models.HostZPool{
			Name:       p.Name,
			Size:       p.Size,
			SizeHuman:  p.SizeHuman,
			Allocated:  p.Allocated,
			AllocHuman: p.AllocHuman,
			Free:       p.Free,
			FreeHuman:  p.FreeHuman,
			Health:     p.Health,
			Devices:    p.Devices,
		})
	}

	jsonResp(w, http.StatusOK, out)
}

// CreateHostZPool creates a new ZFS pool on the specified host block devices.
// Restricted to administrators.
func (h *Handler) CreateHostZPool(w http.ResponseWriter, r *http.Request) {
	var req models.CreateZPoolRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if err := zvol.ValidPoolName(req.Name); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if len(req.Devices) == 0 {
		jsonErr(w, http.StatusBadRequest, "at least one device is required")
		return
	}

	for _, dev := range req.Devices {
		dev = strings.TrimSpace(dev)
		if !safeDiskPathRE.MatchString(dev) {
			jsonErr(w, http.StatusBadRequest, "invalid or unsafe disk path: "+dev)
			return
		}
		if err := h.assertNotSystemDisk(r.Context(), dev); err != nil {
			diskGuardErr(w, err)
			return
		}
		if err := h.assertDiskNotMounted(r.Context(), dev); err != nil {
			diskGuardErr(w, err)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	// Wipe existing signatures on target devices
	for _, dev := range req.Devices {
		_ = exec.CommandContext(ctx, "wipefs", "-a", "-f", dev).Run()
	}

	if err := zvol.CreatePool(ctx, req.Name, req.Topology, req.Devices); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(auditFor(r, "host.zpool_create", req.Name, map[string]any{
		"topology": req.Topology,
		"devices":  req.Devices,
	}))

	jsonResp(w, http.StatusCreated, map[string]any{
		"status":   "created",
		"name":     req.Name,
		"topology": req.Topology,
		"devices":  req.Devices,
	})
}

// CreateHostZVol creates a new ZFS volume (zvol) within an existing pool.
// Restricted to administrators.
func (h *Handler) CreateHostZVol(w http.ResponseWriter, r *http.Request) {
	var req models.CreateZVolRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Pool = strings.TrimSpace(req.Pool)
	req.Name = strings.TrimSpace(req.Name)

	if err := zvol.ValidPoolName(req.Pool); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid pool: "+err.Error())
		return
	}
	if err := zvol.ValidVolumeName(req.Name); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid volume name: "+err.Error())
		return
	}
	if req.SizeGB <= 0 {
		jsonErr(w, http.StatusBadRequest, "size_gb must be greater than 0")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()

	sizeBytes := req.SizeGB * 1024 * 1024 * 1024
	info, err := zvol.CreateVolume(ctx, req.Pool, req.Name, sizeBytes, req.Sparse)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(auditFor(r, "host.zvol_create", info.Name, map[string]any{
		"pool":    req.Pool,
		"name":    req.Name,
		"size_gb": req.SizeGB,
		"sparse":  req.Sparse,
		"device":  info.Device,
	}))

	jsonResp(w, http.StatusCreated, models.HostZVol{
		Name:    info.Name,
		Pool:    info.Pool,
		VolSize: info.VolSize,
		Used:    info.Used,
		Device:  info.Device,
	})
}

// CreateHostRAID initializes a software RAID array (mdadm) across specified devices.
// Restricted to administrators.
func (h *Handler) CreateHostRAID(w http.ResponseWriter, r *http.Request) {
	var req models.CreateRAIDRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Devices) == 0 {
		jsonErr(w, http.StatusBadRequest, "at least two devices are required for RAID")
		return
	}

	for _, dev := range req.Devices {
		dev = strings.TrimSpace(dev)
		if !safeDiskPathRE.MatchString(dev) {
			jsonErr(w, http.StatusBadRequest, "invalid or unsafe disk path: "+dev)
			return
		}
		if err := h.assertNotSystemDisk(r.Context(), dev); err != nil {
			diskGuardErr(w, err)
			return
		}
		if err := h.assertDiskNotMounted(r.Context(), dev); err != nil {
			diskGuardErr(w, err)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	// Wipe existing signatures
	for _, dev := range req.Devices {
		_ = exec.CommandContext(ctx, "wipefs", "-a", "-f", dev).Run()
	}

	mdDev, err := mdraid.Create(ctx, req.Name, req.Level, req.Devices)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(auditFor(r, "host.raid_create", mdDev, map[string]any{
		"level":   req.Level,
		"devices": req.Devices,
	}))

	resp := map[string]any{
		"status":  "created",
		"device":  mdDev,
		"level":   req.Level,
		"devices": req.Devices,
	}

	if req.Format || req.Filesystem != "" {
		volName := req.VolumeName
		if volName == "" {
			cleanName := strings.ReplaceAll(strings.TrimPrefix(mdDev, "/dev/"), "-", "")
			volName = "raid-" + cleanName
		}
		mountPt := req.MountPoint
		if mountPt == "" {
			mountPt = "/mnt/" + volName
		}
		fsType := req.Filesystem
		if fsType == "" {
			fsType = "ext4"
		}
		initReq := models.InitHostDiskRequest{
			DiskPath:    mdDev,
			VolumeName:  volName,
			MountPoint:  mountPt,
			Filesystem:  fsType,
			CreatePool:  req.CreatePool,
			Subfolders:  req.Subfolders,
			PoolPurpose: req.PoolPurpose,
			Mode:        "format",
		}
		initOut, _, err := h.executeInitHostDisk(ctx, initReq)
		if err != nil {
			resp["format_error"] = err.Error()
		} else {
			resp["formatted"] = true
			resp["mount_point"] = initOut.MountPoint
			resp["filesystem"] = initOut.Filesystem
			resp["pools"] = initOut.Pools
			resp["subfolders"] = initOut.Subfolders
			h.audit.Log(auditFor(r, "host.raid_format", mdDev, map[string]any{
				"mount_point": initOut.MountPoint,
				"filesystem":  initOut.Filesystem,
				"pools":       initOut.Pools,
			}))
		}
	}

	jsonResp(w, http.StatusCreated, resp)
}

// ScrubHostZPool triggers a scrub (start or stop) on a ZFS pool.
// Restricted to administrators.
func (h *Handler) ScrubHostZPool(w http.ResponseWriter, r *http.Request) {
	poolName := chi.URLParam(r, "name")
	if err := zvol.ValidPoolName(poolName); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid pool name: "+err.Error())
		return
	}

	var req struct {
		Action string `json:"action"` // "start" or "stop"
	}
	if err := decodeBody(r, &req); err != nil {
		// Default to "start" if body is empty or not provided
		req.Action = "start"
	}

	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	if req.Action == "" {
		req.Action = "start"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	if err := zvol.ScrubPool(ctx, poolName, req.Action); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(auditFor(r, "host.zpool_scrub", poolName, map[string]any{
		"action": req.Action,
	}))

	jsonResp(w, http.StatusOK, map[string]any{
		"status": "ok",
		"pool":   poolName,
		"action": req.Action,
	})
}

// SyncHostRAID triggers or cancels a check/repair on a Linux MD RAID array.
// Restricted to administrators.
func (h *Handler) SyncHostRAID(w http.ResponseWriter, r *http.Request) {
	device := chi.URLParam(r, "device")
	device = strings.TrimPrefix(device, "/")
	device = strings.TrimPrefix(device, "dev/")
	device = "/dev/" + device
	var req struct {
		Action string `json:"action"` // "check", "repair", "idle"
	}
	if err := decodeBody(r, &req); err != nil {
		req.Action = "check"
	}

	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	if req.Action == "" {
		req.Action = "check"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	if err := mdraid.SyncAction(ctx, device, req.Action); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(auditFor(r, "host.raid_sync", device, map[string]any{
		"action": req.Action,
	}))

	jsonResp(w, http.StatusOK, map[string]any{
		"status": "ok",
		"device": device,
		"action": req.Action,
	})
}

// ListHostRAID returns all active software RAID arrays discovered directly via kernel sysfs.
// Restricted to administrators.
func (h *Handler) ListHostRAID(w http.ResponseWriter, r *http.Request) {
	arrays, err := mdraid.List()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "failed to query software RAID: "+err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	mountTable := diskstate.New().Resolve(ctx)

	for i := range arrays {
		dev := arrays[i].Device
		ms := mountTable.ForDisk(dev, nil)
		arrays[i].MountPoints = ms.MountPoints
		arrays[i].Mounted = len(ms.MountPoints) > 0 || ms.Status() == diskstate.StatusActive

		if out, err := exec.CommandContext(ctx, "blkid", "-s", "TYPE", "-o", "value", dev).Output(); err == nil {
			arrays[i].FSType = strings.TrimSpace(string(out))
		}
	}

	jsonResp(w, http.StatusOK, arrays)
}

// StopHostRAID stops and unregisters an MD RAID array.
// Restricted to administrators.
func (h *Handler) StopHostRAID(w http.ResponseWriter, r *http.Request) {
	device := chi.URLParam(r, "device")
	device = strings.TrimPrefix(device, "/")
	device = strings.TrimPrefix(device, "dev/")
	device = "/dev/" + device
	if err := h.assertDiskNotMounted(r.Context(), device); err != nil {
		diskGuardErr(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := mdraid.Stop(ctx, device); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(auditFor(r, "host.raid_stop", device, nil))
	jsonResp(w, http.StatusOK, map[string]string{
		"status": "stopped",
		"device": device,
	})
}

// ProbeHostDiskSMART triggers a fresh SMART query for a specific disk.
// Restricted to administrators.
func (h *Handler) ProbeHostDiskSMART(w http.ResponseWriter, r *http.Request) {
	diskPath := r.URL.Query().Get("path")
	if diskPath == "" {
		jsonErr(w, http.StatusBadRequest, "path query parameter is required")
		return
	}

	if strings.HasPrefix(diskPath, "/dev/md") {
		jsonResp(w, http.StatusOK, map[string]any{
			"device":    diskPath,
			"available": false,
			"status":    "NOT_SUPPORTED",
			"message":   "S.M.A.R.T. telemetry is not applicable to software RAID arrays",
		})
		return
	}

	clean, err := smart.ValidateDevice(diskPath)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	// Invalidate cache if refresh requested
	if r.URL.Query().Get("refresh") == "true" {
		smart.InvalidateCache(clean)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	info, err := smart.Probe(ctx, clean)
	if err != nil && !info.Available {
		jsonErr(w, http.StatusInternalServerError, "SMART probe failed: "+err.Error())
		return
	}

	jsonResp(w, http.StatusOK, info)
}

// RunHostDiskSelfTest initiates a SMART self-test on a disk.
// Restricted to administrators.
func (h *Handler) RunHostDiskSelfTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DiskPath string `json:"disk_path"`
		Type     string `json:"type"` // "short", "long", "abort"
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.HasPrefix(req.DiskPath, "/dev/md") {
		jsonErr(w, http.StatusBadRequest, "S.M.A.R.T. self-test is not applicable to software RAID arrays")
		return
	}

	clean, err := smart.ValidateDevice(req.DiskPath)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	if err := smart.RunSelfTest(ctx, clean, req.Type); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.audit.Log(auditFor(r, "host.disk_selftest", clean, map[string]any{
		"type": req.Type,
	}))

	jsonResp(w, http.StatusOK, map[string]any{
		"status": "ok",
		"device": clean,
		"type":   req.Type,
	})
}
