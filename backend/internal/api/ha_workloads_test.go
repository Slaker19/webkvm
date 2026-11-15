package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/ha"
	"webkvm/internal/sysutil"
)

const cpCIBBody = `<group id="webkvm-cp">
<primitive id="webkvm-fs" class="ocf" provider="heartbeat" type="Filesystem"><instance_attributes><nvpair name="directory" value="%DATA%"/></instance_attributes></primitive>
<primitive id="webkvm-vip" class="ocf" provider="heartbeat" type="IPaddr2"/>
<primitive id="webkvm-app" class="ocf" provider="webkvm" type="webkvm"/></group>
<clone id="webkvm-data-clone"><primitive id="webkvm-data" class="ocf" provider="linbit" type="drbd"/></clone>`

const cpConstraints = `<rsc_colocation id="c" rsc="webkvm-cp" with-rsc="webkvm-data-clone" with-rsc-role="Promoted" score="INFINITY"/>
<rsc_order id="o" first="webkvm-data-clone" first-action="promote" then="webkvm-cp"/>`

const vmCIBBody = `<group id="ha-kvm-web-grp"><primitive id="ha-kvm-web-fs" class="ocf" provider="heartbeat" type="Filesystem"/>
<primitive id="ha-kvm-web" class="ocf" provider="heartbeat" type="VirtualDomain"><instance_attributes><nvpair name="config" value="/mnt/vm/web.xml"/></instance_attributes>
<meta_attributes id="m"><nvpair name="webkvm-kind" value="kvm"/><nvpair name="webkvm-name" value="web"/>%MAINT%</meta_attributes></primitive></group>`

const incusCIBBody = `<primitive id="ha-incus-default-c1" class="ocf" provider="webkvm" type="incus"><instance_attributes><nvpair name="instance" value="c1"/><nvpair name="project" value="default"/></instance_attributes>
<meta_attributes id="mi"><nvpair name="webkvm-kind" value="incus"/><nvpair name="webkvm-name" value="c1"/><nvpair name="webkvm-project" value="default"/>%MAINT%</meta_attributes></primitive>`

const webDomainXML = `<domain type='kvm'><name>web</name><uuid>5b0c6f1e-1111-4222-8333-944455556666</uuid><memory unit='KiB'>131072</memory></domain>`

func nodesStatus(started bool, incusStarted ...bool) string {
	vm := ""
	if started {
		vm = `<group id="ha-kvm-web-grp"><resource id="ha-kvm-web" resource_agent="ocf:heartbeat:VirtualDomain" role="Started" active="true" managed="true"><node name="ha1"/></resource></group>`
	}
	if len(incusStarted) > 0 && incusStarted[0] {
		vm += `<resource id="ha-incus-default-c1" resource_agent="ocf:webkvm:incus" role="Started" active="true" managed="true"><node name="ha2"/></resource>`
	}
	return `<pacemaker-result><status code="0"/><crm_mon><summary><cluster_name>lab</cluster_name><quorum>true</quorum></summary><nodes>
<node name="ha1" online="true" type="member"/><node name="ha2" online="true" type="member"/><node name="ha3" online="true" type="member"/></nodes><resources>` + vm + `</resources>` + fenceHistoryXML + `</crm_mon></pacemaker-result>`
}

// fenceHistoryXML is injected into the fake status by setupWL(fenced=true):
// real evidence is a successful reboot event in fence_history.
var fenceHistoryXML = ""

// wlRunner is a small stateful Pacemaker fake.
type wlRunner struct {
	mu       sync.Mutex
	vm       bool   // VM resource exists in the CIB
	started  bool   // VM reports Started
	maint    string // extra meta nvpairs
	cmds     []string
	watchdog bool
	hasCP    bool
	dataDir  string
	// Incus workload (c1 in the default project).
	inc        bool
	incStarted bool
	// pending* track the edits made against the scratch CIB until cib-push.
	pendingIncus  bool
	pendingDelete bool
}

func (f *wlRunner) cib() string {
	body := ""
	if f.hasCP {
		body += strings.Replace(cpCIBBody, "%DATA%", f.dataDir, 1)
	}
	if f.vm {
		body += strings.Replace(vmCIBBody, "%MAINT%", f.maint, 1)
	}
	if f.inc {
		body += strings.Replace(incusCIBBody, "%MAINT%", f.maint, 1)
	}
	prop := ""
	if f.watchdog {
		prop = `<crm_config><cluster_property_set><nvpair name="stonith-enabled" value="true"/><nvpair name="stonith-watchdog-timeout" value="20s"/></cluster_property_set></crm_config>`
	}
	cons := ""
	if f.hasCP {
		cons = cpConstraints
	}
	return `<cib><configuration>` + prop + `<nodes><node uname="ha1"/><node uname="ha2"/><node uname="ha3"/></nodes><resources>` + body + `</resources><constraints>` + cons + `</constraints></configuration></cib>`
}

