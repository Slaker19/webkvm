package ha

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// Status fixtures of the KVM stack of web_1 (labCIB): group(fs, vm) and the
// promotable DRBD clone ha-kvm-web-1-data-clone with one instance per node.
func kvmStackStatus(fs, vm string, clone [3]string) string {
	res := func(id, agent, role, node string) string {
		active := "true"
		if role == "Stopped" {
			active = "false"
		}
		n := ""
		if node != "" && active == "true" {
			n = `<node name="` + node + `"/>`
		}
		return `<resource id="` + id + `" resource_agent="` + agent + `" role="` + role + `" active="` + active + `" managed="true">` + n + `</resource>`
	}
	nodes := []string{"ha1", "ha2", "ha3"}
	var b strings.Builder
	b.WriteString(`<pacemaker-result><status code="0"/><resources>`)
	b.WriteString(`<clone id="ha-kvm-web-1-data-clone" multi_state="true">`)
	for i, role := range clone {
		b.WriteString(res("ha-kvm-web-1-data", "ocf:linbit:drbd", role, nodes[i]))
	}
	b.WriteString(`</clone><group id="ha-kvm-web-1-grp">`)
	b.WriteString(res("ha-kvm-web-1-fs", "ocf:heartbeat:Filesystem", fs, "ha1"))
	b.WriteString(res("ha-kvm-web-1", "ocf:heartbeat:VirtualDomain", vm, "ha1"))
	b.WriteString(`</group></resources></pacemaker-result>`)
	return b.String()
}

var (
	kvmRunning      = kvmStackStatus("Started", "Started", [3]string{"Promoted", "Unpromoted", "Unpromoted"})
	kvmGroupStopped = kvmStackStatus("Stopped", "Stopped", [3]string{"Promoted", "Unpromoted", "Unpromoted"})
	kvmCloneDemoted = kvmStackStatus("Stopped", "Stopped", [3]string{"Unpromoted", "Unpromoted", "Stopped"})
	kvmAllStopped   = kvmStackStatus("Stopped", "Stopped", [3]string{"Stopped", "Stopped", "Stopped"})
)

func idx(f *fakePCS, sub string) int {
	for i, c := range f.cmds {
		if strings.Contains(c, sub) {
			return i
		}
	}
	return -1
}

func fastDisable(t *testing.T, timeout time.Duration) DisableOptions {
	return DisableOptions{PollInterval: time.Millisecond, StopTimeout: timeout, TmpDir: t.TempDir()}
}

// The v11 incident: the group stopped but the DRBD clone was still Promoted
// when the stack was deleted. The clone must be disabled and every instance
// observed stopped before anything is removed from the CIB.
func TestApplyDisableStopWaitsForPromotedClone(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, statuses: []string{kvmRunning, kvmGroupStopped, kvmGroupStopped, kvmCloneDemoted, kvmAllStopped}}
	pushedWith := ""
	f.onPush = func() { pushedWith = f.lastStatus }
	res, err := ApplyDisable(context.Background(), f, key, ModeStop, fastDisable(t, time.Second))
	if err != nil || !res.Changed || res.State != "absent" {
		t.Fatalf("%+v %v\n%v", res, err, f.cmds)
	}
	dg, dc, push := idx(f, "pcs resource disable ha-kvm-web-1-grp"), idx(f, "pcs resource disable ha-kvm-web-1-data-clone"), idx(f, "cib-push")
	if dg < 0 || dc < 0 || push < 0 || !(dg < dc && dc < push) {
		t.Fatalf("want disable group, then disable clone, then push: %v", f.cmds)
	}
	if pushedWith != kvmAllStopped {
		t.Fatalf("CIB pushed before every clone instance stopped; last status seen:\n%s", pushedWith)
	}
	if !f.has("resource delete ha-kvm-web-1-grp") || !f.has("resource delete ha-kvm-web-1-data-clone") {
		t.Fatalf("stack not deleted: %v", f.cmds)
	}
	if f.has("pcs resource delete") {
		t.Fatalf("delete must be the atomic offline push, after the verified stop: %v", f.cmds)
	}
}

func TestApplyDisableStopCloneTimeoutRollsBack(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, statuses: []string{kvmRunning, kvmGroupStopped}}
	_, err := ApplyDisable(context.Background(), f, key, ModeStop, fastDisable(t, 30*time.Millisecond))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeStopTimeout {
		t.Fatalf("want %s, got %v", CodeStopTimeout, err)
	}
	if f.has("cib-push") || f.has("resource delete") || f.has(" -f ") {
		t.Fatalf("nothing may be deleted when the stack did not stop: %v", f.cmds)
	}
	ec, eg := idx(f, "pcs resource enable ha-kvm-web-1-data-clone"), idx(f, "pcs resource enable ha-kvm-web-1-grp")
	if ec < 0 || eg < 0 || ec > eg {
		t.Fatalf("rollback must re-enable the clone, then the group: %v", f.cmds)
	}
}

