package ha

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"webkvm/internal/sysutil"
)

// PacemakerNodeRE is the only shape of node name that reaches a pcs argv.
var PacemakerNodeRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// Refusal codes of the fencing test and standby operations.
const (
	RefConfirmRequired   = "confirm_required"
	RefPacemakerDown     = "pacemaker_unavailable"
	RefQuorumMissing     = "quorum_missing"
	RefNotEnoughNodes    = "not_enough_nodes"
	RefNodeUnknown       = "node_unknown"
	RefNodeOffline       = "node_offline"
	RefControlPlane      = "target_is_control_plane"
	RefNoFencing         = "fencing_not_enabled"
	RefFenceInProgress   = "fence_in_progress"
	RefWouldLoseQuorum   = "would_lose_quorum"
	RefLastOnlineNode    = "last_online_node"
	RefWouldLoseQuorumAf = "would_lose_quorum_after_fence"
	// RefWatchdogOnlyUnreliable: with watchdog-only SBD `pcs stonith fence`
	// of a live node only makes Pacemaker assume it self-fenced after the
	// timeout; the node does not reboot, so the test proves nothing.
	RefWatchdogOnlyUnreliable = "watchdog_only_unreliable"
)

// Explanations carried next to RefWatchdogOnlyUnreliable.
const (
	ExplSelfFenceAssumed = "self_fence_assumed"
	ExplNodeHasResources = "node_has_resources"
)

// SimulateFailureCommand crashes the kernel of the node it runs on so the
// watchdog/Pacemaker fence it for real. Run on the TARGET node, by hand.
const SimulateFailureCommand = `sudo sh -c 'echo 1 > /proc/sys/kernel/sysrq; echo c > /proc/sysrq-trigger'`

// FenceOptions are the optional switches of a fencing test.
type FenceOptions struct {
	AllowControlPlane bool
	// AcknowledgeUnreliable lets a watchdog-only direct fence run, but only
	// for a node with no resources; the result is never proof of fencing.
	AcknowledgeUnreliable bool
	CPGroup               string
}

func nodeHasResources(st PacemakerStatus, node string) bool {
	for _, r := range st.Resources {
		if r.Node == node && r.Active {
			return true
		}
	}
	return false
}

// WatchdogOnlyExplanation says why a direct fence is refused in
// watchdog_only mode ("" when it is not refused for that reason).
func WatchdogOnlyExplanation(st PacemakerStatus, node string, acknowledged bool) string {
	if FencingMode(st) != FencingModeWatchdogOnly {
		return ""
	}
	if t, ok := nodeState(st, node); !ok || !t.Online {
		return ""
	}
	if nodeHasResources(st, node) {
		return ExplNodeHasResources
	}
	if !acknowledged {
		return ExplSelfFenceAssumed
	}
	return ""
}

// FenceConfirmation is the exact string the operator must type.
func FenceConfirmation(node string) string { return "FENCE " + node }

func nodeState(st PacemakerStatus, name string) (PacemakerNode, bool) {
	for _, n := range st.NodeStates {
		if n.Name == name {
			return n, true
		}
	}
	for _, n := range st.NodeNames {
		if n == name {
			return PacemakerNode{Name: name}, true // known, state unknown: not online
		}
	}
	return PacemakerNode{}, false
}

// controlPlaneNodes lists the nodes running a member of the control-plane
// group. The group is matched by exact id (setting ha.cp_group, default
// webkvm-cp), never by substring: a VM called "webkvm-x" is not the control
// plane.
func controlPlaneNodes(st PacemakerStatus, cpGroup string) map[string]bool {
	cpGroup = normCPGroup(cpGroup)
	out := map[string]bool{}
	for _, r := range st.Resources {
		if r.Node != "" && r.Active && r.Group == cpGroup {
			out[r.Node] = true
		}
	}
	return out
}

func firstGroup(cpGroup []string) string {
	if len(cpGroup) > 0 {
		return cpGroup[0]
	}
	return ""
}

// ControlPlaneNode returns the node currently running the WebKVM control
// plane (same rule FenceBlockers uses to refuse fencing it), or "" when no
// active control plane resource is reported. When several nodes match, the
// lexicographically smallest name is returned so the answer is stable.
// cpGroup is optional and defaults to DefaultCPGroup.
func ControlPlaneNode(st PacemakerStatus, cpGroup ...string) string {
	best := ""
	for name := range controlPlaneNodes(st, firstGroup(cpGroup)) {
		if best == "" || name < best {
			best = name
		}
	}
	return best
}

