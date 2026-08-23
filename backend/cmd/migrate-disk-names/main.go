// One-shot migration: rename disk files in storage pools to the
// new VM-name-based format. Replaces the old patterns:
//
//   *<unix-timestamp>.*            (e.g. "ubuntu-1-1782508261.1782283212")
//   *-restored-YYYY-MM-DDTHH-MM-SS.* (recent v10 format)
//
// with the canonical new format:
//
//   <vmName>.qcow2                 (first disk)
//   <vmName>-2.qcow2, -3, ...      (re-imports / multi-disk)
//
// The migration walks every libvirt domain, finds the disk files
// it references that do NOT match the new format, and renames
// them. Orphan files (no domain references them) are reported
// but left in place unless --cleanup-orphans is passed.
//
// Usage: sudo -E /usr/local/bin/webkvm --migrate-disk-names [--dry-run] [--cleanup-orphans]
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/libvirt/libvirt-go"
	"webkvm/internal/config"
	vmmlibvirt "webkvm/internal/libvirt"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "log actions without performing them")
	cleanupOrphans := flag.Bool("cleanup-orphans", false, "delete pool files that no domain references (DANGEROUS; review the dry-run first)")
	flag.Parse()

	if os.Geteuid() != 0 {
		die("must be run as root (try: sudo webkvm --migrate-disk-names)")
	}

	cfg, err := config.Load()
	if err != nil {
		die("config: %v", err)
	}

	conn := vmmlibvirt.NewConnector(cfg.LibvirtURI, cfg)
	if err := conn.Open(); err != nil {
		die("open libvirt: %v", err)
	}
	defer conn.Close()
	if !conn.IsConnected() {
		die("libvirt not connected")
	}
	libvirtConn := conn.Get()

	// Build map: file basename -> []domainName for every disk
	// referenced by any domain.
	domainForFile := map[string][]string{}
	domainXMLByName := map[string]string{}

	doms, err := libvirtConn.ListAllDomains(0)
	if err != nil {
		die("list domains: %v", err)
	}
	for i := range doms {
		name, _ := doms[i].GetName()
		xml, _ := doms[i].GetXMLDesc(0)
		doms[i].Free()
		domainXMLByName[name] = xml
		for _, src := range extractSourceFiles(xml) {
			base := filepath.Base(src)
			domainForFile[base] = append(domainForFile[base], name)
		}
	}

	pools, err := libvirtConn.ListAllStoragePools(0)
	if err != nil {
		die("list pools: %v", err)
	}

	stats := struct{ renamed, skipped, orphans, errors int }{}

	for i := range pools {
		poolName, _ := pools[i].GetName()
		// Only auto-migrate dir pools — netfs/rBD/etc. have
		// different semantics that we don't want to touch.
		xml, _ := pools[i].GetXMLDesc(0)
		pools[i].Free()
		if !strings.Contains(xml, `type='dir'`) {
			slog.Info("skip_non_dir_pool", "pool", poolName)
			continue
		}
		// Skip the ISO library pool — its files are user-uploaded
		// ISOs, not VM disks. The disk-naming policy doesn't
		// apply to them.
		if poolName == config.ISOPoolName {
			slog.Info("skip_iso_pool", "pool", poolName)
			continue
		}
		poolPath := extractPoolPath(xml)
		if poolPath == "" {
			continue
		}

		slog.Info("migrate_pool", "pool", poolName, "path", poolPath)
		if err := migratePool(poolPath, domainForFile, domainXMLByName, libvirtConn, *dryRun, *cleanupOrphans, &stats); err != nil {
			slog.Error("migrate_pool_failed", "pool", poolName, "err", err)
			stats.errors++
		}
	}

	fmt.Printf("\n=== summary ===\n")
	fmt.Printf("renamed:    %d\n", stats.renamed)
	fmt.Printf("skipped:    %d (collision or no-name available)\n", stats.skipped)
	fmt.Printf("orphans:    %d (no domain reference; %s)\n", stats.orphans,
		boolStr(*cleanupOrphans, "deleted", "left in place"))
	fmt.Printf("errors:     %d\n", stats.errors)
	if *dryRun {
		fmt.Printf("\n(this was a DRY RUN; no changes were made)\n")
	}
}

