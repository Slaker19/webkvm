package ha

import (
	"context"
	"strings"
	"testing"

	"webkvm/internal/sysutil"
)

const drbdRes = "incus-volume-26439be54d4d4485b77c2eaa9dbb0417"

// Real `drbdsetup status --verbose` captured live on the Primary after a
// node crash: the peer node-b is StandAlone.
const drbdPrimarySide = `incus-volume-26439be54d4d4485b77c2eaa9dbb0417 node-id:0 role:Primary suspended:no
  volume:0 minor:1001 disk:UpToDate
      quorum:yes open:yes blocked:no
  node-b node-id:1 connection:StandAlone role:Unknown tls:no congested:no
    volume:0 replication:Off peer-disk:DUnknown resync-suspended:no
  node-c node-id:2 connection:Connected role:Secondary tls:no
    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no
`

// The same resource seen from the stale replica.
const drbdStaleSide = `incus-volume-26439be54d4d4485b77c2eaa9dbb0417 node-id:1 role:Secondary
  volume:0 minor:1001 disk:Inconsistent
      quorum:yes open:no blocked:no
  node-a node-id:0 connection:Connecting role:Unknown tls:no
    volume:0 replication:Off peer-disk:DUnknown resync-suspended:no
  node-c node-id:2 connection:Connected role:Secondary tls:no
    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no
`

const drbdBothUpToDate = `r0 node-id:0 role:Secondary
  volume:0 minor:1 disk:UpToDate
      quorum:yes open:no blocked:no
  nodeb node-id:1 connection:StandAlone role:Unknown tls:no
    volume:0 replication:Off peer-disk:UpToDate resync-suspended:no
`

const drbdHealthy = `r0 node-id:0 role:Primary suspended:no
  volume:0 minor:1 disk:UpToDate
      quorum:yes open:yes blocked:no
  nodeb node-id:1 connection:Connected role:Secondary tls:no
    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no
`

func steps(i DRBDIssue) []string {
	var out []string
	for _, s := range i.RepairSteps {
		out = append(out, s.Node+"|"+s.Command)
	}
	return out
}

func TestParseDRBDPrimarySideStandAlone(t *testing.T) {
	issues := ParseDRBDConnections(drbdPrimarySide, "node-a")
	if len(issues) != 1 {
		t.Fatalf("want 1 issue (connected ha3 is fine): %+v", issues)
	}
	i := issues[0]
	if i.Resource != drbdRes || i.LocalRole != "Primary" || i.LocalDisk != "UpToDate" || i.Peer != "node-b" ||
		i.PeerConnection != "StandAlone" || i.PeerDisk != "DUnknown" || i.Replication != "Off" || i.Kind != DRBDKindStandalone {
		t.Fatalf("issue: %+v", i)
	}
	if i.SafeToDiscardLocal {
		t.Fatal("a Primary must never be told to discard its data")
	}
	want := []string{
		"node-b|sudo drbdadm disconnect " + drbdRes + ":node-a",
		"node-b|sudo drbdadm connect --discard-my-data " + drbdRes + ":node-a",
		"node-a|sudo drbdadm connect " + drbdRes + ":node-b",
	}
	if got := steps(i); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%v\nwant\n%v", got, want)
	}
	if !i.RepairSteps[0].VerifyPeerStale || !i.RepairSteps[1].VerifyPeerStale || i.RepairSteps[2].VerifyPeerStale {
		t.Fatalf("peer discard from the Primary must be conditional on verifying the peer: %+v", i.RepairSteps)
	}
	for _, s := range i.RepairSteps {
		if s.Node == "node-a" && strings.Contains(s.Command, "discard") {
			t.Fatalf("discard on the Primary: %+v", s)
		}
	}
}

func TestParseDRBDStaleSideIsSafeToDiscard(t *testing.T) {
	issues := ParseDRBDConnections(drbdStaleSide, "node-b")
	if len(issues) != 1 {
		t.Fatalf("issues: %+v", issues)
	}
	i := issues[0]
	if i.Peer != "node-a" || i.PeerConnection != "Connecting" || i.LocalDisk != "Inconsistent" ||
		i.LocalRole != "Secondary" || i.Kind != DRBDKindInconsistent || !i.SafeToDiscardLocal {
		t.Fatalf("issue: %+v", i)
	}
	want := []string{
		"node-b|sudo drbdadm disconnect " + drbdRes + ":node-a",
		"node-b|sudo drbdadm connect --discard-my-data " + drbdRes + ":node-a",
		"node-a|sudo drbdadm connect " + drbdRes + ":node-b",
	}
	if got := steps(i); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%v\nwant\n%v", got, want)
	}
	if i.RepairSteps[0].VerifyPeerStale {
		t.Fatal("the local copy is known stale: no extra verification needed")
	}
	if len(i.RepairCommands) != 3 || i.RepairCommands[1] != "sudo drbdadm connect --discard-my-data "+drbdRes+":node-a" {
		t.Fatalf("commands: %v", i.RepairCommands)
	}
}

