package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"webkvm/internal/events"
	"webkvm/internal/models"
	"webkvm/internal/notify"
)

// Alerting (V13-C-04).
//
// Anti-spam state machine per (rule, VM):
//
//	threshold NOT crossed           -> idle
//	threshold crossed (first time)  -> pending  (pendingSince = now)
//	crossed and pendingSince + duration elapsed  -> FIRING (notify once)
//	still crossed, within cooldown  -> stays FIRING, no repeat
//	still crossed, cooldown elapsed -> re-arms to pending (re-fires after
//	                                   duration only when cooldown permits)
//	threshold no longer crossed      -> idle
//
// A single incident therefore fires at most once per ~cooldown, and a
// 1-second spike can never fire anything: a PENDING rule must hold the
// threshold for the configured duration (default 5 min).

// Defaults for rule fields when left zero.
const (
	DefaultAlertDuration = 5 * time.Minute
	DefaultAlertCooldown = time.Hour
)

// AlertRule is one threshold rule. VMID empty = applies to every VM.
type AlertRule struct {
	ID        string  `json:"id"`
	VMID      string  `json:"vm_id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Metric    string  `json:"metric"` // cpu | ram | disk_r | disk_w | net_rx | net_tx
	Threshold float64 `json:"threshold"`
	Above     bool    `json:"above"`                   // true = alert when value > threshold; false = when <
	Duration  int     `json:"duration_secs,omitempty"` // PENDING->FIRING window
	Cooldown  int     `json:"cooldown_secs,omitempty"` // min gap between fires
	Enabled   bool    `json:"enabled"`
}

// duration returns the PENDING->FIRING window with its default.
func (r AlertRule) duration() time.Duration {
	if r.Duration > 0 {
		return time.Duration(r.Duration) * time.Second
	}
	return DefaultAlertDuration
}

// cooldown returns the re-fire gap with its default.
func (r AlertRule) cooldown() time.Duration {
	if r.Cooldown > 0 {
		return time.Duration(r.Cooldown) * time.Second
	}
	return DefaultAlertCooldown
}

// Validate checks a rule's shape.
func (r AlertRule) Validate() error {
	switch r.Metric {
	case "cpu", "ram", "disk_r", "disk_w", "net_rx", "net_tx":
	default:
		return fmt.Errorf("invalid alert metric %q", r.Metric)
	}
	if r.Threshold < 0 {
		return errors.New("alert threshold cannot be negative")
	}
	return nil
}

// ruleState is the runtime state machine for one (rule, VM) pair.
type ruleState struct {
	state        string // idle | pending | firing
	pendingSince time.Time
	lastFired    time.Time
	lastValue    float64
}

// AlertEngine evaluates rules against every sample and fires alerts
// through the notifier + event hub. Rules are persisted to
// {dataDir}/alerts.json (a tiny config file — NOT metric data).
type AlertEngine struct {
	dir  string
	path string

	mu     sync.Mutex
	rules  []AlertRule
	states map[string]*ruleState

	notify *notify.Notifier
	hub    *events.Hub
}

// NewAlertEngine wires the engine to the notifier and event hub.
func NewAlertEngine(dataDir string, notifier *notify.Notifier, hub *events.Hub) *AlertEngine {
	return &AlertEngine{
		dir:    dataDir,
		path:   filepath.Join(dataDir, "alerts.json"),
		states: map[string]*ruleState{},
		notify: notifier,
		hub:    hub,
	}
}

// Load reads the rules file (missing = no rules).
func (e *AlertEngine) Load() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	data, err := os.ReadFile(e.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var file struct {
		Version int         `json:"version"`
		Rules   []AlertRule `json:"rules"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	e.rules = file.Rules
	return nil
}

// Save persists the rules atomically (0600).
func (e *AlertEngine) Save() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.saveLocked()
}

func (e *AlertEngine) saveLocked() error {
	_ = os.MkdirAll(e.dir, 0o700)
	data, err := json.MarshalIndent(map[string]any{"version": 1, "rules": e.rules}, "", "  ")
	if err != nil {
		return err
	}
	tmp := e.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, e.path)
}

// SetRules replaces the rules for one VM (or all, when vmID == ""), keeping
// the others. Persists. Also prunes state-machine entries whose rule no
// longer exists so /alerts/active never reports a deleted rule.
func (e *AlertEngine) SetRules(vmID string, rules []AlertRule) error {
	for _, r := range rules {
		if err := r.Validate(); err != nil {
			return err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var kept []AlertRule
	for _, r := range e.rules {
		if r.VMID != vmID {
			kept = append(kept, r)
		}
	}
	kept = append(kept, rules...)
	e.rules = kept
	e.pruneStatesLocked()
	return e.saveLocked()
}

// pruneStatesLocked drops state-machine entries whose rule is no longer
// present (or disabled). Caller holds e.mu.
func (e *AlertEngine) pruneStatesLocked() {
	live := map[string]bool{}
	for _, r := range e.rules {
		if r.Enabled {
			live[r.ID] = true
		}
	}
	for key := range e.states {
		ruleID, _, ok := splitRuleKey(key)
		if !ok || !live[ruleID] {
			delete(e.states, key)
		}
	}
}

// RulesFor returns the rules applicable to a VM (its own + global).
func (e *AlertEngine) RulesFor(vmID string) []AlertRule {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]AlertRule, 0, len(e.rules))
	for _, r := range e.rules {
		if r.VMID == "" || r.VMID == vmID {
			out = append(out, r)
		}
	}
	return out
}

