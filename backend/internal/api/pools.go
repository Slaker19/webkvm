package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// poolAllowSet returns the set of pools a user may use. The second
// return value is true when the user may use every pool (admins, or
// users without an explicit allowlist). An empty allowlist therefore
// means "all pools", keeping the feature backward-compatible.
func poolAllowSet(u *models.User) (map[string]bool, bool) {
	if u == nil || u.Role == models.RoleAdmin || len(u.AllowedPools) == 0 {
		return nil, true
	}
	set := make(map[string]bool, len(u.AllowedPools))
	for _, p := range u.AllowedPools {
		set[p] = true
	}
	return set, false
}

// poolVisibleTo reports whether pool should be shown to the caller.
// Admins and unrestricted users see everything; a restricted user sees
// only their allowlisted pools.
func (h *Handler) poolVisibleTo(r *http.Request, pool string) bool {
	user, role, _ := audit.FromRequest(r)
	if role == models.RoleAdmin || user == "" || h.userStore == nil {
		return true
	}
	u, err := h.userStore.Get(user)
	if err != nil {
		return false
	}
	return assertPoolAllowed(u, pool) == nil
}

// assertPoolAllowed returns an error if the user is restricted to an
// allowlist that does not contain pool. Admins and unrestricted users
// always pass.
func assertPoolAllowed(u *models.User, pool string) error {
	set, all := poolAllowSet(u)
	if all {
		return nil
	}
	if set[pool] {
		return nil
	}
	return fmt.Errorf("pool %q is not available to this user", pool)
}

// assertPoolPurpose refuses a pool whose declared nature does not match
// what is about to be written into it. Unlike assertPoolAllowed this is
// NOT a permission check, so it applies to admins too: it is about the
// integrity of the storage layout, not about entitlements.
//
// Without it, libvirt happily creates a VM disk inside the ISO library
// (both are plain directory pools, so nothing lower down objects) and
// the operator ends up with a qcow2 sitting among the install media.
// An Incus pool is not a libvirt pool at all, so that combination fails
// deep in the backend with a cryptic "no storage pool with matching
// name" — a 400 naming the real problem is far more useful.
//
// wantPurpose is one of compute.PoolPurpose*. An unknown pool is
// refused: a typo'd name must not silently fall back to a default.
func (h *Handler) assertPoolPurpose(pool, wantPurpose string) error {
	if pool == "" {
		return nil // caller falls back to its own default
	}
	purpose, ok := h.lookupPoolPurpose(pool)
	if !ok {
		return fmt.Errorf("storage pool %q does not exist", pool)
	}
	if compute.HasPurpose(purpose, wantPurpose) {
		return nil
	}
	return fmt.Errorf("pool %q is a %s pool and cannot hold %s storage",
		pool, purposeNoun(purpose), purposeNoun(wantPurpose))
}

// assertPoolPurposeAny accepts the pool when it carries ANY of the given
// natures. For the endpoints that legitimately serve both worlds (the
// import upload handles a KVM archive and a vzdump LXC alike, and only
// learns which after parsing).
func (h *Handler) assertPoolPurposeAny(pool string, want ...string) error {
	if pool == "" || len(want) == 0 {
		return nil
	}
	purpose, ok := h.lookupPoolPurpose(pool)
	if !ok {
		return fmt.Errorf("storage pool %q does not exist", pool)
	}
	for _, n := range want {
		if compute.HasPurpose(purpose, n) {
			return nil
		}
	}
	return fmt.Errorf("pool %q is a %s pool and cannot hold %s storage",
		pool, purposeNoun(purpose), strings.Join(want, "/"))
}

// purposeNoun renders a pool purpose for an operator-facing message.
// Multi-purpose legacy rows are shown verbatim.
func purposeNoun(purpose string) string {
	switch purpose {
	case "":
		return compute.PoolPurposeDisk
	case "lxc":
		return compute.PoolPurposeContainer
	}
	return purpose
}

