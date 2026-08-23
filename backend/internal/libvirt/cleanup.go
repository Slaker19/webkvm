package libvirt

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CleanupStats is the result of a stale-import janitor run. The counters
// let callers log a summary without having to re-scan the pool dirs.
type CleanupStats struct {
	TmpFiles  int
	OvaDirs   int
	BytesFree int64
}

// CleanupStaleImports removes import leftovers older than maxAge from the
// disk and ISO pool paths. The normal import path cleans up after itself
// via `defer os.Remove(tmpPath)`, but if the backend crashes (OOM, panic,
// SIGKILL) the defer never runs and the half-uploaded or partially-
// extracted file leaks into the pool. This janitor is intended to be
// called once at startup so the leak self-heals on the next boot.
//
// The matching is path-based, not libvirt-based: we look for the
// well-known prefixes `webkvm-import-*.tmp` and `.webkvm-ova-import-*`
// inside each pool path. Anything that survives the import handler (a
// partial .tmp, an abandoned work dir) is fair game; pool volumes and
// ISOs are not, because they don't match those names.
//
// Returns the number of files/dirs removed and the bytes reclaimed. On
// any per-entry error we log and continue rather than aborting the
// whole sweep — the import handler already creates new temp files for
// new attempts, and a half-failed cleanup is better than blocking boot.
func (c *Connector) CleanupStaleImports(maxAge time.Duration) (CleanupStats, error) {
	var stats CleanupStats
	now := time.Now()

	pools := []string{c.DiskPoolName(), c.ISOPoolName()}
	seen := map[string]bool{}
	for _, name := range pools {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		path, err := c.GetPoolPath(name)
		if err != nil {
			// Pool might not exist yet on a fresh install. Skip.
			continue
		}
		cleaned, bytes, err := sweepStaleInDir(path, now, maxAge)
		if err != nil {
			return stats, err
		}
		stats.TmpFiles += cleaned.tmp
		stats.OvaDirs += cleaned.ova
		stats.BytesFree += bytes
	}
	return stats, nil
}

type sweepResult struct {
	tmp int
	ova int
}

// sweepStaleInDir removes the two import-prefix leftovers in a single
// pool directory. Returns the per-type counts and total bytes freed.
func sweepStaleInDir(dir string, now time.Time, maxAge time.Duration) (sweepResult, int64, error) {
	var r sweepResult
	var freed int64

	// 1) Flat .tmp files from the streaming upload in importArchive.
	tmpMatches, err := filepath.Glob(filepath.Join(dir, "webkvm-import-*.tmp"))
	if err != nil {
		return r, 0, fmt.Errorf("glob tmp files in %s: %w", dir, err)
	}
	for _, p := range tmpMatches {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if now.Sub(fi.ModTime()) < maxAge {
			// Recent enough — assume the import handler is still
			// running (or crashed less than maxAge ago and might
			// resume). Don't touch it.
			continue
		}
		if err := os.Remove(p); err != nil {
			continue
		}
		freed += fi.Size()
		r.tmp++
	}

	// 2) Work directories from ImportOVA (one per OVA extract).
	ovaMatches, err := filepath.Glob(filepath.Join(dir, ".webkvm-ova-import-*"))
	if err != nil {
		return r, freed, fmt.Errorf("glob ova work dirs in %s: %w", dir, err)
	}
	for _, p := range ovaMatches {
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		if now.Sub(fi.ModTime()) < maxAge {
			continue
		}
		// Add the work-dir size to the freed total (best effort;
		// walking could be expensive on huge imports).
		if size, err := dirSize(p); err == nil {
			freed += size
		}
		if err := os.RemoveAll(p); err != nil {
			continue
		}
		r.ova++
	}

	return r, freed, nil
}

// dirSize sums the on-disk size of every file under root. Errors are
// silently ignored per-file (the janitor is best-effort).
func dirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}