func migratePool(poolPath string, domainForFile map[string][]string,
	domainXMLByName map[string]string, conn *libvirt.Connect,
	dryRun, cleanupOrphans bool,
	stats *struct{ renamed, skipped, orphans, errors int }) error {

	entries, err := os.ReadDir(poolPath)
	if err != nil {
		return fmt.Errorf("read dir: %w", err)
	}

	// Build set of all "new format" basenames for any known
	// domain, so we can identify files that need migration.
	newFormatRE := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}(-\d+)?\.qcow2$`)
	_ = newFormatRE // not used directly; we check per-domain below

	// Cache of new-format check: for a given (domainName,
	// basename) pair, is the basename already in the new format?
	isNewFormat := func(domName, base string) bool {
		pattern := fmt.Sprintf(`^%s(-\d+)?\.qcow2$`, regexp.QuoteMeta(domName))
		re := regexp.MustCompile(pattern)
		return re.MatchString(base)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base := e.Name()
		referencedBy := domainForFile[base]

		// Orphan: no domain references this file.
		if len(referencedBy) == 0 {
			// Skip snapshot/backup files that the webkvm
			// conventions expect to be present (e.g.
			// "vmname.snap" internal snapshots). The
			// migrate step would never touch those
			// because the migration key is the basename
			// pattern, not the libvirt domain binding.
			// We can't perfectly distinguish orphan data
			// from orphan garbage, so the policy is:
			// log, and only delete with --cleanup-orphans.
			slog.Warn("orphan_file", "path", filepath.Join(poolPath, base))
			stats.orphans++
			if cleanupOrphans && !dryRun {
				if err := os.Remove(filepath.Join(poolPath, base)); err != nil {
					slog.Error("orphan_remove_failed", "base", base, "err", err)
					stats.errors++
				} else {
					slog.Info("orphan_removed", "base", base)
				}
			}
			continue
		}

		// Ambiguous: file is referenced by more than one
		// domain. We can't safely rename it (different VMs
		// might want different names).
		if len(referencedBy) > 1 {
			slog.Warn("ambiguous_reference",
				"base", base, "domains", referencedBy)
			stats.skipped++
			continue
		}

		domName := referencedBy[0]
		// Already in the new format?
		if isNewFormat(domName, base) {
			continue
		}

		// Compute the new name.
		newPath, err := vmmlibvirt.VMDiskPath(poolPath, domName, ".qcow2")
		if err != nil {
			slog.Error("resolve_new_name_failed", "base", base, "vm", domName, "err", err)
			stats.errors++
			continue
		}
		newBase := filepath.Base(newPath)
		if newBase == base {
			// vmDiskPath returned the same name; no
			// migration needed.
			continue
		}

		oldPath := filepath.Join(poolPath, base)
		action := "rename"
		if dryRun {
			action = "would_rename"
		}
		slog.Info(action, "from", oldPath, "to", newPath, "vm", domName)

		if dryRun {
			stats.renamed++
			continue
		}

		// Move the file.
		if err := os.Rename(oldPath, newPath); err != nil {
			slog.Error("rename_failed", "from", oldPath, "to", newPath, "err", err)
			stats.errors++
			continue
		}

		// Update the domain XML.
		xml := domainXMLByName[domName]
		newXML := strings.ReplaceAll(xml, oldPath, newPath)
		if newXML == xml {
			slog.Warn("xml_path_not_found", "vm", domName, "old", oldPath)
		} else if _, err := conn.DomainDefineXML(newXML); err != nil {
			// Roll back the file move.
			_ = os.Rename(newPath, oldPath)
			slog.Error("xml_define_failed", "vm", domName, "err", err)
			stats.errors++
			continue
		}
		stats.renamed++
	}

	// Refresh the pool so the new names show up in vol-list.
	if !dryRun && stats.renamed > 0 {
		if pool, err := conn.LookupStoragePoolByName(filepath.Base(poolPath)); err == nil {
			if rerr := pool.Refresh(0); rerr != nil {
				slog.Warn("pool_refresh_failed", "pool", poolPath, "err", rerr)
			}
			pool.Free()
		}
	}
	return nil
}

// extractSourceFiles returns every disk source file path in a
// libvirt domain XML. Handles <disk type='file'><source file='...'/>
// and ignores cdrom blocks. The (?s) flag makes . match newlines
// so the source can be on the next line from the <disk> tag.
func extractSourceFiles(xml string) []string {
	re := regexp.MustCompile(`(?s)<disk[^>]+device='disk'[^>]*>.*?<source\s+file='([^']+)'\s*/>`)
	matches := re.FindAllStringSubmatch(xml, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) >= 2 {
			out = append(out, m[1])
		}
	}
	return out
}

// extractPoolPath mirrors the helper in libvirt/pool_xml.go;
// duplicated here to avoid an import cycle with the libvirt
// package (which is what we're migrating).
func extractPoolPath(xml string) string {
	start := "<path>"
	i := strings.Index(xml, start)
	if i < 0 {
		return ""
	}
	i += len(start)
	e := strings.Index(xml[i:], "</path>")
	if e < 0 {
		return ""
	}
	return xml[i : i+e]
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", args...)
	os.Exit(1)
}

func boolStr(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}
