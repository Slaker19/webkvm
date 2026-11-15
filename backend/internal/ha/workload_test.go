package ha

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"webkvm/internal/sysutil"
)

const labCIB = `<cib><configuration><nodes><node uname="ha1"/><node uname="ha2"/><node uname="ha3"/></nodes><resources>
<group id="webkvm-cp">
 <primitive id="webkvm-fs" class="ocf" provider="heartbeat" type="Filesystem"><instance_attributes><nvpair name="directory" value="/opt/webkvm"/></instance_attributes></primitive>
 <primitive id="webkvm-vip" class="ocf" provider="heartbeat" type="IPaddr2"/>
 <primitive id="webkvm-vip-lan" class="ocf" provider="heartbeat" type="IPaddr2"/>
 <primitive id="webkvm-app" class="ocf" provider="webkvm" type="webkvm"/>
 <meta_attributes id="cpm"><nvpair name="resource-stickiness" value="100"/></meta_attributes>
</group>
<clone id="webkvm-data-clone"><primitive id="webkvm-data" class="ocf" provider="linbit" type="drbd"/></clone>
<clone id="ha-kvm-web-1-data-clone"><primitive id="ha-kvm-web-1-data" class="ocf" provider="linbit" type="drbd"/></clone>
<group id="ha-kvm-web-1-grp">
 <primitive id="ha-kvm-web-1-fs" class="ocf" provider="heartbeat" type="Filesystem"/>
 <primitive id="ha-kvm-web-1" class="ocf" provider="heartbeat" type="VirtualDomain">
  <instance_attributes><nvpair name="config" value="/mnt/web-1/web_1.xml"/></instance_attributes>
  <meta_attributes id="m1"><nvpair name="webkvm-kind" value="kvm"/><nvpair name="webkvm-name" value="web_1"/><nvpair name="migration-threshold" value="3"/><nvpair name="resource-stickiness" value="100"/></meta_attributes>
  <operations><op id="o1" name="monitor"><meta_attributes id="om"><nvpair name="x" value="y"/></meta_attributes></op></operations>
 </primitive>
</group>
<primitive id="ha-test-01" class="ocf" provider="heartbeat" type="VirtualDomain"><instance_attributes><nvpair name="config" value="/nonexistent/ha-test.xml"/></instance_attributes></primitive>
<primitive id="ha-incus-default-c1" class="ocf" provider="webkvm" type="incus"><instance_attributes><nvpair name="instance" value="c1"/><nvpair name="project" value="default"/></instance_attributes>
 <meta_attributes id="m3"><nvpair name="webkvm-kind" value="incus"/><nvpair name="webkvm-name" value="c1"/><nvpair name="webkvm-project" value="default"/><nvpair name="is-managed" value="false"/><nvpair name="webkvm-maint-until" value="2020-01-01T00:00:00Z"/></meta_attributes></primitive>
<primitive id="webkvm-x" class="ocf" provider="heartbeat" type="VirtualDomain"><meta_attributes id="m4"><nvpair name="webkvm-kind" value="kvm"/><nvpair name="webkvm-name" value="webkvm-x"/></meta_attributes></primitive>
</resources><constraints>
<rsc_location id="webkvm-loc-ha-kvm-web-1-grp-ha3" rsc="ha-kvm-web-1-grp" node="ha3" score="-INFINITY"/>
<rsc_location id="webkvm-loc-ha-kvm-web-1-grp-ha1" rsc="ha-kvm-web-1-grp" node="ha1" score="100"/>
<rsc_location id="manual" rsc="ha-kvm-web-1-grp" node="ha2" score="50"/>
</constraints></configuration></cib>`

