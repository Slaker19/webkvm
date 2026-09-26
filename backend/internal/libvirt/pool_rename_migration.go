package libvirt

// pool_rename_migration.go — one-shot rename of the legacy "ISOS" pool.
//
// The two pools WebKVM creates on the system disk are named after the
// product (webkvm-disks, webkvm-incus) and live under
// /opt/webkvm/pools/<name>. The ISO library predates that convention
// and shipped as a bare, upper-case "ISOS" — the only pool on the
// system disk that does not follow the pattern, and the only one whose
// name says nothing about who owns it.
//
// It also disagreed with the OTHER naming scheme in the codebase: a
// disk initialised through the init-disk flow gets <volume>-isos (see
// folderPoolSuffix in api/pools_init.go), so an external disk produced
// "Lexar-isos" while the system disk produced "ISOS".
//
// Renaming a libvirt dir pool is not an operation libvirt offers, so
// this undefines the old pool and defines the new one over the SAME
// directory, then renames the directory itself. Nothing is copied and
// no ISO is touched: the files keep their inodes.

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	libvirt "libvirt.org/go/libvirt"

	"webkvm/internal/config"
)

// legacyISOPoolName is the pre-convention name of the system-disk ISO
// library. Kept here rather than in config so nothing new can reference
// it: it exists only to be migrated away from.
const legacyISOPoolName = "ISOS"

// onSystemDisk reports whether poolPath is a direct child of the
// managed pools directory, i.e. a pool WebKVM itself laid out under
// DATA_DIR.
//
// Only those are migrated. A pool named ISOS that the operator pointed
// at an external disk is their own naming, not WebKVM's legacy default,
// and moving that directory could cross a filesystem boundary — turning
// what is meant to be an atomic rename into a non-atomic copy of many
// gigabytes of install media.
func onSystemDisk(poolPath, poolsDir string) bool {
	if poolPath == "" || poolsDir == "" {
		return false
	}
	return filepath.Dir(filepath.Clean(poolPath)) == filepath.Clean(poolsDir)
}

// migrateLegacyISOPool renames the system-disk ISO pool from "ISOS" to
// config.ISOPoolName and moves its directory to match, once.
//
// It runs BEFORE ensureAppPool so the new pool is not created empty
// next to the old one — which would leave the operator with two ISO
// pools and their media in the wrong one.
//
// Every failure path leaves the old pool exactly as it was. A partial
// rename is worse than no rename: the ISOs would still be on disk but
// no pool would point at them, and the VMs booting from them would
// break. So the directory is moved FIRST (a single atomic rename
// within one filesystem) and the pool is only redefined once that
// succeeded; if defining the new pool then fails, the directory is
// moved back.
func (c *Connector) migrateLegacyISOPool() {
	if config.ISOPoolName == legacyISOPoolName {
		return // nothing to migrate
	}

	oldPool, err := c.conn.LookupStoragePoolByName(legacyISOPoolName)
	if err != nil {
		// Already migrated, or a fresh install. An earlier build ran
		// the rename without repointing the domains, leaving every VM
		// with a CD-ROM from the library unable to start. Repair that
		// here too: it is a no-op once no domain references the old
		// directory, and it only runs once the old directory is
		// really gone, so a hand-made ISOS folder is never "fixed".
		legacyPath := filepath.Join(c.cfg.PoolsDir(), legacyISOPoolName)
		newPath := filepath.Clean(c.cfg.ISOPoolPath())
		if _, serr := os.Stat(legacyPath); os.IsNotExist(serr) && legacyPath != newPath {
			if _, nerr := os.Stat(newPath); nerr == nil {
				c.repointDomainsFromLegacyISOPool(legacyPath, newPath)
			}
		}
		return
	}
	defer oldPool.Free()

	// A pool under the new name already existing means a previous run
	// got half way, or the operator made one by hand. Merging two ISO
	// libraries is not something to guess at, so this reports it and
	// leaves both alone.
	if existing, lerr := c.conn.LookupStoragePoolByName(config.ISOPoolName); lerr == nil {
		existing.Free()
		slog.Warn("iso_pool_migration_skipped",
			"reason", "both pools exist",
			"old", legacyISOPoolName, "new", config.ISOPoolName,
			"hint", "move the ISOs into the new pool by hand, then delete the old one")
		return
	}

	xmlDesc, err := oldPool.GetXMLDesc(0)
	if err != nil {
		slog.Warn("iso_pool_migration_failed", "step", "read old pool", "err", err)
		return
	}
	oldPath := filepath.Clean(extractPoolPath(xmlDesc))
	newPath := filepath.Clean(c.cfg.ISOPoolPath())
	if oldPath == "" {
		slog.Warn("iso_pool_migration_failed", "step", "old pool has no path")
		return
	}

	if !onSystemDisk(oldPath, c.cfg.PoolsDir()) {
		slog.Info("iso_pool_migration_skipped",
			"reason", "pool is not on the system disk",
			"pool", legacyISOPoolName, "path", oldPath)
		return
	}

	// Stop the pool before touching its directory: libvirt keeps the
	// target open, and moving it underneath a running pool leaves the
	// pool pointing at a path that no longer exists.
	info, _ := oldPool.GetInfo()
	wasRunning := info.State == libvirt.STORAGE_POOL_RUNNING
	if wasRunning {
		if err := oldPool.Destroy(); err != nil {
			slog.Warn("iso_pool_migration_failed", "step", "stop old pool", "err", err)
			return
		}
	}

	// Move the data first. Same parent directory, so this is an atomic
	// rename, not a copy — the ISOs keep their inodes and no space is
	// needed for a second copy.
	movedDir := false
	if oldPath != newPath {
		if err := os.Rename(oldPath, newPath); err != nil {
			slog.Warn("iso_pool_migration_failed", "step", "move directory",
				"from", oldPath, "to", newPath, "err", err)
			if wasRunning {
				_ = oldPool.Create(0) // put it back the way it was
			}
			return
		}
		movedDir = true
	}

	// undo restores the directory and restarts the old pool, so a
	// failure below leaves the system exactly as it was found.
	undo := func() {
		if movedDir {
			_ = os.Rename(newPath, oldPath)
		}
		if wasRunning {
			_ = oldPool.Create(0)
		}
	}

	if err := oldPool.Undefine(); err != nil {
		slog.Warn("iso_pool_migration_failed", "step", "undefine old pool", "err", err)
		undo()
		return
	}

	c.defineAndStartAppPool(config.ISOPoolName, newPath)

	// defineAndStartAppPool logs its own failures but does not report
	// them, so confirm the new pool is really there before declaring
	// the migration done.
	newPool, err := c.conn.LookupStoragePoolByName(config.ISOPoolName)
	if err != nil {
		slog.Error("iso_pool_migration_failed", "step", "define new pool", "err", err,
			"hint", "restoring the previous layout")
		if movedDir {
			_ = os.Rename(newPath, oldPath)
		}
		c.defineAndStartAppPool(legacyISOPoolName, oldPath)
		return
	}
	newPool.Free()

	// Carry the declared purpose across. Without this the new pool
	// falls back to InferPoolPurpose, and the stale "ISOS" row would
	// linger in pool-purposes.json forever.
	if c.purposes != nil {
		if err := c.purposes.Set(config.ISOPoolName, PoolPurposeISO); err != nil {
			slog.Warn("pool_purpose_save_failed", "pool", config.ISOPoolName, "err", err)
		}
		if err := c.purposes.Delete(legacyISOPoolName); err != nil {
			slog.Warn("pool_purpose_delete_failed", "pool", legacyISOPoolName, "err", err)
		}
	}

	// The ISOs moved, but every domain with one in its CD-ROM still
	// names the old path (or the old pool, for type='volume' disks) in
	// its persistent XML and would fail to start with "Cannot access
	// storage file". Failures are logged per domain; the pool rename
	// itself is done and is not undone for them.
	c.repointDomainsFromLegacyISOPool(oldPath, newPath)

	slog.Info("iso_pool_migrated",
		"from", legacyISOPoolName, "to", config.ISOPoolName,
		"old_path", oldPath, "new_path", newPath)
}

