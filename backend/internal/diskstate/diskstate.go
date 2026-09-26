// Package diskstate resolves whether a block device is in use by the
// host, using *declared intent* rather than only the kernel's current
// view.
//
// The naive check — "is it in /proc/mounts right now?" — has a blind
// spot that loses data. A systemd automount unit (which is exactly what
// WebKVM itself writes when it initialises a disk) sits in state
// "active (waiting)" with its matching .mount unit "inactive (dead)"
// whenever nothing has touched the path recently. The kernel reports no
// mountpoint, lsblk reports an empty list, and the disk looks free — so
// the UI offers it for formatting and the wipe guard lets it through.
// The moment any process reads that directory, systemd mounts the
// device again, on top of the filesystem that was just destroyed.
//
// The same hole applies to fstab entries that have not been mounted yet
// (noauto, or a failed boot) and to libvirt storage pools that point at
// a directory on a currently-unmounted disk.
//
// This package therefore cross-references five sources:
//
//	/proc/self/mountinfo   what is mounted right now
//	systemd automount/mount units   what will be mounted on access
//	/etc/fstab             what is declared to be mounted
//	libvirt pool targets   what a storage pool expects to find
//	/dev/disk/by-uuid      to resolve UUID= references to real devices
//
// and reports a device as free only when every one of them agrees.
package diskstate

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Source identifies where an intent-to-mount declaration came from, so
// the UI and the guard error messages can tell the operator what to
// disable before the disk becomes safe to touch.
type Source string

const (
	SourceKernel    Source = "kernel"            // mounted right now
	SourceAutomount Source = "systemd-automount" // will mount on access
	SourceMountUnit Source = "systemd-mount"     // .mount unit exists
	SourceFstab     Source = "fstab"             // declared in /etc/fstab
	SourceLibvirt   Source = "libvirt-pool"      // a storage pool targets it
)

// State is the verdict for a single block device.
type State struct {
	// Device is the canonical device path (/dev/sda1).
	Device string `json:"device"`
	// Active is true when the kernel has it mounted right now.
	Active bool `json:"active"`
	// Configured is true when something declares it should be mounted,
	// even if it is not mounted at this instant. This is the field the
	// destructive guards must honour.
	Configured bool `json:"configured"`
	// MountPoints are every path this device is or would be mounted at.
	MountPoints []string `json:"mountpoints,omitempty"`
	// Sources lists which detectors fired, in a stable order.
	Sources []Source `json:"sources,omitempty"`
	// Units are the systemd units holding the declaration, e.g.
	// "mnt-storage\\x2dsdc.automount". Shown verbatim so the operator
	// can copy-paste them into systemctl.
	Units []string `json:"units,omitempty"`
	// Pools are libvirt storage pools whose target lives on this device.
	Pools []string `json:"pools,omitempty"`
}

// InUse reports whether the device must be treated as carrying live
// data. A device is in use when it is mounted OR when a mount of it is
// declared anywhere — the second half is the whole point of this
// package.
func (s State) InUse() bool { return s.Active || s.Configured }

// Reason renders a short, actionable explanation of why a device is
// considered in use, suitable for an HTTP error body.
func (s State) Reason() string {
	switch {
	case s.Active && len(s.MountPoints) > 0:
		return "mounted at " + s.MountPoints[0]
	case s.Active:
		return "currently mounted"
	case len(s.Units) > 0:
		mp := ""
		if len(s.MountPoints) > 0 {
			mp = " for " + s.MountPoints[0]
		}
		return "a systemd unit (" + s.Units[0] + ") will mount it on access" + mp +
			"; stop and disable that unit before continuing"
	case len(s.Pools) > 0:
		return "libvirt storage pool " + s.Pools[0] + " targets it; delete that pool first"
	case s.hasSource(SourceFstab):
		return "declared in /etc/fstab; remove that entry before continuing"
	default:
		return "in use"
	}
}

