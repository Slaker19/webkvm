package notify

import (
	"context"
	"log/slog"
	"time"
)

// Sources provides the runtime signals the alert engine reads. The
// Handler wires these closures so the engine stays decoupled and
// testable.
type Sources struct {
	// VMsNotRunning lists the names of VMs that are configured to
	// autostart but are currently not running (unexpected downtime).
	// Returned map key = VM name. Optional.
	VMState func() (map[string]bool, error) // name -> isRunning

	// DiskFreePercent returns the free space of the data dir as a
	// 0-100 percentage. Optional.
	DiskFreePercent func() (int, error)

	// LastBackupResult reports the most recent backup status per
	// target. Optional.
	LastBackupResult func() map[string]string // targetID -> "success"|"error"|""
}

// AlertEngine periodically evaluates conditions and emits alerts. It
// deduplicates by subject+level for a quiet window so a flapping VM
// doesn't spam the channels.
type AlertEngine struct {
	notifier *Notifier
	sources  Sources
	interval time.Duration

	// lastSeen dedupes alerts: map[subject+level] -> last time sent.
	lastSeen map[string]time.Time
	quiet    time.Duration

	logger *slog.Logger
}

// NewEngine builds an alert engine. quiet is the minimum time between
// repeats of the same alert.
func NewEngine(n *Notifier, sources Sources, interval time.Duration, quiet time.Duration, logger *slog.Logger) *AlertEngine {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if quiet <= 0 {
		quiet = 1 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AlertEngine{
		notifier: n,
		sources:  sources,
		interval: interval,
		lastSeen: map[string]time.Time{},
		quiet:    quiet,
		logger:   logger,
	}
}

// Run blocks, evaluating conditions every interval until ctx is done.
func (e *AlertEngine) Run(ctx context.Context) {
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.evaluate()
		}
	}
}

// Evaluate runs a single check pass (used by the test hook and the
// engine loop). Exported so tests can drive it directly.
func (e *AlertEngine) Evaluate() {
	e.evaluate()
}

func (e *AlertEngine) evaluate() {
	// VM downtime: autostart VMs that are not running.
	if e.sources.VMState != nil {
		states, err := e.sources.VMState()
		if err != nil {
			e.logger.Warn("alert_vmstate_failed", "err", err)
		} else {
			for name, running := range states {
				if !running {
					e.emit("warning", "VM "+name+" is not running", "A VM configured to autostart is currently down.")
				}
			}
		}
	}

	// Disk low.
	if e.sources.DiskFreePercent != nil {
		if pct, err := e.sources.DiskFreePercent(); err == nil {
			threshold := e.notifier.Status().Config.DiskFreePercent
			if threshold > 0 && pct < threshold {
				e.emit("critical", "Low disk space",
					fmtDiskAlert(threshold, pct))
			}
		}
	}

	// Backup failures.
	if e.sources.LastBackupResult != nil {
		for targetID, status := range e.sources.LastBackupResult() {
			if status == "error" {
				e.emit("warning", "Backup failed: "+targetID, "A backup run for target "+targetID+" failed.")
			}
		}
	}
}

// emit records + delivers an alert unless it was sent within the quiet
// window.
func (e *AlertEngine) emit(level, subject, message string) {
	key := level + "|" + subject
	now := time.Now()
	if last, ok := e.lastSeen[key]; ok && now.Sub(last) < e.quiet {
		return
	}
	e.lastSeen[key] = now
	e.notifier.Record(level, subject, message)
}

func fmtDiskAlert(threshold, pct int) string {
	return "Free disk space on the host is below the configured threshold (threshold " + itoa(threshold) + "%, current " + itoa(pct) + "%)."
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
