package ha

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"webkvm/internal/sysutil"
)

// DefaultCPGroup is the Pacemaker group that holds the WebKVM control plane
// when the `ha.cp_group` setting is unset.
const DefaultCPGroup = "webkvm-cp"

// Workload kinds.
const (
	KindKVM   = "kvm"
	KindIncus = "incus"
)

// Workload states.
const (
	WorkloadStarted   = "started"
	WorkloadStarting  = "starting"
	WorkloadStopped   = "stopped"
	WorkloadFailed    = "failed"
	WorkloadUnmanaged = "unmanaged"
	WorkloadPaused    = "paused"
	WorkloadBlocked   = "blocked"
)

// Meta attributes WebKVM writes on the primitives it creates. They are the
// source of truth (D1): resource IDs are never parsed back into names.
const (
	MetaKind       = "webkvm-kind"
	MetaName       = "webkvm-name"
	MetaProject    = "webkvm-project"
	MetaMaintUntil = "webkvm-maint-until"
	MetaMaintPrev  = "webkvm-maint-prev"
)

// WorkloadNameRE is the only shape of VM/instance/project name accepted on
// its way to a pcs argv. It cannot start with '-' and has no shell or pcs
// metacharacters (no '=', spaces, quotes).
var WorkloadNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// WorkloadKey identifies a workload independently of Pacemaker resource IDs.
// The API form is `kvm:<name>` or `incus:<project>/<name>`.
type WorkloadKey struct {
	Kind    string
	Project string
	Name    string
}

func (k WorkloadKey) String() string {
	if k.Kind == KindIncus {
		return KindIncus + ":" + k.Project + "/" + k.Name
	}
	return k.Kind + ":" + k.Name
}

// Validate rejects anything that is not a well-formed key.
func (k WorkloadKey) Validate() error {
	switch k.Kind {
	case KindKVM:
		if k.Project != "" {
			return errors.New("kvm workloads have no project")
		}
	case KindIncus:
		if !WorkloadNameRE.MatchString(k.Project) {
			return errors.New("invalid incus project name")
		}
	default:
		return errors.New("unknown workload kind")
	}
	if !WorkloadNameRE.MatchString(k.Name) {
		return errors.New("invalid workload name")
	}
	return nil
}

// ParseWorkloadKey parses the API form of a key.
func ParseWorkloadKey(s string) (WorkloadKey, error) {
	kind, rest, ok := strings.Cut(s, ":")
	if !ok {
		return WorkloadKey{}, errors.New("workload key must be kvm:<name> or incus:<project>/<name>")
	}
	key := WorkloadKey{Kind: kind, Name: rest}
	if kind == KindIncus {
		project, name, ok := strings.Cut(rest, "/")
		if !ok {
			return WorkloadKey{}, errors.New("incus workload key must be incus:<project>/<name>")
		}
		key.Project, key.Name = project, name
	}
	if err := key.Validate(); err != nil {
		return WorkloadKey{}, err
	}
	return key, nil
}

// Reason is one cause of ineligibility with a hint the UI can show.
type Reason struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
	// Commands are exact, copyable commands that fix the reason (optional).
	Commands []string `json:"commands,omitempty"`
}

// Eligibility says whether a workload can be put under HA.
type Eligibility struct {
	Eligible bool     `json:"eligible"`
	Reasons  []Reason `json:"reasons"`
}

// WorkloadFailure is one failed operation reported by crm_mon.
type WorkloadFailure struct {
	Op           string `json:"op"`
	Node         string `json:"node"`
	ExitReason   string `json:"exit_reason,omitempty"`
	RC           string `json:"rc,omitempty"`
	LastRCChange string `json:"last_rc_change,omitempty"`
}