func (s State) hasSource(want Source) bool {
	for _, src := range s.Sources {
		if src == want {
			return true
		}
	}
	return false
}

// Table maps canonical device paths to their resolved state.
type Table map[string]State

// Lookup returns the state for a device, resolving symlinks (so
// /dev/disk/by-uuid/... and /dev/sda1 hit the same entry).
func (t Table) Lookup(device string) (State, bool) {
	if s, ok := t[device]; ok {
		return s, true
	}
	s, ok := t[canonicalDevice(device)]
	return s, ok
}

// Resolver gathers mount intent. The exported fields exist so tests can
// point the scanners at fixtures instead of the live host; zero values
// mean "use the real paths".
type Resolver struct {
	MountInfoPath string // default /proc/self/mountinfo
	FstabPath     string // default /etc/fstab
	DevDiskDir    string // default /dev/disk
	// Systemd lists automount/mount units. Nil uses systemctl.
	Systemd func(ctx context.Context) ([]SystemdUnit, error)
	// Libvirt lists storage pool target paths. Nil uses virsh.
	Libvirt func(ctx context.Context) (map[string]string, error)
}

// SystemdUnit is one automount/mount unit with its resolved backing
// device and target directory.
type SystemdUnit struct {
	Name   string // mnt-storage\x2dsdc.automount
	What   string // /dev/disk/by-uuid/1203c705-... or UUID=...
	Where  string // /mnt/storage-sdc
	Active bool   // unit is loaded and armed (not necessarily mounted)
}

// New returns a Resolver bound to the live host.
func New() *Resolver { return &Resolver{} }

func (r *Resolver) mountInfoPath() string {
	if r.MountInfoPath != "" {
		return r.MountInfoPath
	}
	return "/proc/self/mountinfo"
}

func (r *Resolver) fstabPath() string {
	if r.FstabPath != "" {
		return r.FstabPath
	}
	return "/etc/fstab"
}

func (r *Resolver) devDiskDir() string {
	if r.DevDiskDir != "" {
		return r.DevDiskDir
	}
	return "/dev/disk"
}

// Resolve builds the full table for the host. It never returns an
// error: every source degrades independently, because a missing
// /etc/fstab or an absent systemd must not stop the kernel-level checks
// from running. Callers that need fail-closed behaviour on a *specific*
// device should combine this with their own probe.
func (r *Resolver) Resolve(ctx context.Context) Table {
	t := make(Table)

	for dev, mp := range r.scanMountInfo() {
		for _, p := range mp {
			t.add(dev, p, SourceKernel, "", "")
		}
	}

	for _, u := range r.scanSystemd(ctx) {
		dev := r.resolveWhat(u.What)
		if dev == "" {
			continue
		}
		src := SourceMountUnit
		if strings.HasSuffix(u.Name, ".automount") {
			src = SourceAutomount
		}
		t.add(dev, u.Where, src, u.Name, "")
	}

	for dev, mp := range r.scanFstab() {
		for _, p := range mp {
			t.add(dev, p, SourceFstab, "", "")
		}
	}

	// Libvirt pools are keyed by target directory, so they attach to
	// whichever device currently provides that path. Resolved last so
	// the mount table above is already populated.
	for pool, target := range r.scanLibvirt(ctx) {
		dev := t.deviceProviding(target)
		if dev == "" {
			continue
		}
		t.add(dev, target, SourceLibvirt, "", pool)
	}

	return t
}

// add merges one observation into the table.
func (t Table) add(device, mountPoint string, src Source, unit, pool string) {
	device = canonicalDevice(device)
	if device == "" {
		return
	}
	s := t[device]
	s.Device = device

	switch src {
	case SourceKernel:
		s.Active = true
	default:
		// Every non-kernel source is a declaration of intent. This is
		// the flag the wipe/format guards read.
		s.Configured = true
	}

	if mountPoint != "" && !contains(s.MountPoints, mountPoint) {
		s.MountPoints = append(s.MountPoints, mountPoint)
	}
	if !containsSource(s.Sources, src) {
		s.Sources = append(s.Sources, src)
	}
	if unit != "" && !contains(s.Units, unit) {
		s.Units = append(s.Units, unit)
	}
	if pool != "" && !contains(s.Pools, pool) {
		s.Pools = append(s.Pools, pool)
	}
	t[device] = s
}

