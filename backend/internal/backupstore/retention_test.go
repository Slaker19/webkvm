package backupstore

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// mkRun builds a run with a deterministic suffix + UTC newest time.
func mkRun(day time.Time, hour int, n int) retentionRun {
	ts := day.Add(time.Duration(hour) * time.Hour)
	return retentionRun{
		Suffix:     fmt.Sprintf("%s-%s", ts.UTC().Format("20060102T150405.000000000Z"), fmt.Sprintf("abc%02d", n)),
		NewestTime: ts.UTC(),
	}
}

// TestDecideRetention_MonthlyKeepsNewestPerMonth: 30 runs repartidos en 3
// meses (10 por mes). KeepMonthly=1 debe conservar EXACTAMENTE el run más
// nuevo de cada mes (3 en total), ordenados de forma determinista.
func TestDecideRetention_MonthlyKeepsNewestPerMonth(t *testing.T) {
	var runs []retentionRun
	// 3 meses: enero, febrero, marzo. 10 runs por mes en los días 5..14
	// (siempre dentro del mes, sin desbordes), más nuevos al final (d=9).
	for m := 0; m < 3; m++ {
		for d := 0; d < 10; d++ {
			day := time.Date(2026, time.Month(1+m), 5+d, 12, 0, 0, 0, time.UTC)
			runs = append(runs, mkRun(day, 0, d))
		}
	}
	policy := RetentionPolicy{KeepMonthly: 1}
	keep := decideRetention(policy, time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), runs)

	// El más nuevo de cada mes es el día 14 (d=9).
	want := map[string]bool{}
	for m := 0; m < 3; m++ {
		newest := mkRun(time.Date(2026, time.Month(1+m), 14, 12, 0, 0, 0, time.UTC), 0, 9)
		want[newest.Suffix] = true
	}
	if len(keep) != 3 {
		t.Fatalf("keep = %d runs, want 3 (one per month)", len(keep))
	}
	for suf := range want {
		if !keep[suf] {
			t.Errorf("monthly newest %q not kept", suf)
		}
	}
	// Ningún run no-newest de cada mes debe conservarse.
	for _, rr := range runs {
		if want[rr.Suffix] {
			continue
		}
		if keep[rr.Suffix] {
			t.Errorf("run %q should NOT be kept by KeepMonthly=1", rr.Suffix)
		}
	}
}

// TestDecideRetention_DailyAndWeekly: KeepDaily=1 conserva el más nuevo de
// cada día; KeepWeekly=1 el más nuevo de cada semana ISO. 30 runs con fechas
// únicas repartidas en 3 meses.
func TestDecideRetention_DailyAndWeekly(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var runs []retentionRun
	distinctDays := 0
	for i := 0; i < 30; i++ {
		day := base.AddDate(0, 0, i*3) // 30 runs, un día distinto cada uno
		distinctDays++
		runs = append(runs, mkRun(day, 10, i))
	}
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	// KeepDaily=1: uno por cada día distinto.
	keepD := decideRetention(RetentionPolicy{KeepDaily: 1}, now, runs)
	if len(keepD) != distinctDays {
		t.Errorf("KeepDaily kept %d runs, want %d distinct days", len(keepD), distinctDays)
	}
	for _, rr := range runs {
		if !keepD[rr.Suffix] {
			t.Errorf("daily run %q should be kept", rr.Suffix)
		}
	}

	// KeepWeekly=1: uno por semana ISO (30 días / 7 ≈ 5 semanas).
	keepW := decideRetention(RetentionPolicy{KeepWeekly: 1}, now, runs)
	weekly := 0
	seen := map[string]bool{}
	for _, rr := range runs {
		k := retentionBucket(rr.NewestTime, "weekly")
		if !seen[k] {
			seen[k] = true
			weekly++
		}
	}
	if len(keepW) != weekly {
		t.Errorf("KeepWeekly kept %d runs, want %d distinct ISO weeks", len(keepW), weekly)
	}
}

// TestDecideRetention_CombinedOR: con varias reglas, un run se conserva si
// CUALQUIER regla lo selecciona (OR) — el run más nuevo se conserva siempre
// por KeepLast, los de la última semana por KeepDays, etc.
func TestDecideRetention_CombinedOR(t *testing.T) {
	now := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	old := now.AddDate(0, -2, 0) // hace 2 meses
	runs := []retentionRun{
		mkRun(now, 9, 0),                    // el más reciente
		mkRun(now.Add(-24*time.Hour), 9, 1), // ayer
		mkRun(old.Add(-24*time.Hour), 9, 2), // viejo (hace ~2 meses)
	}
	policy := RetentionPolicy{KeepLast: 1, KeepDays: 2, KeepMonthly: 1}
	keep := decideRetention(policy, now, runs)
	for _, rr := range runs {
		if !keep[rr.Suffix] {
			t.Errorf("combined policy should keep %q", rr.Suffix)
		}
	}
	// Con KeepLast=1 solamente, solo se conserva el más nuevo.
	keepLast := decideRetention(RetentionPolicy{KeepLast: 1}, now, runs)
	if len(keepLast) != 1 || !keepLast[runs[0].Suffix] {
		t.Errorf("KeepLast=1 kept %d, want only the newest", len(keepLast))
	}
}