func (f *wlRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, line)
	switch {
	case line == "pcs cluster cib":
		return []byte(f.cib()), nil
	case line == "pcs status xml":
		return []byte(nodesStatus(f.started, f.incStarted)), nil
	case strings.Contains(line, "ocf:webkvm:incus"):
		f.pendingIncus = true
	case strings.Contains(line, " resource delete "):
		f.pendingDelete = true
	case strings.Contains(line, "cib-push"):
		switch {
		case f.pendingDelete:
			f.vm, f.started, f.inc, f.incStarted = false, false, false, false
		case f.pendingIncus:
			f.inc, f.incStarted = true, true
		default:
			f.vm, f.started = true, true
		}
		f.pendingDelete, f.pendingIncus = false, false
	case strings.HasPrefix(line, "pcs resource disable"):
		f.started, f.incStarted = false, false
		f.maint += `<nvpair name="target-role" value="Stopped"/>`
	case strings.HasPrefix(line, "pcs resource enable"):
		f.started, f.incStarted = f.vm, f.inc
		f.maint = strings.ReplaceAll(f.maint, `<nvpair name="target-role" value="Stopped"/>`, "")
	case strings.HasPrefix(line, "pcs resource unmanage"):
		f.maint = `<nvpair name="is-managed" value="false"/>`
	case strings.HasPrefix(line, "pcs resource meta"):
		for _, a := range args {
			if strings.HasPrefix(a, "webkvm-maint-until=") && a != "webkvm-maint-until=" {
				f.maint += `<nvpair name="webkvm-maint-until" value="` + strings.TrimPrefix(a, "webkvm-maint-until=") + `"/>`
			}
		}
	case strings.HasPrefix(line, "pcs resource manage"):
		f.maint = ""
	case name == "drbdsetup":
		return []byte("kvm_data node-id:0 role:Primary\n  volume:0 minor:1001 disk:UpToDate\n  ha2 node-id:1 connection:Connected\n    volume:0 peer-disk:UpToDate\n"), nil
	case name == "findmnt":
		return []byte("/mnt/vm /dev/drbd1001 ext4\n"), nil
	}
	return nil, nil
}
func (f *wlRunner) RunWithTimeout(n string, _ time.Duration, a ...string) ([]byte, error) {
	return f.Run(context.Background(), n, a...)
}
func (f *wlRunner) has(sub string) bool {
	for _, c := range f.cmds {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

type fakeFacts struct {
	vm      kvmVMFacts
	insp    ha.IncusInspector
	members []string
}

func (f fakeFacts) KVM(context.Context, string) (kvmVMFacts, error) { return f.vm, nil }
func (f fakeFacts) Incus(context.Context, string, string) (ha.IncusInspector, []string, error) {
	if f.insp == nil {
		return nil, nil, compute.ErrNotFound
	}
	return f.insp, f.members, nil
}
func (f fakeFacts) NodeBridges(context.Context) (map[string][]string, error) { return nil, nil }

func setupWL(t *testing.T, r *wlRunner, facts workloadFacts, fenced bool) http.Handler {
	t.Helper()
	_, router := setupWLHandler(t, r, facts, fenced)
	return router
}

func setupWLHandler(t *testing.T, r *wlRunner, facts workloadFacts, fenced bool) (*Handler, http.Handler) {
	t.Helper()
	prevRunner, prevFacts, prevTune, prevProbe := sysutil.DefaultRunner, newWorkloadFacts, workloadTuning, incusHealingProbe
	t.Cleanup(func() {
		sysutil.DefaultRunner, newWorkloadFacts, workloadTuning, incusHealingProbe = prevRunner, prevFacts, prevTune, prevProbe
	})
	sysutil.DefaultRunner = r
	newWorkloadFacts = func(*Handler) workloadFacts { return facts }
	// Released-VM capture (8.20): the fake VM runs on ha1 = this node and its
	// definition is readable on the replicated filesystem.
	prevRead, prevLocal, prevLocalXML, prevPeerXML, prevRelTune := releaseReadFile, releaseLocalNode, releaseLocalDomainXML, releasePeerDomainXML, releaseTuning
	t.Cleanup(func() {
		releaseReadFile, releaseLocalNode, releaseLocalDomainXML, releasePeerDomainXML, releaseTuning = prevRead, prevLocal, prevLocalXML, prevPeerXML, prevRelTune
	})
	releaseLocalNode = func() string { return "ha1" }
	releaseReadFile = func(p string) ([]byte, error) {
		if p == "/mnt/vm/web.xml" {
			return []byte(webDomainXML), nil
		}
		return nil, os.ErrNotExist
	}
	releaseLocalDomainXML = func(*Handler, string) (string, error) { return "", errors.New("no local libvirt in tests") }
	releasePeerDomainXML = func(context.Context, *Handler, string, string) (string, error) {
		return "", errors.New("no peer libvirt in tests")
	}
	releaseTuning.ConnectWait, releaseTuning.Poll = 30*time.Millisecond, 5*time.Millisecond
	incusHealingProbe = func(context.Context) (int64, bool) { return 0, false }
	workloadTuning.StartTimeout, workloadTuning.StopTimeout, workloadTuning.MoveTimeout, workloadTuning.Poll = 200*time.Millisecond, 200*time.Millisecond, 200*time.Millisecond, 5*time.Millisecond
	dir := t.TempDir()
	r.dataDir = dir
	fenceHistoryXML = ""
	if fenced {
		fenceHistoryXML = `<fence_history><fence_event action="reboot" target="ha2" status="success" completed="2026-11-12 10:00:00 +00:00"/></fence_history>`
		t.Cleanup(func() { fenceHistoryXML = "" })
	}
	h := &Handler{cfg: &config.Config{DataDir: dir}}
	router := chi.NewRouter()
	h.registerHAWorkloadRoutes(router)
	return h, router
}

func doWL(h http.Handler, method, path, body string) (int, map[string]any) {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func blockerList(m map[string]any) string {
	b, _ := json.Marshal(m["blockers"])
	return string(b)
}

func goodFacts() fakeFacts {
	return fakeFacts{vm: kvmVMFacts{Exists: true, DiskPaths: []string{"/mnt/vm/web.qcow2"}, XMLPath: "/mnt/vm/web.xml"}}
}

func TestHAWorkloadEnablePreconditionFailed(t *testing.T) {
	r := &wlRunner{}
	h := setupWL(t, r, goodFacts(), false)
	code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{}`)
	if code != http.StatusConflict || body["code"] != "precondition_failed" {
		t.Fatalf("%d %v", code, body)
	}
	for _, want := range []string{"fencing_test_required", "sbd_not_active", "control_plane_unhealthy"} {
		if !strings.Contains(blockerList(body), want) {
			t.Errorf("missing %s in %s", want, blockerList(body))
		}
	}
	if r.has("cib-push") {
		t.Fatal("must not touch the CIB")
	}
}

func TestHAWorkloadEnableNotEligible(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	f := goodFacts()
	f.vm.Running = true
	h := setupWL(t, r, f, true)
	code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{"dry_run":true}`)
	if code != http.StatusConflict || !strings.Contains(blockerList(body), "not_eligible") {
		t.Fatalf("%d %v", code, body)
	}
	reasons, _ := json.Marshal(body["reasons"])
	if !strings.Contains(string(reasons), "vm_running") {
		t.Fatalf("%s", reasons)
	}
}

func TestHAWorkloadEnableHappyPathAndIdempotent(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	h := setupWL(t, r, goodFacts(), true)
	code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{"allowed_nodes":["ha1","ha2"],"preferred_node":"ha1"}`)
	if code != http.StatusOK || body["changed"] != true || body["state"] != "started" {
		t.Fatalf("%d %v", code, body)
	}
	cmds, _ := json.Marshal(body["commands"])
	if !strings.Contains(string(cmds), "cib-push") || !strings.Contains(string(cmds), "webkvm-loc-ha-kvm-web-grp-ha3") {
		t.Fatalf("%s", cmds)
	}
	code, body = doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{}`)
	if code != http.StatusOK || body["changed"] != false {
		t.Fatalf("second call must be a no-op: %d %v", code, body)
	}
	// dry-run on a clean cluster changes nothing
	r2 := &wlRunner{watchdog: true, hasCP: true}
	h2 := setupWL(t, r2, goodFacts(), true)
	code, body = doWL(h2, http.MethodPost, "/workloads/kvm/web/enable", `{"dry_run":true}`)
	if code != http.StatusOK || body["changed"] != true || r2.has("cib-push") {
		t.Fatalf("%d %v", code, body)
	}
}

func TestHAWorkloadValidation(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	h := setupWL(t, r, goodFacts(), true)
	for _, tc := range []struct{ method, path, body, code string }{
		{http.MethodPost, "/workloads/kvm/web/enable", `{"allowed_nodes":["ha1;rm -rf"]}`, "invalid_node_name"},
		{http.MethodPost, "/workloads/kvm/web/enable", `{"allowed_nodes":["ghost"]}`, "invalid_node_name"},
		{http.MethodPost, "/workloads/kvm/-bad/enable", `{}`, "invalid_body"},
		{http.MethodPost, "/workloads/lxc/web/enable", `{}`, "invalid_body"},
		{http.MethodPost, "/workloads/kvm/web/move", `{"node":"a b"}`, "invalid_node_name"},
		{http.MethodPost, "/workloads/kvm/web/maintenance", `{"action":"x"}`, "invalid_body"},
		{http.MethodDelete, "/workloads/kvm/web?mode=nuke", ``, "invalid_body"},
		{http.MethodDelete, "/workloads/kvm/web?mode=forget", ``, "confirmation_required"},
		{http.MethodPost, "/workloads/bulk", `{"action":"enable","items":[]}`, "invalid_body"},
	} {
		code, body := doWL(h, tc.method, tc.path, tc.body)
		if body["code"] != tc.code || code < 400 {
			t.Errorf("%s %s: %d %v want %s", tc.method, tc.path, code, body, tc.code)
		}
	}
	if len(r.cmds) > 0 && r.has("pcs resource") {
		t.Fatalf("validation failures must not reach pcs: %v", r.cmds)
	}
}

func TestHAWorkloadNotFoundAndFactsUnavailable(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	f := goodFacts()
	f.vm.Exists = false
	h := setupWL(t, r, f, true)
	if code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{}`); code != 404 || body["code"] != "workload_not_found" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/move", `{"node":"ha2"}`); code != 404 || body["code"] != "workload_not_found" {
		t.Fatalf("%d %v", code, body)
	}
	newWorkloadFacts = nil
	if code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{}`); code != 503 || body["code"] != "facts_unavailable" {
		t.Fatalf("%d %v", code, body)
	}
}