// Statuses returns the live state machine for the rules of a VM.
func (e *AlertEngine) Statuses(vmID string) []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []map[string]any{}
	for _, r := range e.rules {
		if r.VMID != "" && r.VMID != vmID {
			continue
		}
		st := e.states[ruleKey(r.ID, vmID)]
		status := map[string]any{
			"rule":  r,
			"state": "idle",
		}
		if st != nil {
			status["state"] = st.state
			status["value"] = st.lastValue
			if !st.pendingSince.IsZero() {
				status["pending_since"] = st.pendingSince.Unix()
			}
			if !st.lastFired.IsZero() {
				status["last_fired"] = st.lastFired.Unix()
			}
		}
		out = append(out, status)
	}
	return out
}

// Active returns the currently FIRING alerts (rule + VM + value) across
// all VMs, for the UI badge / notification center. Stale states whose
// rule was removed or disabled are skipped.
func (e *AlertEngine) Active() []map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := []map[string]any{}
	for key, st := range e.states {
		if st.state != "firing" {
			continue
		}
		ruleID, vmID, ok := splitRuleKey(key)
		if !ok {
			continue
		}
		var rule AlertRule
		found := false
		for _, r := range e.rules {
			if r.ID == ruleID {
				rule = r
				found = true
				break
			}
		}
		// The rule was deleted or disabled: the state is stale, drop it.
		if !found || !rule.Enabled {
			delete(e.states, key)
			continue
		}
		out = append(out, map[string]any{
			"rule":  rule,
			"vm_id": vmID,
			"value": st.lastValue,
			"since": st.lastFired.Unix(),
		})
	}
	return out
}

// Evaluate runs the state machine for every applicable rule against one
// sample set. Called from the collector sink every sampling tick.
func (e *AlertEngine) Evaluate(vmID string, at time.Time, m models.VMMetrics) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range e.rules {
		r := &e.rules[i]
		if r.VMID != "" && r.VMID != vmID {
			continue
		}
		if !r.Enabled {
			continue
		}
		v, ok := metricValue(m, r.Metric)
		if !ok {
			continue
		}
		key := ruleKey(r.ID, vmID)
		st := e.states[key]
		if st == nil {
			st = &ruleState{state: "idle"}
			e.states[key] = st
		}
		st.lastValue = v
		crossed := v > r.Threshold
		if !r.Above {
			crossed = v < r.Threshold
		}

		switch st.state {
		case "idle":
			if crossed {
				st.state = "pending"
				st.pendingSince = at
			}
		case "pending":
			if !crossed {
				st.state = "idle"
				continue
			}
			if at.Sub(st.pendingSince) >= r.duration() && at.Sub(st.lastFired) >= r.cooldown() {
				st.state = "firing"
				st.lastFired = at
				e.fireLocked(*r, vmID, v, at)
			}
		case "firing":
			if !crossed {
				st.state = "idle"
				continue
			}
			if at.Sub(st.lastFired) >= r.cooldown() {
				// Cooldown elapsed while still crossing: re-arm and let
				// the duration window elapse before firing again.
				st.state = "pending"
				st.pendingSince = at
			}
		}
	}
}

// fireLocked emits one alert through the notifier and the SSE hub. The
// engine mutex is held by the caller.
func (e *AlertEngine) fireLocked(rule AlertRule, vmID string, value float64, at time.Time) {
	subject := "VM alert"
	message := fmt.Sprintf("%s exceeded threshold (%.1f %s%s, got %.1f)",
		ruleName(rule, vmID), rule.Threshold, strings.ToUpper(rule.Metric),
		cmpWord(rule.Above), value)
	if e.notify != nil {
		e.notify.Record("alert", subject, message)
	}
	if e.hub != nil {
		e.hub.Broadcast(events.Event{
			Type:      "vm.alert",
			VmID:      vmID,
			Timestamp: at.Unix(),
			Data: map[string]any{
				"rule_id": rule.ID, "metric": rule.Metric, "value": value,
				"threshold": rule.Threshold, "above": rule.Above, "state": "firing",
			},
		})
	}
}

func ruleName(rule AlertRule, vmID string) string {
	if rule.Name != "" {
		return rule.Name
	}
	if rule.VMID == "" {
		return "all VMs"
	}
	return vmID
}

func cmpWord(above bool) string {
	if above {
		return ">"
	}
	return "<"
}

// metricValue extracts the latest value for a metric kind from a sample set.
func metricValue(m models.VMMetrics, kind string) (float64, bool) {
	var s models.MetricsSeries
	switch kind {
	case "cpu":
		s = m.CPU
	case "ram":
		s = m.RAM
	case "disk_r":
		s = m.DiskRead
	case "disk_w":
		s = m.DiskWrite
	case "net_rx":
		s = m.NetRx
	case "net_tx":
		s = m.NetTx
	default:
		return 0, false
	}
	if len(s.Points) == 0 {
		return 0, false
	}
	return s.Points[len(s.Points)-1].V, true
}

func ruleKey(ruleID, vmID string) string {
	return ruleID + "/" + vmID
}

func splitRuleKey(key string) (string, string, bool) {
	i := strings.LastIndexByte(key, '/')
	if i < 0 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}
