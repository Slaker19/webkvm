package ha

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"webkvm/internal/sysutil"
)

func bp(v bool) *bool { return &v }

func goodStatus() PacemakerStatus {
	return PacemakerStatus{
		State: StateObserved, ClusterName: "c", Quorum: bp(true), NodesConfigured: 3, NodesOnline: 3,
		NodeNames:       []string{"n1", "n2", "n3"},
		NodeStates:      []PacemakerNode{{"n1", true, false}, {"n2", true, false}, {"n3", true, false}},
		FencingEnabled:  bp(true),
		WatchdogFencing: bp(true),
		FenceDevices:    1, // device mode; watchdog-only cases clear it
		Resources:       []PacemakerResource{{ID: "webkvm-ip", Group: "webkvm-cp", Active: true, Node: "n1", Role: "Started"}},
	}
}

func TestFenceBlockersTable(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*PacemakerStatus)
		node    string
		confirm string
		allowCP bool
		want    string // "" = allowed
	}{
		{"ok", nil, "n2", "FENCE n2", false, ""},
		{"wrong confirm", nil, "n2", "fence n2", false, RefConfirmRequired},
		{"empty confirm", nil, "n2", "", false, RefConfirmRequired},
		{"confirm for other node", nil, "n2", "FENCE n3", false, RefConfirmRequired},
		{"no quorum", func(s *PacemakerStatus) { s.Quorum = bp(false) }, "n2", "FENCE n2", false, RefQuorumMissing},
		{"quorum unknown", func(s *PacemakerStatus) { s.Quorum = nil }, "n2", "FENCE n2", false, RefQuorumMissing},
		{"two online", func(s *PacemakerStatus) {
			s.NodesOnline = 2
			s.NodeStates[2].Online = false
		}, "n2", "FENCE n2", false, RefNotEnoughNodes},
		{"control plane", nil, "n1", "FENCE n1", false, RefControlPlane},
		{"control plane allowed", nil, "n1", "FENCE n1", true, ""},
		{"offline target", func(s *PacemakerStatus) { s.NodesOnline = 3; s.NodeStates[1].Online = false }, "n2", "FENCE n2", false, RefNodeOffline},
		{"unknown target", nil, "zz", "FENCE zz", false, RefNodeUnknown},
		{"pacemaker down", func(s *PacemakerStatus) { s.State = StateNotConfigured }, "n2", "FENCE n2", false, RefPacemakerDown},
		{"no fencing", func(s *PacemakerStatus) { s.WatchdogFencing = bp(false); s.FenceDevices = 0 }, "n2", "FENCE n2", false, RefNoFencing},
		{"stonith device instead of watchdog", func(s *PacemakerStatus) { s.WatchdogFencing = bp(false); s.FenceDevices = 1 }, "n2", "FENCE n2", false, ""},
		{"stonith disabled", func(s *PacemakerStatus) { s.FencingEnabled = bp(false) }, "n2", "FENCE n2", false, RefNoFencing},
		{"would lose quorum", func(s *PacemakerStatus) { s.NodesConfigured = 5 }, "n2", "FENCE n2", false, RefWouldLoseQuorumAf},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := goodStatus()
			if tc.mutate != nil {
				tc.mutate(&st)
			}
			b := FenceBlockers(st, tc.node, tc.confirm, tc.allowCP)
			if tc.want == "" && len(b) != 0 || tc.want != "" && !containsStr(b, tc.want) {
				t.Fatalf("want %q got %v", tc.want, b)
			}
		})
	}
}

func wdOnly(s *PacemakerStatus) { s.FenceDevices = 0 }

