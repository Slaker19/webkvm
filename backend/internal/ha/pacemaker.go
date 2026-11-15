package ha

import (
	"context"
	"encoding/xml"
	"errors"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"webkvm/internal/sysutil"
)

const (
	StateNotInstalled  = "not_installed"
	StateNotConfigured = "not_configured"
	StateObserved      = "observed"
	StateBlocked       = "blocked"
	StateError         = "error"
)

// PacemakerResource is one primitive reported by the cluster, including the
// node it runs on. Clone instances appear once per node with their role.
type PacemakerResource struct {
	ID     string `json:"id"`
	Agent  string `json:"agent"`
	Role   string `json:"role"`
	Active bool   `json:"active"`
	Node   string `json:"node,omitempty"`
	// Group is the enclosing Pacemaker group ("" for a bare primitive or a
	// clone instance). The control plane is identified by it, never by the
	// resource id.
	Group string `json:"group,omitempty"`
}

// PacemakerNode is the live state of one cluster member as reported by
// crm_mon. A standby node is online (it keeps its corosync vote) but cannot
// run resources.
type PacemakerNode struct {
	Name    string `json:"name"`
	Online  bool   `json:"online"`
	Standby bool   `json:"standby"`
}

// PacemakerStatus contains only non-secret status fields. CIB fencing
// credentials are parsed in memory and never copied into this response.
type PacemakerStatus struct {
	State             string              `json:"state"`
	ClusterName       string              `json:"cluster_name,omitempty"`
	Quorum            *bool               `json:"quorum,omitempty"`
	NodesConfigured   int                 `json:"nodes_configured"`
	NodesOnline       int                 `json:"nodes_online"`
	NodeNames         []string            `json:"node_names"`
	NodeStates        []PacemakerNode     `json:"node_states,omitempty"`
	Resources         []PacemakerResource `json:"resources,omitempty"`
	FencingEnabled    *bool               `json:"fencing_enabled,omitempty"`
	WatchdogFencing   *bool               `json:"watchdog_fencing,omitempty"`
	FenceAgents       []string            `json:"fence_agents"`
	HypervisorFencing bool                `json:"hypervisor_fencing,omitempty"`
	FencingTested     bool                `json:"fencing_tested,omitempty"`
	// FenceEvents are the entries of <fence_history> in `pcs status xml`,
	// most recent first, capped at MaxFenceEvents.
	FenceEvents  []FenceEvent `json:"fence_events,omitempty"`
	ManagedIP    bool         `json:"managed_ip,omitempty"`
	FenceDevices int          `json:"fence_devices"`
	Blockers     []string     `json:"blockers"`
}

type xmlElement struct {
	XMLName  xml.Name
	Attrs    []xml.Attr   `xml:",any,attr"`
	Text     string       `xml:",chardata"`
	Children []xmlElement `xml:",any"`
}

