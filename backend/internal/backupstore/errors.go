package backupstore

import "errors"

// Sentinel errors used to communicate failure modes from the
// store / runner up to the HTTP layer. The handler maps these
// to the right HTTP status code with errors.Is — anything that
// isn't a sentinel falls through to 500.
//
// Why sentinels: before this commit every backup error came back
// as 500, including "target not found" (should be 404),
// "target is disabled" (409), "invalid cron" (400), and so on.
// The error string was already in the response body, so the UI
// could show the right message, but a 500 triggers browser
// network-tab alarms and breaks the contract of HTTP status
// codes. Sentinels let the handler pick the right code without
// string-matching the error.
var (
	// ErrTargetNotFound — the URL refers to a target that
	// doesn't exist (or was deleted). 404.
	ErrTargetNotFound = errors.New("target not found")

	// ErrTargetDisabled — the target exists but is paused.
	// Re-enable from the UI to fire backups. 409 Conflict.
	ErrTargetDisabled = errors.New("target is disabled")

	// ErrScheduleNotFound — the URL refers to a schedule that
	// doesn't exist. 404.
	ErrScheduleNotFound = errors.New("schedule not found")

	// ErrInvalidCron — the cron expression doesn't parse with
	// cron.ParseStandard. We surface this from CreateSchedule /
	// UpdateSchedule so the user gets a 400 at the moment of
	// submit, instead of a "schedule added" toast that never
	// fires. 400.
	ErrInvalidCron = errors.New("invalid cron expression")

	// ErrTargetPathUnwritable — the target's path couldn't be
	// created or written to. Raised by CreateTarget (during the
	// bootstrap MkdirAll) and by Runner.writeBackup (during the
	// pre-create of the archive). Distinct from ErrTargetNotFound
	// so the operator can tell "wrong URL" from "fix your mount".
	// 400.
	//
	// Wired up by the A2 commit (path-safety); declared here so
	// the HTTP status mapping in api/backup.go is complete.
	ErrTargetPathUnwritable = errors.New("target path is not writable")

	// ErrDiskFull — the pre-flight free-space check (added in
	// A8) estimated the run will not fit on the target's
	// filesystem. Distinct from the generic ENOSPC that comes
	// back from os.WriteFile 60s into a 5 GB copy, which is
	// what the v3 release surfaced when an operator pointed a
	// target at /tmp (3.6 GB tmpfs) on .130. 507 Insufficient
	// Storage.
	ErrDiskFull = errors.New("not enough free space on target")
)