func TestHAWorkloadListGetDisableMove(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true}
	h := setupWL(t, r, goodFacts(), true)
	code, body := doWL(h, http.MethodGet, "/workloads", "")
	wls, _ := body["workloads"].([]any)
	if code != 200 || len(wls) != 1 || body["preconditions"].(map[string]any)["ok"] != true {
		t.Fatalf("%d %v", code, body)
	}
	if body["summary"].(map[string]any)["managed"].(float64) != 1 {
		t.Fatalf("%v", body["summary"])
	}
	code, body = doWL(h, http.MethodGet, "/workloads/kvm/web", "")
	if code != 200 || body["node"] != "ha1" || body["resource_id"] != "ha-kvm-web" {
		t.Fatalf("%d %v", code, body)
	}
	code, body = doWL(h, http.MethodPost, "/workloads/kvm/web/clear-failcount", ``)
	if code != 200 || !r.has("pcs resource cleanup ha-kvm-web-grp") {
		t.Fatalf("%d %v %v", code, body, r.cmds)
	}
	code, body = doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=pause", ``)
	if code != 200 || body["state"] != "paused" || !r.has("pcs resource disable ha-kvm-web-grp") {
		t.Fatalf("%d %v", code, body)
	}
	if code, body = doWL(h, http.MethodPost, "/workloads/kvm/web/move", `{"node":"ha3"}`); code != 200 && code != 504 && code != 409 {
		t.Fatalf("%d %v", code, body)
	}
}

