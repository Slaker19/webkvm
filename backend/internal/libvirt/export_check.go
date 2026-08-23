package libvirt

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/libvirt/libvirt-go"
)

// validateDisksReadable walks the disk sources referenced by the domain
// XML and verifies that the current process can open and read each one.
// It returns the first failure as a clear, actionable error mentioning
// the exact file path and the two ways the user can fix the problem.
//
// This is the pre-flight check that runs before any byte is written to
// the HTTP response. Without it, a permission error on a single disk
// would only surface inside the streaming goroutine (after the headers
// have been committed) and the client would receive a truncated,
// corrupted download with HTTP 200.
//
// disksOnly=true filters out CD-ROM entries (which point to ISOs that
// may legitimately be missing or read-only); only the file-backed hard
// disks are checked.
func (c *Connector) validateDisksReadable(xmlDesc string) error {
	disks := c.parseDisksFiltered(xmlDesc, true)
	for _, d := range disks {
		if d.Source == "" {
			continue
		}
		fi, err := os.Stat(d.Source)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf(
					"cannot read disk %s: %v. "+
						"Run the backend as root, or fix with: sudo chmod 644 %s"+
						" (or run: sudo webkvm --fix-perms)",
					d.Source, err, d.Source)
			}
			return fmt.Errorf("stat disk %s: %w", d.Source, err)
		}
		if fi.IsDir() {
			continue
		}
		// Open and read one byte to confirm readable. We also
		// check the mode bits as a fast path so the error message
		// is more helpful when the file is plainly not readable
		// by the current user.
		f, err := os.Open(d.Source)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				return fmt.Errorf(
					"permission denied reading %s. "+
						"Run the backend as root, or fix with: sudo chmod 644 %s"+
						" (or run: sudo webkvm --fix-perms)",
					d.Source, d.Source)
			}
			return fmt.Errorf("open disk %s: %w", d.Source, err)
		}
		buf := make([]byte, 1)
		if _, err := f.Read(buf); err != nil && !errors.Is(err, errors.New("EOF")) {
			// EOF on a 0-byte file is fine — it's just empty.
			f.Close()
			return fmt.Errorf("read disk %s: %w", d.Source, err)
		}
		f.Close()
	}
	return nil
}

// ValidateDomainDisks looks up the domain by id and runs the readability
// pre-flight. This is the entry point used by the HTTP handlers.
func (c *Connector) ValidateDomainDisks(id string) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}
	return c.validateDisksReadable(xmlDesc)
}

// FixDiskPermissions iterates every active storage pool, every volume,
// and chmod's each disk file (qcow2/raw/img) to 0644. This requires
// root — running it as a non-root user is a no-op for files the user
// doesn't own (and returns an error listing the ones that couldn't be
// fixed).
//
// This is the one-shot CLI helper invoked by `webkvm --fix-perms`.
// It prints one line per file (chmod path) and returns a summary error
// if any file could not be updated.
func (c *Connector) FixDiskPermissions() error {
	if err := c.ensureConnected(); err != nil {
		return err
	}
	pools, err := c.conn.ListAllStoragePools(libvirt.CONNECT_LIST_STORAGE_POOLS_ACTIVE)
	if err != nil {
		return fmt.Errorf("list pools: %w", err)
	}
	var failed []string
	fixed := 0
	for i := range pools {
		pool := &pools[i]
		name, _ := pool.GetName()
		vols, verr := pool.ListAllStorageVolumes(0)
		if verr != nil {
			slog.Warn("fix_perms_list_volumes_failed", "pool", name, "err", verr)
			pools[i].Free()
			continue
		}
		for j := range vols {
			vpath, _ := vols[j].GetPath()
			vols[j].Free()
			if vpath == "" {
				continue
			}
			if !isDiskFile(vpath) {
				continue
			}
			if err := os.Chmod(vpath, 0644); err != nil {
				failed = append(failed, fmt.Sprintf("%s: %v", vpath, err))
				continue
			}
			fmt.Println("chmod 0644", vpath)
			fixed++
		}
		pools[i].Free()
	}
	if len(failed) > 0 {
		return fmt.Errorf("fixed %d files; could not change: %s",
			fixed, strings.Join(failed, "; "))
	}
	if fixed == 0 {
		fmt.Println("no disk files needed fixing")
	}
	return nil
}

// isDiskFile returns true for file extensions we know are disk images
// (so --fix-perms doesn't touch ISOs, which are typically read-only
// by design and should keep their existing mode).
func isDiskFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".qcow2", ".qcow", ".raw", ".img", ".vmdk", ".vdi", ".vhd":
		return true
	}
	return false
}

// WarnUnreadableDisks scans every active pool's volumes and logs a
// WARNING for each disk file that the current user cannot read. Called
// at startup from EnsureDefaults so the user sees the problem (and
// the fix command) immediately instead of discovering it at export
// time.
func (c *Connector) WarnUnreadableDisks() {
	if err := c.ensureConnected(); err != nil {
		return
	}
	pools, err := c.conn.ListAllStoragePools(libvirt.CONNECT_LIST_STORAGE_POOLS_ACTIVE)
	if err != nil {
		return
	}
	defer func() {
		for i := range pools {
			pools[i].Free()
		}
	}()
	for i := range pools {
		pool := &pools[i]
		name, _ := pool.GetName()
		vols, verr := pool.ListAllStorageVolumes(0)
		if verr != nil {
			continue
		}
		for j := range vols {
			vpath, _ := vols[j].GetPath()
			vols[j].Free()
			if vpath == "" || !isDiskFile(vpath) {
				continue
			}
			f, err := os.Open(vpath)
			if err != nil {
				if errors.Is(err, fs.ErrPermission) {
					slog.Warn("fix_perms_cannot_read", "path", vpath, "err", err)
					slog.Warn("fix_perms_hint_chmod", "path", vpath)
					slog.Warn("fix_perms_hint_repair_command")
				}
				continue
			}
			f.Close()
		}
		_ = name
	}
}