func (e xmlElement) attr(name string) string {
	for _, attr := range e.Attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func (e xmlElement) find(name string) *xmlElement {
	if e.XMLName.Local == name {
		return &e
	}
	for i := range e.Children {
		if found := e.Children[i].find(name); found != nil {
			return found
		}
	}
	return nil
}

func (e xmlElement) descendants(name string, out *[]xmlElement) {
	if e.XMLName.Local == name {
		*out = append(*out, e)
	}
	for _, child := range e.Children {
		child.descendants(name, out)
	}
}

func parsePacemakerStatusXML(data []byte) (PacemakerStatus, error) {
	var root xmlElement
	if err := xml.Unmarshal(data, &root); err != nil {
		return PacemakerStatus{}, err
	}
	status := PacemakerStatus{State: StateError, NodeNames: []string{}, Blockers: []string{}}
	if result := root.find("status"); result != nil && result.attr("code") != "" {
		code, err := strconv.Atoi(result.attr("code"))
		if err == nil && code != 0 {
			message := strings.ToLower(result.attr("message"))
			if code == 102 || strings.Contains(message, "not connected") {
				status.State = StateNotConfigured
				status.Blockers = append(status.Blockers, "pacemaker_cluster_not_configured")
				return status, nil
			}
			status.Blockers = append(status.Blockers, "pacemaker_status_unavailable")
			return status, nil
		}
	}

	monitor := root.find("crm_mon")
	if monitor == nil {
		// Pacemaker 3 / pcs 0.12 emits the crm_mon report directly under
		// <pacemaker-result> (summary + nodes) with no <crm_mon> wrapper.
		if root.XMLName.Local == "crm_mon" || (root.find("summary") != nil && root.find("nodes") != nil) {
			monitor = &root
		}
	}
	if monitor == nil {
		return PacemakerStatus{}, errors.New("pacemaker XML did not contain a cluster status")
	}
	status.State = StateObserved
	status.ClusterName = firstText(monitor, "cluster_name")
	if quorum := monitor.find("quorum"); quorum != nil {
		if value, err := strconv.ParseBool(strings.TrimSpace(quorum.Text)); err == nil {
			status.Quorum = &value
		}
	} else if dc := monitor.find("current_dc"); dc != nil && dc.attr("with_quorum") != "" {
		if value, err := strconv.ParseBool(dc.attr("with_quorum")); err == nil {
			status.Quorum = &value
		}
	}
	if nodes := monitor.find("nodes"); nodes != nil {
		for _, node := range nodes.Children {
			if node.XMLName.Local != "node" || node.attr("type") == "remote" {
				continue
			}
			name := node.attr("name")
			if name == "" {
				name = node.attr("uname")
			}
			if name != "" {
				status.NodeNames = append(status.NodeNames, name)
			}
			status.NodesConfigured++
			online, err := strconv.ParseBool(node.attr("online"))
			online = err == nil && online
			if online {
				status.NodesOnline++
			}
			if name != "" {
				standby, _ := strconv.ParseBool(node.attr("standby"))
				status.NodeStates = append(status.NodeStates, PacemakerNode{Name: name, Online: online, Standby: standby})
			}
		}
	}
	if status.NodesConfigured == 0 {
		if configured := firstText(monitor, "nodes_configured"); configured != "" {
			status.NodesConfigured, _ = strconv.Atoi(configured)
		}
		if online := firstText(monitor, "nodes_online"); online != "" {
			status.NodesOnline, _ = strconv.Atoi(online)
		}
	}
	if resources := monitor.find("resources"); resources != nil {
		var walk func(e xmlElement, group string)
		walk = func(e xmlElement, group string) {
			for _, item := range e.Children {
				if item.XMLName.Local != "resource" {
					if item.XMLName.Local == "group" {
						walk(item, item.attr("id"))
					} else {
						walk(item, group)
					}
					continue
				}
				active, _ := strconv.ParseBool(item.attr("active"))
				node := ""
				if child := item.find("node"); child != nil {
					node = child.attr("name")
				}
				status.Resources = append(status.Resources, PacemakerResource{
					ID:     item.attr("id"),
					Agent:  item.attr("resource_agent"),
					Role:   item.attr("role"),
					Active: active,
					Node:   node,
					Group:  group,
				})
				if item.attr("resource_agent") == "ocf:heartbeat:IPaddr2" &&
					item.attr("role") == "Started" && active {
					status.ManagedIP = true
				}
			}
		}
		walk(*resources, "")
	}
	if history := monitor.find("fence_history"); history != nil {
		var events []xmlElement
		history.descendants("fence_event", &events)
		status.FenceEvents, status.FencingTested = collectFenceEvents(events, status.NodeNames)
	}
	if status.Quorum != nil && !*status.Quorum {
		status.State = StateBlocked
		status.Blockers = append(status.Blockers, "pacemaker_quorum_missing")
	}
	return status, nil
}

func firstText(root *xmlElement, name string) string {
	if element := root.find(name); element != nil {
		return strings.TrimSpace(element.Text)
	}
	return ""
}

type pacemakerConfig struct {
	FencingEnabled  *bool
	WatchdogFencing *bool
	FenceDevices    int
	FenceAgents     []string
	NodesConfigured int
	NodeNames       []string
	ClusterName     string
}

func parsePacemakerConfig(data []byte) (pacemakerConfig, error) {
	var root xmlElement
	if err := xml.Unmarshal(data, &root); err != nil {
		return pacemakerConfig{}, err
	}
	config := pacemakerConfig{NodeNames: []string{}}
	var values []xmlElement
	root.descendants("nvpair", &values)
	for _, value := range values {
		if value.attr("name") == "stonith-enabled" {
			enabled, err := strconv.ParseBool(value.attr("value"))
			if err == nil {
				config.FencingEnabled = &enabled
			}
		}
		if value.attr("name") == "stonith-watchdog-timeout" {
			// SBD watchdog-only fencing has no stonith primitive; a non-zero
			// watchdog timeout is the fence mechanism in that setup.
			raw := strings.TrimSpace(value.attr("value"))
			enabled := raw != "" && raw != "0" && raw != "0s" && raw != "0ms"
			config.WatchdogFencing = &enabled
		}
		if value.attr("name") == "cluster-name" {
			config.ClusterName = strings.TrimSpace(value.attr("value"))
		}
	}
	// Pacemaker treats stonith-enabled as true when the property is unset.
	if config.FencingEnabled == nil {
		enabled := true
		config.FencingEnabled = &enabled
	}
	if resources := root.find("resources"); resources != nil {
		var primitives []xmlElement
		resources.descendants("primitive", &primitives)
		for _, primitive := range primitives {
			if primitive.attr("class") == "stonith" {
				config.FenceDevices++
				if agent := primitive.attr("type"); agent != "" {
					config.FenceAgents = append(config.FenceAgents, agent)
				}
			}
		}
	}
	if nodes := root.find("nodes"); nodes != nil {
		for _, node := range nodes.Children {
			if node.XMLName.Local != "node" {
				continue
			}
			name := node.attr("uname")
			if name == "" {
				name = node.attr("name")
			}
			if name != "" {
				config.NodeNames = append(config.NodeNames, name)
				config.NodesConfigured++
			}
		}
	}
	return config, nil
}

// ProbePacemaker performs read-only status/configuration queries. It does not
// form a cluster, install packages, or test a fence device.
func ProbePacemaker(ctx context.Context, runner sysutil.CommandRunner) PacemakerStatus {
	status := PacemakerStatus{State: StateError, NodeNames: []string{}, Blockers: []string{}}
	if runner == nil {
		status.Blockers = append(status.Blockers, "pacemaker_probe_unavailable")
		return status
	}
	statusXML, err := runReadOnly(ctx, runner, "status", "xml")
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			status.State = StateNotInstalled
			status.Blockers = append(status.Blockers, "pacemaker_tools_not_installed")
			return status
		}
	}
	parsed, parseErr := parsePacemakerStatusXML(statusXML)
	if parseErr != nil {
		status.Blockers = append(status.Blockers, "pacemaker_status_unavailable")
		return status
	}
	if parsed.State == StateNotConfigured || parsed.State == StateError {
		return parsed
	}
	status = parsed
	cib, cibErr := runReadOnly(ctx, runner, "cluster", "cib")
	if cibErr != nil {
		status.State = StateBlocked
		status.Blockers = append(status.Blockers, "pacemaker_config_unavailable")
		return status
	}
	config, configErr := parsePacemakerConfig(cib)
	if configErr != nil {
		status.State = StateBlocked
		status.Blockers = append(status.Blockers, "pacemaker_config_unavailable")
		return status
	}
	status.FencingEnabled = config.FencingEnabled
	status.WatchdogFencing = config.WatchdogFencing
	status.FenceDevices = config.FenceDevices
	status.FenceAgents = config.FenceAgents
	status.HypervisorFencing = HasHypervisorFence(config.FenceAgents)
	if status.ClusterName == "" {
		status.ClusterName = config.ClusterName
	}
	if config.NodesConfigured > status.NodesConfigured {
		status.NodesConfigured = config.NodesConfigured
	}
	if len(config.NodeNames) > 0 {
		status.NodeNames = config.NodeNames
	}
	if status.Quorum == nil {
		status.State = StateBlocked
		status.Blockers = append(status.Blockers, "pacemaker_quorum_unknown")
	} else if !*status.Quorum {
		status.State = StateBlocked
		status.Blockers = append(status.Blockers, "pacemaker_quorum_missing")
	}
	watchdogFencing := status.WatchdogFencing != nil && *status.WatchdogFencing
	if (status.FencingEnabled == nil || !*status.FencingEnabled || status.FenceDevices == 0) && !watchdogFencing {
		status.State = StateBlocked
		status.Blockers = append(status.Blockers, "pacemaker_fencing_unconfigured")
	}
	if status.NodesConfigured < 2 {
		status.State = StateBlocked
		status.Blockers = append(status.Blockers, "pacemaker_needs_multiple_nodes")
	}
	return status
}