// TestDecideRetention_Disabled: sin política no se conserva nada (nada que
// borrar).
func TestDecideRetention_Disabled(t *testing.T) {
	runs := []retentionRun{mkRun(time.Now(), 1, 0)}
	keep := decideRetention(RetentionPolicy{}, time.Now(), runs)
	if len(keep) != 0 {
		t.Errorf("disabled policy kept %d runs", len(keep))
	}
}

// TestRetentionBucket_UTC: los buckets usan UTC de forma consistente; un
// timestamp cerca de medianoche UTC no mezcla zonas.
func TestRetentionBucket_UTC(t *testing.T) {
	// 2026-01-01 23:59 UTC en una zona -05 sería 2026-01-01 local, pero el
	// bucket debe seguir siendo 2026-01-02 UTC.
	ts := time.Date(2026, 1, 1, 23, 59, 0, 0, time.UTC)
	if got := retentionBucket(ts, "daily"); got != "2026-01-01" {
		t.Errorf("daily bucket = %q, want 2026-01-01 (UTC)", got)
	}
	if got := retentionBucket(ts, "monthly"); got != "2026-01" {
		t.Errorf("monthly bucket = %q, want 2026-01", got)
	}
	y, w := ts.ISOWeek()
	if got := retentionBucket(ts, "weekly"); got != fmt.Sprintf("%04d-W%02d", y, w) {
		t.Errorf("weekly bucket = %q", got)
	}
}

// TestApplyRetention_PrunesOldLocalRuns: con un target local y una política,
// ApplyRetention borra los runs viejos y conserva los nuevos.
func TestApplyRetention_PrunesOldLocalRuns(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	pool := filepath.Join(dir, "pool")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatal(err)
	}
	tgt, err := s.CreateTargetOpts("loc", pool, TargetLocal, "all", nil, TargetOptions{})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	// 3 runs viejos + 1 reciente. Fechamos los viejos con mtimes antiguos
	// (el algoritmo ordena por Modified, no por el nombre).
	old := []struct {
		name string
		at   time.Time
	}{
		{"webkvm-h-20260101T120000.000000000Z-aaa001-config.tar.zst", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)},
		{"webkvm-h-20260102T120000.000000000Z-aaa002-config.tar.zst", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)},
		{"webkvm-h-20260103T120000.000000000Z-aaa003-config.tar.zst", time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)},
	}
	recent := "webkvm-h-" + now.Format("20060102T150405.000000000Z") + "-bbb004-config.tar.zst"
	for _, f := range old {
		if err := os.WriteFile(filepath.Join(pool, f.name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(pool, f.name), f.at, f.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(pool, recent), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	tgt.Retention = RetentionPolicy{KeepLast: 1}
	removed, err := ApplyRetention(s, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Errorf("removed %d runs, want 3", removed)
	}
	// El reciente se conserva; los viejos no.
	if _, err := os.Stat(filepath.Join(pool, recent)); err != nil {
		t.Error("recent run was pruned")
	}
	for _, f := range old {
		if _, err := os.Stat(filepath.Join(pool, f.name)); !os.IsNotExist(err) {
			t.Errorf("old run %s should be gone", f.name)
		}
	}
}

// TestSweepRetention_IsolatesFailures: un target con retención que falla
// (p.ej. listado inaccesible) NO aborta el sweep — el siguiente target se
// procesa igualmente.
func TestSweepRetention_IsolatesFailures(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Target que falla: path inexistente + retención activa.
	badPath := filepath.Join(dir, "nope")
	badTgt, err := s.CreateTargetOpts("bad", badPath, TargetLocal, "all", nil, TargetOptions{Retention: RetentionPolicy{KeepLast: 1}})
	if err != nil {
		t.Fatal(err)
	}
	_ = badTgt
	// Target bueno: pool local con un run viejo.
	goodPath := filepath.Join(dir, "good-pool")
	if err := os.MkdirAll(goodPath, 0o755); err != nil {
		t.Fatal(err)
	}
	goodTgt, err := s.CreateTargetOpts("good", goodPath, TargetLocal, "all", nil, TargetOptions{Retention: RetentionPolicy{KeepLast: 1}})
	if err != nil {
		t.Fatal(err)
	}
	_ = goodTgt
	// Escribir un run viejo + uno reciente en el bueno: KeepLast=1
	// conserva el reciente y poda el viejo. Mtimes explícitos para que la
	// ordenación sea determinista.
	oldFile := "webkvm-h-20260101T120000.000000000Z-ccc001-config.tar.zst"
	recent := "webkvm-h-" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-ccc002-config.tar.zst"
	for _, f := range []string{oldFile, recent} {
		if err := os.WriteFile(filepath.Join(goodPath, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	oldAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(filepath.Join(goodPath, oldFile), oldAt, oldAt); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	removed, _ := sweepRetention(s, logger)
	// El sweep completo: el bueno debe haber borrado su run viejo.
	if removed != 1 {
		t.Errorf("sweep removed %d, want 1 (the good target's old run)", removed)
	}
	if _, err := os.Stat(filepath.Join(goodPath, oldFile)); !os.IsNotExist(err) {
		t.Error("good target old run should have been pruned despite the bad target failing")
	}
}
