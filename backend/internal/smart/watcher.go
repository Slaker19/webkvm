package smart

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// AlertFunc callback invoked when a disk health degradation is detected.
type AlertFunc func(level, subject, msg string)

// Watcher runs background periodic SMART health audits on all physical host disks.
type Watcher struct {
	interval time.Duration
	alertFn  AlertFunc
	logger   *slog.Logger
	stopCh   chan struct{}
}

// NewWatcher creates a new background SMART health monitor.
func NewWatcher(interval time.Duration, alertFn AlertFunc, logger *slog.Logger) *Watcher {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Watcher{
		interval: interval,
		alertFn:  alertFn,
		logger:   logger,
		stopCh:   make(chan struct{}),
	}
}

// Start begins periodic background SMART sweeps.
func (w *Watcher) Start(ctx context.Context) {
	if !IsAvailable() {
		w.logger.Info("smart_watcher_disabled", "reason", "smartctl not installed")
		return
	}
	w.logger.Info("smart_watcher_started", "interval", w.interval.String())

	// Run initial audit in background shortly after startup
	go func() {
		select {
		case <-time.After(30 * time.Second):
			w.audit(ctx)
		case <-w.stopCh:
			return
		case <-ctx.Done():
			return
		}

		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				w.audit(ctx)
			case <-w.stopCh:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Stop shuts down the watcher loop.
func (w *Watcher) Stop() {
	select {
	case <-w.stopCh:
	default:
		close(w.stopCh)
	}
}

// audit sweeps all physical disks on the host and checks for warning or failure conditions.
func (w *Watcher) audit(ctx context.Context) {
	disks := ListPhysicalDisks()
	if len(disks) == 0 {
		return
	}

	for _, dev := range disks {
		// Invalidate cache before scheduled audit to ensure fresh metrics from disk
		InvalidateCache(dev)
		info, err := Probe(ctx, dev)
		if err != nil {
			w.logger.Debug("smart_watcher_probe_skipped", "dev", dev, "err", err)
			continue
		}
		if !info.Available {
			continue
		}

		if info.Status == "FAILED" || !info.Healthy {
			subject := fmt.Sprintf("CRITICAL: S.M.A.R.T. failure on %s", dev)
			msg := fmt.Sprintf("Physical disk %s (%s, S/N %s) reports S.M.A.R.T. health failure! Status=%s. Replace immediately.",
				dev, info.Model, info.Serial, info.Status)
			w.logger.Error("smart_health_failure", "dev", dev, "model", info.Model, "status", info.Status)
			if w.alertFn != nil {
				w.alertFn("critical", subject, msg)
			}
		} else if info.Status == "WARNING" || info.ReallocatedSectors > 50 || info.TemperatureC >= 60 {
			subject := fmt.Sprintf("WARNING: S.M.A.R.T. health warning on %s", dev)
			msg := fmt.Sprintf("Physical disk %s (%s, S/N %s) has warning indicators: Status=%s, Temp=%d°C, ReallocatedSectors=%d.",
				dev, info.Model, info.Serial, info.Status, info.TemperatureC, info.ReallocatedSectors)
			w.logger.Warn("smart_health_warning", "dev", dev, "model", info.Model, "status", info.Status, "temp", info.TemperatureC, "reallocated", info.ReallocatedSectors)
			if w.alertFn != nil {
				w.alertFn("warning", subject, msg)
			}
		}
	}
}
