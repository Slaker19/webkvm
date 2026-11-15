package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/ha"
	"webkvm/internal/nodes"
	"webkvm/internal/sysutil"
)

func TestGetHAReadinessIsReadOnlyAndDoesNotReturnFencingSecrets(t *testing.T) {
	previousRunner := sysutil.DefaultRunner
	t.Cleanup(func() { sysutil.DefaultRunner = previousRunner })
	sysutil.DefaultRunner = &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "systemctl" {
			if len(args) != 2 || args[0] != "is-active" {
				t.Fatalf("unexpected systemctl args: %v", args)
			}
			if args[1] == "sbd" {
				return []byte("inactive\n"), nil
			}
			return []byte("active\n"), nil
		}
		if name == "modinfo" {
			return []byte("filename: /lib/modules/kernel/updates/dkms/drbd.ko.zst\n"), nil
		}
		if name != "pcs" {
			t.Fatalf("unexpected executable %q", name)
		}
		switch strings.Join(args, " ") {
		case "status xml":
			return []byte(`<pacemaker-result><status code="0"/><crm_mon><summary><cluster_name>ha-lab</cluster_name><quorum>true</quorum></summary><nodes><node name="kvm-a" type="member" online="true"/><node name="kvm-b" type="member" online="true"/></nodes></crm_mon></pacemaker-result>`), nil
		case "cluster cib":
			return []byte(`<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-enabled" value="true"/></cluster_property_set></crm_config><nodes><node uname="kvm-a"/><node uname="kvm-b"/></nodes><resources><primitive id="fence-a" class="stonith" type="fence_ipmilan"><instance_attributes><nvpair name="password" value="fence-secret"/></instance_attributes></primitive></resources></configuration></cib>`), nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	}}

	registry, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.CreateTyped("incus-west", "https://incus.example:8443", nodes.BackendIncus, "containers", strings.Repeat("a", 64), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	(&Handler{nodes: registry}).GetHAReadiness(response, httptest.NewRequest(http.MethodGet, "/api/ha/readiness", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "fence-secret") {
		t.Fatalf("HA readiness exposed Pacemaker configuration secrets: %s", response.Body.String())
	}
	var got haReadiness
	if err := decodeBody(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(response.Body.String())), &got); err != nil {
		t.Fatal(err)
	}
	if got.CanEnable || got.State != "blocked" {
		t.Fatalf("unverified HA must remain blocked: %+v", got)
	}
	if got.Pacemaker.Quorum == nil || !*got.Pacemaker.Quorum || got.Pacemaker.FenceDevices != 1 {
		t.Fatalf("Pacemaker facts not parsed: %+v", got.Pacemaker)
	}
	if len(got.IncusTargets) != 1 || got.IncusTargets[0].State != "cluster_admin_certificate_required" {
		t.Fatalf("Incus authorization blocker missing: %+v", got.IncusTargets)
	}
	if got.CheckedAt.IsZero() || got.CheckedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("invalid check timestamp: %v", got.CheckedAt)
	}
	if len(got.Prerequisites) != 6 {
		t.Fatalf("expected six node prerequisites, got %+v", got.Prerequisites)
	}
	states := map[string]string{}
	for _, prerequisite := range got.Prerequisites {
		states[prerequisite.ID] = prerequisite.State
	}
	if states["pacemaker"] != "ok" || states["drbd"] != "ok" || states["drbd_reactor"] != "ok" || states["sbd"] != "inactive" {
		t.Fatalf("prerequisite states not parsed: %+v", states)
	}
	if !containsString(got.Blockers, "node_prerequisites_missing") {
		t.Fatalf("missing node prerequisite must block HA: %+v", got.Blockers)
	}
}