const labStatus = `<pacemaker-result><status code="0"/><summary/><nodes/><resources>
<group id="ha-kvm-web-1-grp"><resource id="ha-kvm-web-1-fs" resource_agent="ocf:heartbeat:Filesystem" role="Started" active="true" managed="true"><node name="ha1"/></resource>
<resource id="ha-kvm-web-1" resource_agent="ocf:heartbeat:VirtualDomain" role="Started" active="true" managed="true"><node name="ha1"/></resource></group>
<resource id="ha-test-01" resource_agent="ocf:heartbeat:VirtualDomain" role="Stopped" active="false" managed="true" failed="true"/>
</resources>
<node_history><node name="ha1"><resource_history id="ha-test-01" fail-count="2" migration-threshold="3"><operation_history call="1" task="monitor" last-rc-change="Thu Nov 12 10:00:00 2026"/></resource_history></node></node_history>
<failures><failure op_key="ha-test-01_monitor_30000" node="ha1" exitreason="boom" rc_text="not running" last-rc-change="Thu Nov 12 10:00:00 2026"/></failures>
</pacemaker-result>`

func TestParseWorkloads(t *testing.T) {
	list, err := ParseWorkloads([]byte(labCIB), []byte(labStatus), "")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]Workload{}
	for _, w := range list {
		byKey[w.Key] = w
	}
	if len(list) != 4 {
		t.Fatalf("want 4 workloads (control plane excluded), got %d: %+v", len(list), list)
	}
	if _, ok := byKey["kvm:webkvm-x"]; !ok {
		t.Fatal("a VM named webkvm-x is a workload, not the control plane")
	}
	web := byKey["kvm:web_1"]
	if web.State != WorkloadStarted || web.Node != "ha1" || web.TopResource() != "ha-kvm-web-1-grp" || web.ResourceID != "ha-kvm-web-1" {
		t.Fatalf("web_1 = %+v", web)
	}
	if got := strings.Join(web.ResourceIDs, ","); got != "ha-kvm-web-1-grp,ha-kvm-web-1-data-clone" {
		t.Fatalf("stack = %s", got)
	}
	if strings.Join(web.AllowedNodes, ",") != "ha1,ha2" || web.PreferredNode != "ha1" {
		t.Fatalf("placement: allowed=%v preferred=%q (only webkvm-loc is read; manual constraint is ignored)", web.AllowedNodes, web.PreferredNode)
	}
	if web.Stickiness != "100" || web.MigrationThreshold != "3" {
		t.Fatalf("meta not read from the primitive only: %+v", web)
	}
	legacy := byKey["kvm:ha-test-01"]
	if !legacy.Adopted || legacy.State != WorkloadFailed || legacy.Failcount != 2 || len(legacy.Failures) != 1 || legacy.Failures[0].Op != "monitor" {
		t.Fatalf("legacy = %+v", legacy)
	}
	inc := byKey["incus:default/c1"]
	if inc.Managed || inc.State != WorkloadUnmanaged || !inc.MaintenanceExpired(time.Now()) {
		t.Fatalf("incus = %+v", inc)
	}
	if exp := ExpiredMaintenance(list, time.Now()); len(exp) != 1 {
		t.Fatalf("expired = %+v", exp)
	}
}

func TestParseWorkloadsAdoptsNameFromXML(t *testing.T) {
	old := domainNameReader
	domainNameReader = func(p string) string {
		if p == "/nonexistent/ha-test.xml" {
			return "real-name"
		}
		return ""
	}
	defer func() { domainNameReader = old }()
	list, _ := ParseWorkloads([]byte(labCIB), nil, "webkvm-cp")
	found := false
	for _, w := range list {
		found = found || w.Key == "kvm:real-name"
	}
	if !found {
		t.Fatalf("legacy name not read from XML: %+v", list)
	}
}