func TestFenceBlockersWatchdogOnlyMatrix(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PacemakerStatus)
		node   string
		ack    bool
		want   bool // watchdog_only_unreliable expected
		expl   string
	}{
		{"online idle node, no ack", wdOnly, "n2", false, true, ExplSelfFenceAssumed},
		{"online idle node, ack", wdOnly, "n2", true, false, ""},
		{"node with resources, ack", func(s *PacemakerStatus) {
			wdOnly(s)
			s.Resources = append(s.Resources, PacemakerResource{ID: "ha-vm", Node: "n2", Active: true, Role: "Started"})
		}, "n2", true, true, ExplNodeHasResources},
		{"offline node is not this refusal", func(s *PacemakerStatus) { wdOnly(s); s.NodeStates[1].Online = false }, "n2", false, false, ""},
		{"device mode never refuses", nil, "n2", false, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := goodStatus()
			if tc.mutate != nil {
				tc.mutate(&st)
			}
			b := FenceBlockersOpts(st, tc.node, "FENCE "+tc.node, FenceOptions{AcknowledgeUnreliable: tc.ack})
			if containsStr(b, RefWatchdogOnlyUnreliable) != tc.want {
				t.Fatalf("want refusal=%v got %v", tc.want, b)
			}
			if got := WatchdogOnlyExplanation(st, tc.node, tc.ack); got != tc.expl {
				t.Fatalf("explanation %q want %q", got, tc.expl)
			}
		})
	}
}

