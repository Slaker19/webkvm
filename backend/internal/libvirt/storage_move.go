package libvirt

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"libvirt.org/go/libvirt"

	"webkvm/internal/models"
)

// Storage relocation between libvirt pools.
//
// libvirt has no "move this domain's storage" primitive. What it has are
// the three pieces this file composes:
//
//	copy the image       → a faithful sparse-aware copy (copyFile)
//	repoint the domain   → UpdateDiskSource
//	drop the original    → os.Remove + pool refresh
//
// The order matters and is not negotiable: the domain is only repointed
// once the destination file is complete, and the source is only removed
// once the domain no longer references it. A failure at any step leaves
// the VM pointing at a file that exists — either the old one or the new
// one, never at nothing.
//
// Everything here is cold-move only. Relocating the disk of a running
// VM needs a libvirt block-copy job; copying the file underneath a live
// QEMU would produce a torn image, so a running domain is refused rather
// than risked.

// MoveVolumeOpts mirrors compute.MoveVolumeOpts. It is duplicated here
// because internal/libvirt cannot import internal/compute (the compute
// package imports this one), which is the same reason the sentinel
// errors above are declared twice and translated in kvm.go.
type MoveVolumeOpts struct {
	Kind       string
	NewName    string
	KeepSource bool
	OnProgress func(pct float64, stage string)
}

// movableDisk is one <disk device='disk'> of a domain. (Named apart from
// ova.go's diskEntry, which models an OVA's disk instead.)
type movableDisk struct {
	target string // dev name, e.g. "vda"
	path   string // current source file
	format string // qcow2 / raw
}

var (
	diskBlockRe  = regexp.MustCompile(`<disk\b[^>]+device='disk'[^>]*>[\s\S]*?</disk>`)
	diskSourceRe = regexp.MustCompile(`<source\b[^>]*file='([^']+)'[^>]*/>`)
	diskTargetRe = regexp.MustCompile(`<target\b[^>]*dev='([^']+)'`)
	diskDriverRe = regexp.MustCompile(`<driver[^>]*type='([^']+)'`)
)

// domainDisks parses the writable disks of a domain's XML.
func domainDisks(xmlDesc string) []movableDisk {
	var out []movableDisk
	for _, block := range diskBlockRe.FindAllString(xmlDesc, -1) {
		src := diskSourceRe.FindStringSubmatch(block)
		tgt := diskTargetRe.FindStringSubmatch(block)
		if len(src) < 2 || len(tgt) < 2 {
			continue
		}
		format := "qcow2"
		if fm := diskDriverRe.FindStringSubmatch(block); len(fm) > 1 {
			format = fm[1]
		}
		out = append(out, movableDisk{target: tgt[1], path: src[1], format: format})
	}
	return out
}

// freeBytes reports the free space of the filesystem holding dir.
func freeBytes(dir string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, err
	}
	if st.Bsize <= 0 {
		return 0, nil
	}
	maxBlocks := uint64(math.MaxInt64) / uint64(st.Bsize)
	if st.Bavail > maxBlocks {
		return math.MaxInt64, nil
	}
	return int64(st.Bavail * uint64(st.Bsize)), nil
}

// sameFilesystem reports whether two paths live on one device, in which
// case a move is a rename (instant) rather than a copy (minutes, and it
// needs room for both copies at once).
func sameFilesystem(a, b string) bool {
	da, db := deviceIDOf(a), deviceIDOf(b)
	// deviceIDOf reports 0 on a stat failure, so two unreadable paths
	// would otherwise "match" and take the rename path — which would
	// then fail across devices. Unknown means "assume different".
	return da != 0 && da == db
}