func runReadOnly(ctx context.Context, runner sysutil.CommandRunner, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return runner.Run(commandCtx, "pcs", args...)
}

func runVirshReadOnly(ctx context.Context, runner sysutil.CommandRunner, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return runner.Run(commandCtx, "virsh", args...)
}

// virtualDomainConfigPaths returns the libvirt XML paths of every
// VirtualDomain primitive in the cluster CIB.
func virtualDomainConfigPaths(cib []byte) []string {
	var root xmlElement
	if err := xml.Unmarshal(cib, &root); err != nil {
		return nil
	}
	paths := []string{}
	for _, primitive := range cibPrimitives(root) {
		if primitive.isVirtualDomain() && primitive.Params["config"] != "" {
			paths = append(paths, primitive.Params["config"])
		}
	}
	return paths
}

// ManagedDomain is a libvirt domain owned by a Pacemaker VirtualDomain
// resource and the cluster node where that resource currently runs. Node is
// empty when the status could not be read (pcs stopped, standby node).
type ManagedDomain struct {
	Name string
	Node string
}

// managedDomains is the KVM subset of the workload model. The name comes
// from the webkvm-name meta, else the XML config when readable on this node,
// else the resource id (the XML lives inside the per-VM DRBD filesystem,
// mounted only on the owner node).
func managedDomains(cib, statusXML []byte) []ManagedDomain {
	workloads, err := ParseWorkloads(cib, statusXML, DefaultCPGroup)
	if err != nil {
		return nil
	}
	out := make([]ManagedDomain, 0, len(workloads))
	seen := map[string]bool{}
	for _, w := range workloads {
		if w.Kind != KindKVM || seen[w.Name] {
			continue
		}
		seen[w.Name] = true
		out = append(out, ManagedDomain{Name: w.Name, Node: w.Node})
	}
	return out
}

