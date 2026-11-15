package hainstall

import (
	"context"
	"testing"
	"time"
)

// A host where HA was never installed must not report a failed step just
// because the packages are absent.
func TestReconcileDoesNotReportFailedStepOnCleanHost(t *testing.T) {
	fake := newFakeNode()
	fake.installed[Packages[0]] = true // partial: something is present, the rest is not
	m := New(t.TempDir(), fake.runner())

	status := m.Reconcile(context.Background())

	if status.State != StatePartial {
		t.Fatalf("state = %q, want %q", status.State, StatePartial)
	}
	if status.FailedStep != "" {
		t.Fatalf("failed_step = %q, want empty when nothing was installed", status.FailedStep)
	}
	for _, step := range status.Steps {
		if step.ID == "verify" && step.State == StepFailed {
			t.Fatalf("a clean host must not show a failed verify step: %+v", step)
		}
	}
}

// The UI polls the status endpoint; a repeated read must reuse the probe
// instead of appending another reconcile line every time.
func TestReconcileReusesRecentResult(t *testing.T) {
	m := New(t.TempDir(), newFakeNode().runner())

	first := m.Reconcile(context.Background())
	lines := len(first.Log)
	second := m.Reconcile(context.Background())

	if len(second.Log) != lines {
		t.Fatalf("log grew from %d to %d lines on a cached read", lines, len(second.Log))
	}
}

func TestReconcileExpiresAfterTTLAndVerifyForces(t *testing.T) {
	m := New(t.TempDir(), newFakeNode().runner())
	clock := time.Unix(1000, 0)
	m.now = func() time.Time { return clock }

	first := m.Reconcile(context.Background())
	lines := len(first.Log)

	clock = clock.Add(reconcileTTL + time.Second)
	second := m.Reconcile(context.Background())
	if len(second.Log) <= lines {
		t.Fatalf("log did not grow after the TTL: %d -> %d", lines, len(second.Log))
	}

	lines = len(second.Log)
	third := m.Verify(context.Background())
	if len(third.Log) <= lines {
		t.Fatalf("Verify reused the cache instead of re-probing: %d -> %d", lines, len(third.Log))
	}
}