func TestApplyDisableStopGroupTimeoutNeverTouchesClone(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, status: kvmRunning}
	_, err := ApplyDisable(context.Background(), f, key, ModeStop, fastDisable(t, 20*time.Millisecond))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeStopTimeout {
		t.Fatalf("err = %v", err)
	}
	if f.has("disable ha-kvm-web-1-data-clone") || f.has("cib-push") || f.has("resource delete") {
		t.Fatalf("clone disabled or stack deleted while the group was running: %v", f.cmds)
	}
	if !f.has("pcs resource enable ha-kvm-web-1-grp") || f.has("enable ha-kvm-web-1-data-clone") {
		t.Fatalf("rollback must re-enable only what was disabled: %v", f.cmds)
	}
}

func TestApplyDisableStopUnreadableStatusDeletesNothing(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	// Resolve sees the running stack; afterwards status has no <resources>
	// section (crm_mon failed): the stop cannot be proven, nothing is deleted.
	f := &fakePCS{cib: labCIB, statuses: []string{kvmRunning, `<pacemaker-result><status code="102"/></pacemaker-result>`}}
	_, err := ApplyDisable(context.Background(), f, key, ModeStop, fastDisable(t, 20*time.Millisecond))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeStopTimeout || f.has("cib-push") {
		t.Fatalf("err = %v cmds = %v", err, f.cmds)
	}
}

func TestApplyDisableStopPausedKeepsGroupDisabledOnRollback(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	paused := strings.Replace(labCIB, `<group id="ha-kvm-web-1-grp">`,
		`<group id="ha-kvm-web-1-grp"><meta_attributes id="gm"><nvpair name="target-role" value="Stopped"/></meta_attributes>`, 1)
	f := &fakePCS{cib: paused, statuses: []string{kvmGroupStopped}}
	_, err := ApplyDisable(context.Background(), f, key, ModeStop, fastDisable(t, 20*time.Millisecond))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeStopTimeout {
		t.Fatalf("err = %v", err)
	}
	if f.has("enable ha-kvm-web-1-grp") || !f.has("pcs resource enable ha-kvm-web-1-data-clone") {
		t.Fatalf("a group paused before the call must stay paused: %v", f.cmds)
	}
}

func TestApplyDisableStopAlreadyStoppedDeletes(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, status: kvmAllStopped}
	res, err := ApplyDisable(context.Background(), f, key, ModeStop, fastDisable(t, time.Second))
	if err != nil || res.State != "absent" || !f.has("cib-push") {
		t.Fatalf("%+v %v %v", res, err, f.cmds)
	}
}

// Pause never deletes, so it may leave the DRBD clone Promoted (fast resume).
func TestApplyDisablePauseKeepsClone(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, status: kvmRunning}
	res, err := ApplyDisable(context.Background(), f, key, ModePause, fastDisable(t, time.Second))
	if err != nil || res.State != WorkloadPaused || f.has("data-clone") || f.has("delete") || f.has("cib-push") {
		t.Fatalf("%+v %v %v", res, err, f.cmds)
	}
}

// Removing an active resource from the CIB makes it an orphan that Pacemaker
// stops without its parameters: forget/keep_running refuse while anything of
// the stack is active and change nothing.
func TestApplyDisableForgetRefusesActiveStack(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	for _, st := range []string{kvmRunning, kvmGroupStopped} {
		for _, mode := range []string{ModeForget, ModeKeepRunning} {
			f := &fakePCS{cib: labCIB, status: st}
			opts := fastDisable(t, time.Second)
			opts.AllowKeepRunning = true
			_, err := ApplyDisable(context.Background(), f, key, mode, opts)
			var oe *OpError
			if !errors.As(err, &oe) || oe.Code != CodeWorkloadActive {
				t.Fatalf("%s: err = %v", mode, err)
			}
			for _, c := range f.cmds {
				if c != "pcs cluster cib" && c != "pcs status xml" {
					t.Fatalf("%s changed the cluster: %v", mode, f.cmds)
				}
			}
		}
	}
	f := &fakePCS{cib: labCIB, status: kvmAllStopped}
	res, err := ApplyDisable(context.Background(), f, key, ModeForget, fastDisable(t, time.Second))
	if err != nil || res.State != "absent" || !f.has("resource delete ha-kvm-web-1-data-clone") {
		t.Fatalf("forget of a stopped stack: %+v %v %v", res, err, f.cmds)
	}
}

