package backupstore

import (
	"testing"
	"time"
)

// A backup that the remote backend cannot date must never be deleted.
//
// Retention decides what to keep from the mtime the storage backend
// reports. S3-compatible providers do not always supply LastModified:
// MinIO, Ceph RGW, Cloudflare R2 and several NAS gateways can return a
// zero timestamp, and SFTP relays can report the epoch. A zero time is
// the year 1, so `now.Sub(zero)` is astronomically larger than any
// KeepDays window, the run fails every keep rule, and ApplyRetention
// deletes it.
//
// Deletion is irreversible and keeping costs only disk, so an
// undatable backup has to be kept. These tests pin that.

func retentionPolicyForTest(keepLast, keepDays int) RetentionPolicy {
	return RetentionPolicy{KeepLast: keepLast, KeepDays: keepDays}
}

func TestDecideRetention_ZeroMtimeIsKept(t *testing.T) {
	// One healthy backup from yesterday, one whose mtime the backend
	// could not determine. Policy keeps the last 1 and 7 days.
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	runs := []retentionRun{
		{Suffix: "20260924T101500-abcdef", NewestTime: now.Add(-24 * time.Hour)},
		{Suffix: "20260925T090000-fedcba", NewestTime: time.Time{}},
	}

	keep := decideRetention(retentionPolicyForTest(1, 7), now, runs)

	if !keep["20260925T090000-fedcba"] {
		t.Error("a backup with an unknown mtime was NOT kept; it would be deleted")
	}
	if !keep["20260924T101500-abcdef"] {
		t.Error("the healthy backup was not kept")
	}
}

func TestDecideRetention_ZeroMtimeOutranksKeepLast(t *testing.T) {
	// KeepLast=1 means only the newest survives. An undatable backup
	// sorts as the OLDEST possible, so it lands last and would be the
	// first candidate for deletion. It must still be kept: the policy
	// is a floor on how much to keep, not a quota that an unknown clock
	// gets to override.
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	runs := []retentionRun{
		{Suffix: "undated", NewestTime: time.Time{}},
		{Suffix: "recent", NewestTime: now.Add(-time.Hour)},
	}

	keep := decideRetention(retentionPolicyForTest(1, 0), now, runs)

	if !keep["undated"] {
		t.Error("undated backup dropped despite being outside every age rule")
	}
	if !keep["recent"] {
		t.Error("recent backup dropped")
	}
}

// Every run undatable: the policy must not wipe the target.
func TestDecideRetention_AllUndatedKeepsEverything(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	runs := []retentionRun{
		{Suffix: "a", NewestTime: time.Time{}},
		{Suffix: "b", NewestTime: time.Time{}},
		{Suffix: "c", NewestTime: time.Time{}},
	}

	keep := decideRetention(retentionPolicyForTest(1, 1), now, runs)
	for _, r := range runs {
		if !keep[r.Suffix] {
			t.Errorf("run %q deleted although no run on the target has a usable date", r.Suffix)
		}
	}
}

// The normal case must be untouched: an old, properly dated backup is
// still pruned once it falls outside every window.
func TestDecideRetention_OldDatedBackupStillPruned(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	runs := []retentionRun{
		{Suffix: "ancient", NewestTime: now.Add(-400 * 24 * time.Hour)},
		{Suffix: "recent", NewestTime: now.Add(-time.Hour)},
	}

	keep := decideRetention(retentionPolicyForTest(1, 7), now, runs)
	if keep["ancient"] {
		t.Error("a 400-day-old backup survived; retention is not pruning")
	}
	if !keep["recent"] {
		t.Error("recent backup dropped")
	}
}