// FenceBlockers evaluates every precondition of a fencing test. Empty means
// the test may run. Pure: no I/O.
func FenceBlockers(st PacemakerStatus, node, confirm string, allowControlPlane bool, cpGroup ...string) []string {
	return FenceBlockersOpts(st, node, confirm, FenceOptions{AllowControlPlane: allowControlPlane, CPGroup: firstGroup(cpGroup)})
}

// FenceBlockersOpts is FenceBlockers with the full option set.
func FenceBlockersOpts(st PacemakerStatus, node, confirm string, o FenceOptions) []string {
	allowControlPlane := o.AllowControlPlane
	b := []string{}
	if confirm != FenceConfirmation(node) {
		b = append(b, RefConfirmRequired)
	}
	if st.State != StateObserved && st.State != StateBlocked {
		return append(b, RefPacemakerDown)
	}
	if st.Quorum == nil || !*st.Quorum {
		b = append(b, RefQuorumMissing)
	}
	if st.NodesOnline < 3 {
		b = append(b, RefNotEnoughNodes)
	}
	target, ok := nodeState(st, node)
	switch {
	case !ok:
		b = append(b, RefNodeUnknown)
	case !target.Online:
		b = append(b, RefNodeOffline)
	default:
		if controlPlaneNodes(st, o.CPGroup)[node] && !allowControlPlane {
			b = append(b, RefControlPlane)
		}
		if st.NodesOnline-1 <= st.NodesConfigured/2 {
			b = append(b, RefWouldLoseQuorumAf)
		}
		if WatchdogOnlyExplanation(st, node, o.AcknowledgeUnreliable) != "" {
			b = append(b, RefWatchdogOnlyUnreliable)
		}
	}
	watchdog := st.WatchdogFencing != nil && *st.WatchdogFencing
	if !watchdog && st.FenceDevices == 0 {
		b = append(b, RefNoFencing)
	} else if st.FencingEnabled == nil || !*st.FencingEnabled {
		b = append(b, RefNoFencing)
	}
	return b
}

// StandbyBlockers evaluates a standby request. unstandby never blocks beyond
// the node existing (checked by the caller).
func StandbyBlockers(st PacemakerStatus, node string) []string {
	if st.State != StateObserved && st.State != StateBlocked {
		return []string{RefPacemakerDown}
	}
	target, ok := nodeState(st, node)
	if !ok {
		return []string{RefNodeUnknown}
	}
	active := 0
	for _, n := range st.NodeStates {
		if n.Online && !n.Standby {
			active++
		}
	}
	if len(st.NodeStates) == 0 {
		active = st.NodesOnline
		target.Online = true
	}
	if target.Online && !target.Standby {
		active--
	}
	b := []string{}
	if active <= 0 {
		b = append(b, RefLastOnlineNode)
	}
	if active < st.NodesConfigured/2+1 {
		b = append(b, RefWouldLoseQuorum)
	}
	return b
}

// FenceTester serialises fencing tests inside this process.
type FenceTester struct{ mu sync.Mutex }

// FenceResult is the 200 body of POST /api/ha/fencing/test.
type FenceResult struct {
	OK       bool      `json:"ok"`
	Node     string    `json:"node"`
	FencedAt time.Time `json:"fenced_at"`
	History  []string  `json:"history"`
	// Mode is the fencing mode at the time of the test. A result is never
	// proof of fencing in watchdog_only mode.
	Mode string `json:"mode,omitempty"`
}

// Fence runs `pcs stonith fence <node>` if every precondition holds. blockers
// is non-empty when it refused (nothing was executed).
func (f *FenceTester) Fence(ctx context.Context, runner sysutil.CommandRunner, node, confirm string, allowControlPlane bool, cpGroup ...string) (res FenceResult, blockers []string, st PacemakerStatus) {
	return f.FenceWith(ctx, runner, node, confirm, FenceOptions{AllowControlPlane: allowControlPlane, CPGroup: firstGroup(cpGroup)})
}

// FenceWith is Fence with the full option set.
func (f *FenceTester) FenceWith(ctx context.Context, runner sysutil.CommandRunner, node, confirm string, o FenceOptions) (res FenceResult, blockers []string, st PacemakerStatus) {
	if !PacemakerNodeRE.MatchString(node) {
		return res, []string{RefNodeUnknown}, st
	}
	if !f.mu.TryLock() {
		return res, []string{RefFenceInProgress}, st
	}
	defer f.mu.Unlock()
	st = ProbePacemaker(ctx, runner)
	if blockers = FenceBlockersOpts(st, node, confirm, o); len(blockers) > 0 {
		return res, blockers, st
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	_, err := runner.Run(cctx, "pcs", "stonith", "fence", node)
	res = FenceResult{OK: err == nil, Node: node, FencedAt: time.Now().UTC(), History: []string{}, Mode: FencingMode(st)}
	hctx, hcancel := context.WithTimeout(ctx, 15*time.Second)
	defer hcancel()
	if out, herr := runner.Run(hctx, "pcs", "stonith", "history", "show", node); herr == nil {
		lines := []string{}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) > 10 {
			lines = lines[len(lines)-10:]
		}
		res.History = lines
	}
	return res, nil, st
}