// ManagedVirtualDomainsDetailed returns the libvirt domains owned by
// Pacemaker plus the running node, read from `pcs status xml`. The UI must
// not guess the node from the WebKVM instance that happens to serve the
// request: after a failover the domain runs on a different member.
//
// When pcs is installed but the CIB cannot be read (Pacemaker stopped or
// unreachable), the answer must not be "nothing is managed": that would
// re-enable the start/stop/delete buttons for a VM Pacemaker still owns.
// VirtualDomain starts its domain as a transient libvirt domain, so those
// names are used as a fail-closed fallback.
func ManagedVirtualDomainsDetailed(ctx context.Context, runner sysutil.CommandRunner) []ManagedDomain {
	cib, err := runReadOnly(ctx, runner, "cluster", "cib")
	if err != nil {
		if _, versionErr := runReadOnly(ctx, runner, "--version"); versionErr != nil {
			return nil // pcs is not installed: this host has no Pacemaker
		}
		names := transientVirtualDomains(ctx, runner)
		out := make([]ManagedDomain, 0, len(names))
		for _, name := range names {
			out = append(out, ManagedDomain{Name: name})
		}
		return out
	}
	if len(virtualDomainConfigPaths(cib)) == 0 {
		return []ManagedDomain{}
	}
	statusXML, statusErr := runReadOnly(ctx, runner, "status", "xml")
	if statusErr != nil {
		statusXML = nil
	}
	return managedDomains(cib, statusXML)
}

// ManagedVirtualDomains returns the domain names only. Kept for callers that
// do not need node attribution.
func ManagedVirtualDomains(ctx context.Context, runner sysutil.CommandRunner) []string {
	cib, err := runReadOnly(ctx, runner, "cluster", "cib")
	if err != nil {
		if _, versionErr := runReadOnly(ctx, runner, "--version"); versionErr != nil {
			return nil // pcs is not installed: this host has no Pacemaker
		}
		return transientVirtualDomains(ctx, runner)
	}
	domains := managedDomains(cib, nil)
	names := make([]string, 0, len(domains))
	for _, domain := range domains {
		names = append(names, domain.Name)
	}
	return names
}