// deviceProviding returns the device whose mountpoint set contains the
// longest prefix of path — i.e. the device that actually backs it.
func (t Table) deviceProviding(path string) string {
	best, bestLen := "", -1
	for dev, s := range t {
		for _, mp := range s.MountPoints {
			if mp == "/" {
				continue // the root fs backs everything; too coarse to be useful
			}
			if (path == mp || strings.HasPrefix(path, mp+"/")) && len(mp) > bestLen {
				best, bestLen = dev, len(mp)
			}
		}
	}
	return best
}

// scanMountInfo parses /proc/self/mountinfo. Preferred over
// /proc/mounts because the mount source is in a fixed field position
// even when the mountpoint contains escaped spaces.
func (r *Resolver) scanMountInfo() map[string][]string {
	out := make(map[string][]string)
	f, err := os.Open(r.mountInfoPath())
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		// Format: ID PARENT MAJ:MIN ROOT MOUNTPOINT OPTS... - FSTYPE SOURCE SUPEROPTS
		fields := strings.Fields(sc.Text())
		sep := -1
		for i, f := range fields {
			if f == "-" {
				sep = i
				break
			}
		}
		if sep < 0 || len(fields) < sep+3 || sep < 5 {
			continue
		}
		mountPoint := unescapeOctal(fields[4])
		source := unescapeOctal(fields[sep+2])
		if !strings.HasPrefix(source, "/dev/") {
			continue
		}
		dev := canonicalDevice(source)
		if !contains(out[dev], mountPoint) {
			out[dev] = append(out[dev], mountPoint)
		}
	}
	return out
}

var fstabFieldRE = regexp.MustCompile(`\s+`)

// scanFstab parses /etc/fstab, resolving UUID=/LABEL= references.
func (r *Resolver) scanFstab() map[string][]string {
	out := make(map[string][]string)
	f, err := os.Open(r.fstabPath())
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := fstabFieldRE.Split(line, -1)
		if len(fields) < 3 {
			continue
		}
		fsType := fields[2]
		// Pseudo filesystems have no backing block device.
		switch fsType {
		case "swap", "proc", "sysfs", "tmpfs", "devpts", "none", "cgroup", "cgroup2":
			continue
		}
		dev := r.resolveWhat(fields[0])
		mp := unescapeOctal(fields[1])
		if dev == "" || mp == "" || mp == "none" {
			continue
		}
		if !contains(out[dev], mp) {
			out[dev] = append(out[dev], mp)
		}
	}
	return out
}

// resolveWhat turns any of the forms systemd and fstab accept —
// /dev/sda1, /dev/disk/by-uuid/<uuid>, UUID=<uuid>, LABEL=<label>,
// PARTUUID=<uuid> — into a canonical /dev path.
func (r *Resolver) resolveWhat(what string) string {
	what = strings.TrimSpace(strings.Trim(what, `"`))
	if what == "" {
		return ""
	}
	if tag, value, ok := strings.Cut(what, "="); ok && !strings.HasPrefix(what, "/") {
		var dir string
		switch strings.ToUpper(tag) {
		case "UUID":
			dir = "by-uuid"
		case "LABEL":
			dir = "by-label"
		case "PARTUUID":
			dir = "by-partuuid"
		case "PARTLABEL":
			dir = "by-partlabel"
		case "ID":
			dir = "by-id"
		default:
			return ""
		}
		what = filepath.Join(r.devDiskDir(), dir, value)
	}
	if !strings.HasPrefix(what, "/") {
		return ""
	}
	return canonicalDevice(what)
}

