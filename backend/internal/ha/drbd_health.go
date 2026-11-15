package ha

import (
	"context"
	"regexp"
	"strings"

	"webkvm/internal/sysutil"
)

// DRBD issue kinds.
const (
	DRBDKindStandalone   = "standalone"
	DRBDKindConnecting   = "connecting"
	DRBDKindInconsistent = "inconsistent"
	DRBDKindOutdated     = "outdated"
	DRBDKindSplitBrain   = "split_brain_suspected"

	DRBDSeverityWarning  = "warning"
	DRBDSeverityCritical = "critical"
)

// drbdNameRe validates every resource and node name before it is embedded in
// a command the operator will paste into a root shell.
var drbdNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// DRBDStep is one repair command and the node it must run on.
type DRBDStep struct {
	Node    string `json:"node"`
	Command string `json:"command"`
	// VerifyPeerStale is true when the command targets another node whose
	// disk state this node cannot see: the operator must confirm there that
	// the copy is Inconsistent/Outdated before running it.
	VerifyPeerStale bool `json:"verify_peer_stale,omitempty"`
}

// DRBDIssue is one unhealthy connection of a DRBD resource, seen from the
// local node.
type DRBDIssue struct {
	Resource           string `json:"resource"`
	LocalRole          string `json:"local_role"`
	LocalDisk          string `json:"local_disk"`
	LocalNode          string `json:"local_node"`
	Peer               string `json:"peer"`
	PeerConnection     string `json:"peer_connection"`
	PeerDisk           string `json:"peer_disk"`
	Replication        string `json:"replication"`
	Kind               string `json:"kind"`
	Severity           string `json:"severity"`
	SafeToDiscardLocal bool   `json:"safe_to_discard_local"`
	// ManualResolution is true when WebKVM cannot propose a safe automatic
	// repair (diverged copies, or a name that failed validation).
	ManualResolution bool       `json:"manual_resolution"`
	RepairCommands   []string   `json:"repair_commands"`
	RepairSteps      []DRBDStep `json:"repair_steps"`
}

// DRBDHealth is the additive `drbd` block of the HA readiness.
type DRBDHealth struct {
	Healthy bool        `json:"healthy"`
	Issues  []DRBDIssue `json:"issues"`
}

// ProbeDRBD runs the read-only `drbdsetup status --verbose` through the
// runner. An unavailable probe (no DRBD) is healthy: absence is not a finding.
func ProbeDRBD(ctx context.Context, runner sysutil.CommandRunner, localNode string) DRBDHealth {
	out, err := runTool(ctx, runner, "drbdsetup", "status", "--verbose")
	if err != nil {
		return DRBDHealth{Healthy: true, Issues: []DRBDIssue{}}
	}
	issues := ParseDRBDConnections(string(out), localNode)
	return DRBDHealth{Healthy: len(issues) == 0, Issues: issues}
}

type drbdPeer struct {
	name, conn, disk, repl, role string
}

type drbdResource struct {
	name, role, disk string
	// quorum is the local quorum flag of `--verbose` ("yes"/"no"; "" when
	// the output does not report it).
	quorum string
	open   bool
	peers  []*drbdPeer
}

func kv(fields []string) map[string]string {
	m := make(map[string]string, len(fields))
	for _, f := range fields {
		if k, v, ok := strings.Cut(f, ":"); ok {
			m[k] = v
		}
	}
	return m
}

func parseDRBDStatus(text string) []*drbdResource {
	var resources []*drbdResource
	var res *drbdResource
	var peer *drbdPeer
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		fields := strings.Fields(line)
		m := kv(fields)
		switch {
		case indent == 0:
			res = &drbdResource{name: fields[0], role: m["role"]}
			peer = nil
			resources = append(resources, res)
		case res == nil:
		case strings.HasPrefix(fields[0], "volume:"):
			if peer == nil {
				if d, ok := m["disk"]; ok && res.disk == "" {
					res.disk = d
				}
				if q, ok := m["quorum"]; ok && res.quorum != "no" {
					res.quorum = q
				}
			} else {
				peer.disk, peer.repl = m["peer-disk"], m["replication"]
			}
		case fields[0] == "quorum:yes" || strings.HasPrefix(fields[0], "quorum:"):
			if o, ok := m["open"]; ok && peer == nil {
				res.open = o == "yes"
			}
			if q, ok := m["quorum"]; ok && peer == nil && res.quorum != "no" {
				res.quorum = q
			}
		case m["connection"] != "" || m["node-id"] != "":
			peer = &drbdPeer{name: fields[0], conn: m["connection"], role: m["role"]}
			res.peers = append(res.peers, peer)
		}
	}
	return resources
}

func isStale(disk string) bool { return disk == "Inconsistent" || disk == "Outdated" }