// copyFile copies a regular file byte for byte, preserving holes, and
// syncs before it reports success: a move deletes the source
// afterwards, so "written" has to mean "on the platter", not "in the
// page cache".
//
// The copy is faithful rather than re-encoded. Disk images carry more
// than the guest data qemu-img convert can see — internal snapshots,
// and the backing-file link that makes a linked clone a clone instead
// of a full copy — and a move is only supposed to change where a disk
// lives.
//
// Holes matter just as much: a 4 GiB raw image holding 1 MiB of data
// occupies 1 MiB, and copying it naively writes out four gigabytes of
// zeroes. On a pool sized for the real usage that alone fills the disk.
func copyFile(src, dst string) error {
	if strings.Contains(src, "..") || strings.Contains(dst, "..") {
		return fmt.Errorf("invalid path: traversal not allowed")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	fi, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if err := copySparse(out, in, fi.Size()); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// copySparse copies src to dst, skipping the holes instead of writing
// them out as zeroes. It walks the file's extent map with SEEK_DATA /
// SEEK_HOLE, which every filesystem WebKVM runs on (ext4, xfs, btrfs)
// supports; on one that does not, the first seek reports a single data
// extent covering the whole file and the copy degrades to a plain one.
func copySparse(dst *os.File, src *os.File, size int64) error {
	const (
		seekData = 3
		seekHole = 4
	)
	// Set the final length up front. Seeking past the end to skip a
	// hole otherwise looks to XFS like a file being extended, and it
	// answers with speculative preallocation — tens of megabytes of
	// real blocks behind a region that holds nothing. Declaring the
	// size first makes every later seek a seek within the file.
	if err := dst.Truncate(size); err != nil {
		return err
	}
	var offset int64
	for offset < size {
		dataStart, err := syscall.Seek(int(src.Fd()), offset, seekData)
		if err != nil {
			if errors.Is(err, syscall.ENXIO) {
				// No data left: the rest of the file is a hole.
				break
			}
			// The filesystem does not implement the extent seeks, so
			// fall back to a dense copy of what is left.
			if _, serr := src.Seek(offset, io.SeekStart); serr != nil {
				return serr
			}
			if _, cerr := io.Copy(dst, src); cerr != nil {
				return cerr
			}
			return dst.Truncate(size)
		}
		dataEnd, err := syscall.Seek(int(src.Fd()), dataStart, seekHole)
		if err != nil {
			dataEnd = size
		}
		if _, err := src.Seek(dataStart, io.SeekStart); err != nil {
			return err
		}
		if _, err := dst.Seek(dataStart, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(dst, src, dataEnd-dataStart); err != nil {
			return err
		}
		offset = dataEnd
	}
	// A file ending in a hole has no data to copy there, so the size
	// has to be set explicitly.
	return dst.Truncate(size)
}

// MoveDomainStorage relocates every writable disk of a VM into destPool.
func (c *Connector) MoveDomainStorage(id, destPool string, onProgress func(pct float64, stage string)) error {
	report := func(pct float64, stage string) {
		if onProgress != nil {
			onProgress(pct, stage)
		}
	}

	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	// A live QEMU holds a write lock on its image; copying it would
	// capture a torn state that boots into filesystem repair at best.
	state, _, err := dom.GetState()
	if err != nil {
		return fmt.Errorf("get domain state: %w", err)
	}
	if state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED {
		return ErrDomainMustBeStoppedToMove
	}

	destPath, err := c.GetPoolPath(destPool)
	if err != nil {
		return fmt.Errorf("destination pool %q: %w", destPool, err)
	}

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}
	disks := domainDisks(xmlDesc)
	if len(disks) == 0 {
		return fmt.Errorf("this VM has no writable disk to move")
	}

	// Disks already in the destination are skipped rather than copied
	// onto themselves.
	var todo []movableDisk
	var total int64
	for _, d := range disks {
		if filepath.Dir(d.path) == filepath.Clean(destPath) {
			continue
		}
		todo = append(todo, d)
		if fi, serr := os.Stat(d.path); serr == nil {
			total += fi.Size()
		}
	}
	if len(todo) == 0 {
		return ErrSamePool
	}

	// A disk of this VM may be the backing file of linked clones —
	// which is the normal state of affairs for a template, the very
	// thing an operator is most likely to move onto a dedicated pool.
	// The clones record an absolute path, so relocating the disk
	// leaves every one of them unopenable, and nothing in the domain
	// XML reveals the dependency.
	poolDirs := c.allPoolDirs()
	for _, d := range todo {
		deps, derr := backingDependents(d.path, poolDirs)
		if derr != nil {
			return derr
		}
		if len(deps) > 0 {
			return fmt.Errorf("%w: %s", ErrVolumeHasDependents, strings.Join(deps, ", "))
		}
	}

	// A disk shared with another domain (a shared data disk, or one
	// attached by hand to a second VM) must not move: the cleanup step
	// below deletes the source, and the other domain would be left
	// pointing at a file that no longer exists. Refused up front,
	// before any byte is copied, the same way MoveVolume refuses.
	selfUUID, _ := dom.GetUUIDString()
	selfName, _ := dom.GetName()
	for _, d := range todo {
		atts, aerr := c.findPathAttachments(d.path)
		if aerr != nil {
			return fmt.Errorf("check attachments of %s: %w", filepath.Base(d.path), aerr)
		}
		if others := otherDomainAttachments(atts, selfUUID, selfName); len(others) > 0 {
			return fmt.Errorf("%w: %s is also attached to %s",
				ErrVolumeInUse, filepath.Base(d.path), others[0].VMName)
		}
	}

	// Space is checked before the first byte is written: finding out
	// halfway through leaves a truncated image and a full pool.
	if avail, ferr := freeBytes(destPath); ferr == nil && total > 0 && avail < total {
		return fmt.Errorf("%w: need %d MB, %d MB available",
			ErrInsufficientSpace, total/(1<<20), avail/(1<<20))
	}

	report(5, "move_preparing")

	var done []movedDisk
	// Roll back by deleting what was written and pointing the domain
	// back at the originals, which are still on disk at this stage.
	rollback := func() {
		for _, m := range done {
			if err := c.UpdateDiskSource(id, m.target, m.from); err != nil {
				slog.Error("move_rollback_repoint_failed",
					"vm", id, "target", m.target, "path", m.from, "err", err.Error())
			}
			_ = os.Remove(m.to)
		}
	}

	for i, d := range todo {
		base := strings.TrimSuffix(filepath.Base(d.path), filepath.Ext(d.path))
		ext := filepath.Ext(d.path)
		if ext == "" {
			ext = "." + d.format
		}
		newPath, nerr := VMDiskPath(destPath, base, ext)
		if nerr != nil {
			rollback()
			return fmt.Errorf("pick destination name: %w", nerr)
		}

		report(float64(10+(i*70)/len(todo)), "move_copying")
		// A byte-for-byte copy, not qemu-img convert. Convert rebuilds
		// the image from the guest data it can see, which silently
		// discards everything else the container was carrying:
		// internal snapshots vanish, and an overlay is flattened into
		// a full copy that no longer shares its base. A move is
		// supposed to change where a disk lives, nothing else.
		if cerr := copyFile(d.path, newPath); cerr != nil {
			_ = os.Remove(newPath)
			rollback()
			return fmt.Errorf("copy %s: %w", filepath.Base(d.path), cerr)
		}

		// Repoint before touching the source: if this fails, the VM is
		// still valid on its original disk.
		if uerr := c.UpdateDiskSource(id, d.target, newPath); uerr != nil {
			_ = os.Remove(newPath)
			rollback()
			return fmt.Errorf("repoint disk %s: %w", d.target, uerr)
		}
		done = append(done, movedDisk{from: d.path, to: newPath, target: d.target})
	}

	// Past this point the domain no longer references the originals, so
	// removing them cannot break it. A failure here costs disk space,
	// not correctness — hence a warning rather than a rollback that
	// would undo a completed, working move.
	report(85, "move_cleanup")
	for _, m := range done {
		if rerr := os.Remove(m.from); rerr != nil {
			slog.Warn("move_source_cleanup_failed",
				"vm", id, "path", m.from, "err", rerr.Error())
		}
	}

	c.refreshPoolsFor(append(collectDirs(done), destPath))
	report(100, "move_done")
	return nil
}

// otherDomainAttachments filters out the attachments that belong to the
// domain itself (matched by UUID or name, since VolumeAttachment.VMID
// is whichever ID ListDomains reports), leaving only the other domains
// that reference the same file.
func otherDomainAttachments(atts []models.VolumeAttachment, selfUUID, selfName string) []models.VolumeAttachment {
	var out []models.VolumeAttachment
	for _, a := range atts {
		if (selfUUID != "" && a.VMID == selfUUID) || (selfName != "" && (a.VMID == selfName || a.VMName == selfName)) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// movedDisk records one completed disk relocation, so a later failure
// can be rolled back and the source directories refreshed.
type movedDisk struct{ from, to, target string }

// collectDirs returns the distinct source directories of a move.
func collectDirs(done []movedDisk) []string {
	seen := map[string]bool{}
	var dirs []string
	for _, m := range done {
		d := filepath.Dir(m.from)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// refreshPoolsFor re-scans every pool backing one of the given paths, so
// the UI's volume list matches what is on disk.
func (c *Connector) refreshPoolsFor(paths []string) {
	pools, err := c.ListStoragePools()
	if err != nil {
		return
	}
	want := map[string]bool{}
	for _, p := range paths {
		want[filepath.Clean(p)] = true
	}
	for _, p := range pools {
		if p.Path == "" || !want[filepath.Clean(p.Path)] {
			continue
		}
		if rerr := c.RefreshPool(p.Name); rerr != nil {
			slog.Warn("move_pool_refresh_failed", "pool", p.Name, "err", rerr.Error())
		}
	}
}

// MoveVolume relocates a single file (a VM disk or an ISO) between two
// libvirt pools.
func (c *Connector) MoveVolume(srcPool, volName, destPool string, opts MoveVolumeOpts) error {
	if srcPool == destPool {
		return ErrSamePool
	}
	srcDir, err := c.GetPoolPath(srcPool)
	if err != nil {
		return fmt.Errorf("source pool %q: %w", srcPool, err)
	}
	destDir, err := c.GetPoolPath(destPool)
	if err != nil {
		return fmt.Errorf("destination pool %q: %w", destPool, err)
	}

	// The name is rejected outright rather than sanitised: a volume
	// name carrying a path separator is either a bug or an attempt to
	// escape the pool directory.
	if strings.ContainsAny(volName, `/\`) || volName == "." || volName == ".." {
		return fmt.Errorf("invalid volume name %q", volName)
	}
	if strings.Contains(volName, "/") || strings.Contains(volName, "\\") || strings.Contains(volName, "..") {
		return fmt.Errorf("invalid source volume name %q", volName)
	}
	newName := opts.NewName
	if newName == "" {
		newName = volName
	}
	if strings.Contains(newName, "/") || strings.Contains(newName, "\\") || strings.Contains(newName, "..") {
		return fmt.Errorf("invalid destination name %q", newName)
	}

	srcPath := filepath.Join(srcDir, volName)
	destPath := filepath.Join(destDir, newName)
	if rel, rerr := filepath.Rel(srcDir, srcPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return fmt.Errorf("invalid source path")
	}
	if rel, rerr := filepath.Rel(destDir, destPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return fmt.Errorf("invalid destination path")
	}
	fi, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("source volume %q: %w", volName, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%q is a directory, not a volume", volName)
	}
	if _, serr := os.Stat(destPath); serr == nil {
		return fmt.Errorf("a volume named %q already exists in pool %q", newName, destPool)
	}

	// A file still referenced by a defined domain must not move out
	// from under it: the domain would keep pointing at a path that no
	// longer exists and would fail to start.
	//
	// This covers ISOs as well as disks. A running VM with the ISO in
	// its cdrom keeps working — libvirt already holds the fd — which
	// is exactly what makes it dangerous: the breakage only surfaces
	// at the next boot, long after the move looked successful.
	// (MoveDomainStorage is the supported way to relocate a VM's own
	// disks, since it repoints the domain as part of the operation.)
	//
	// A copy is exempt: the original stays where it is, so nothing
	// that references it is disturbed.
	//
	// The same applies to a linked clone's backing file, which the
	// attachment check above cannot see: that dependency is recorded
	// inside the clone's own qcow2 header, and the disk attached to
	// the VM is the overlay, never the image underneath. Moving the
	// image away leaves every clone built on it unopenable.
	if !opts.KeepSource {
		if atts, aerr := c.findPathAttachments(srcPath); aerr == nil && len(atts) > 0 {
			return fmt.Errorf("%w (%s)", ErrVolumeInUse, atts[0].VMName)
		}
		deps, derr := backingDependents(srcPath, c.allPoolDirs())
		if derr != nil {
			return derr
		}
		if len(deps) > 0 {
			return fmt.Errorf("%w: %s", ErrVolumeHasDependents, strings.Join(deps, ", "))
		}
	}

	report := func(pct float64, stage string) {
		if opts.OnProgress != nil {
			opts.OnProgress(pct, stage)
		}
	}
	report(5, "move_preparing")

	if avail, ferr := freeBytes(destDir); ferr == nil && avail < fi.Size() {
		return fmt.Errorf("%w: need %d MB, %d MB available",
			ErrInsufficientSpace, fi.Size()/(1<<20), avail/(1<<20))
	}

	// Within one filesystem a move is a rename: instant, atomic, and it
	// needs no second copy of the data. Only worth it for a true move —
	// a copy has to duplicate the bytes by definition.
	if !opts.KeepSource && sameFilesystem(srcDir, destDir) {
		report(50, "move_copying")
		if rerr := os.Rename(srcPath, destPath); rerr != nil {
			return fmt.Errorf("move %q: %w", volName, rerr)
		}
		c.refreshPoolsFor([]string{srcDir, destDir})
		report(100, "move_done")
		return nil
	}

	report(10, "move_copying")
	if cerr := copyFile(srcPath, destPath); cerr != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("copy %q: %w", volName, cerr)
	}

	if !opts.KeepSource {
		report(90, "move_cleanup")
		if rerr := os.Remove(srcPath); rerr != nil {
			// The copy is complete and valid, so the operation
			// succeeded; the leftover costs space, not correctness.
			slog.Warn("move_source_cleanup_failed",
				"pool", srcPool, "volume", volName, "err", rerr.Error())
		}
	}

	c.refreshPoolsFor([]string{srcDir, destDir})
	report(100, "move_done")
	return nil
}