func TestBuildHAReadinessReadyWhenControlPlaneVerified(t *testing.T) {
	quorum := true
	pacemaker := ha.PacemakerStatus{State: ha.StateObserved, Quorum: &quorum, NodesConfigured: 3, NodesOnline: 3}
	controlPlane := controlPlaneHAReadiness{
		State: "active", SharedDataDirVerified: true, VirtualIPConfigured: true,
		FencingTested: true, SharedStorageVerified: true,
	}
	fencing := fencingHAReadiness{Enabled: true, Hypervisor: true}
	got := buildHAReadiness(pacemaker, []ha.Prerequisite{{ID: "pacemaker", State: ha.PrereqOK}}, nil, controlPlane, fencing, ha.LocalInstallStatus{}, time.Now())
	if got.State != "ready" || !got.CanEnable || len(got.Blockers) != 0 {
		t.Fatalf("verified HA must be ready: %+v", got)
	}
	if got.Blockers == nil {
		t.Fatal("blockers must be an empty slice, not null")
	}
	if got.ControlPlane.State != "active" || !got.ControlPlane.VirtualIPConfigured {
		t.Fatalf("control plane not reflected: %+v", got.ControlPlane)
	}
}

func TestBuildHAReadinessCriticalWithoutHypervisorFencing(t *testing.T) {
	quorum := true
	pacemaker := ha.PacemakerStatus{State: ha.StateObserved, Quorum: &quorum, NodesConfigured: 3, NodesOnline: 3}
	controlPlane := controlPlaneHAReadiness{
		State: "active", SharedDataDirVerified: true, VirtualIPConfigured: true,
		FencingTested: true, SharedStorageVerified: true,
	}
	watchdogOnly := fencingHAReadiness{Enabled: true, Watchdog: true, Critical: true}
	got := buildHAReadiness(pacemaker, []ha.Prerequisite{{ID: "pacemaker", State: ha.PrereqOK}}, nil, controlPlane, watchdogOnly, ha.LocalInstallStatus{}, time.Now())
	if got.State != "blocked" || got.CanEnable {
		t.Fatalf("watchdog-only fencing must block HA activation: %+v", got)
	}
	if !got.Fencing.Critical || !containsString(got.Blockers, "pacemaker_no_hypervisor_fencing") {
		t.Fatalf("missing hypervisor fencing must be critical: %+v", got)
	}
	// fence_pve present: no critical, no blocker.
	withPVE := fencingHAReadiness{Enabled: true, Hypervisor: true, Watchdog: true}
	got = buildHAReadiness(pacemaker, []ha.Prerequisite{{ID: "pacemaker", State: ha.PrereqOK}}, nil, controlPlane, withPVE, ha.LocalInstallStatus{}, time.Now())
	if got.Fencing.Critical || containsString(got.Blockers, "pacemaker_no_hypervisor_fencing") {
		t.Fatalf("hypervisor fencing must clear the critical finding: %+v", got)
	}
}

func TestFencingReadinessIsNotCriticalWithoutCluster(t *testing.T) {
	h := &Handler{}
	got := h.fencingReadiness(context.Background(), ha.PacemakerStatus{State: ha.StateNotInstalled})
	if got.Critical {
		t.Fatalf("a host without Pacemaker must not be reported as missing fencing: %+v", got)
	}
}

type fakeProfileSecondary struct {
	compute.Backend
	issues []ha.Reason
	err    error
}

func (f *fakeProfileSecondary) DefaultProfileNICIssues(context.Context) ([]ha.Reason, error) {
	return f.issues, f.err
}

type fakePrimaryWithSecondary struct {
	compute.Backend
	sec compute.Backend
}

func (f *fakePrimaryWithSecondary) Secondary() compute.Backend { return f.sec }

