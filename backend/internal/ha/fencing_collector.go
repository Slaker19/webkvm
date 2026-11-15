package ha

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"webkvm/internal/sysutil"
)

// FencingCollector feeds the fencing evidence ledger in the background so a
// real fence event is captured even when nobody opens the readiness page
// before Pacemaker's in-memory history is lost (e.g. a cluster restart).
type FencingCollector struct {
	DataDir string
	// Enabled gates every probe (modules.cluster_ha); when false nothing is
	// executed.
	Enabled func() bool
	Runner  sysutil.CommandRunner
	Logger  *slog.Logger

	Interval    time.Duration // default 60s
	FirstDelay  time.Duration // default 10s
	MaxBackoff  time.Duration // default 5min
	WarnAfter   int           // consecutive failures before WARN, default 5
	Now         func() time.Time
	After       func(time.Duration) <-chan time.Time
	running     atomic.Bool
	failures    int
	warnedOnce  bool
	lastWait    time.Duration
	ProbeStatus func(context.Context) PacemakerStatus // test hook; default ProbePacemaker
}

func (c *FencingCollector) defaults() {
	if c.Interval <= 0 {
		c.Interval = 60 * time.Second
	}
	if c.FirstDelay <= 0 {
		c.FirstDelay = 10 * time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 5 * time.Minute
	}
	if c.WarnAfter <= 0 {
		c.WarnAfter = 5
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.After == nil {
		c.After = time.After
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.ProbeStatus == nil {
		runner := c.Runner
		if runner == nil {
			runner = sysutil.DefaultRunner
		}
		c.ProbeStatus = func(ctx context.Context) PacemakerStatus { return ProbePacemaker(ctx, runner) }
	}
}

// Start runs the loop until ctx is cancelled. It returns a channel closed when
// the goroutine has exited; a second Start while running is a no-op (closed
// channel returned).
func (c *FencingCollector) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	if !c.running.CompareAndSwap(false, true) {
		close(done)
		return done
	}
	c.defaults()
	go func() {
		defer close(done)
		defer c.running.Store(false)
		wait := c.FirstDelay
		for {
			select {
			case <-ctx.Done():
				return
			case <-c.After(wait):
			}
			wait = c.next(c.Tick(ctx))
		}
	}()
	return done
}

// next computes the delay after a tick, backing off after repeated failures.
func (c *FencingCollector) next(failed bool) time.Duration {
	if !failed {
		c.failures, c.warnedOnce = 0, false
		c.lastWait = c.Interval
		return c.Interval
	}
	c.failures++
	d := c.Interval
	if c.failures >= 3 {
		d = c.Interval << uint(c.failures-2)
		if d > c.MaxBackoff || d <= 0 {
			d = c.MaxBackoff
		}
	}
	c.lastWait = d
	return d
}

// Tick performs one collection and reports whether the probe failed. It never
// execs anything while the module is disabled.
func (c *FencingCollector) Tick(ctx context.Context) (failed bool) {
	c.defaults()
	if c.Enabled == nil || !c.Enabled() {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st := c.ProbeStatus(cctx)
	switch st.State {
	case StateObserved, StateBlocked:
		n := len(MergeFencingLedger(c.DataDir, st, c.Now()))
		c.Logger.Debug("ha_fencing_ledger_collected", "events", n)
		return false
	case StateError:
		if c.failures+1 >= c.WarnAfter && !c.warnedOnce {
			c.warnedOnce = true
			c.Logger.Warn("ha_fencing_ledger_probe_failing", "consecutive_failures", c.failures+1)
		} else {
			c.Logger.Debug("ha_fencing_ledger_probe_failed")
		}
		return true
	}
	return false // not installed / not configured: nothing to collect
}
