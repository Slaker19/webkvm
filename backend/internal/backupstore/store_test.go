package backupstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCreateDefault(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := s.ListTargets()
	if len(got) != 1 {
		t.Fatalf("expected default target, got %d", len(got))
	}
	if got[0].Name != "default" || got[0].Type != TargetLocal {
		t.Fatalf("unexpected default: %+v", got[0])
	}
}

func TestStoreCreateTarget(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, err := s.CreateTarget("nfs1", filepath.Join(dir, "nfs-mount"), TargetNFS, "all", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Name != "nfs1" || tgt.Type != TargetNFS {
		t.Fatalf("bad target: %+v", tgt)
	}
	if tgt.VMFilter != "all" {
		t.Fatalf("default VMFilter should be 'all', got %q", tgt.VMFilter)
	}
}

func TestStoreCreateTargetIncludeEmpty(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	// vm_filter=include with no vm_ids must be rejected — otherwise
	// the target would be created but back up nothing, which is a
	// confusing silent failure.
	if _, err := s.CreateTarget("only", filepath.Join(dir, "x"), TargetLocal, "include", nil); err == nil {
		t.Fatal("expected error for include with empty vm_ids")
	}
}

func TestStoreCreateTargetInvalidFilter(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if _, err := s.CreateTarget("bad", filepath.Join(dir, "x"), TargetLocal, "garbage", nil); err == nil {
		t.Fatal("expected error for invalid vm_filter")
	}
}

func TestStoreCreateDuplicateName(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, _ = s.CreateTarget("dup", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	if _, err := s.CreateTarget("dup", filepath.Join(dir, "y"), TargetLocal, "all", nil); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestStoreUpdateTarget(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, _ := s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	filter := "include"
	vmIDs := []string{"vm-1", "vm-2"}
	enabled := false
	renamed := "n1-renamed"
	newPath := filepath.Join(dir, "y")
	got, err := s.UpdateTarget(tgt.ID, &renamed, &newPath, nil, &filter, &vmIDs, &enabled, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "n1-renamed" || got.VMFilter != "include" || len(got.VMIDs) != 2 || got.Enabled {
		t.Fatalf("update: %+v", got)
	}
}

func TestStoreUpdateTargetIncludeEmpty(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, _ := s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	filter := "include"
	empty := []string{}
	if _, err := s.UpdateTarget(tgt.ID, nil, nil, nil, &filter, &empty, nil, nil); err == nil {
		t.Fatal("expected error setting vm_filter=include with empty vm_ids")
	}
}

func TestDeleteBackupFile(t *testing.T) {
	dir := t.TempDir()
	// Lay out a real-looking archive so ValidBackupFilename accepts it.
	if err := os.MkdirAll(filepath.Join(dir, "default"), 0o755); err != nil {
		t.Fatal(err)
	}
	realName := "webkvm-testhost-20260625T120000Z.tar.gz"
	if err := os.WriteFile(filepath.Join(dir, "default", realName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tgt := Target{ID: "default", Name: "default", Type: TargetLocal, Path: filepath.Join(dir, "default")}

	// Happy path.
	if err := DeleteBackupFile(tgt, realName); err != nil {
		t.Fatalf("expected delete to succeed, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "default", realName)); !os.IsNotExist(err) {
		t.Fatalf("file should be gone, stat err=%v", err)
	}

	// Bad filenames are rejected before any I/O.
	for _, bad := range []string{
		"../../etc/passwd",
		"foo.tar.gz",
		"webkvm-testhost-20260625T12000Z.tar.gz", // too short
		"webkvm-testhost-20260625T120000X.tar.gz", // bad timestamp suffix
		"webkvm-testhost-2026-06-25T12-00-00Z.tar.gz",
	} {
		if err := DeleteBackupFile(tgt, bad); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		}
	}
}

func TestStoreCannotDeleteDefault(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if err := s.DeleteTarget("default"); err == nil {
		t.Fatal("expected error")
	}
}

func TestStoreSchedule(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, _ := s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	sc, err := s.CreateSchedule("nightly", "0 2 * * *", tgt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Cron != "0 2 * * *" {
		t.Fatalf("bad schedule: %+v", sc)
	}
}

func TestStoreScheduleBadTarget(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if _, err := s.CreateSchedule("nightly", "0 2 * * *", "nonexistent"); err == nil {
		t.Fatal("expected error")
	}
}

func TestStoreJob(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	j := Job{TargetID: "default", StartedAt: time.Now(), Status: "running"}
	if _, err := s.RecordJob(j); err != nil {
		t.Fatal(err)
	}
	got := s.ListJobs(0)
	if len(got) != 1 {
		t.Fatalf("expected 1 job, got %d", len(got))
	}
}

// TestRecordJobReturnsGeneratedID guards the contract that the
// canonical job (with the generated ID) comes back to the caller.
// The runner relies on this to later call UpdateJob; without it,
// the in-memory map and the disk file would diverge — UpdateJob
// would return "job not found" silently, and the job would stay
// at status="running" forever.
func TestRecordJobReturnsGeneratedID(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	j := Job{TargetID: "default", Status: "running"}
	if j.ID != "" {
		t.Fatal("precondition: job ID should be empty")
	}
	out, err := s.RecordJob(j)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID == "" {
		t.Fatal("RecordJob must populate the generated ID in the returned job")
	}
	// And UpdateJob with the returned value must succeed.
	out.Status = "error"
	out.Error = "simulated"
	if err := s.UpdateJob(out); err != nil {
		t.Fatalf("UpdateJob on the returned job failed: %v", err)
	}
	got := s.ListJobs(0)
	if len(got) != 1 || got[0].Status != "error" {
		t.Fatalf("UpdateJob didn't take effect: %+v", got)
	}
}

// TestSweepStuckJobsOnLoad verifies that a job left in "running"
// state on disk is promoted to "error" when the store is
// reopened. Mirrors the real-world case where the backend was
// killed mid-backup (or the tar command bug left a job stuck
// forever) and the operator restarts the service.
func TestSweepStuckJobsOnLoad(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	stuck := Job{
		ID:        "j_stuck",
		TargetID:  "default",
		StartedAt: time.Now().Add(-10 * time.Minute).UTC(),
		Status:    "running",
	}
	if _, err := s.RecordJob(stuck); err != nil {
		t.Fatal(err)
	}

	// Reopen the store: the sweeper should mark the job as errored.
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	jobs := s2.ListJobs(0)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	got := jobs[0]
	if got.Status != "error" {
		t.Fatalf("stuck job should be swept to error, got status=%q", got.Status)
	}
	if got.EndedAt.IsZero() {
		t.Fatal("swept job must have EndedAt set")
	}
	if got.Error == "" {
		t.Fatal("swept job must carry an explanation in Error")
	}
}

// TestNoSweepForCompletedJobs makes sure the sweeper only touches
// jobs that are actually stuck, not every job with a non-success
// status. This guards against accidentally nuking the audit trail
// for jobs that ended normally with a real error.
func TestNoSweepForCompletedJobs(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	finished := Job{
		ID:        "j_done",
		TargetID:  "default",
		StartedAt: time.Now().Add(-5 * time.Minute).UTC(),
		EndedAt:   time.Now().Add(-4 * time.Minute).UTC(),
		Status:    "error",
		Error:     "tar exited 2",
	}
	if _, err := s.RecordJob(finished); err != nil {
		t.Fatal(err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	jobs := s2.ListJobs(0)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	if jobs[0].Error != "tar exited 2" {
		t.Fatalf("finished job Error got rewritten: %q", jobs[0].Error)
	}
}

func TestStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, _ = s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.ListTargets(); len(got) != 2 {
		t.Fatalf("after reopen: %d", len(got))
	}
}

func TestStoreFileLayout(t *testing.T) {
	dir := t.TempDir()
	_, _ = New(dir)
	if _, err := os.Stat(filepath.Join(dir, "backup", "targets.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "backup", "schedules.json")); err != nil {
		t.Fatal(err)
	}
}

// TestCreateScheduleInvalidCron guards the A1 fix for bug #2:
// the cron expression is now validated server-side at the moment
// of submit, so a "schedule added" toast no longer lies about a
// schedule that will never fire. The previous code accepted any
// non-empty string and let the cron library silently drop the
// schedule at Start() time.
func TestCreateScheduleInvalidCron(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, _ := s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	for _, bad := range []string{
		"not a cron",
		"* * * *",    // 4 fields instead of 5
		"60 0 * * *", // minute out of range
		"0 25 * * *", // hour out of range
		"0 0 32 * *", // day out of range
		"0 0 * 13 *", // month out of range
		"0 0 * * 8",  // dow out of range
	} {
		if _, err := s.CreateSchedule("sc-"+bad, bad, tgt.ID); err == nil {
			t.Errorf("expected error for %q, got nil", bad)
		} else if !errors.Is(err, ErrInvalidCron) {
			t.Errorf("expected ErrInvalidCron for %q, got %v", bad, err)
		}
	}
}

// TestUpdateScheduleInvalidCron is the same guard for the
// update path: a PATCH with a garbage cron must return 400
// without mutating the existing schedule.
func TestUpdateScheduleInvalidCron(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, _ := s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	sc, err := s.CreateSchedule("nightly", "0 2 * * *", tgt.ID)
	if err != nil {
		t.Fatal(err)
	}
	garbage := "garbage"
	_, err = s.UpdateSchedule(sc.ID, nil, &garbage, nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid cron update")
	}
	if !errors.Is(err, ErrInvalidCron) {
		t.Fatalf("expected ErrInvalidCron, got %v", err)
	}
	got, _ := s.GetSchedule(sc.ID)
	if got.Cron != "0 2 * * *" {
		t.Fatalf("UpdateSchedule mutated cron on error: %q", got.Cron)
	}
}

// TestSweepStuckJobsNormalisesRunningWithEndedAt covers bug #3:
// a job with status="running" AND a non-zero EndedAt is a
// contradiction (EndedAt is set on terminal status). The previous
// code `continue`d on this case, leaving the contradictory record
// in jobs.json forever. We now normalise it like any other stuck
// job and make sure it survives a reopen.
func TestSweepStuckJobsNormalisesRunningWithEndedAt(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	contradictory := Job{
		ID:        "j_contradictory",
		TargetID:  "default",
		StartedAt: time.Now().Add(-30 * time.Minute).UTC(),
		EndedAt:   time.Now().Add(-25 * time.Minute).UTC(), // contradictory
		Status:    "running",
	}
	if _, err := s.RecordJob(contradictory); err != nil {
		t.Fatal(err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	jobs := s2.ListJobs(0)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(jobs))
	}
	got := jobs[0]
	if got.Status != "error" {
		t.Fatalf("contradictory job should be normalised to error, got status=%q", got.Status)
	}
	if got.Error == "" {
		t.Fatal("normalised job must carry an explanation")
	}
	if !got.EndedAt.Equal(contradictory.EndedAt) {
		t.Fatalf("normalised EndedAt changed: was %v, now %v", contradictory.EndedAt, got.EndedAt)
	}
}

// TestSetScheduleLastRunReturnsError covers bug #9: the previous
// signature returned nothing and silently swallowed a save
// failure. The new signature returns an error so the runner can
// log it; "schedule not found" now bubbles up too.
func TestSetScheduleLastRunReturnsError(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, _ := s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	sc, _ := s.CreateSchedule("nightly", "0 2 * * *", tgt.ID)

	if err := s.SetScheduleLastRun(sc.ID, "success", "", time.Time{}); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	got, _ := s.GetSchedule(sc.ID)
	if got.LastStatus != "success" || got.LastError != "" {
		t.Fatalf("happy path: status=%q error=%q", got.LastStatus, got.LastError)
	}

	err := s.SetScheduleLastRun("s_does_not_exist", "error", "x", time.Time{})
	if err == nil {
		t.Fatal("expected error for unknown schedule id")
	}
	if !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("expected ErrScheduleNotFound, got %v", err)
	}
}

// TestUpdateScheduleAndTargetUseErrTargetNotFound guards the
// sentinel swap: previously the store returned a string
// "target not found" that was indistinguishable from any other
// 4xx. Now it returns ErrTargetNotFound / ErrScheduleNotFound,
// which the HTTP handler maps to 404.
func TestUpdateScheduleAndTargetUseErrTargetNotFound(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, err := s.UpdateTarget("t_does_not_exist", nil, nil, nil, nil, nil, nil, nil)
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("UpdateTarget: expected ErrTargetNotFound, got %v", err)
	}
	_, err = s.UpdateSchedule("s_does_not_exist", nil, nil, nil, nil)
	if !errors.Is(err, ErrScheduleNotFound) {
		t.Fatalf("UpdateSchedule: expected ErrScheduleNotFound, got %v", err)
	}
}

// TestUpdateTargetPointerConvention covers the A4 fix for bug
// #7: a real partial update (just toggling Enabled=false) must
// reach the store. Under the old "empty string = don't change"
// convention this was indistinguishable from "leave it alone",
// so the operator's "disable this target" button silently
// did nothing.
func TestUpdateTargetPointerConvention(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, _ := s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	if !tgt.Enabled {
		t.Fatal("precondition: target should be enabled after Create")
	}

	// Only set Enabled=false; everything else nil.
	off := false
	got, err := s.UpdateTarget(tgt.ID, nil, nil, nil, nil, nil, &off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("UpdateTarget should have set Enabled=false")
	}
	if got.Name != tgt.Name {
		t.Fatalf("UpdateTarget mutated Name: was %q, now %q", tgt.Name, got.Name)
	}
	if got.Path != tgt.Path {
		t.Fatalf("UpdateTarget mutated Path: was %q, now %q", tgt.Path, got.Path)
	}

	// And re-enable with the same pointer convention.
	on := true
	got, err = s.UpdateTarget(tgt.ID, nil, nil, nil, nil, nil, &on, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled {
		t.Fatal("UpdateTarget should have set Enabled=true")
	}
}

// TestUpdateSchedulePointerConvention is the parallel for the
// schedule edit path. A future "edit schedule" UI will send
// partial updates; under the new convention, nil = leave
// alone and *x = set, exactly matching the target edit
// semantics.
func TestUpdateSchedulePointerConvention(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, _ := s.CreateTarget("n1", filepath.Join(dir, "x"), TargetLocal, "all", nil)
	sc, _ := s.CreateSchedule("nightly", "0 2 * * *", tgt.ID)
	if !sc.Enabled {
		t.Fatal("precondition: schedule should be enabled after Create")
	}

	// Just toggle Enabled=false.
	off := false
	got, err := s.UpdateSchedule(sc.ID, nil, nil, nil, &off)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Fatal("UpdateSchedule should have set Enabled=false")
	}
	if got.Cron != sc.Cron {
		t.Fatalf("UpdateSchedule mutated Cron on partial update: was %q, now %q", sc.Cron, got.Cron)
	}
	if got.Name != sc.Name {
		t.Fatalf("UpdateSchedule mutated Name on partial update: was %q, now %q", sc.Name, got.Name)
	}

	// Switch cron with a different valid value; everything else nil.
	// The Enabled state from the previous partial update (false)
	// must survive this call because we pass enabled=nil.
	newCron := "0 3 * * *"
	got, err = s.UpdateSchedule(sc.ID, nil, &newCron, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Cron != "0 3 * * *" {
		t.Fatalf("UpdateSchedule should have set Cron=%q, got %q", newCron, got.Cron)
	}
	if got.Enabled {
		t.Fatal("UpdateSchedule mutated Enabled on partial update (was false from prev toggle)")
	}
}