func TestReadinessWarnsWhenDefaultProfileNICIsNotPortable(t *testing.T) {
	issue := ha.Reason{Code: ha.ReasonNICNetworkNotOnAllMembers, Detail: "NIC eth0 uses parent bridge/interface vmbr0, which is missing on: b, c",
		Commands: []string{"incus network create ha-net --target a", "incus network create ha-net"}}
	run := func(sec *fakeProfileSecondary) haReadiness {
		h := &Handler{compute: &fakePrimaryWithSecondary{sec: sec}}
		res := haReadiness{Warnings: []string{}}
		h.applyIncusProfileWarning(context.Background(), &res)
		return res
	}
	res := run(&fakeProfileSecondary{issues: []ha.Reason{issue}})
	if len(res.Warnings) != 1 || res.Warnings[0] != "incus_default_profile_network_not_portable" ||
		len(res.DefaultProfileNIC) != 1 || res.DefaultProfileNIC[0].Commands[0] != "incus network create ha-net --target a" {
		t.Fatalf("%+v", res)
	}
	for name, sec := range map[string]*fakeProfileSecondary{
		"portable": {}, "probe failed": {err: errors.New("boom")},
	} {
		if res := run(sec); len(res.Warnings) != 0 || res.DefaultProfileNIC != nil {
			t.Fatalf("%s: %+v", name, res)
		}
	}
	// No compute backend at all (KVM-only host): silently nothing.
	res = haReadiness{Warnings: []string{}}
	(&Handler{}).applyIncusProfileWarning(context.Background(), &res)
	if len(res.Warnings) != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestReadinessExposesActionableDRBDIssues(t *testing.T) {
	host := localNodeName()
	stale := (`incus-volume-x node-id:1 role:Secondary
  volume:0 minor:1001 disk:Inconsistent
      quorum:yes open:no blocked:no
  node-a node-id:0 connection:Connecting role:Unknown tls:no
    volume:0 replication:Off peer-disk:DUnknown resync-suspended:no
  node-c node-id:2 connection:Connected role:Secondary tls:no
    volume:0 replication:Established peer-disk:UpToDate resync-suspended:no
`)
	var verbose int
	prev := sysutil.DefaultRunner
	t.Cleanup(func() { sysutil.DefaultRunner = prev })
	sysutil.DefaultRunner = &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "drbdsetup" && strings.Join(args, " ") == "status --verbose" {
			verbose++
			return []byte(stale), nil
		}
		return nil, errors.New("not available")
	}}
	rec := httptest.NewRecorder()
	h := &Handler{cfg: &config.Config{DataDir: t.TempDir()}}
	h.GetHAReadiness(rec, httptest.NewRequest(http.MethodGet, "/api/ha/readiness", nil))
	var got struct {
		Warnings []string `json:"warnings"`
		DRBD     struct {
			Healthy bool `json:"healthy"`
			Issues  []struct {
				Resource           string `json:"resource"`
				Peer               string `json:"peer"`
				SafeToDiscardLocal bool   `json:"safe_to_discard_local"`
				RepairSteps        []struct {
					Node, Command string
				} `json:"repair_steps"`
			} `json:"issues"`
		} `json:"drbd"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if verbose == 0 || got.DRBD.Healthy || len(got.DRBD.Issues) != 1 || !containsString(got.Warnings, "drbd_link_degraded") {
		t.Fatalf("verbose=%d %s", verbose, rec.Body.String())
	}
	i := got.DRBD.Issues[0]
	if i.Resource != "incus-volume-x" || i.Peer != "node-a" || !i.SafeToDiscardLocal || len(i.RepairSteps) != 3 || i.RepairSteps[0].Node != host {
		t.Fatalf("issue: %+v", i)
	}
}

func TestReadinessDRBDBlockIsHealthyWhenAbsent(t *testing.T) {
	rec := httptest.NewRecorder()
	(&Handler{}).GetHAReadiness(rec, httptest.NewRequest(http.MethodGet, "/api/ha/readiness", nil))
	if !strings.Contains(rec.Body.String(), `"drbd":{"healthy":true,"issues":[]}`) {
		t.Fatalf("missing additive drbd block: %s", rec.Body.String())
	}
}