func TestFencingModeFromCIB(t *testing.T) {
	wd := `<cib><configuration><crm_config><cluster_property_set id="x"><nvpair id="a" name="stonith-watchdog-timeout" value="20s"/></cluster_property_set></crm_config><resources/></configuration></cib>`
	dev := `<cib><configuration><crm_config/><resources><primitive id="f" class="stonith" type="fence_pve"/></resources></configuration></cib>`
	both := `<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-watchdog-timeout" value="20s"/></cluster_property_set></crm_config><resources><primitive id="f" class="stonith" type="fence_virsh"/></resources></configuration></cib>`
	zero := `<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-watchdog-timeout" value="0"/></cluster_property_set></crm_config><resources/></configuration></cib>`
	for name, tc := range map[string]struct{ cib, want string }{
		"watchdog": {wd, FencingModeWatchdogOnly}, "device": {dev, FencingModeDevice},
		"device+watchdog": {both, FencingModeDevice}, "zero timeout": {zero, FencingModeNone},
	} {
		got, err := FencingModeFromCIB([]byte(tc.cib))
		if err != nil || got != tc.want {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
}

func TestBuildFencingEvidence(t *testing.T) {
	ev := []FenceEvent{{Target: "n2", Action: "reboot", Status: "success", Completed: "2026-11-12 10:00:00 +00:00"}}
	wd := goodStatus()
	wd.FenceDevices = 0
	rec := &FencingRecord{TestedAt: time.Now()}
	if got := BuildFencingEvidence(wd, rec); got.Tested {
		t.Fatalf("record must not count in watchdog_only: %+v", got)
	}
	wd.FencingTested, wd.FenceEvents = true, ev
	if got := BuildFencingEvidence(wd, nil); !got.Tested || got.RecordedBy != "event" || got.TestedAt != "2026-11-12T10:00:00Z" {
		t.Fatalf("%+v", got)
	}
	dev := goodStatus()
	if got := BuildFencingEvidence(dev, rec); !got.Tested || got.RecordedBy != "api" {
		t.Fatalf("%+v", got)
	}
	if got := BuildFencingEvidence(dev, nil); got.Tested || got.RecordedBy != "" {
		t.Fatalf("%+v", got)
	}
}

func fenceRunner(calls *[]string, mu *sync.Mutex, block chan struct{}) *sysutil.MockCommandRunner {
	f := &fakeHost{stonith: true}
	inner := f.runner().Handler
	return &sysutil.MockCommandRunner{Handler: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		line := name + " " + strings.Join(args, " ")
		mu.Lock()
		*calls = append(*calls, line)
		mu.Unlock()
		if line == "pcs stonith fence n2" && block != nil {
			<-block
		}
		if strings.HasPrefix(line, "pcs stonith history") {
			return []byte("n2 was rebooted\n"), nil
		}
		return inner(ctx, name, args...)
	}}
}

func TestFenceRunsOnlyPcsStonithFenceAndInProgressMutex(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	block := make(chan struct{})
	var tester FenceTester
	runner := fenceRunner(&calls, &mu, block)
	done := make(chan FenceResult)
	go func() {
		res, b, _ := tester.Fence(context.Background(), runner, "n2", "FENCE n2", false)
		if len(b) != 0 {
			t.Errorf("blockers %v", b)
		}
		done <- res
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		started := strings.Contains(strings.Join(calls, "|"), "pcs stonith fence n2")
		mu.Unlock()
		if started || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, b, _ := tester.Fence(context.Background(), runner, "n3", "FENCE n3", false)
	if len(b) != 1 || b[0] != RefFenceInProgress {
		t.Fatalf("second fence must be refused: %v", b)
	}
	close(block)
	res := <-done
	if !res.OK || res.Node != "n2" || len(res.History) != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestFenceRefusalNeverExecutes(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	var tester FenceTester
	_, b, _ := tester.Fence(context.Background(), fenceRunner(&calls, &mu, nil), "n2", "nope", false)
	if !containsStr(b, RefConfirmRequired) {
		t.Fatalf("%v", b)
	}
	if _, b, _ = tester.Fence(context.Background(), fenceRunner(&calls, &mu, nil), "--off", "FENCE --off", false); len(b) == 0 {
		t.Fatal("flag-like node must be refused")
	}
	for _, c := range calls {
		if strings.Contains(c, "stonith fence") {
			t.Fatalf("fence executed: %s", c)
		}
	}
}

func TestStandbyBlockers(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PacemakerStatus)
		node   string
		want   string
	}{
		{"ok three nodes", nil, "n2", ""},
		{"two node cluster loses quorum", func(s *PacemakerStatus) {
			s.NodesConfigured, s.NodesOnline = 2, 2
			s.NodeStates = s.NodeStates[:2]
		}, "n2", RefWouldLoseQuorum},
		{"one already standby", func(s *PacemakerStatus) { s.NodeStates[0].Standby = true }, "n2", RefWouldLoseQuorum},
		{"last online", func(s *PacemakerStatus) {
			s.NodeStates[0].Online, s.NodeStates[2].Online = false, false
		}, "n2", RefLastOnlineNode},
		{"unknown node", nil, "zz", RefNodeUnknown},
		{"pacemaker down", func(s *PacemakerStatus) { s.State = StateError }, "n2", RefPacemakerDown},
		{"already standby target is not double counted", func(s *PacemakerStatus) { s.NodeStates[1].Standby = true }, "n2", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := goodStatus()
			if tc.mutate != nil {
				tc.mutate(&st)
			}
			b := StandbyBlockers(st, tc.node)
			if tc.want == "" && len(b) != 0 || tc.want != "" && !containsStr(b, tc.want) {
				t.Fatalf("want %q got %v", tc.want, b)
			}
		})
	}
}

func TestPacemakerNodeRE(t *testing.T) {
	for _, bad := range []string{"", "-x", "--force", "a b", "a;b", "a/b", "$(x)", strings.Repeat("a", 64)} {
		if PacemakerNodeRE.MatchString(bad) {
			t.Fatalf("%q must be rejected", bad)
		}
	}
	if !PacemakerNodeRE.MatchString("webkvm-1.lab_x") {
		t.Fatal("valid name rejected")
	}
}

func TestFencingRecordRoundTripAndClusterMismatch(t *testing.T) {
	dir := t.TempDir()
	if _, ok := LoadFencingRecord(dir, "c"); ok {
		t.Fatal("no record yet")
	}
	rec := FencingRecord{TestedAt: time.Now().UTC(), Node: "n2", ClusterName: "c", By: "admin"}
	if err := SaveFencingRecord(dir, rec); err != nil {
		t.Fatal(err)
	}
	if got, ok := LoadFencingRecord(dir, "c"); !ok || got.Node != "n2" {
		t.Fatalf("%+v %v", got, ok)
	}
	if _, ok := LoadFencingRecord(dir, "other"); ok {
		t.Fatal("record of another cluster must not count")
	}
}

