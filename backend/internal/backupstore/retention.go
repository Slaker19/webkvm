// Retention engine (V13-BCK-03).
//
// Retention is split into a PURE decision (decideRetention — no I/O,
// fully unit-testable) and the I/O wrapper (ApplyRetention, the janitor).
// Time bucketing is done strictly in UTC for all tiers (daily / weekly /
// monthly) so a policy never mixes timezones.
//
// Failure isolation: a single failing target or a single failing run never
// aborts a pass — errors are logged and the loop continues, so the 6-hour
// janitor always visits every target regardless of one being unreachable.
package backupstore

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"webkvm/internal/safego"
)

// retentionBucket returns the UTC bucket key for a run's newest time under
// a tier. All three tiers use UTC consistently.
func retentionBucket(t time.Time, period string) string {
	t = t.UTC()
	switch period {
	case "daily":
		return t.Format("2006-01-02")
	case "weekly":
		y, w := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	case "monthly":
		return t.Format("2006-01")
	}
	return ""
}

// decideRetention computes the set of run suffixes to KEEP for a policy.
// It is PURE: given runs (suffix + newest time) and a UTC "now", it never
// touches I/O. Semantics (OR across all rules, consistent with the original
// KeepLast||KeepDays):
//
//   - KeepLast: keep the N newest runs.
//   - KeepDays: keep runs younger than N days.
//   - KeepDaily / KeepWeekly / KeepMonthly: for each UTC bucket, keep the
//     N newest runs that fall in that bucket.
//
// Runs are sorted newest-first so tiered picks and KeepLast are
// deterministic regardless of input order.
func decideRetention(policy RetentionPolicy, now time.Time, runs []retentionRun) map[string]bool {
	keep := map[string]bool{}
	if !policy.Enabled() {
		return keep
	}
	sorted := make([]retentionRun, len(runs))
	copy(sorted, runs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].NewestTime.After(sorted[j].NewestTime) })

	for i, rr := range sorted {
		if policy.KeepLast > 0 && i < policy.KeepLast {
			keep[rr.Suffix] = true
		}
		if policy.KeepDays > 0 && now.Sub(rr.NewestTime) <= time.Duration(policy.KeepDays)*24*time.Hour {
			keep[rr.Suffix] = true
		}
	}

	for _, tier := range []struct {
		n      int
		period string
	}{
		{policy.KeepDaily, "daily"},
		{policy.KeepWeekly, "weekly"},
		{policy.KeepMonthly, "monthly"},
	} {
		if tier.n <= 0 {
			continue
		}
		perBucket := map[string][]retentionRun{}
		for _, rr := range sorted { // already newest-first
			k := retentionBucket(rr.NewestTime, tier.period)
			perBucket[k] = append(perBucket[k], rr)
		}
		for _, bucketRuns := range perBucket {
			for i := 0; i < len(bucketRuns) && i < tier.n; i++ {
				keep[bucketRuns[i].Suffix] = true
			}
		}
	}
	return keep
}

// ApplyRetention prunes a target's old runs according to its policy.
// Runs are removed as whole units (DeleteBackupRun); a failure on ONE run
// is logged and the pass continues — leftover files of a partially-deleted
// run are re-pruned on the next cycle (idempotent, no panic, no blocked
// queue). Returns the number of runs removed and the first error, if any.
func ApplyRetention(store *Store, tgt Target) (int, error) {
	if !tgt.Retention.Enabled() {
		return 0, nil
	}
	files, err := ListBackupsOnTarget(tgt)
	if err != nil {
		return 0, err
	}
	byRun := map[string]retentionRun{}
	for _, f := range files {
		suf := runSuffixFromFilename(f.Filename)
		if suf == "" {
			continue // legacy / foreign file: never managed
		}
		rr := byRun[suf]
		rr.Suffix = suf
		if f.Modified.After(rr.NewestTime) {
			rr.NewestTime = f.Modified
		}
		byRun[suf] = rr
	}
	if len(byRun) == 0 {
		return 0, nil
	}
	runs := make([]retentionRun, 0, len(byRun))
	for _, rr := range byRun {
		runs = append(runs, rr)
	}

	keep := decideRetention(tgt.Retention, time.Now().UTC(), runs)
	removed := 0
	var firstErr error
	for _, rr := range runs {
		if keep[rr.Suffix] {
			continue
		}
		if _, derr := DeleteBackupRun(tgt, rr.Suffix); derr != nil {
			// Isolated failure: log, keep going, retry next cycle.
			slog.Warn("backup_retention_delete_failed", "target", tgt.ID, "run", rr.Suffix, "err", derr)
			if firstErr == nil {
				firstErr = derr
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

// sweepRetention visits every target with a retention policy once. A
// failure (or panic) on one target is logged and NEVER aborts the cycle —
// the next target is still processed.
func sweepRetention(store *Store, logger *slog.Logger) (int, error) {
	if logger == nil {
		logger = slog.Default()
	}
	totalRemoved := 0
	var firstErr error
	for _, tgt := range store.ListTargets() {
		if !tgt.Retention.Enabled() {
			continue
		}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					logger.Error("backup_retention_panicked", "target", tgt.ID, "recover", rec)
					if firstErr == nil {
						firstErr = fmt.Errorf("retention panic on %s", tgt.ID)
					}
				}
			}()
			removed, err := ApplyRetention(store, tgt)
			if err != nil {
				logger.Warn("backup_retention_target_failed", "target", tgt.ID, "err", err)
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			if removed > 0 {
				logger.Info("backup_retention_pruned", "target", tgt.ID, "removed_runs", removed)
			}
			totalRemoved += removed
		}()
	}
	return totalRemoved, firstErr
}

// StartRetentionJanitor runs the retention sweep on a background ticker
// (default 6h). It is fully decoupled from the cron ticker that fires
// backup jobs — a slow target never blocks scheduled runs, and one
// failing target never aborts the cycle.
func StartRetentionJanitor(ctx context.Context, interval time.Duration, store *Store, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	go func() {
		defer safego.Recover("retention_sweep")
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				removed, _ := sweepRetention(store, logger)
				if removed > 0 {
					logger.Info("backup_retention_janitor_cycle", "removed_runs", removed)
				}
			}
		}
	}()
}