// transientVirtualDomains lists running domains with no persistent
// definition. Only Pacemaker's VirtualDomain agent creates them here, so a
// hit means the domain belongs to the cluster even without a readable CIB.
func transientVirtualDomains(ctx context.Context, runner sysutil.CommandRunner) []string {
	out, err := runVirshReadOnly(ctx, runner, "list", "--transient", "--name")
	if err != nil {
		return nil
	}
	names := make([]string, 0)
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names
}

// MaxFenceEvents caps how many fence_history entries are exposed.
const MaxFenceEvents = 10

// FenceEvent is one <fence_event> of the Pacemaker fence history.
type FenceEvent struct {
	Target    string `json:"target"`
	Action    string `json:"action"`
	Status    string `json:"status"`
	Completed string `json:"completed,omitempty"`
	Delegate  string `json:"delegate,omitempty"`
	Origin    string `json:"origin,omitempty"`
	// Source is "history" (live fence_history) or "ledger" (persisted
	// evidence ledger); FirstSeen is set on ledger entries only.
	Source    string `json:"source,omitempty"`
	FirstSeen string `json:"first_seen,omitempty"`
}

// Successful reports whether the event is a completed reboot/off.
func (e FenceEvent) Successful() bool {
	return e.Status == "success" && (e.Action == "reboot" || e.Action == "off")
}

var fenceTimeLayouts = []string{
	"2006-01-02 15:04:05 -07:00",
	"2006-01-02 15:04:05 -0700",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02 15:04:05",
}

// ParseFenceTime parses the `completed` attribute of a fence event.
func ParseFenceTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	for _, layout := range fenceTimeLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// collectFenceEvents returns the events most recent first (capped) and
// whether a successful reboot/off exists for a node of this cluster. When
// the node list is unknown every target counts.
func collectFenceEvents(elems []xmlElement, nodeNames []string) ([]FenceEvent, bool) {
	events := make([]FenceEvent, 0, len(elems))
	for _, e := range elems {
		events = append(events, FenceEvent{
			Target: e.attr("target"), Action: e.attr("action"), Status: e.attr("status"),
			Completed: e.attr("completed"), Delegate: e.attr("delegate"), Origin: e.attr("origin"),
		})
	}
	// Stable: unparsable times keep document order relative to each other.
	sort.SliceStable(events, func(i, j int) bool {
		ti, oki := ParseFenceTime(events[i].Completed)
		tj, okj := ParseFenceTime(events[j].Completed)
		if oki && okj {
			return ti.After(tj)
		}
		return oki && !okj
	})
	inCluster := func(target string) bool {
		if len(nodeNames) == 0 {
			return target != ""
		}
		for _, n := range nodeNames {
			if n == target {
				return true
			}
		}
		return false
	}
	tested := false
	kept := events[:0]
	for _, ev := range events {
		if !inCluster(ev.Target) {
			continue
		}
		if ev.Successful() {
			tested = true
		}
		kept = append(kept, ev)
	}
	if len(kept) > MaxFenceEvents {
		kept = kept[:MaxFenceEvents]
	}
	if len(kept) == 0 {
		return nil, tested
	}
	return kept, tested
}

// Fencing modes reported by FencingMode.
const (
	FencingModeWatchdogOnly = "watchdog_only"
	FencingModeDevice       = "device"
	FencingModeNone         = "none"
)

// FencingMode classifies how this cluster fences: only the SBD watchdog
// (stonith-watchdog-timeout set, no stonith primitive), a stonith device, or
// nothing. Pure.
func FencingMode(st PacemakerStatus) string {
	switch {
	case st.FenceDevices > 0:
		return FencingModeDevice
	case st.WatchdogFencing != nil && *st.WatchdogFencing:
		return FencingModeWatchdogOnly
	}
	return FencingModeNone
}

// FencingModeFromCIB is FencingMode for a raw `pcs cluster cib` document.
func FencingModeFromCIB(cib []byte) (string, error) {
	cfg, err := parsePacemakerConfig(cib)
	if err != nil {
		return FencingModeNone, err
	}
	return FencingMode(PacemakerStatus{FenceDevices: cfg.FenceDevices, WatchdogFencing: cfg.WatchdogFencing}), nil
}