func TestParseDRBDStaleWithoutOtherUpToDatePeerNeverDiscards(t *testing.T) {
	text := strings.Replace(drbdStaleSide, "peer-disk:UpToDate", "peer-disk:Inconsistent", 1)
	i := ParseDRBDConnections(text, "node-b")[0]
	if i.SafeToDiscardLocal {
		t.Fatalf("no UpToDate peer left: %+v", i)
	}
	for _, c := range i.RepairCommands {
		if strings.Contains(c, "--discard-my-data") {
			t.Fatalf("discard suggested without a good copy: %q", c)
		}
	}
	if len(i.RepairCommands) == 0 || !strings.Contains(i.RepairCommands[0], "drbdadm connect "+drbdRes) {
		t.Fatalf("must suggest a plain connect first: %v", i.RepairCommands)
	}
}

func TestParseDRBDOpenOrPrimaryStaleNeverDiscards(t *testing.T) {
	open := strings.Replace(drbdStaleSide, "open:no", "open:yes", 1)
	if i := ParseDRBDConnections(open, "n")[0]; i.SafeToDiscardLocal {
		t.Fatalf("open device: %+v", i)
	}
	prim := strings.Replace(drbdStaleSide, "node-id:1 role:Secondary", "node-id:1 role:Primary", 1)
	if i := ParseDRBDConnections(prim, "n")[0]; i.SafeToDiscardLocal {
		t.Fatalf("primary: %+v", i)
	}
}

func TestParseDRBDUpToDateVsUpToDateIsSplitBrain(t *testing.T) {
	issues := ParseDRBDConnections(drbdBothUpToDate, "nodea")
	if len(issues) != 1 {
		t.Fatalf("issues: %+v", issues)
	}
	i := issues[0]
	if i.Kind != DRBDKindSplitBrain || i.SafeToDiscardLocal || !i.ManualResolution || i.Severity != DRBDSeverityCritical {
		t.Fatalf("issue: %+v", i)
	}
	for _, c := range i.RepairCommands {
		if strings.Contains(c, "--discard-my-data") {
			t.Fatalf("split brain must not suggest discard: %q", c)
		}
	}
	if got := steps(i); len(got) != 1 || got[0] != "nodea|sudo drbdadm connect r0:nodeb" {
		t.Fatalf("steps: %v", got)
	}
}

func TestParseDRBDHealthyAndEmpty(t *testing.T) {
	if got := ParseDRBDConnections(drbdHealthy, "nodea"); len(got) != 0 {
		t.Fatalf("healthy: %+v", got)
	}
	if got := ParseDRBDConnections("", "nodea"); got == nil || len(got) != 0 {
		t.Fatalf("empty must be a non-nil empty slice: %#v", got)
	}
	resync := strings.Replace(drbdHealthy, "replication:Established peer-disk:UpToDate", "replication:SyncTarget peer-disk:UpToDate", 1)
	resync = strings.Replace(resync, "disk:UpToDate", "disk:Inconsistent", 1)
	if got := ParseDRBDConnections(resync, "nodea"); len(got) != 0 {
		t.Fatalf("an ongoing resync is not an issue: %+v", got)
	}
}

func TestParseDRBDRejectsUnsafeNames(t *testing.T) {
	bad := strings.ReplaceAll(drbdStaleSide, "node-a", "evil;rm -rf /")
	issues := ParseDRBDConnections(bad, "node-b")
	if len(issues) == 0 {
		t.Fatal("the issue must still be reported")
	}
	for _, i := range issues {
		if len(i.RepairCommands) != 0 || len(i.RepairSteps) != 0 || !i.ManualResolution {
			t.Fatalf("unsafe peer name must produce no commands: %+v", i)
		}
	}
	badRes := strings.ReplaceAll(drbdStaleSide, drbdRes, "res$(id)")
	for _, i := range ParseDRBDConnections(badRes, "node-b") {
		if len(i.RepairCommands) != 0 {
			t.Fatalf("unsafe resource name must produce no commands: %+v", i)
		}
	}
	for _, i := range ParseDRBDConnections(drbdStaleSide, "bad host") {
		if len(i.RepairCommands) != 0 {
			t.Fatalf("unsafe local name must produce no commands: %+v", i)
		}
	}
}

func TestProbeDRBDUsesVerboseStatus(t *testing.T) {
	var got string
	runner := &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = name + " " + strings.Join(args, " ")
		return []byte(drbdStaleSide), nil
	}}
	h := ProbeDRBD(context.Background(), runner, "node-b")
	if got != "drbdsetup status --verbose" || h.Healthy || len(h.Issues) != 1 {
		t.Fatalf("%q %+v", got, h)
	}
	failing := &sysutil.MockCommandRunner{Handler: func(context.Context, string, ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}}
	if h := ProbeDRBD(context.Background(), failing, "x"); !h.Healthy || h.Issues == nil || len(h.Issues) != 0 {
		t.Fatalf("an unavailable probe is not a finding: %+v", h)
	}
}