func TestHAWorkloadBulk(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	h := setupWL(t, r, goodFacts(), true)
	code, body := doWL(h, http.MethodPost, "/workloads/bulk", `{"action":"enable","items":[{"kind":"kvm","name":"web"},{"kind":"kvm","name":"bad name"}]}`)
	res, _ := body["results"].([]any)
	if code != 200 || len(res) != 2 || res[0].(map[string]any)["ok"] != true || res[1].(map[string]any)["code"] != "invalid_body" {
		t.Fatalf("%d %v", code, body)
	}
	items := make([]string, 51)
	for i := range items {
		items[i] = `{"kind":"kvm","name":"v"}`
	}
	if code, body = doWL(h, http.MethodPost, "/workloads/bulk", `{"action":"enable","items":[`+strings.Join(items, ",")+`]}`); code != 400 {
		t.Fatalf("51 items accepted: %d %v", code, body)
	}
}

func TestIncusHealingOnlyAcceptsZero(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.UpdateIncusHealing(rec, httptest.NewRequest(http.MethodPut, "/api/incus/healing", strings.NewReader(`{"threshold_seconds":300}`)))
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusConflict || body["code"] != "healing_managed_by_pacemaker" {
		t.Fatalf("%d %v", rec.Code, body)
	}
}

func TestReadinessIncusHealingBlockerAndExpiredMaintenance(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true,
		maint: `<nvpair name="is-managed" value="false"/><nvpair name="webkvm-maint-until" value="2020-01-01T00:00:00Z"/>`}
	prevRunner, prevProbe := sysutil.DefaultRunner, incusHealingProbe
	t.Cleanup(func() { sysutil.DefaultRunner, incusHealingProbe = prevRunner, prevProbe })
	sysutil.DefaultRunner = r
	incusHealingProbe = func(context.Context) (int64, bool) { return 300, true }
	r.dataDir = t.TempDir()
	h := &Handler{cfg: &config.Config{DataDir: r.dataDir}}
	res := buildHAReadiness(ha.ProbePacemaker(context.Background(), r), nil, nil, controlPlaneHAReadiness{}, fencingHAReadiness{}, ha.LocalInstallStatus{}, time.Now())
	h.applyWorkloadReadiness(context.Background(), &res)
	if !contains(res.Blockers, "incus_healing_enabled") || res.CanEnable {
		t.Fatalf("%v", res.Blockers)
	}
	if !contains(res.Warnings, "workload_unmanaged_expired") || len(res.ExpiredMaintenance) != 1 || res.ExpiredMaintenance[0] != "kvm:web" {
		t.Fatalf("%v %v", res.Warnings, res.ExpiredMaintenance)
	}
	if len(res.ControlPlane.Checks) != 0 {
		t.Fatalf("lab control plane must verify: %+v", res.ControlPlane.Checks)
	}
}

