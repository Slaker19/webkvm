package ha

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"webkvm/internal/sysutil"
)

func TestParsePacemakerStatusXML(t *testing.T) {
	status, err := parsePacemakerStatusXML([]byte(`
<pacemaker-result api-version="3.0" request="pcs status xml">
  <status code="0" message="OK"/>
  <crm_mon>
    <summary><cluster_name>webkvm-ha</cluster_name><quorum>true</quorum></summary>
    <nodes>
      <node name="kvm-a" type="member" online="true"/>
      <node name="kvm-b" type="member" online="false"/>
    </nodes>
  </crm_mon>
</pacemaker-result>`))
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateObserved || status.ClusterName != "webkvm-ha" || status.Quorum == nil || !*status.Quorum {
		t.Fatalf("unexpected status: %+v", status)
	}
	if status.NodesConfigured != 2 || status.NodesOnline != 1 {
		t.Fatalf("unexpected node counts: %+v", status)
	}
}

func TestParsePacemakerStatusXMLRejectsDisconnectedAndMalformed(t *testing.T) {
	disconnected, err := parsePacemakerStatusXML([]byte(`<pacemaker-result><status code="102" message="Not connected"/></pacemaker-result>`))
	if err != nil {
		t.Fatal(err)
	}
	if disconnected.State != StateNotConfigured {
		t.Fatalf("disconnected cluster status = %+v", disconnected)
	}
	if _, err := parsePacemakerStatusXML([]byte(`<pacemaker-result>`)); err == nil {
		t.Fatal("malformed XML should fail")
	}
}

func TestParsePacemakerConfigChecksFencingWithoutExposingValues(t *testing.T) {
	config, err := parsePacemakerConfig([]byte(`
<cib><configuration>
  <crm_config><cluster_property_set>
    <nvpair name="stonith-enabled" value="true"/>
    <nvpair name="cluster-name" value="webkvm-ha"/>
  </cluster_property_set></crm_config>
  <nodes><node uname="kvm-a"/><node uname="kvm-b"/></nodes>
  <resources><primitive id="fence-kvm-a" class="stonith" type="fence_ipmilan">
    <instance_attributes><nvpair name="password" value="never-return-this"/></instance_attributes>
  </primitive></resources>
</configuration></cib>`))
	if err != nil {
		t.Fatal(err)
	}
	if config.FencingEnabled == nil || !*config.FencingEnabled || config.FenceDevices != 1 || config.NodesConfigured != 2 || config.ClusterName != "webkvm-ha" {
		t.Fatalf("unexpected cluster config: %+v", config)
	}
	if strings.Contains(strings.Join(config.NodeNames, ","), "never-return-this") {
		t.Fatal("cluster config parser included secret material")
	}
}

func TestParsePacemakerConfigDefaultsFencingEnabled(t *testing.T) {
	config, err := parsePacemakerConfig([]byte(`<cib><configuration><crm_config><cluster_property_set/></crm_config></configuration></cib>`))
	if err != nil {
		t.Fatal(err)
	}
	if config.FencingEnabled == nil || !*config.FencingEnabled {
		t.Fatalf("absent stonith-enabled should default to true: %+v", config)
	}
}

func TestProbePacemakerUsesOnlyReadOnlyStatusCommands(t *testing.T) {
	var calls []string
	runner := &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		switch strings.Join(args, " ") {
		case "status xml":
			return []byte(`<pacemaker-result><status code="0"/><crm_mon><summary><cluster_name>lab</cluster_name><quorum>true</quorum></summary><nodes><node name="a" type="member" online="true"/><node name="b" type="member" online="true"/></nodes></crm_mon></pacemaker-result>`), nil
		case "cluster cib":
			return []byte(`<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-enabled" value="true"/></cluster_property_set></crm_config><nodes><node uname="a"/><node uname="b"/></nodes><resources><primitive id="fence-a" class="stonith"/></resources></configuration></cib>`), nil
		default:
			return nil, errors.New("unexpected command")
		}
	}}
	status := ProbePacemaker(context.Background(), runner)
	if status.State != StateObserved || status.FenceDevices != 1 || status.FencingEnabled == nil || !*status.FencingEnabled {
		t.Fatalf("unexpected Pacemaker probe: %+v", status)
	}
	if strings.Join(calls, ",") != "pcs status xml,pcs cluster cib" {
		t.Fatalf("probe commands = %v, want only read-only pcs status/config queries", calls)
	}
}

