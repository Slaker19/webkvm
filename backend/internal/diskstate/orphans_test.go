package diskstate

import "testing"

// hostUnitsAsFound reproduces `systemctl list-units --type=automount,mount
// --all` on the machine where this was discovered. mnt-Lexar and
// mnt-mydisk are the real orphans: enabled automounts whose .mount
// units systemd reports as not-found. /mnt/Lexar held a running VM's
// 137 GB qcow2 at the time, with the automount armed to shadow it.
var hostUnitsAsFound = []systemctlUnit{
	{Unit: "mnt-Lexar.automount", Load: "loaded", Active: "inactive", Sub: "dead"},
	{Unit: "mnt-Lexar.mount", Load: "not-found", Active: "inactive", Sub: "dead"},
	{Unit: "mnt-mydisk.automount", Load: "loaded", Active: "inactive", Sub: "dead"},
	{Unit: "mnt-mydisk.mount", Load: "not-found", Active: "inactive", Sub: "dead"},
	{Unit: "mnt-Seagate.automount", Load: "loaded", Active: "active", Sub: "running"},
	{Unit: "mnt-Seagate.mount", Load: "loaded", Active: "active", Sub: "mounted"},
	{Unit: `mnt-storage\x2dsdc.automount`, Load: "loaded", Active: "active", Sub: "waiting"},
	{Unit: `mnt-storage\x2dsdc.mount`, Load: "loaded", Active: "inactive", Sub: "dead"},
	{Unit: "mnt-xd.automount", Load: "loaded", Active: "active", Sub: "waiting"},
	{Unit: "mnt-xd.mount", Load: "loaded", Active: "inactive", Sub: "dead"},
	// systemd lists units that are merely referenced but have no unit
	// file as not-found. boot.automount ships in that state on this
	// host: nothing to disable, nothing shadowed, not an orphan.
	{Unit: "boot.automount", Load: "not-found", Active: "inactive", Sub: "dead"},
	{Unit: "boot.mount", Load: "not-found", Active: "inactive", Sub: "dead"},
}

func TestFindOrphans_MatchesTheHostBreakage(t *testing.T) {
	got := findOrphans(hostUnitsAsFound)

	want := []string{"mnt-Lexar.automount", "mnt-mydisk.automount"}
	if len(got) != len(want) {
		t.Fatalf("findOrphans returned %d units (%+v), want %d", len(got), got, len(want))
	}
	for i, w := range want {
		if got[i].Unit != w {
			t.Errorf("orphan[%d] = %q, want %q", i, got[i].Unit, w)
		}
	}
}

func TestFindOrphans_HealthyPairsAreNotOrphans(t *testing.T) {
	for _, u := range findOrphans(hostUnitsAsFound) {
		switch u.Unit {
		case "boot.automount":
			t.Error("a not-found unit has no unit file; flagging it is a false alarm")
		case "mnt-Seagate.automount":
			t.Error("a fully mounted pair must never be flagged")
		case `mnt-storage\x2dsdc.automount`, "mnt-xd.automount":
			// Both are armed with a dead .mount — the normal idle state
			// of an automount, and the case the resolver already covers.
			// Flagging it here would fire on every healthy WebKVM disk.
			t.Errorf("%s has a loaded .mount; an idle automount is not an orphan", u.Unit)
		}
	}
}

func TestFindOrphans_Empty(t *testing.T) {
	if got := findOrphans(nil); got != nil {
		t.Errorf("findOrphans(nil) = %+v, want nil", got)
	}
	healthy := []systemctlUnit{
		{Unit: "mnt-ok.automount", Load: "loaded", Active: "active", Sub: "waiting"},
		{Unit: "mnt-ok.mount", Load: "loaded", Active: "inactive", Sub: "dead"},
	}
	if got := findOrphans(healthy); got != nil {
		t.Errorf("findOrphans(healthy) = %+v, want nil", got)
	}
}
