package api

import (
	"fmt"

	"webkvm/internal/models"
)

// networkAllowSet mirrors poolAllowSet (pools.go) exactly, for
// AllowedNetworks instead of AllowedPools: the set of bridges/networks a
// user may attach a VM/container NIC to. The second return value is true
// when the user may use every network (admins, or users without an
// explicit allowlist) — an empty allowlist means "all networks", same
// backward-compatible semantics as AllowedPools.
func networkAllowSet(u *models.User) (map[string]bool, bool) {
	if u == nil || u.Role == models.RoleAdmin || len(u.AllowedNetworks) == 0 {
		return nil, true
	}
	set := make(map[string]bool, len(u.AllowedNetworks))
	for _, n := range u.AllowedNetworks {
		set[n] = true
	}
	return set, false
}

// assertNetworkAllowed mirrors assertPoolAllowed exactly, for networks.
func assertNetworkAllowed(u *models.User, network string) error {
	set, all := networkAllowSet(u)
	if all {
		return nil
	}
	if set[network] {
		return nil
	}
	return fmt.Errorf("network %q is not available to this user", network)
}