func TestParseNodeStatesStandby(t *testing.T) {
	st, err := parsePacemakerStatusXML([]byte(`<pacemaker-result><status code="0"/><crm_mon><summary><quorum>true</quorum></summary><nodes><node name="a" online="true" standby="true" type="member"/><node name="b" online="false" type="member"/></nodes></crm_mon></pacemaker-result>`))
	if err != nil || len(st.NodeStates) != 2 || !st.NodeStates[0].Standby || !st.NodeStates[0].Online || st.NodeStates[1].Online {
		t.Fatalf("%v %+v", err, st.NodeStates)
	}
}

func TestControlPlaneNode(t *testing.T) {
	st := PacemakerStatus{Resources: []PacemakerResource{
		{ID: "kvm-vm", Node: "n3", Active: true},
		{ID: "webkvm-x", Group: "ha-kvm-webkvm-x-grp", Node: "n0", Active: true},
		{ID: "webkvm-app", Group: "webkvm-cp", Node: "n2", Active: true},
		{ID: "webkvm-fs", Group: "webkvm-cp", Node: "n2", Active: true},
		{ID: "webkvm-vip", Group: "webkvm-cp", Node: "n1", Active: false},
	}}
	if got := ControlPlaneNode(st); got != "n2" {
		t.Fatalf("ControlPlaneNode = %q, want n2 (inactive and non-webkvm resources ignored)", got)
	}
	st.Resources = append(st.Resources, PacemakerResource{ID: "webkvm-data", Group: "webkvm-cp", Node: "n1", Active: true})
	if got := ControlPlaneNode(st); got != "n1" {
		t.Fatalf("ControlPlaneNode = %q, want deterministic smallest name n1", got)
	}
	if got := ControlPlaneNode(PacemakerStatus{}); got != "" {
		t.Fatalf("ControlPlaneNode(empty) = %q, want empty", got)
	}
}

func TestControlPlaneNodeCustomGroup(t *testing.T) {
	st := PacemakerStatus{Resources: []PacemakerResource{
		{ID: "app", Group: "panel", Node: "n3", Active: true},
		{ID: "webkvm-app", Group: "webkvm-cp", Node: "n1", Active: true},
	}}
	if got := ControlPlaneNode(st, "panel"); got != "n3" {
		t.Fatalf("ControlPlaneNode(panel) = %q, want n3", got)
	}
}

func TestFenceWithWatchdogOnlyRefusesWithoutAckAndNeverRunsPcs(t *testing.T) {
	var calls []string
	var mu sync.Mutex
	r := fenceRunner(&calls, &mu, nil)
	inner := r.Handler
	r.Handler = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "pcs" && strings.Join(args, " ") == "cluster cib" {
			return []byte(`<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-watchdog-timeout" value="20s"/></cluster_property_set></crm_config><nodes><node uname="n1"/><node uname="n2"/><node uname="n3"/></nodes></configuration></cib>`), nil
		}
		return inner(ctx, name, args...)
	}
	var tester FenceTester
	_, b, st := tester.FenceWith(context.Background(), r, "n2", "FENCE n2", FenceOptions{})
	if !containsStr(b, RefWatchdogOnlyUnreliable) || FencingMode(st) != FencingModeWatchdogOnly {
		t.Fatalf("%v %s", b, FencingMode(st))
	}
	res, b, _ := tester.FenceWith(context.Background(), r, "n2", "FENCE n2", FenceOptions{AcknowledgeUnreliable: true})
	if len(b) != 0 || res.Mode != FencingModeWatchdogOnly {
		t.Fatalf("%v %+v", b, res)
	}
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, c := range calls {
		if c == "pcs stonith fence n2" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("pcs stonith fence must run only for the acknowledged call: %d", n)
	}
}