func TestApplyDisableStopIncus(t *testing.T) {
	key := WorkloadKey{Kind: KindIncus, Project: "default", Name: "c2"}
	cib := strings.Replace(labCIB, `</resources>`, `<primitive id="ha-incus-default-c2" class="ocf" provider="webkvm" type="incus"><instance_attributes><nvpair name="instance" value="c2"/><nvpair name="project" value="default"/></instance_attributes>
 <meta_attributes id="m9"><nvpair name="webkvm-kind" value="incus"/><nvpair name="webkvm-name" value="c2"/><nvpair name="webkvm-project" value="default"/></meta_attributes></primitive></resources>`, 1)
	running := `<pacemaker-result><resources><resource id="ha-incus-default-c2" role="Started" active="true" managed="true"><node name="ha2"/></resource></resources></pacemaker-result>`
	stopped := `<pacemaker-result><resources><resource id="ha-incus-default-c2" role="Stopped" active="false" managed="true"/></resources></pacemaker-result>`
	f := &fakePCS{cib: cib, statuses: []string{running, running, stopped}}
	pushedWith := ""
	f.onPush = func() { pushedWith = f.lastStatus }
	res, err := ApplyDisable(context.Background(), f, key, ModeStop, fastDisable(t, time.Second))
	if err != nil || res.State != "absent" || pushedWith != stopped || !f.has("pcs resource disable ha-incus-default-c2") {
		t.Fatalf("%+v %v %v", res, err, f.cmds)
	}
	f = &fakePCS{cib: cib, status: running}
	_, err = ApplyDisable(context.Background(), f, key, ModeStop, fastDisable(t, 20*time.Millisecond))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeStopTimeout || f.has("cib-push") || !f.has("pcs resource enable ha-incus-default-c2") {
		t.Fatalf("incus timeout: %v %v", err, f.cmds)
	}
}

// A failed enable is rolled back with the same safe order: nothing is
// deleted while any instance of the new stack is still active.
func TestRollbackEnableWaitsForStackStop(t *testing.T) {
	plan, _ := PlanEnableKVM(kvmSpec(), true)
	f := &fakePCS{cib: labCIB, statuses: []string{kvmGroupStopped}}
	left := rollbackEnable(context.Background(), f, plan, 20*time.Millisecond, time.Millisecond)
	if len(left) == 0 || f.has("resource delete") {
		t.Fatalf("rollback deleted an active clone: %v %v", left, f.cmds)
	}
	if !f.has("pcs resource disable ha-kvm-web-1-data-clone") {
		t.Fatalf("clone not disabled: %v", f.cmds)
	}
	f = &fakePCS{cib: labCIB, statuses: []string{kvmGroupStopped, kvmAllStopped}}
	left = rollbackEnable(context.Background(), f, plan, time.Second, time.Millisecond)
	if len(left) != 0 || !f.has("pcs resource delete ha-kvm-web-1-grp") || !f.has("pcs resource delete ha-kvm-web-1-data-clone") {
		t.Fatalf("%v %v", left, f.cmds)
	}
}

// cibFailsAfter serves the fake until n CIB reads were made, then fails them.
type cibFailsAfter struct {
	*fakePCS
	n, seen int
}

func (c *cibFailsAfter) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if strings.Join(args, " ") == "cluster cib" {
		c.seen++
		if c.seen > c.n {
			c.fakePCS.cmds = append(c.fakePCS.cmds, "pcs cluster cib (failed)")
			return nil, errors.New("cib unreadable")
		}
	}
	return c.fakePCS.Run(ctx, name, args...)
}

func TestApplyDisableStopUnreadableCIBChangesNothing(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, status: kvmRunning}
	_, err := ApplyDisable(context.Background(), &cibFailsAfter{fakePCS: f, n: 1}, key, ModeStop, fastDisable(t, time.Second))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodePCSFailed || f.has("resource disable") || f.has("cib-push") {
		t.Fatalf("err = %v cmds = %v", err, f.cmds)
	}
}

// forget of a workload that is already paused (group disabled, clone still
// active) must say it is paused and what to do, not "use pause".
func TestApplyDisableForgetPausedWorkloadMessage(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	paused := strings.Replace(labCIB, `<group id="ha-kvm-web-1-grp">`,
		`<group id="ha-kvm-web-1-grp"><meta_attributes id="gm"><nvpair name="target-role" value="Stopped"/></meta_attributes>`, 1)
	f := &fakePCS{cib: paused, status: kvmGroupStopped}
	_, err := ApplyDisable(context.Background(), f, key, ModeForget, fastDisable(t, time.Second))
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeWorkloadPaused {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(oe.Msg, "paused") || strings.Contains(oe.Msg, "use pause") || !strings.Contains(oe.Msg, "resume") {
		t.Fatalf("message = %q", oe.Msg)
	}
	if len(oe.Leftovers) == 0 {
		t.Fatalf("no leftovers")
	}
	for _, c := range f.cmds {
		if c != "pcs cluster cib" && c != "pcs status xml" {
			t.Fatalf("changed the cluster: %v", f.cmds)
		}
	}
}