// FencingRecord persists a successful fencing test (DATA_DIR/ha-fencing.json).
type FencingRecord struct {
	TestedAt    time.Time `json:"tested_at"`
	Node        string    `json:"node"`
	ClusterName string    `json:"cluster_name"`
	By          string    `json:"by"`
}

// FencingRecordPath is the state file inside dataDir.
func FencingRecordPath(dataDir string) string { return filepath.Join(dataDir, "ha-fencing.json") }

// SaveFencingRecord writes atomically.
func SaveFencingRecord(dataDir string, rec FencingRecord) error {
	if dataDir == "" {
		return errors.New("data dir not set")
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return WriteFileAtomic(FencingRecordPath(dataDir), data, 0o600)
}

// LoadFencingRecord returns the record only when it belongs to the current
// cluster (names must match when both are known), so a recreated cluster
// does not inherit an old test.
func LoadFencingRecord(dataDir, clusterName string) (FencingRecord, bool) {
	var rec FencingRecord
	if dataDir == "" {
		return rec, false
	}
	data, err := os.ReadFile(FencingRecordPath(dataDir))
	if err != nil || json.Unmarshal(data, &rec) != nil || rec.TestedAt.IsZero() {
		return FencingRecord{}, false
	}
	if clusterName != "" && rec.ClusterName != "" && rec.ClusterName != clusterName {
		return FencingRecord{}, false
	}
	return rec, true
}

// FencingEvidence is what proves fencing works in this cluster.
type FencingEvidence struct {
	Mode   string
	Tested bool
	// TestedAt is RFC 3339 when the proof time is known ("" otherwise).
	TestedAt string
	Events   []FenceEvent
	// RecordedBy is "event" (Pacemaker fence_history), "api" (ha-fencing.json,
	// device mode only) or "" when untested.
	RecordedBy string
}

// BuildFencingEvidence is BuildFencingEvidenceWithLedger without a ledger.
func BuildFencingEvidence(st PacemakerStatus, rec *FencingRecord) FencingEvidence {
	return BuildFencingEvidenceWithLedger(st, rec, nil)
}

// BuildFencingEvidenceWithLedger decides whether fencing is proven. A
// successful reboot/off event in fence_history, or persisted in the evidence
// ledger (which survives a full cluster restart that empties Pacemaker's
// in-memory history), for a node of this cluster is the proof in
// watchdog_only mode; ha-fencing.json (rec) is a secondary source for device
// mode only. Pure.
func BuildFencingEvidenceWithLedger(st PacemakerStatus, rec *FencingRecord, ledger []FenceEvent) FencingEvidence {
	ev := FencingEvidence{Mode: FencingMode(st), Events: []FenceEvent{}}
	for _, e := range st.FenceEvents {
		e.Source = "history"
		ev.Events = append(ev.Events, e)
	}
	seen := map[string]bool{}
	for _, e := range ev.Events {
		seen[ledgerKey(e)] = true
	}
	added := false
	for _, e := range ledger {
		if seen[ledgerKey(e)] {
			continue
		}
		e.Source = "ledger"
		ev.Events = append(ev.Events, e)
		added = true
	}
	if added {
		sortFenceEvents(ev.Events)
	}
	if st.FencingTested || len(ledger) > 0 {
		ev.RecordedBy = "ledger"
		for _, e := range ev.Events { // most recent first
			if !e.Successful() {
				continue
			}
			if e.Source == "history" {
				ev.RecordedBy = "event"
			}
			if t, ok := ParseFenceTime(e.Completed); ok {
				ev.TestedAt = t.Format(time.RFC3339)
			}
			break
		}
		if st.FencingTested {
			ev.RecordedBy = "event"
		}
		ev.Tested = true
		return ev
	}
	if ev.Mode == FencingModeDevice && rec != nil && !rec.TestedAt.IsZero() {
		ev.Tested, ev.RecordedBy = true, "api"
		ev.TestedAt = rec.TestedAt.UTC().Format(time.RFC3339)
	}
	return ev
}
