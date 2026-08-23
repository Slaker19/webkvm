package backupstore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Path safety for backup targets. A target's Path is the local
// directory under which the runner writes archives; restore reads
// from it. Both sides trust the path, so a hostile or careless
// choice (pointing at /etc, /var/lib/libvirt, /proc, etc.) can
// corrupt system state — either at write time (overwriting
// configs the runner never owned) or at restore time (extracting
// archives into system paths if a future bug ever trusts
// filenames, which is exactly bug #8's worst case).
//
// ValidateTargetPath is the single gate. It is called from
// Store.CreateTarget and Store.UpdateTarget. The runner also
// calls it on the resolved pre-create parent in writeBackup so
// an attack that bypasses the API (e.g. hand-edited targets.json)
// still cannot write to system paths.
//
// Rejection surfaces as ErrTargetPathUnwritable, which the HTTP
// handler maps to 400. The error message names the deny-list
// entry that matched, so the operator can tell *why* the path
// was rejected without having to read this file.

// deniedTargetPaths is the set of filesystem roots the runner
// refuses to write into or restore from. Add to this list when a
// new "do not touch" path is identified. The default set covers
// the most common foot-guns:
//   - /etc, /usr, /bin, /sbin, /lib, /lib64  → core OS trees
//   - /boot                                    → kernel image
//   - /proc, /sys                              → kernel interfaces
//   - /var/lib/{dpkg,rpm,libvirt}              → package & hypervisor DB
//
// Note: we deliberately do NOT deny the app's dataDir (e.g.
// /opt/webkvm/backup). The default target lives there and would
// otherwise fail its own bootstrap.
var deniedTargetPaths = []string{
	"/etc",
	"/usr",
	"/boot",
	"/proc",
	"/sys",
	"/bin",
	"/sbin",
	"/lib",
	"/lib64",
	"/var/lib/dpkg",
	"/var/lib/rpm",
	"/var/lib/libvirt",
}

// ValidateTargetPath returns nil if `p` is acceptable as a
// target Path, or an error wrapping ErrTargetPathUnwritable.
// `appDataDir` is the app's data directory (e.g. /opt/webkvm); a
// target under it is always allowed so the bootstrap "default"
// target works.
func ValidateTargetPath(p, appDataDir string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("%w: path is empty", ErrTargetPathUnwritable)
	}
	cleaned := filepath.Clean(p)
	if !filepath.IsAbs(cleaned) {
		return fmt.Errorf("%w: path must be absolute, got %q", ErrTargetPathUnwritable, p)
	}
	resolved := resolveForCheck(cleaned)
	// Always allow paths under the app's data dir.
	if isUnder(resolved, appDataDir) {
		return nil
	}
	for _, d := range deniedTargetPaths {
		if resolved == d || isUnder(resolved, d) {
			return fmt.Errorf("%w: path %q is under denied root %q", ErrTargetPathUnwritable, p, d)
		}
	}
	return nil
}

// resolveForCheck resolves a path to its real location,
// following symlinks when the target exists, or walking up
// parents when only an ancestor exists. If nothing on the path
// can be resolved (e.g. the user is creating a new target in
// a not-yet-existing mount point), it returns filepath.Clean(p)
// so the deny-list check still has something deterministic to
// match against.
//
// Walk-up logic: at each step we keep the basename of the
// current cur in the tail. When we finally find a parent that
// resolves via EvalSymlinks, we join the resolved parent +
// cur's basename + tail — that's the full reconstructed path
// with symlinks resolved as far as possible. Walking up to /
// and falling through returns the cleaned input unchanged.
func resolveForCheck(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	cur := p
	tail := ""
	for {
		curBase := filepath.Base(cur)
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		if real, err := filepath.EvalSymlinks(parent); err == nil {
			return filepath.Join(real, curBase, tail)
		}
		tail = filepath.Join(curBase, tail)
		cur = parent
	}
	return filepath.Clean(p)
}

// isUnder reports whether child is the same as parent or a
// descendant of parent. Both arguments are expected to be
// cleaned absolute paths.
func isUnder(child, parent string) bool {
	if child == parent {
		return true
	}
	sep := string(os.PathSeparator)
	return strings.HasPrefix(child, parent+sep)
}
