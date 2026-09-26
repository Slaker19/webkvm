package diskstate

import (
	"context"
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
)

// Orphan describes a systemd automount unit whose paired .mount unit
// does not exist.
//
// This state is worse than it looks. systemd keeps the trigger enabled
// and re-arms it at every boot, overlaying an autofs on the mountpoint.
// Any access to the directory then fails with "unit to trigger not
// loaded" after a timeout — and, critically, the autofs *hides whatever
// the directory really contains*. A folder holding live VM disks can
// silently appear empty, which looks exactly like data loss and invites
// an operator to "recreate" files on top of the hidden originals.
//
// It is also invisible to the mount-intent resolver: with no .mount
// unit there is no What= to resolve, so the automount points at no
// device and never reaches the Table. It therefore needs its own scan.
type Orphan struct {
	// Unit is the automount unit name, verbatim for systemctl.
	Unit string `json:"unit"`
	// MountPoint is the directory it shadows.
	MountPoint string `json:"mountpoint"`
	// Enabled reports whether it will come back after a reboot.
	Enabled bool `json:"enabled"`
}

// Orphans returns every automount unit with no backing .mount unit.
// Degrades to nil when systemd is unavailable, like every other scan
// here: a missing detector must not break the disk listing.
func (r *Resolver) Orphans(ctx context.Context) []Orphan {
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

	orphans := findOrphans(listed)
	for i := range orphans {
		_, where := systemdUnitPaths(ctx, orphans[i].Unit)
		orphans[i].MountPoint = where
		orphans[i].Enabled = unitEnabled(ctx, orphans[i].Unit)
	}
	return orphans
}

// findOrphans is the pure matching step: every .automount with no
// loadable .mount sibling. Split out from Orphans so it can be tested
// without a live systemd.
func findOrphans(listed []systemctlUnit) []Orphan {
	// A .mount unit is "real" only when systemd could load it. An entry
	// listed as not-found is precisely the missing half we look for.
	loadedMounts := make(map[string]bool)
	for _, u := range listed {
		if strings.HasSuffix(u.Unit, ".mount") && u.Load == "loaded" {
			loadedMounts[u.Unit] = true
		}
	}

	var orphans []Orphan
	for _, u := range listed {
		if !strings.HasSuffix(u.Unit, ".automount") {
			continue
		}
		// The automount itself must exist. systemd also lists units that
		// are merely *referenced* by something else with Load=not-found
		// (boot.automount is a stock example); those have no unit file at
		// all, shadow nothing, and reporting them would be a false alarm.
		if u.Load != "loaded" {
			continue
		}
		if loadedMounts[strings.TrimSuffix(u.Unit, ".automount")+".mount"] {
			continue
		}
		orphans = append(orphans, Orphan{Unit: u.Unit})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Unit < orphans[j].Unit })
	return orphans
}

// unitEnabled reports whether the unit is wired to start at boot.
// `systemctl is-enabled` exits non-zero for disabled units, so the exit
// status is not an error condition here — only the printed state is.
func unitEnabled(ctx context.Context, unit string) bool {
	out, _ := exec.CommandContext(ctx, "systemctl", "is-enabled", unit).Output()
	return strings.TrimSpace(string(out)) == "enabled"
}
