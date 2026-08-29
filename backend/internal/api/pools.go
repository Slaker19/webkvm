package api

import (
	"fmt"
	"path/filepath"
	"strings"

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
	if h.lv == nil {
		return fmt.Errorf("storage backend unavailable")
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("invalid source path")
	}
	if real, rerr := filepath.EvalSymlinks(resolved); rerr == nil {
		resolved = real
	}
	pools, err := h.lv.ListStoragePools()
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
