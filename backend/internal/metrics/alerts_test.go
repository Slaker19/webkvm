package metrics

import (
	"log/slog"
	"os"
	"testing"
	"time"

	"webkvm/internal/events"
	"webkvm/internal/models"
	"webkvm/internal/notify"
)

func newTestEngine(t *testing.T) *AlertEngine {
	t.Helper()
	dir := t.TempDir()
	hub := events.NewHub()
	n, err := notify.New(dir, notify.Config{Enabled: false}, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return NewAlertEngine(dir, n, hub)
}

func cpuRule(above bool, threshold float64, dur, cool int) AlertRule {
	return AlertRule{
		ID: "r1", VMID: "vm1", Metric: "cpu", Threshold: threshold, Above: above,
		Duration: dur, Cooldown: cool, Enabled: true,
	}
}

func cpuSample(v float64) models.VMMetrics {
	return models.VMMetrics{
		VMID: "vm1",
		CPU:  models.MetricsSeries{Kind: "cpu", Points: []models.MetricsSample{{T: 1, V: v}}},
	}
}

// A 1-second spike must NOT fire: the rule has to stay PENDING for the
// configured duration before it FIRES.
func TestAlert_SpikeDoesNotFire(t *testing.T) {
	e := newTestEngine(t)
	if err := e.SetRules("vm1", []AlertRule{cpuRule(true, 90, 300, 3600)}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	e.Evaluate("vm1", at, cpuSample(95))                    // crossed -> pending
	e.Evaluate("vm1", at.Add(5*time.Second), cpuSample(10)) // resolved
	if len(e.Active()) != 0 {
		t.Fatal("a resolved 5s spike must not fire")
	}
}

// Sustained crossing transitions pending -> firing exactly once.
func TestAlert_FiresAfterDuration(t *testing.T) {
	e := newTestEngine(t)
	rule := cpuRule(true, 90, 300, 3600)
	if err := e.SetRules("vm1", []AlertRule{rule}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	e.Evaluate("vm1", at, cpuSample(95)) // pending
	if len(e.Active()) != 0 {
		t.Fatal("should still be pending")
	}
	st := e.Statuses("vm1")
	if len(st) != 1 || st[0]["state"] != "pending" {
		t.Fatalf("expected pending status, got %+v", st)
	}
	// Well before duration: still pending.
	e.Evaluate("vm1", at.Add(1*time.Minute), cpuSample(96))
	if len(e.Active()) != 0 {
		t.Fatal("should still be pending after 1m (< 5m)")
	}
	// After duration: FIRING.
	e.Evaluate("vm1", at.Add(6*time.Minute), cpuSample(97))
	active := e.Active()
	if len(active) != 1 {
		t.Fatalf("expected 1 firing alert, got %d", len(active))
	}
	st = e.Statuses("vm1")
	if st[0]["state"] != "firing" {
		t.Fatalf("expected firing status, got %+v", st)
	}
}

// Cooldown: once firing, the same alert does not re-fire within the
// cooldown window even if the threshold stays crossed.
func TestAlert_CooldownSuppressesReFire(t *testing.T) {
	e := newTestEngine(t)
	if err := e.SetRules("vm1", []AlertRule{cpuRule(true, 90, 5, 3600)}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	e.Evaluate("vm1", at, cpuSample(95))
	e.Evaluate("vm1", at.Add(6*time.Second), cpuSample(95)) // duration (5s) elapsed -> fires
	if len(e.Active()) != 1 {
		t.Fatal("should fire once")
	}
	// Still crossed 1 hour later -> within cooldown (not re-armed yet):
	e.Evaluate("vm1", at.Add(30*time.Minute), cpuSample(95))
	if len(e.Active()) != 1 {
		t.Fatalf("cooldown violated: %d active", len(e.Active()))
	}
	// After cooldown, still crossed -> re-arms, needs duration again.
	e.Evaluate("vm1", at.Add(2*time.Hour), cpuSample(95))
	e.Evaluate("vm1", at.Add(2*time.Hour+10*time.Second), cpuSample(95))
	if len(e.Active()) != 1 {
		t.Fatal("should re-fire after cooldown+duration")
	}
}

// Resolution: dropping below the threshold returns the rule to idle and
// the next incident starts fresh.
func TestAlert_ResolvesToIdle(t *testing.T) {
	e := newTestEngine(t)
	if err := e.SetRules("vm1", []AlertRule{cpuRule(true, 90, 5, 3600)}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	e.Evaluate("vm1", at, cpuSample(95))
	e.Evaluate("vm1", at.Add(6*time.Second), cpuSample(95)) // fires
	e.Evaluate("vm1", at.Add(7*time.Second), cpuSample(10)) // resolved
	st := e.Statuses("vm1")
	if st[0]["state"] != "idle" {
		t.Fatalf("expected idle after resolution, got %+v", st)
	}
	if len(e.Active()) != 0 {
		t.Fatal("no active alerts after resolution")
	}
}

// Below-threshold rule fires when value drops under it.
func TestAlert_BelowThreshold(t *testing.T) {
	e := newTestEngine(t)
	rule := cpuRule(false, 10, 5, 3600) // alert when cpu < 10
	if err := e.SetRules("vm1", []AlertRule{rule}); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	e.Evaluate("vm1", at, cpuSample(5))                    // crossed (below) -> pending
	e.Evaluate("vm1", at.Add(6*time.Second), cpuSample(4)) // fires
	if len(e.Active()) != 1 {
		t.Fatalf("below-threshold rule should fire, got %d", len(e.Active()))
	}
}

// SetRules scopes rules to a VM; global (VMID empty) rules apply to all.
func TestAlert_RuleScoping(t *testing.T) {
	e := newTestEngine(t)
	if err := e.SetRules("vm1", []AlertRule{{ID: "a", VMID: "vm1", Metric: "cpu", Threshold: 90, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetRules("", []AlertRule{{ID: "g", Metric: "ram", Threshold: 80, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.RulesFor("vm1")); n != 2 {
		t.Fatalf("vm1 should see its own + global rules, got %d", n)
	}
	if n := len(e.RulesFor("vm2")); n != 1 {
		t.Fatalf("vm2 should see only the global rule, got %d", n)
	}
	// Replacing vm1 rules keeps the global one.
	if err := e.SetRules("vm1", []AlertRule{}); err != nil {
		t.Fatal(err)
	}
	if n := len(e.RulesFor("vm1")); n != 1 {
		t.Fatalf("global rule must survive a VM rules replace, got %d", n)
	}
}

// Rules persist to alerts.json and reload.
func TestAlert_Persists(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub()
	n, _ := notify.New(dir, notify.Config{Enabled: false}, slog.Default())
	e := NewAlertEngine(dir, n, hub)
	if err := e.SetRules("vm1", []AlertRule{cpuRule(true, 90, 300, 3600)}); err != nil {
		t.Fatal(err)
	}
	e2 := NewAlertEngine(dir, n, hub)
	if err := e2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := len(e2.RulesFor("vm1")); got != 1 {
		t.Fatalf("rules not persisted: %d", got)
	}
	if fi, err := os.Stat(e.path); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("alerts.json mode = %v, want 0600", fi.Mode().Perm())
	}
}

// Invalid rules are refused before touching the file.
func TestAlert_InvalidRuleRefused(t *testing.T) {
	e := newTestEngine(t)
	if err := e.SetRules("vm1", []AlertRule{{ID: "b", Metric: "banana", Threshold: 1, Enabled: true}}); err == nil {
		t.Fatal("invalid metric should be refused")
	}
	if err := e.SetRules("vm1", []AlertRule{{ID: "b", Metric: "cpu", Threshold: -1, Enabled: true}}); err == nil {
		t.Fatal("negative threshold should be refused")
	}
	if got := len(e.RulesFor("vm1")); got != 0 {
		t.Fatalf("no rules should be stored, got %d", got)
	}
}