// validateDiskSourcePath ensures a caller-supplied disk/cdrom "source"
// path resolves to a file inside one of the connector's known storage
// pools. Without this check, AttachDisk/UpdateDiskSource would let any
// operator point a VM's disk at an arbitrary host file (e.g. /etc/shadow,
// another tenant's disk image) since libvirt only requires a readable
// path, not that it belongs to a managed pool.
func (h *Handler) validateDiskSourcePath(path string) error {
	if path == "" {
		return nil
	}
	if h.compute == nil {
		return fmt.Errorf("storage backend unavailable")
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid source path")
	}
	if real, rerr := filepath.EvalSymlinks(resolved); rerr == nil {
		resolved = real
	}
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		return fmt.Errorf("could not validate source path")
	}
	for _, p := range pools {
		base := p.Path
		if base == "" {
			continue
		}
		if realBase, berr := filepath.EvalSymlinks(base); berr == nil {
			base = realBase
		}
		base = filepath.Clean(base)
		if resolved == base {
			continue // the pool directory itself is never a valid disk source
		}
		rel, err := filepath.Rel(base, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("source path %q is not inside a known storage pool", path)
}

// resolveVolumeFromSource maps an absolute disk source path back to its
// (pool, volumeName) pair, so guards that only know the path (FindVolume
// // Attachments) can be applied. Returns ok=false when the path is not a
// known volume (e.g. it is a raw host block device).
func (h *Handler) resolveVolumeFromSource(path string) (pool, name string, ok bool) {
	if path == "" || h.compute == nil {
		return "", "", false
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", "", false
	}
	if real, rerr := filepath.EvalSymlinks(resolved); rerr == nil {
		resolved = real
	}
	resolved = filepath.Clean(resolved)
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		return "", "", false
	}
	for _, p := range pools {
		base := filepath.Clean(p.Path)
		if realBase, berr := filepath.EvalSymlinks(base); berr == nil {
			base = filepath.Clean(realBase)
		}
		rel, rerr := filepath.Rel(base, resolved)
		if rerr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		// rel is the volume name (possibly nested); libvirt volume names
		// are flat file names, so accept only a single component.
		if strings.ContainsRune(rel, filepath.Separator) {
			continue
		}
		return p.Name, rel, true
	}
	return "", "", false
}

// sharedFolderDenylist is checked before anything else, unconditionally —
// even for an admin. A 9p shared folder exposes an entire directory tree
// RECURSIVELY (unlike a single-file disk source), so these paths are
// never acceptable regardless of who's asking.
var sharedFolderDenylist = []string{"/", "/etc", "/root", "/boot", "/sys", "/proc", "/dev"}

// validateSharedFolderPath ensures a caller-supplied shared-folder host
// path is safe to expose to a guest: not one of the hard-denied system
// paths, and inside a known storage pool (never the pool's own root
// directory, which would expose every other VM's disks on that pool).
// The endpoint that calls this is admin-only at the router level (see
// router.go's "/shared-folders" group) — this function only validates
// path safety, not a per-user allowlist.
func (h *Handler) validateSharedFolderPath(path string) error {
	if path == "" {
		return fmt.Errorf("host_path is required")
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid host path")
	}
	if real, rerr := filepath.EvalSymlinks(resolved); rerr == nil {
		resolved = real
	}
	resolved = filepath.Clean(resolved)
	for _, deny := range sharedFolderDenylist {
		if resolved == deny {
			return fmt.Errorf("sharing %q is never allowed", resolved)
		}
	}
	if h.lv == nil {
		return fmt.Errorf("storage backend unavailable")
	}
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		return fmt.Errorf("could not validate host path")
	}
	for _, p := range pools {
		base := p.Path
		if base == "" {
			continue
		}
		if realBase, berr := filepath.EvalSymlinks(base); berr == nil {
			base = realBase
		}
		base = filepath.Clean(base)
		if resolved == base {
			continue // the pool root itself is never a valid shared-folder target
		}
		rel, err := filepath.Rel(base, resolved)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("host path %q is not inside a known storage pool", path)
}