func TestProbePacemakerReportsMissingToolsWithoutFailingRequest(t *testing.T) {
	runner := &sysutil.MockCommandRunner{Handler: func(context.Context, string, ...string) ([]byte, error) {
		return nil, &exec.Error{Name: "pcs", Err: exec.ErrNotFound}
	}}
	status := ProbePacemaker(context.Background(), runner)
	if status.State != StateNotInstalled || len(status.Blockers) == 0 {
		t.Fatalf("missing Pacemaker tools = %+v", status)
	}
}

// Pacemaker 3 / pcs 0.12 emits the crm_mon report directly under
// <pacemaker-result> with no <crm_mon> wrapper and with quorum on current_dc.
func TestParsePacemakerStatusXMLPacemaker3FlatReport(t *testing.T) {
	status, err := parsePacemakerStatusXML([]byte(`
<pacemaker-result api-version="2.38" request="/usr/sbin/crm_mon --one-shot --inactive --output-as xml">
  <summary>
    <current_dc present="true" name="node-b" id="2" with_quorum="true"/>
    <nodes_configured number="3"/>
  </summary>
  <nodes>
    <node name="node-a" id="1" online="true" type="member"/>
    <node name="node-b" id="2" online="true" type="member"/>
    <node name="node-c" id="3" online="true" type="member"/>
  </nodes>
  <status code="0" message="OK"/>
</pacemaker-result>`))
	if err != nil {
		t.Fatal(err)
	}
	if status.State != StateObserved || status.Quorum == nil || !*status.Quorum {
		t.Fatalf("unexpected flat status: %+v", status)
	}
	if status.NodesConfigured != 3 || status.NodesOnline != 3 {
		t.Fatalf("unexpected flat node counts: %+v", status)
	}
}

// SBD watchdog-only fencing has no stonith primitive; the readiness probe must
// accept the watchdog timeout as the fence mechanism instead of blocking HA.
func TestProbePacemakerAcceptsWatchdogFencing(t *testing.T) {
	runner := &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch strings.Join(args, " ") {
		case "status xml":
			return []byte(`<pacemaker-result><summary><current_dc with_quorum="true"/></summary><nodes><node name="a" type="member" online="true"/><node name="b" type="member" online="true"/><node name="c" type="member" online="true"/></nodes><status code="0" message="OK"/></pacemaker-result>`), nil
		case "cluster cib":
			return []byte(`<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-enabled" value="true"/><nvpair name="stonith-watchdog-timeout" value="20s"/></cluster_property_set></crm_config><nodes><node uname="a"/><node uname="b"/><node uname="c"/></nodes></configuration></cib>`), nil
		default:
			return nil, errors.New("unexpected command")
		}
	}}
	status := ProbePacemaker(context.Background(), runner)
	if status.State != StateObserved {
		t.Fatalf("watchdog fencing should not block HA: %+v", status)
	}
	if status.WatchdogFencing == nil || !*status.WatchdogFencing {
		t.Fatalf("watchdog fencing not detected: %+v", status)
	}
	for _, blocker := range status.Blockers {
		if blocker == "pacemaker_fencing_unconfigured" {
			t.Fatalf("watchdog fencing reported as unconfigured: %+v", status.Blockers)
		}
	}
}

func TestParsePacemakerStatusXMLReadsManagedIPAndFenceHistory(t *testing.T) {
	status, err := parsePacemakerStatusXML([]byte(`
<pacemaker-result api-version="2.38">
  <summary><current_dc with_quorum="true"/></summary>
  <nodes><node name="node-b" type="member" online="true"/></nodes>
  <resources>
    <group id="webkvm-cp">
      <resource id="webkvm-vip" resource_agent="ocf:heartbeat:IPaddr2" role="Started" active="true"><node name="node-b"/></resource>
    </group>
  </resources>
  <fence_history>
    <fence_event action="reboot" target="node-b" status="success" completed="2026-11-12 10:00:00 +00:00" delegate="node-b"/>
  </fence_history>
  <status code="0" message="OK"/>
</pacemaker-result>`))
	if err != nil {
		t.Fatal(err)
	}
	if !status.ManagedIP || !status.FencingTested {
		t.Fatalf("managed IP / fencing history not parsed: %+v", status)
	}
	if len(status.Resources) != 1 || status.Resources[0].ID != "webkvm-vip" ||
		status.Resources[0].Node != "node-b" || !status.Resources[0].Active {
		t.Fatalf("Pacemaker resources not parsed: %+v", status.Resources)
	}
}