func TestWorkloadKey(t *testing.T) {
	k, err := ParseWorkloadKey("incus:proj/c1")
	if err != nil || k.String() != "incus:proj/c1" {
		t.Fatalf("%v %v", k, err)
	}
	for _, bad := range []string{"kvm:", "kvm:-x", "kvm:a b", "incus:c1", "vm:x", "kvm:a;b", "incus:p/-x"} {
		if _, err := ParseWorkloadKey(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

// fakePCS is a stateful fake: it records commands and serves a CIB/status.
type fakePCS struct {
	mu       sync.Mutex
	cib      string
	status   string
	cmds     []string
	failOn   string
	onPush   func()
	statuses []string // successive status responses; last repeats
	// lastStatus is the status XML served most recently (tests check what
	// the code had observed when it pushed the CIB).
	lastStatus string
}

func (f *fakePCS) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, line)
	if f.failOn != "" && strings.Contains(line, f.failOn) {
		return []byte("boom"), errors.New("exit 1")
	}
	switch {
	case line == "pcs cluster cib":
		return []byte(f.cib), nil
	case line == "pcs status xml":
		if len(f.statuses) > 0 {
			s := f.statuses[0]
			if len(f.statuses) > 1 {
				f.statuses = f.statuses[1:]
			}
			f.lastStatus = s
			return []byte(s), nil
		}
		f.lastStatus = f.status
		return []byte(f.status), nil
	case strings.Contains(line, "cib-push") && f.onPush != nil:
		f.onPush()
	}
	return nil, nil
}
func (f *fakePCS) RunWithTimeout(n string, _ time.Duration, a ...string) ([]byte, error) {
	return f.Run(context.Background(), n, a...)
}

var _ sysutil.CommandRunner = (*fakePCS)(nil)

func (f *fakePCS) has(sub string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

const emptyCIB = `<cib><configuration><nodes><node uname="ha1"/><node uname="ha2"/><node uname="ha3"/></nodes><resources/><constraints/></configuration></cib>`

func startedStatus(id, node string) string {
	return `<pacemaker-result><resources><resource id="` + id + `" resource_agent="ocf:heartbeat:VirtualDomain" role="Started" active="true" managed="true"><node name="` + node + `"/></resource></resources></pacemaker-result>`
}

func kvmSpec() KVMSpec {
	return KVMSpec{Name: "web_1", DRBDResource: "kvm_data", Device: "/dev/drbd/by-res/kvm_data/0", MountDir: "/mnt/web-1",
		ConfigPath: "/mnt/web-1/web_1.xml", ClusterNodes: []string{"ha1", "ha2", "ha3"}, AllowedNodes: []string{"ha1", "ha2"}, PreferredNode: "ha1"}
}

func TestPlanEnableKVM(t *testing.T) {
	p, err := PlanEnableKVM(kvmSpec(), true)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(p.Display("/tmp/x"), "\n")
	for _, want := range []string{
		"pcs -f /tmp/x resource create ha-kvm-web-1-data ocf:linbit:drbd drbd_resource=kvm_data",
		"promotable promoted-max=1 promoted-node-max=1 clone-max=3 clone-node-max=1 notify=true",
		"ocf:heartbeat:Filesystem device=/dev/drbd/by-res/kvm_data/0 directory=/mnt/web-1 fstype=ext4 --group ha-kvm-web-1-grp",
		"ocf:heartbeat:VirtualDomain config=/mnt/web-1/web_1.xml hypervisor=qemu:///system force_stop=true",
		"webkvm-kind=kvm webkvm-name=web_1 migration-threshold=3 resource-stickiness=100",
		"constraint colocation add ha-kvm-web-1-grp with Promoted ha-kvm-web-1-data-clone score=INFINITY id=webkvm-col-ha-kvm-web-1-grp",
		"constraint order promote ha-kvm-web-1-data-clone then start ha-kvm-web-1-grp id=webkvm-ord-ha-kvm-web-1-grp",
		"constraint location add webkvm-loc-ha-kvm-web-1-grp-ha3 ha-kvm-web-1-grp ha3 score=-INFINITY",
		"constraint location add webkvm-loc-ha-kvm-web-1-grp-ha1 ha-kvm-web-1-grp ha1 score=100",
		"pcs cluster cib-push /tmp/x --config",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
	if strings.Contains(all, "--disabled") {
		t.Error("start=true must not create disabled")
	}
	for _, c := range p.Constraints {
		if !strings.HasPrefix(c, "webkvm-") {
			t.Errorf("constraint %s lacks webkvm- prefix", c)
		}
	}
	p2, _ := PlanEnableKVM(kvmSpec(), false)
	if !strings.Contains(strings.Join(p2.Display("t"), "\n"), "--disabled") {
		t.Error("start=false must create disabled")
	}
}

func TestPlanValidation(t *testing.T) {
	bad := []func(*KVMSpec){
		func(s *KVMSpec) { s.Name = "a b" },
		func(s *KVMSpec) { s.Name = "-x" },
		func(s *KVMSpec) { s.DRBDResource = "x;y" },
		func(s *KVMSpec) { s.Device = "/dev/sda" },
		func(s *KVMSpec) { s.MountDir = "mnt" },
		func(s *KVMSpec) { s.ConfigPath = "/a/../b.xml" },
		func(s *KVMSpec) { s.FSType = "ntfs" },
		func(s *KVMSpec) { s.AllowedNodes = []string{"ha1;rm"} },
		func(s *KVMSpec) { s.PreferredNode = "ha3" },
	}
	for i, mut := range bad {
		s := kvmSpec()
		mut(&s)
		if _, err := PlanEnableKVM(s, true); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if _, err := PlanEnableIncus(IncusSpec{Name: "c1", Project: "p;x"}, true); err == nil {
		t.Error("bad project accepted")
	}
	if id := IncusResourceID("My Proj", "C_1"); id != "ha-incus-my-proj-c-1" {
		t.Errorf("id = %s", id)
	}
}

func TestPlanEnableIncus(t *testing.T) {
	p, err := PlanEnableIncus(IncusSpec{Name: "c1", Project: "default", ClusterNodes: []string{"ha1", "ha2"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(p.Display("T"), "\n")
	for _, want := range []string{"incus config set c1 boot.autostart=false --project default",
		"resource create ha-incus-default-c1 ocf:webkvm:incus instance=c1 project=default op start timeout=300s op stop timeout=120s op monitor interval=30s",
		"webkvm-kind=incus webkvm-name=c1 webkvm-project=default migration-threshold=3 resource-stickiness=100 failure-timeout=10m"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
}

func TestApplyEnableAtomicSequence(t *testing.T) {
	f := &fakePCS{cib: emptyCIB}
	plan, _ := PlanEnableKVM(kvmSpec(), true)
	f.onPush = func() { f.status = startedStatus("ha-kvm-web-1", "ha1"); f.cib = labCIB }
	res, err := ApplyEnable(context.Background(), f, plan, EnableOptions{Start: true, StartTimeout: time.Second, PollInterval: time.Millisecond, TmpDir: t.TempDir()})
	if err != nil || !res.Changed || res.State != WorkloadStarted {
		t.Fatalf("%+v %v", res, err)
	}
	firstFile, push := -1, -1
	for i, c := range f.cmds {
		if strings.HasPrefix(c, "pcs -f ") && firstFile < 0 {
			firstFile = i
		}
		if strings.Contains(c, "cib-push") {
			push = i
		}
	}
	if firstFile < 0 || push < firstFile {
		t.Fatalf("edits must run against the file before the push: %v", f.cmds)
	}
	for _, c := range f.cmds[:push] {
		if strings.HasPrefix(c, "pcs resource ") {
			t.Fatalf("live edit before push: %s", c)
		}
	}
}

func TestApplyEnableIdempotent(t *testing.T) {
	f := &fakePCS{cib: labCIB, status: labStatus}
	plan, _ := PlanEnableKVM(kvmSpec(), true)
	res, err := ApplyEnable(context.Background(), f, plan, EnableOptions{Start: true, TmpDir: t.TempDir()})
	if err != nil || res.Changed || res.State != WorkloadStarted {
		t.Fatalf("%+v %v", res, err)
	}
	if f.has("cib-push") {
		t.Fatal("idempotent enable must not push")
	}
}

func TestApplyEnableCollision(t *testing.T) {
	f := &fakePCS{cib: labCIB, status: labStatus}
	spec := kvmSpec()
	spec.Name = "web-1" // same sanitized id as web_1
	plan, _ := PlanEnableKVM(spec, true)
	_, err := ApplyEnable(context.Background(), f, plan, EnableOptions{TmpDir: t.TempDir()})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeIDCollision {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyEnableDryRun(t *testing.T) {
	f := &fakePCS{cib: emptyCIB}
	plan, _ := PlanEnableKVM(kvmSpec(), true)
	res, err := ApplyEnable(context.Background(), f, plan, EnableOptions{DryRun: true})
	if err != nil || !res.Changed || len(res.Commands) == 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if f.has("cib-push") || f.has("-f") {
		t.Fatal("dry run touched the cluster")
	}
}

func TestApplyEnablePushFailureLeavesNothing(t *testing.T) {
	f := &fakePCS{cib: emptyCIB, failOn: "cib-push"}
	plan, _ := PlanEnableKVM(kvmSpec(), true)
	_, err := ApplyEnable(context.Background(), f, plan, EnableOptions{Start: true, TmpDir: t.TempDir()})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodePCSFailed {
		t.Fatalf("err = %v", err)
	}
	if f.has("resource delete") || f.has("resource disable") {
		t.Fatal("nothing live to roll back before the push")
	}
}

func TestApplyEnableStartTimeoutRollsBack(t *testing.T) {
	f := &fakePCS{cib: emptyCIB}
	plan, _ := PlanEnableKVM(kvmSpec(), true)
	_, err := ApplyEnable(context.Background(), f, plan, EnableOptions{Start: true, RollbackOnStartFailure: true, StartTimeout: 20 * time.Millisecond, PollInterval: 5 * time.Millisecond, TmpDir: t.TempDir()})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeStartTimeout {
		t.Fatalf("err = %v", err)
	}
	if !f.has("pcs resource disable ha-kvm-web-1-grp") || !f.has("pcs resource delete ha-kvm-web-1-grp") || !f.has("pcs resource delete ha-kvm-web-1-data-clone") {
		t.Fatalf("rollback missing: %v", f.cmds)
	}
}

func TestApplyEnableRollbackFailureReportsLeftovers(t *testing.T) {
	f := &fakePCS{cib: emptyCIB, failOn: "resource delete"}
	plan, _ := PlanEnableKVM(kvmSpec(), true)
	_, err := ApplyEnable(context.Background(), f, plan, EnableOptions{Start: true, RollbackOnStartFailure: true, StartTimeout: 10 * time.Millisecond, PollInterval: 2 * time.Millisecond, TmpDir: t.TempDir()})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeRollbackFailed || len(oe.Leftovers) != 2 {
		t.Fatalf("err = %v %+v", err, oe)
	}
}

func TestApplyEnableBusy(t *testing.T) {
	unlock, ok := LockKey("kvm:web_1")
	if !ok {
		t.Fatal()
	}
	defer unlock()
	plan, _ := PlanEnableKVM(kvmSpec(), true)
	_, err := ApplyEnable(context.Background(), &fakePCS{cib: emptyCIB}, plan, EnableOptions{})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeBusy {
		t.Fatalf("err = %v", err)
	}
}

func TestApplyEnableIncusSetsAutostartFirst(t *testing.T) {
	f := &fakePCS{cib: emptyCIB}
	plan, _ := PlanEnableIncus(IncusSpec{Name: "c1", Project: "default"}, false)
	if _, err := ApplyEnable(context.Background(), f, plan, EnableOptions{TmpDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	idx := -1
	for i, c := range f.cmds {
		if strings.HasPrefix(c, "incus config set c1 boot.autostart=false") {
			idx = i
		}
	}
	if idx < 0 || !f.has("--disabled") {
		t.Fatalf("%v", f.cmds)
	}
}

func TestApplyDisableModes(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, statuses: []string{labStatus, strings.Replace(labStatus, `role="Started" active="true"`, `role="Stopped" active="false"`, 2)}}
	res, err := ApplyDisable(context.Background(), f, key, "stop", DisableOptions{PollInterval: time.Millisecond, TmpDir: t.TempDir(), StopTimeout: time.Second})
	if err != nil || !res.Changed || res.State != "absent" {
		t.Fatalf("%+v %v", res, err)
	}
	if !f.has("pcs resource disable ha-kvm-web-1-grp") || !f.has("-f ") || !f.has("resource delete ha-kvm-web-1-grp") || !f.has("resource delete ha-kvm-web-1-data-clone") {
		t.Fatalf("%v", f.cmds)
	}

	f = &fakePCS{cib: labCIB, status: labStatus}
	res, err = ApplyDisable(context.Background(), f, key, "pause")
	if err != nil || res.State != WorkloadPaused || f.has("delete") {
		t.Fatalf("pause: %+v %v %v", res, err, f.cmds)
	}

	// forget of a running stack would orphan active resources: refused,
	// nothing touched (v11 incident). Of a stopped stack: deleted.
	f = &fakePCS{cib: labCIB, status: labStatus}
	_, err = ApplyDisable(context.Background(), f, key, "forget", DisableOptions{TmpDir: t.TempDir()})
	var fe *OpError
	if !errors.As(err, &fe) || fe.Code != CodeWorkloadActive || f.has("resource disable") || f.has("resource delete") {
		t.Fatalf("forget of a running stack: %v %v", err, f.cmds)
	}
	stoppedStatus := strings.Replace(labStatus, `role="Started" active="true"`, `role="Stopped" active="false"`, 2)
	f = &fakePCS{cib: labCIB, status: stoppedStatus}
	if _, err = ApplyDisable(context.Background(), f, key, "forget", DisableOptions{TmpDir: t.TempDir(), PollInterval: time.Millisecond}); err != nil || !f.has("resource delete ha-kvm-web-1-grp") {
		t.Fatalf("forget: %v %v", err, f.cmds)
	}

	f = &fakePCS{cib: labCIB, status: labStatus}
	_, err = ApplyDisable(context.Background(), f, key, "keep_running")
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeConfirmation || len(f.cmds) != 0 {
		t.Fatalf("keep_running without flag: %v", err)
	}
	// keep_running of an active stack is refused for the same reason.
	if _, err = ApplyDisable(context.Background(), f, key, "keep_running", DisableOptions{AllowKeepRunning: true, TmpDir: t.TempDir()}); !errors.As(err, &oe) || oe.Code != CodeWorkloadActive || f.has("resource delete") || f.has("cib-push") {
		t.Fatalf("keep_running: %v %v", err, f.cmds)
	}

	f = &fakePCS{cib: emptyCIB}
	res, err = ApplyDisable(context.Background(), f, key, "stop")
	if err != nil || res.Changed {
		t.Fatalf("absent must be a no-op: %+v %v", res, err)
	}
}

func TestMoveWorkload(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, statuses: []string{labStatus, strings.ReplaceAll(labStatus, `<node name="ha1"/>`, `<node name="ha2"/>`)}}
	w, err := MoveWorkload(context.Background(), f, key, "ha2", MoveOptions{Timeout: time.Second, PollInterval: time.Millisecond})
	if err != nil || w.Node != "ha2" {
		t.Fatalf("%+v %v", w, err)
	}
	mv, cl := -1, -1
	for i, c := range f.cmds {
		if c == "pcs resource move ha-kvm-web-1-grp ha2" {
			mv = i
		}
		if c == "pcs resource clear ha-kvm-web-1-grp" {
			cl = i
		}
	}
	if mv < 0 || cl < mv {
		t.Fatalf("move then clear expected: %v", f.cmds)
	}
	// node banned by webkvm-loc is refused; invalid node too
	_, err = MoveWorkload(context.Background(), &fakePCS{cib: labCIB, status: labStatus}, key, "ha3", MoveOptions{})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeNodeNotAllowed {
		t.Fatalf("err = %v", err)
	}
	if _, err = MoveWorkload(context.Background(), f, key, "ha2;x", MoveOptions{}); err == nil {
		t.Fatal("invalid node accepted")
	}
	// timeout still clears
	g := &fakePCS{cib: labCIB, status: labStatus}
	_, err = MoveWorkload(context.Background(), g, key, "ha2", MoveOptions{Timeout: 10 * time.Millisecond, PollInterval: 2 * time.Millisecond})
	if !errors.As(err, &oe) || oe.Code != CodeStartTimeout || !g.has("resource clear") {
		t.Fatalf("err=%v cmds=%v", err, g.cmds)
	}
}

func TestMoveRefusedWhenUnmanaged(t *testing.T) {
	f := &fakePCS{cib: labCIB, status: labStatus}
	_, err := MoveWorkload(context.Background(), f, WorkloadKey{Kind: KindIncus, Project: "default", Name: "c1"}, "ha1", MoveOptions{})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeMaintenanceActive {
		t.Fatalf("err = %v", err)
	}
}

func TestClearFailcountAndPlacement(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, status: labStatus}
	if err := ClearFailcount(context.Background(), f, key, ""); err != nil || !f.has("pcs resource cleanup ha-kvm-web-1-grp") {
		t.Fatalf("%v %v", err, f.cmds)
	}
	f = &fakePCS{cib: labCIB, status: labStatus}
	if _, err := ApplyPlacement(context.Background(), f, key, []string{"ha1", "ha3"}, "ha3", "", t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"constraint remove webkvm-loc-ha-kvm-web-1-grp-ha3", "constraint remove webkvm-loc-ha-kvm-web-1-grp-ha1",
		"constraint location add webkvm-loc-ha-kvm-web-1-grp-ha2 ha-kvm-web-1-grp ha2 score=-INFINITY", "location add webkvm-loc-ha-kvm-web-1-grp-ha3 ha-kvm-web-1-grp ha3 score=100"} {
		if !f.has(want) {
			t.Errorf("missing %q in %v", want, f.cmds)
		}
	}
	if f.has("constraint remove manual") {
		t.Error("a constraint without the webkvm- prefix must never be deleted")
	}
	if _, err := ApplyPlacement(context.Background(), f, key, []string{"nope"}, "", "", t.TempDir()); err == nil {
		t.Error("unknown node accepted")
	}
}

func TestAntiAffinity(t *testing.T) {
	cmd, id, err := PlanAntiAffinity("ha-kvm-b-grp", "ha-kvm-a-grp")
	if err != nil || id != "webkvm-anti-ha-kvm-a-grp-ha-kvm-b-grp" || cmd[len(cmd)-1] != "id="+id {
		t.Fatalf("%v %s %v", cmd, id, err)
	}
	if _, _, err := PlanAntiAffinity("a;b", "ha-x"); err == nil {
		t.Fatal("invalid id accepted")
	}
}

func TestMaintenance(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	f := &fakePCS{cib: labCIB, status: labStatus}
	res, err := BeginMaintenance(context.Background(), f, key, time.Hour, MaintOptions{Now: func() time.Time { return now }})
	if err != nil || !res.Changed || res.Until != "2026-11-12T11:00:00Z" {
		t.Fatalf("%+v %v", res, err)
	}
	if !f.has("pcs resource unmanage ha-kvm-web-1-grp") || !f.has("resource meta ha-kvm-web-1 webkvm-maint-until=2026-11-12T11:00:00Z webkvm-maint-prev=started") {
		t.Fatalf("%v", f.cmds)
	}
	if _, err := BeginMaintenance(context.Background(), f, key, time.Second, MaintOptions{}); err == nil {
		t.Fatal("ttl below minimum accepted")
	}
	g := &fakePCS{cib: labCIB, status: labStatus}
	incus := WorkloadKey{Kind: KindIncus, Project: "default", Name: "c1"}
	res, err = EndMaintenance(context.Background(), g, incus, MaintOptions{Timeout: 20 * time.Millisecond, PollInterval: 5 * time.Millisecond})
	var oe *OpError
	if !errors.As(err, &oe) || oe.Code != CodeStartTimeout || !g.has("resource manage ha-incus-default-c1") || !g.has("resource refresh") {
		t.Fatalf("%+v %v %v", res, err, g.cmds)
	}
}

func TestSafeEditXML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "vm.xml")
	orig := `<domain type="kvm"><name>web_1</name></domain>`
	os.WriteFile(p, []byte(orig), 0o600)
	now := time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC)
	if _, err := SafeEditXML(p, []byte(`<domain><name>other</name></domain>`), "web_1", now, nil); err == nil {
		t.Fatal("name mismatch accepted")
	}
	if _, err := SafeEditXML(p, []byte(`<domain`), "web_1", now, nil); err == nil {
		t.Fatal("malformed accepted")
	}
	if b, _ := os.ReadFile(p); string(b) != orig {
		t.Fatal("file changed by a rejected edit")
	}
	next := `<domain type="kvm"><name>web_1</name><memory>2</memory></domain>`
	bak, err := SafeEditXML(p, []byte(next), "web_1", now, func() error { return errors.New("start failed") })
	if err == nil || !strings.HasSuffix(bak, ".webkvm-bak-20261112T010203Z") {
		t.Fatalf("%q %v", bak, err)
	}
	if b, _ := os.ReadFile(p); string(b) != orig {
		t.Fatal("not restored after failed start")
	}
	if _, err := SafeEditXML(p, []byte(next), "web_1", now, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != next {
		t.Fatal("edit not applied")
	}
	if err := RestoreXML(p, "/etc/passwd"); err == nil {
		t.Fatal("foreign backup accepted")
	}
}

func TestApplyResumePausedWorkload(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	pausedCIB := strings.Replace(labCIB, `<nvpair name="webkvm-kind" value="kvm"/><nvpair name="webkvm-name" value="web_1"/>`,
		`<nvpair name="webkvm-kind" value="kvm"/><nvpair name="webkvm-name" value="web_1"/><nvpair name="target-role" value="Stopped"/>`, 1)
	stopped := strings.Replace(labStatus, `role="Started" active="true"`, `role="Stopped" active="false"`, 2)
	f := &fakePCS{cib: pausedCIB, statuses: []string{stopped, labStatus}}
	res, err := ApplyResume(context.Background(), f, key, ResumeOptions{StartTimeout: time.Second, PollInterval: time.Millisecond})
	if err != nil || !res.Changed || res.State != WorkloadStarted || !f.has("pcs resource enable ha-kvm-web-1-grp") {
		t.Fatalf("%+v %v %v", res, err, f.cmds)
	}
	// Not paused: no-op, nothing is enabled.
	f = &fakePCS{cib: labCIB, status: labStatus}
	res, err = ApplyResume(context.Background(), f, key, ResumeOptions{})
	if err != nil || res.Changed || f.has("resource enable") {
		t.Fatalf("running workload must be a no-op: %+v %v %v", res, err, f.cmds)
	}
}

func TestMoveWorkloadAlreadyOnNodeIsNoop(t *testing.T) {
	key := WorkloadKey{Kind: KindKVM, Name: "web_1"}
	f := &fakePCS{cib: labCIB, status: labStatus}
	w, err := MoveWorkload(context.Background(), f, key, "ha1", MoveOptions{Timeout: time.Second, PollInterval: time.Millisecond})
	if err != nil || w.Node != "ha1" || f.has("resource move") {
		t.Fatalf("%+v %v %v", w, err, f.cmds)
	}
}