func TestCPGroupSettingFallback(t *testing.T) {
	if got := (&Handler{}).cpGroup(); got != "webkvm-cp" {
		t.Fatal(got)
	}
}

func TestHAWorkloadEnableResumesPaused(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true}
	h := setupWL(t, r, goodFacts(), true)
	if code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{}`); code != 200 || body["state"] != "started" {
		t.Fatalf("%d %v", code, body)
	}
	if code, body := doWL(h, http.MethodDelete, "/workloads/kvm/web?mode=pause", ``); code != 200 || body["state"] != "paused" {
		t.Fatalf("%d %v", code, body)
	}
	code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{}`)
	if code != 200 || body["changed"] != true || body["state"] != "started" || !r.has("pcs resource enable ha-kvm-web-grp") {
		t.Fatalf("re-enable must resume a paused workload: %d %v %v", code, body, r.cmds)
	}
	code, body = doWL(h, http.MethodPost, "/workloads/kvm/web/enable", `{}`)
	if code != 200 || body["changed"] != false {
		t.Fatalf("started workload stays a no-op: %d %v", code, body)
	}
}

func TestOpToAPIDisableSafetyCodes(t *testing.T) {
	if e := opToAPI(&ha.OpError{Code: ha.CodeStopTimeout, Msg: "x"}); e.Status != http.StatusGatewayTimeout || e.Code != "stop_timeout" {
		t.Fatalf("stop_timeout -> %+v", e)
	}
	if e := opToAPI(&ha.OpError{Code: ha.CodeWorkloadActive, Msg: "x", Leftovers: []string{"ha-kvm-a-data"}}); e.Status != http.StatusConflict || e.Code != "workload_active" || len(e.Leftovers) != 1 {
		t.Fatalf("workload_active -> %+v", e)
	}
	if e := opToAPI(&ha.OpError{Code: ha.CodeWorkloadPaused, Msg: "x", Leftovers: []string{"ha-kvm-a-data"}}); e.Status != http.StatusConflict || e.Code != "workload_paused" || len(e.Leftovers) != 1 {
		t.Fatalf("workload_paused -> %+v", e)
	}
}