func TestManagedVirtualDomainsReadsConfigName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ha-test.xml")
	if err := os.WriteFile(path, []byte(`<domain><name>ha-test</name></domain>`), 0o600); err != nil {
		t.Fatal(err)
	}
	cib := `<cib><configuration><resources><group id="kvm-ha"><primitive id="kvm-vm" class="ocf" provider="heartbeat" type="VirtualDomain"><instance_attributes><nvpair name="config" value="` + path + `"/></instance_attributes></primitive></group></resources></configuration></cib>`
	runner := &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "pcs" || strings.Join(args, " ") != "cluster cib" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return []byte(cib), nil
	}}
	names := ManagedVirtualDomains(context.Background(), runner)
	if len(names) != 1 || names[0] != "ha-test" {
		t.Fatalf("managed domains = %v", names)
	}
	if paths := virtualDomainConfigPaths([]byte(cib)); len(paths) != 1 || paths[0] != path {
		t.Fatalf("config paths = %v", paths)
	}
}

func TestManagedVirtualDomainsFallsBackToTransientWhenCIBUnavailable(t *testing.T) {
	runner := &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "pcs" && strings.Join(args, " ") == "cluster cib":
			return nil, errors.New("cluster is not currently running")
		case name == "pcs" && strings.Join(args, " ") == "--version":
			return []byte("Pacemaker 3.0.1"), nil
		case name == "virsh" && strings.Join(args, " ") == "list --transient --name":
			return []byte("ha-test\n\nother\n"), nil
		}
		t.Fatalf("unexpected command %s %v", name, args)
		return nil, nil
	}}
	names := ManagedVirtualDomains(context.Background(), runner)
	if len(names) != 2 || names[0] != "ha-test" || names[1] != "other" {
		t.Fatalf("managed domains = %v", names)
	}
}

func TestManagedVirtualDomainsIsEmptyWhenPcsIsNotInstalled(t *testing.T) {
	runner := &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "pcs" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return nil, errors.New("executable file not found in $PATH")
	}}
	if names := ManagedVirtualDomains(context.Background(), runner); len(names) != 0 {
		t.Fatalf("managed domains = %v, want none", names)
	}
}

func TestFenceHistoryRealShapeOrderingAndClusterScope(t *testing.T) {
	status, err := parsePacemakerStatusXML([]byte(`<pacemaker-result api-version="2.38">
  <summary><current_dc with_quorum="true"/></summary>
  <nodes><node name="n1" type="member" online="true"/><node name="n2" type="member" online="true"/></nodes>
  <fence_history>
    <fence_event target="n2" action="reboot" delegate="n1" client="stonithd.1" origin="n1" status="failed" exit-reason="x" completed="2026-11-12 09:00:00 +00:00"/>
    <fence_event target="n2" action="reboot" delegate="n1" status="success" completed="2026-11-12 11:00:00 +00:00"/>
    <fence_event target="n2" action="on" status="success" completed="2026-11-12 12:00:00 +00:00"/>
  </fence_history>
  <status code="0" message="OK"/>
</pacemaker-result>`))
	if err != nil {
		t.Fatal(err)
	}
	if !status.FencingTested || len(status.FenceEvents) != 3 || status.FenceEvents[0].Action != "on" || status.FenceEvents[1].Delegate != "n1" {
		t.Fatalf("%+v", status.FenceEvents)
	}
	foreign, _ := parsePacemakerStatusXML([]byte(`<pacemaker-result><summary><current_dc with_quorum="true"/></summary><nodes><node name="n1" type="member" online="true"/></nodes><fence_history><fence_event target="old-node" action="reboot" status="success" completed="2026-11-12 11:00:00 +00:00"/></fence_history><status code="0"/></pacemaker-result>`))
	if foreign.FencingTested || len(foreign.FenceEvents) != 0 {
		t.Fatalf("event of another cluster counted: %+v", foreign)
	}
	var many string
	for i := 0; i < 15; i++ {
		many += fmt.Sprintf(`<fence_event target="n1" action="reboot" status="success" completed="2026-11-12 10:%02d:00 +00:00"/>`, i)
	}
	capped, _ := parsePacemakerStatusXML([]byte(`<pacemaker-result><summary><current_dc with_quorum="true"/></summary><nodes><node name="n1" type="member" online="true"/></nodes><fence_history>` + many + `</fence_history><status code="0"/></pacemaker-result>`))
	if len(capped.FenceEvents) != MaxFenceEvents || capped.FenceEvents[0].Completed != "2026-11-12 10:14:00 +00:00" {
		t.Fatalf("%+v", capped.FenceEvents)
	}
}