// Workload is one VM or Incus instance as Pacemaker sees it.
type Workload struct {
	Key        string `json:"key"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Project    string `json:"project,omitempty"`
	ResourceID string `json:"resource_id"`
	// Group is the enclosing group of the primitive ("" for a bare primitive).
	Group string `json:"group,omitempty"`
	// ResourceIDs is the whole stack WebKVM would delete on disable, top-level
	// IDs first (group, then the DRBD clone).
	ResourceIDs []string `json:"resource_ids"`
	// Adopted is true for legacy resources that lack the webkvm-* meta.
	Adopted            bool              `json:"adopted,omitempty"`
	Managed            bool              `json:"managed"`
	State              string            `json:"state"`
	Node               string            `json:"node"`
	Since              string            `json:"since,omitempty"`
	Failcount          int               `json:"failcount"`
	Failures           []WorkloadFailure `json:"failures"`
	Stickiness         string            `json:"stickiness,omitempty"`
	MigrationThreshold string            `json:"migration_threshold,omitempty"`
	AllowedNodes       []string          `json:"allowed_nodes"`
	PreferredNode      string            `json:"preferred_node,omitempty"`
	// MaintUntil is the RFC 3339 expiry of an active maintenance window.
	MaintUntil  string      `json:"maint_until,omitempty"`
	Eligibility Eligibility `json:"eligibility"`
}

// WorkloadKeyOf rebuilds the typed key of a parsed workload.
func (w Workload) WorkloadKeyOf() WorkloadKey {
	return WorkloadKey{Kind: w.Kind, Project: w.Project, Name: w.Name}
}

// TopResource is the resource pcs must act on to move, disable or unmanage
// the whole workload: the group when there is one, else the primitive.
func (w Workload) TopResource() string {
	if w.Group != "" {
		return w.Group
	}
	return w.ResourceID
}

// MaintenanceExpired reports whether an unmanaged workload outlived its TTL.
func (w Workload) MaintenanceExpired(now time.Time) bool {
	if w.MaintUntil == "" {
		return false
	}
	until, err := time.Parse(time.RFC3339, w.MaintUntil)
	return err == nil && now.After(until)
}

// cibPrimitive is one primitive of the CIB with its direct attributes and
// the containers around it.
type cibPrimitive struct {
	ID     string
	Agent  string // class:provider:type (or class:type)
	Type   string
	Group  string
	Clone  string
	Params map[string]string
	Meta   map[string]string
	// GroupMeta/CloneMeta are the meta attributes of the enclosing containers.
	GroupMeta map[string]string
	CloneMeta map[string]string
}

// directPairs collects the nvpairs of the direct <kind> children of e. It
// deliberately ignores nested sets (operations carry their own attributes).
func directPairs(e xmlElement, kind string) map[string]string {
	out := map[string]string{}
	for _, child := range e.Children {
		if child.XMLName.Local != kind {
			continue
		}
		for _, pair := range child.Children {
			if pair.XMLName.Local == "nvpair" && pair.attr("name") != "" {
				out[pair.attr("name")] = pair.attr("value")
			}
		}
	}
	return out
}

func agentOf(p xmlElement) string {
	parts := []string{}
	for _, name := range []string{"class", "provider", "type"} {
		if v := p.attr(name); v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, ":")
}

// cibPrimitives is the single CIB walk every caller shares.
func cibPrimitives(root xmlElement) []cibPrimitive {
	resources := root.find("resources")
	if resources == nil {
		return nil
	}
	var out []cibPrimitive
	var walk func(e xmlElement, group, clone string, groupMeta, cloneMeta map[string]string)
	walk = func(e xmlElement, group, clone string, groupMeta, cloneMeta map[string]string) {
		for _, child := range e.Children {
			switch child.XMLName.Local {
			case "primitive":
				out = append(out, cibPrimitive{
					ID: child.attr("id"), Agent: agentOf(child), Type: child.attr("type"),
					Group: group, Clone: clone,
					Params:    directPairs(child, "instance_attributes"),
					Meta:      directPairs(child, "meta_attributes"),
					GroupMeta: groupMeta, CloneMeta: cloneMeta,
				})
			case "group":
				walk(child, child.attr("id"), clone, directPairs(child, "meta_attributes"), cloneMeta)
			case "clone", "master", "bundle":
				walk(child, group, child.attr("id"), groupMeta, directPairs(child, "meta_attributes"))
			}
		}
	}
	walk(*resources, "", "", nil, nil)
	return out
}

func (p cibPrimitive) isVirtualDomain() bool { return p.Type == "VirtualDomain" }
func (p cibPrimitive) isIncus() bool         { return p.Agent == "ocf:webkvm:incus" }

// meta looks a meta attribute up on the primitive, then its group.
func (p cibPrimitive) meta(name string) string {
	if v, ok := p.Meta[name]; ok {
		return v
	}
	return p.GroupMeta[name]
}

// domainNameReader reads the libvirt domain name from a VirtualDomain config
// file. It is a variable so tests do not depend on the filesystem.
var domainNameReader = func(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var domain struct {
		Name string `xml:"name"`
	}
	if err := xml.Unmarshal(data, &domain); err != nil {
		return ""
	}
	return strings.TrimSpace(domain.Name)
}

// statusResource is the runtime view of one resource in `pcs status xml`.
type statusResource struct {
	Role    string
	Active  bool
	Failed  bool
	Blocked bool
	Managed bool
	Node    string
	seen    bool
}

type statusHistory struct {
	Failcount    int
	LastRCChange string
	Threshold    string
}

type statusView struct {
	Resources map[string]statusResource
	History   map[string]statusHistory
	Failures  map[string][]WorkloadFailure
}

func parseBoolDefault(raw string, def bool) bool {
	if v, err := strconv.ParseBool(raw); err == nil {
		return v
	}
	return def
}

// opKeyResource strips `_<op>_<interval>` from a Pacemaker operation key.
func opKeyResource(opKey string) (resource, op string) {
	i := strings.LastIndex(opKey, "_")
	if i <= 0 {
		return opKey, ""
	}
	j := strings.LastIndex(opKey[:i], "_")
	if j <= 0 {
		return opKey[:i], opKey[i+1:]
	}
	return opKey[:j], opKey[j+1 : i]
}

// parseStatusView extracts per-resource runtime state. fail-count,
// last-rc-change and <failures> are best effort: their presence in the
// default `pcs status xml` output of Pacemaker 3.0.1 was not verified against
// a real cluster, so missing sections simply yield zero values.
func parseStatusView(statusXML []byte) statusView {
	view := statusView{
		Resources: map[string]statusResource{},
		History:   map[string]statusHistory{},
		Failures:  map[string][]WorkloadFailure{},
	}
	var root xmlElement
	if len(statusXML) == 0 || xml.Unmarshal(statusXML, &root) != nil {
		return view
	}
	if resources := root.find("resources"); resources != nil {
		var items []xmlElement
		resources.descendants("resource", &items)
		for _, item := range items {
			id := item.attr("id")
			// Clone instances may be reported as "id:0"; keep the base id.
			if i := strings.Index(id, ":"); i > 0 {
				id = id[:i]
			}
			cur := statusResource{
				Role:    item.attr("role"),
				Active:  parseBoolDefault(item.attr("active"), false),
				Failed:  parseBoolDefault(item.attr("failed"), false),
				Blocked: parseBoolDefault(item.attr("blocked"), false),
				Managed: parseBoolDefault(item.attr("managed"), true),
				seen:    true,
			}
			if node := item.find("node"); node != nil {
				cur.Node = node.attr("name")
			}
			// Prefer the active instance when a resource appears several times.
			if prev, ok := view.Resources[id]; ok && prev.Active && !cur.Active {
				continue
			}
			view.Resources[id] = cur
		}
	}
	if history := root.find("node_history"); history != nil {
		var items []xmlElement
		history.descendants("resource_history", &items)
		for _, item := range items {
			id := item.attr("id")
			h := view.History[id]
			if n, err := strconv.Atoi(item.attr("fail-count")); err == nil {
				h.Failcount += n
			} else if strings.EqualFold(item.attr("fail-count"), "INFINITY") {
				h.Failcount += 1000000
			}
			if v := item.attr("migration-threshold"); v != "" {
				h.Threshold = v
			}
			for _, op := range item.Children {
				if op.XMLName.Local == "operation_history" && op.attr("last-rc-change") != "" {
					h.LastRCChange = op.attr("last-rc-change")
				}
			}
			view.History[id] = h
		}
	}
	if failures := root.find("failures"); failures != nil {
		for _, item := range failures.Children {
			if item.XMLName.Local != "failure" {
				continue
			}
			key := item.attr("op_key")
			if key == "" {
				key = item.attr("id")
			}
			resource, op := opKeyResource(key)
			view.Failures[resource] = append(view.Failures[resource], WorkloadFailure{
				Op: op, Node: item.attr("node"), ExitReason: item.attr("exitreason"),
				RC: item.attr("rc_text"), LastRCChange: item.attr("last-rc-change"),
			})
		}
	}
	return view
}

type cibLocation struct{ ID, Rsc, Node, Score string }

func cibLocations(root xmlElement) []cibLocation {
	constraints := root.find("constraints")
	if constraints == nil {
		return nil
	}
	var out []cibLocation
	for _, c := range constraints.Children {
		if c.XMLName.Local == "rsc_location" && c.attr("node") != "" {
			out = append(out, cibLocation{ID: c.attr("id"), Rsc: c.attr("rsc"), Node: c.attr("node"), Score: c.attr("score")})
		}
	}
	return out
}

func cibNodeNames(root xmlElement) []string {
	names := []string{}
	if nodes := root.find("nodes"); nodes != nil {
		for _, node := range nodes.Children {
			if node.XMLName.Local != "node" {
				continue
			}
			if name := node.attr("uname"); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func normCPGroup(cpGroup string) string {
	if strings.TrimSpace(cpGroup) == "" {
		return DefaultCPGroup
	}
	return cpGroup
}

// ParseWorkloads builds the workload list from the CIB (configuration, the
// source of truth) and, optionally, `pcs status xml` (runtime state).
// Primitives inside the control-plane group cpGroup are never workloads.
// statusXML may be nil: every workload is then reported stopped with no node.
func ParseWorkloads(cibXML, statusXML []byte, cpGroup string) ([]Workload, error) {
	var root xmlElement
	if err := xml.Unmarshal(cibXML, &root); err != nil {
		return nil, fmt.Errorf("parse CIB: %w", err)
	}
	cpGroup = normCPGroup(cpGroup)
	primitives := cibPrimitives(root)
	view := parseStatusView(statusXML)
	locations := cibLocations(root)
	nodes := cibNodeNames(root)

	out := []Workload{}
	for _, p := range primitives {
		if p.ID == "" || p.Group == cpGroup {
			continue
		}
		w := Workload{ResourceID: p.ID, Group: p.Group, Failures: []WorkloadFailure{}, AllowedNodes: []string{},
			Eligibility: Eligibility{Eligible: true, Reasons: []Reason{}}}
		kind := p.Meta[MetaKind]
		switch {
		case kind == KindKVM && p.isVirtualDomain():
			w.Kind, w.Name = KindKVM, p.Meta[MetaName]
		case kind == KindIncus && p.isIncus():
			w.Kind, w.Name, w.Project = KindIncus, p.Meta[MetaName], p.Meta[MetaProject]
		case kind == "" && p.isVirtualDomain():
			w.Kind, w.Adopted = KindKVM, true
			if config := p.Params["config"]; config != "" {
				w.Name = domainNameReader(config)
			}
		case kind == "" && p.isIncus():
			w.Kind, w.Adopted = KindIncus, true
			w.Name, w.Project = p.Params["instance"], p.Params["project"]
		default:
			continue
		}
		if w.Name == "" {
			w.Name = p.ID // legacy VM whose XML is not readable on this node
		}
		if w.Kind == KindIncus && w.Project == "" {
			w.Project = "default"
		}
		w.Key = w.WorkloadKeyOf().String()
		w.ResourceIDs = stackResources(p, primitives)

		w.Stickiness = p.meta("resource-stickiness")
		w.MigrationThreshold = p.meta("migration-threshold")
		w.MaintUntil = p.meta(MetaMaintUntil)

		top := w.TopResource()
		banned := map[string]bool{}
		for _, loc := range locations {
			if loc.Rsc != top && loc.Rsc != p.ID {
				continue
			}
			switch {
			case strings.HasPrefix(loc.Score, "-"):
				banned[loc.Node] = true
			case strings.HasPrefix(loc.ID, "webkvm-loc-") && loc.Score != "" && loc.Score != "0":
				w.PreferredNode = loc.Node
			}
		}
		for _, node := range nodes {
			if !banned[node] {
				w.AllowedNodes = append(w.AllowedNodes, node)
			}
		}

		cibManaged := !strings.EqualFold(p.meta("is-managed"), "false")
		disabled := strings.EqualFold(p.meta("target-role"), "Stopped")
		st := view.Resources[p.ID]
		w.Managed = cibManaged && (!st.seen || st.Managed)
		w.Node = st.Node
		hist := view.History[p.ID]
		w.Failcount = hist.Failcount
		w.Since = hist.LastRCChange
		if w.MigrationThreshold == "" {
			w.MigrationThreshold = hist.Threshold
		}
		if f := view.Failures[p.ID]; len(f) > 0 {
			w.Failures = f
		}
		running := st.Active && (st.Role == "Started" || st.Role == "Promoted")
		switch {
		case !w.Managed:
			w.State = WorkloadUnmanaged
		case st.Blocked:
			w.State = WorkloadBlocked
		case st.Failed:
			w.State = WorkloadFailed
		case running:
			w.State = WorkloadStarted
		case st.Role == "Starting" || st.Role == "Migrating":
			w.State = WorkloadStarting
		case disabled:
			w.State = WorkloadPaused
		case len(w.Failures) > 0 || w.Failcount > 0:
			w.State = WorkloadFailed
		default:
			w.State = WorkloadStopped
		}
		if !running {
			w.Node = ""
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// stackResources lists the top-level resources that form the workload stack:
// the group (or bare primitive) plus, for the WebKVM KVM pattern, the DRBD
// clone named after the VM resource. Only resources that exist are listed.
func stackResources(p cibPrimitive, all []cibPrimitive) []string {
	ids := []string{p.ID}
	if p.Group != "" {
		ids = []string{p.Group}
	}
	if p.Meta[MetaKind] == KindKVM {
		for _, other := range all {
			if other.ID == p.ID+"-data" && other.Clone != "" {
				ids = append(ids, other.Clone)
			}
		}
	}
	return ids
}

// FindWorkload returns the workloads matching key. More than one match means
// two resources claim the same VM (ambiguous).
func FindWorkload(list []Workload, key WorkloadKey) []Workload {
	var out []Workload
	want := key.String()
	for _, w := range list {
		if w.Key == want {
			out = append(out, w)
		}
	}
	return out
}

// ErrPacemakerUnavailable is returned when the CIB cannot be read.
var ErrPacemakerUnavailable = errors.New("pacemaker CIB is not readable")

// readCIBAndStatus runs the two read-only queries. A missing status is not an
// error (state falls back to stopped).
func readCIBAndStatus(ctx context.Context, runner sysutil.CommandRunner) (cib, status []byte, err error) {
	if runner == nil {
		return nil, nil, ErrPacemakerUnavailable
	}
	cib, err = runReadOnly(ctx, runner, "cluster", "cib")
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrPacemakerUnavailable, err)
	}
	status, statusErr := runReadOnly(ctx, runner, "status", "xml")
	if statusErr != nil {
		status = nil
	}
	return cib, status, nil
}

// ManagedWorkloads reads the live CIB and status and returns every workload
// Pacemaker owns.
func ManagedWorkloads(ctx context.Context, runner sysutil.CommandRunner, cpGroup string) ([]Workload, error) {
	cib, status, err := readCIBAndStatus(ctx, runner)
	if err != nil {
		return nil, err
	}
	return ParseWorkloads(cib, status, cpGroup)
}

// ManagedWorkloadsAndNodes is ManagedWorkloads plus the Pacemaker node names
// of the CIB (callers use them to tell whether a registered node is a member
// of this cluster and therefore shares its view).
func ManagedWorkloadsAndNodes(ctx context.Context, runner sysutil.CommandRunner, cpGroup string) ([]Workload, []string, error) {
	cib, status, err := readCIBAndStatus(ctx, runner)
	if err != nil {
		return nil, nil, err
	}
	list, err := ParseWorkloads(cib, status, cpGroup)
	if err != nil {
		return nil, nil, err
	}
	var root xmlElement
	if xml.Unmarshal(cib, &root) != nil {
		return list, nil, nil
	}
	return list, cibNodeNames(root), nil
}