// legacySourceTagRe matches one <source .../> (or opening <source ...>)
// tag of a domain XML, the only place a disk names its file or pool.
var legacySourceTagRe = regexp.MustCompile(`<source\b[^>]*>`)

// rewriteLegacyPoolRefs repoints every disk source in a domain XML that
// lives under oldDir to the same file under newDir, and every
// type='volume' source naming oldPool to newPool. It reports whether
// anything changed. The trailing separator on the directory match keeps
// a sibling such as ".../ISOS2/x.iso" untouched.
func rewriteLegacyPoolRefs(xmlDesc, oldDir, newDir, oldPool, newPool string) (string, bool) {
	oldFile := "file='" + xmlEscape(filepath.Clean(oldDir)) + "/"
	newFile := "file='" + xmlEscape(filepath.Clean(newDir)) + "/"
	oldPoolAttr := "pool='" + xmlEscape(oldPool) + "'"
	newPoolAttr := "pool='" + xmlEscape(newPool) + "'"
	changed := false
	out := legacySourceTagRe.ReplaceAllStringFunc(xmlDesc, func(tag string) string {
		t := strings.ReplaceAll(tag, oldFile, newFile)
		if oldPool != "" {
			t = strings.ReplaceAll(t, oldPoolAttr, newPoolAttr)
		}
		if t != tag {
			changed = true
		}
		return t
	})
	return out, changed
}

// repointDomainsFromLegacyISOPool rewrites the persistent definition of
// every defined domain whose disks (CD-ROM or otherwise) reference the
// legacy ISO directory or pool, so they start again after the rename.
// Idempotent: a domain with nothing to rewrite is left untouched.
func (c *Connector) repointDomainsFromLegacyISOPool(oldDir, newDir string) {
	doms, err := c.conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_PERSISTENT)
	if err != nil {
		slog.Warn("iso_pool_migration_domains_failed", "step", "list domains", "err", err)
		return
	}
	for i := range doms {
		dom := &doms[i]
		name, _ := dom.GetName()
		// SECURE keeps graphics passwords and the like, which an
		// ordinary dump omits and a redefine would otherwise drop.
		xmlDesc, xerr := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE)
		if xerr != nil {
			slog.Warn("iso_pool_migration_domain_failed", "vm", name, "step", "read xml", "err", xerr)
			dom.Free()
			continue
		}
		newXML, changed := rewriteLegacyPoolRefs(xmlDesc, oldDir, newDir, legacyISOPoolName, config.ISOPoolName)
		if changed {
			if nd, derr := c.conn.DomainDefineXML(newXML); derr != nil {
				slog.Warn("iso_pool_migration_domain_failed", "vm", name, "step", "redefine", "err", derr)
			} else {
				nd.Free()
				slog.Info("iso_pool_migration_domain_repointed", "vm", name, "from", oldDir, "to", newDir)
			}
		}
		dom.Free()
	}
}
