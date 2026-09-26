package compute

import "testing"

// TestHasPurpose pins the membership semantics shared with
// frontend/src/lib/purpose.js. Equality was the bug: legacy unified
// pools ("iso,disk") matched neither "iso" nor "disk".
func TestHasPurpose(t *testing.T) {
	cases := []struct {
		purpose string
		nature  string
		want    bool
	}{
		{PoolPurposeISO, PoolPurposeISO, true},
		{PoolPurposeISO, PoolPurposeDisk, false},
		{"iso,disk", PoolPurposeISO, true},
		{"iso,disk", PoolPurposeDisk, true},
		{"iso,disk", PoolPurposeContainer, false},
		{"disk, iso , container", PoolPurposeContainer, true},
		// "lxc" is the legacy spelling of "container", on both sides.
		{"lxc", PoolPurposeContainer, true},
		{PoolPurposeContainer, "lxc", true},
		// An existing pool with no declared purpose is a disk pool.
		{"", PoolPurposeDisk, true},
		{"", PoolPurposeISO, false},
		{"", PoolPurposeContainer, false},
		// Substrings must not match: "isos" is not "iso".
		{"isos", PoolPurposeISO, false},
		{PoolPurposeBackup, PoolPurposeBackup, true},
		{PoolPurposeTemplate, PoolPurposeDisk, false},
	}
	for _, c := range cases {
		if got := HasPurpose(c.purpose, c.nature); got != c.want {
			t.Errorf("HasPurpose(%q, %q) = %v, want %v", c.purpose, c.nature, got, c.want)
		}
	}
}