func isSyncing(repl string) bool {
	switch repl {
	case "SyncSource", "SyncTarget", "PausedSyncS", "PausedSyncT", "StartingSyncS", "StartingSyncT",
		"WFBitMapS", "WFBitMapT", "WFSyncUUID", "VerifyS", "VerifyT":
		return true
	}
	return false
}

// ParseDRBDConnections turns `drbdsetup status --verbose` text into one
// issue per unhealthy peer connection. Connected peers are never reported
// (a resync in progress is normal). The result is never nil.
func ParseDRBDConnections(statusVerbose, localNode string) []DRBDIssue {
	issues := []DRBDIssue{}
	for _, res := range parseDRBDStatus(statusVerbose) {
		for _, p := range res.peers {
			if p.conn == "Connected" || p.disk == "Diskless" {
				continue
			}
			if p.conn == "" {
				continue
			}
			issue := DRBDIssue{
				Resource: res.name, LocalRole: res.role, LocalDisk: res.disk, LocalNode: localNode,
				Peer: p.name, PeerConnection: p.conn, PeerDisk: p.disk, Replication: p.repl,
				RepairCommands: []string{}, RepairSteps: []DRBDStep{},
			}
			if isSyncing(p.repl) {
				continue
			}
			issue.Kind = classifyDRBD(res, p)
			issue.Severity = DRBDSeverityWarning
			if issue.Kind == DRBDKindSplitBrain || issue.Kind == DRBDKindStandalone {
				issue.Severity = DRBDSeverityCritical
			}
			issue.SafeToDiscardLocal = isStale(res.disk) && res.role == "Secondary" && !res.open &&
				otherUpToDatePeer(res, p)
			planDRBDRepair(&issue, res, p)
			issues = append(issues, issue)
		}
	}
	return issues
}

func classifyDRBD(res *drbdResource, p *drbdPeer) string {
	switch {
	case strings.Contains(strings.ToLower(p.conn), "splitbrain"),
		res.disk == "UpToDate" && p.disk == "UpToDate" && p.conn != "Connecting" && p.conn != "Unconnected":
		return DRBDKindSplitBrain
	case res.disk == "Inconsistent":
		return DRBDKindInconsistent
	case res.disk == "Outdated":
		return DRBDKindOutdated
	case p.conn == "StandAlone":
		return DRBDKindStandalone
	}
	return DRBDKindConnecting
}

func otherUpToDatePeer(res *drbdResource, except *drbdPeer) bool {
	for _, o := range res.peers {
		if o != except && o.conn == "Connected" && o.disk == "UpToDate" {
			return true
		}
	}
	return false
}

// planDRBDRepair fills the repair steps. The standard DRBD procedure is to
// discard the stale copy and reconnect; --discard-my-data is only ever
// proposed for a node that is provably stale (this node when
// SafeToDiscardLocal) or, from a Primary, for a peer the operator must first
// confirm is stale.
func planDRBDRepair(issue *DRBDIssue, res *drbdResource, p *drbdPeer) {
	if !drbdNameRe.MatchString(res.name) || !drbdNameRe.MatchString(p.name) || !drbdNameRe.MatchString(issue.LocalNode) {
		issue.ManualResolution = true
		return
	}
	add := func(node, cmd string, verify bool) {
		issue.RepairSteps = append(issue.RepairSteps, DRBDStep{Node: node, Command: cmd, VerifyPeerStale: verify})
		issue.RepairCommands = append(issue.RepairCommands, cmd)
	}
	local, peer, r := issue.LocalNode, p.name, res.name
	discard := func(node, other string, verify bool) {
		add(node, "sudo drbdadm disconnect "+r+":"+other, verify)
		add(node, "sudo drbdadm connect --discard-my-data "+r+":"+other, verify)
		add(other, "sudo drbdadm connect "+r+":"+node, false)
	}
	switch {
	case issue.Kind == DRBDKindSplitBrain:
		issue.ManualResolution = true
		add(local, "sudo drbdadm connect "+r+":"+peer, false)
	case issue.SafeToDiscardLocal:
		discard(local, peer, false)
	case isStale(res.disk):
		// Stale but no proven good copy elsewhere, or the device is in use.
		add(local, "sudo drbdadm connect "+r+":"+peer, false)
	case res.disk == "UpToDate" && isStale(p.disk):
		discard(peer, local, false)
	case res.disk == "UpToDate" && (res.role == "Primary" || res.open) && p.disk != "UpToDate" && p.conn == "StandAlone":
		// The peer is stale in every real case, but only the peer can say so.
		discard(peer, local, true)
	default:
		add(local, "sudo drbdadm connect "+r+":"+peer, false)
	}
}