// canonicalDevice resolves symlinks so every alias of a device folds
// onto one key. Falls back to the cleaned input when the path does not
// exist (fixtures, or a disk that has been removed).
func canonicalDevice(path string) string {
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}

type systemctlUnit struct {
	Unit   string `json:"unit"`
	Load   string `json:"load"`
	Active string `json:"active"`
	Sub    string `json:"sub"`
}

// scanSystemd lists automount and mount units and reads What=/Where=
// from each. An automount in "waiting" sub-state is precisely the case
// the kernel cannot see, so it is reported as armed.
func (r *Resolver) scanSystemd(ctx context.Context) []SystemdUnit {
	if r.Systemd != nil {
		units, err := r.Systemd(ctx)
		if err != nil {
			return nil
		}
		return units
	}

	out, err := exec.CommandContext(ctx, "systemctl", "list-units",
		"--type=automount,mount", "--all", "--no-legend", "--no-pager",
		"--output=json").Output()
	if err != nil {
		return nil
	}
	var listed []systemctlUnit
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil
	}

	var units []SystemdUnit
	for _, u := range listed {
		if u.Load != "loaded" {
			continue
		}
		// "inactive/dead" .mount units are still meaningful when a
		// sibling .automount is armed; that pairing is handled by
		// keeping both and letting the caller merge on device.
		armed := u.Active == "active"
		what, where := systemdUnitPaths(ctx, u.Unit)
		if what == "" || where == "" {
			continue
		}
		units = append(units, SystemdUnit{
			Name: u.Unit, What: what, Where: where, Active: armed,
		})
	}
	return units
}

// systemdUnitPaths reads What= and Where= from a unit. `systemctl show`
// is used instead of parsing unit files directly so generated units
// (fstab-generator) and drop-ins are covered.
func systemdUnitPaths(ctx context.Context, unit string) (string, string) {
	out, err := exec.CommandContext(ctx, "systemctl", "show", unit,
		"--property=What", "--property=Where", "--no-pager").Output()
	if err != nil {
		return "", ""
	}
	var what, where string
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch k {
		case "What":
			what = v
		case "Where":
			where = v
		}
	}
	// An .automount unit carries Where= but not What=; pair it with the
	// matching .mount unit, which is where the device is declared.
	if what == "" && strings.HasSuffix(unit, ".automount") {
		what, _ = systemdUnitPaths(ctx, strings.TrimSuffix(unit, ".automount")+".mount")
	}
	return what, where
}

// scanLibvirt maps pool name to its target directory, including pools
// that are defined but not currently active — an inactive pool still
// means the operator intends that disk to hold VM storage.
func (r *Resolver) scanLibvirt(ctx context.Context) map[string]string {
	if r.Libvirt != nil {
		pools, err := r.Libvirt(ctx)
		if err != nil {
			return nil
		}
		return pools
	}

	out, err := exec.CommandContext(ctx, "virsh", "-q", "pool-list", "--all", "--name").Output()
	if err != nil {
		return nil
	}
	pools := make(map[string]string)
	for _, name := range strings.Fields(string(out)) {
		xml, err := exec.CommandContext(ctx, "virsh", "pool-dumpxml", name).Output()
		if err != nil {
			continue
		}
		if m := poolTargetRE.FindSubmatch(xml); len(m) > 1 {
			pools[name] = strings.TrimSpace(string(m[1]))
		}
	}
	return pools
}

var poolTargetRE = regexp.MustCompile(`(?s)<target>.*?<path>([^<]*)</path>`)

// unescapeOctal decodes the \040 style escapes the kernel and fstab use
// for spaces, tabs, newlines and backslashes in paths.
func unescapeOctal(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) &&
			isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			v := (int(s[i+1]-'0') << 6) | (int(s[i+2]-'0') << 3) | int(s[i+3]-'0')
			b.WriteByte(byte(v))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func containsSource(list []Source, want Source) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// DefaultTimeout bounds a full host scan. Every external command is
// cheap, but virsh can block when libvirtd is wedged.
const DefaultTimeout = 20 * time.Second
