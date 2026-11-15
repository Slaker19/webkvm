package ha

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"webkvm/internal/sysutil"
)

// Operation error codes surfaced to the API.
const (
	CodeBusy              = "operation_busy"
	CodePCSFailed         = "pcs_failed"
	CodeStartTimeout      = "start_timeout"
	CodeRollbackFailed    = "rollback_failed"
	CodeNotFound          = "workload_not_found"
	CodeAmbiguous         = "ambiguous_workload"
	CodeIDCollision       = "id_collision"
	CodeInvalid           = "invalid_spec"
	CodeMaintenanceActive = "maintenance_active"
	CodeNodeNotAllowed    = "node_not_allowed"
	CodeConfirmation      = "confirmation_required"
	// CodeStopTimeout: the stack did not stop in time; nothing was deleted
	// and what the operation disabled was enabled again.
	CodeStopTimeout = "stop_timeout"
	// CodeWorkloadActive: forget/keep_running refused because part of the
	// stack is still active (deleting it would orphan a running resource).
	CodeWorkloadActive = "workload_active"
	// CodeWorkloadPaused: like workload_active, but the workload is paused
	// (group disabled, clone still active): resume then stop, or stop it.
	CodeWorkloadPaused = "workload_paused"
)

// OpError is a typed failure of a workload operation.
type OpError struct {
	Code      string
	Msg       string
	Leftovers []string
	// Details are extra typed fields for the API body (e.g. the DRBD peers
	// that must be brought up for drbd_peers_down).
	Details map[string]any
}

func (e *OpError) Error() string { return e.Code + ": " + e.Msg }

func opErr(code, format string, args ...any) *OpError {
	return &OpError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// pcsGlobal serialises every CIB edit of this process; keyLocks serialise
// the whole operation (including the start wait) per workload.
var (
	pcsGlobal sync.Mutex
	keyLocks  sync.Map // string -> *sync.Mutex
)

// LockKey takes the per-workload lock without blocking. ok is false when
// another operation on the same workload is running (operation_busy).
func LockKey(key string) (unlock func(), ok bool) {
	v, _ := keyLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	if !mu.TryLock() {
		return func() {}, false
	}
	return mu.Unlock, true
}

var (
	drbdResourceRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,47}$`)
	drbdDeviceRE   = regexp.MustCompile(`^/dev/drbd(/by-res/[A-Za-z0-9][A-Za-z0-9_.-]{0,47}/[0-9]+|[0-9]+)$`)
	absPathRE      = regexp.MustCompile(`^/[A-Za-z0-9._+/-]{1,500}$`)
	idCleanRE      = regexp.MustCompile(`[^a-z0-9-]+`)
)

// SanitizeID maps a name to [a-z0-9-]. Different names can collide
// ("Web_1" and "web-1"); callers must run CheckIDCollision.
func SanitizeID(name string) string {
	s := strings.Trim(idCleanRE.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if s == "" {
		s = "x"
	}
	return s
}

// KVMResourceIDs returns the ids of the KVM pattern for a VM name.
func KVMResourceIDs(name string) (data, clone, fs, vm, group string) {
	n := SanitizeID(name)
	return "ha-kvm-" + n + "-data", "ha-kvm-" + n + "-data-clone", "ha-kvm-" + n + "-fs", "ha-kvm-" + n, "ha-kvm-" + n + "-grp"
}

// IncusResourceID returns the single primitive id of an Incus workload.
func IncusResourceID(project, name string) string {
	return "ha-incus-" + SanitizeID(project) + "-" + SanitizeID(name)
}

// KVMSpec describes the DRBD + Filesystem + VirtualDomain stack of one VM.
// The DRBD resource itself must already exist (LINSTOR/drbdadm); only the
// Pacemaker side is created here.
type KVMSpec struct {
	Name               string
	DRBDResource       string
	Device             string
	MountDir           string
	ConfigPath         string
	FSType             string
	ClusterNodes       []string
	AllowedNodes       []string
	PreferredNode      string
	Stickiness         int
	MigrationThreshold int
	CloneMax           int
}

// IncusSpec describes the single ocf:webkvm:incus primitive of an instance.
type IncusSpec struct {
	Name               string
	Project            string
	ClusterNodes       []string
	AllowedNodes       []string
	PreferredNode      string
	Stickiness         int
	MigrationThreshold int
}

// Plan is the set of pcs edits that create a workload. Commands are pcs argv
// without the leading "pcs" and without "-f <file>"; Pre commands are full
// argv (binary first) run before the CIB edit.
type Plan struct {
	Key         WorkloadKey `json:"-"`
	Pre         [][]string  `json:"pre,omitempty"`
	Commands    [][]string  `json:"commands"`
	Resources   []string    `json:"resources"`
	Constraints []string    `json:"constraints"`
	// Top is the resource pcs acts on (group, or the Incus primitive); VM is
	// the primitive that carries the webkvm-* meta and the runtime state.
	Top string `json:"top"`
	VM  string `json:"vm"`
}

// Display renders the plan as copyable command lines for the given temp file.
func (p Plan) Display(tmp string) []string {
	out := []string{}
	for _, c := range p.Pre {
		out = append(out, strings.Join(c, " "))
	}
	out = append(out, "pcs cluster cib "+tmp)
	for _, c := range p.Commands {
		out = append(out, "pcs -f "+tmp+" "+strings.Join(c, " "))
	}
	out = append(out, "pcs cluster cib-push "+tmp+" --config")
	return out
}

func validNodes(nodes []string) error {
	for _, n := range nodes {
		if !PacemakerNodeRE.MatchString(n) {
			return fmt.Errorf("invalid node name %q", n)
		}
	}
	return nil
}

func defaults(stick, thr int) (int, int) {
	if stick == 0 {
		stick = 100
	}
	if thr == 0 {
		thr = 3
	}
	return stick, thr
}

func validateCommon(allowed []string, preferred string, cluster []string, stick, thr int) error {
	if err := validNodes(allowed); err != nil {
		return err
	}
	if err := validNodes(cluster); err != nil {
		return err
	}
	if preferred != "" {
		if !PacemakerNodeRE.MatchString(preferred) {
			return fmt.Errorf("invalid preferred node %q", preferred)
		}
		if len(allowed) > 0 && !containsStr(allowed, preferred) {
			return errors.New("preferred node is not in allowed nodes")
		}
	}
	if stick < 0 || stick > 1000000 || thr < 0 || thr > 1000 {
		return errors.New("stickiness or migration threshold out of range")
	}
	return nil
}

// locationCommands builds the placement constraints of a top resource:
// -INFINITY on every cluster node not allowed, and a positive score on the
// preferred node. Only ids starting with "webkvm-loc-" are created.
func locationCommands(top string, cluster, allowed []string, preferred string) (cmds [][]string, ids []string) {
	if len(allowed) > 0 {
		for _, node := range cluster {
			if containsStr(allowed, node) {
				continue
			}
			id := "webkvm-loc-" + top + "-" + node
			cmds = append(cmds, []string{"constraint", "location", "add", id, top, node, "score=-INFINITY"})
			ids = append(ids, id)
		}
	}
	if preferred != "" {
		id := "webkvm-loc-" + top + "-" + preferred
		cmds = append(cmds, []string{"constraint", "location", "add", id, top, preferred, "score=100"})
		ids = append(ids, id)
	}
	return cmds, ids
}

func metaArgs(kv ...string) []string { return append([]string{"meta"}, kv...) }

// PlanEnableKVM builds the D2 pattern: DRBD promotable clone + group(fs, vm)
// with colocation and order against the Promoted clone.
func PlanEnableKVM(spec KVMSpec, start bool) (Plan, error) {
	key := WorkloadKey{Kind: KindKVM, Name: spec.Name}
	if err := key.Validate(); err != nil {
		return Plan{}, opErr(CodeInvalid, "%v", err)
	}
	if !drbdResourceRE.MatchString(spec.DRBDResource) {
		return Plan{}, opErr(CodeInvalid, "invalid DRBD resource name")
	}
	if !drbdDeviceRE.MatchString(spec.Device) {
		return Plan{}, opErr(CodeInvalid, "device must be /dev/drbd/by-res/<res>/<n> or /dev/drbd<N>")
	}
	for _, p := range []string{spec.MountDir, spec.ConfigPath} {
		if !absPathRE.MatchString(p) || strings.Contains(p, "..") {
			return Plan{}, opErr(CodeInvalid, "invalid path %q", p)
		}
	}
	if spec.FSType == "" {
		spec.FSType = "ext4"
	}
	if spec.FSType != "ext4" && spec.FSType != "xfs" {
		return Plan{}, opErr(CodeInvalid, "unsupported filesystem type")
	}
	stick, thr := defaults(spec.Stickiness, spec.MigrationThreshold)
	if err := validateCommon(spec.AllowedNodes, spec.PreferredNode, spec.ClusterNodes, stick, thr); err != nil {
		return Plan{}, opErr(CodeInvalid, "%v", err)
	}
	cloneMax := spec.CloneMax
	if cloneMax <= 0 {
		cloneMax = len(spec.ClusterNodes)
		if cloneMax < 2 {
			cloneMax = 3
		}
	}
	data, clone, fs, vm, grp := KVMResourceIDs(spec.Name)
	disabled := []string{}
	if !start {
		disabled = []string{"--disabled"}
	}
	cmds := [][]string{
		{"resource", "create", data, "ocf:linbit:drbd", "drbd_resource=" + spec.DRBDResource,
			"op", "monitor", "interval=29s", "role=Promoted", "op", "monitor", "interval=31s", "role=Unpromoted",
			"promotable", "promoted-max=1", "promoted-node-max=1", fmt.Sprintf("clone-max=%d", cloneMax), "clone-node-max=1", "notify=true"},
		append([]string{"resource", "create", fs, "ocf:heartbeat:Filesystem", "device=" + spec.Device, "directory=" + spec.MountDir,
			"fstype=" + spec.FSType, "--group", grp}, disabled...),
		append(append([]string{"resource", "create", vm, "ocf:heartbeat:VirtualDomain", "config=" + spec.ConfigPath,
			"hypervisor=qemu:///system", "force_stop=true",
			"op", "start", "timeout=180s", "op", "stop", "timeout=120s", "op", "monitor", "interval=30s", "timeout=30s",
			"op", "migrate_to", "timeout=120s", "op", "migrate_from", "timeout=120s"},
			metaArgs(MetaKind+"="+KindKVM, MetaName+"="+spec.Name,
				fmt.Sprintf("migration-threshold=%d", thr), fmt.Sprintf("resource-stickiness=%d", stick), "failure-timeout=10m")...),
			append([]string{"--group", grp}, disabled...)...),
		{"constraint", "colocation", "add", grp, "with", "Promoted", clone, "score=INFINITY", "id=webkvm-col-" + grp},
		{"constraint", "order", "promote", clone, "then", "start", grp, "id=webkvm-ord-" + grp},
	}
	constraints := []string{"webkvm-col-" + grp, "webkvm-ord-" + grp}
	loc, ids := locationCommands(grp, spec.ClusterNodes, spec.AllowedNodes, spec.PreferredNode)
	cmds = append(cmds, loc...)
	constraints = append(constraints, ids...)
	return Plan{Key: key, Commands: cmds, Resources: []string{grp, clone}, Constraints: constraints, Top: grp, VM: vm}, nil
}

// PlanEnableIncus builds the D3 plan: one ocf:webkvm:incus primitive. The
// Incus volume lives on a shared pool, so no DRBD or Filesystem resource is
// created. boot.autostart is forced to false before the CIB edit.
func PlanEnableIncus(spec IncusSpec, start bool) (Plan, error) {
	key := WorkloadKey{Kind: KindIncus, Project: spec.Project, Name: spec.Name}
	if err := key.Validate(); err != nil {
		return Plan{}, opErr(CodeInvalid, "%v", err)
	}
	stick, thr := defaults(spec.Stickiness, spec.MigrationThreshold)
	if err := validateCommon(spec.AllowedNodes, spec.PreferredNode, spec.ClusterNodes, stick, thr); err != nil {
		return Plan{}, opErr(CodeInvalid, "%v", err)
	}
	id := IncusResourceID(spec.Project, spec.Name)
	create := append([]string{"resource", "create", id, "ocf:webkvm:incus", "instance=" + spec.Name, "project=" + spec.Project,
		"op", "start", "timeout=300s", "op", "stop", "timeout=120s", "op", "monitor", "interval=30s", "timeout=30s"},
		metaArgs(MetaKind+"="+KindIncus, MetaName+"="+spec.Name, MetaProject+"="+spec.Project,
			fmt.Sprintf("migration-threshold=%d", thr), fmt.Sprintf("resource-stickiness=%d", stick), "failure-timeout=10m")...)
	if !start {
		create = append(create, "--disabled")
	}
	cmds := [][]string{create}
	loc, ids := locationCommands(id, spec.ClusterNodes, spec.AllowedNodes, spec.PreferredNode)
	cmds = append(cmds, loc...)
	return Plan{
		Key:         key,
		Pre:         [][]string{{"incus", "config", "set", spec.Name, "boot.autostart=false", "--project", spec.Project}},
		Commands:    cmds,
		Resources:   []string{id},
		Constraints: ids,
		Top:         id,
		VM:          id,
	}, nil
}

// PlanAntiAffinity keeps two workloads on different nodes.
func PlanAntiAffinity(topA, topB string) ([]string, string, error) {
	for _, id := range []string{topA, topB} {
		if !strings.HasPrefix(id, "ha-") || idCleanRE.MatchString(id) {
			return nil, "", opErr(CodeInvalid, "invalid resource id %q", id)
		}
	}
	if topA == topB {
		return nil, "", opErr(CodeInvalid, "a workload cannot be anti-affine to itself")
	}
	if topA > topB {
		topA, topB = topB, topA
	}
	id := "webkvm-anti-" + topA + "-" + topB
	return []string{"constraint", "colocation", "add", topA, "with", topB, "score=-INFINITY", "id=" + id}, id, nil
}

// EnableOptions tunes ApplyEnable.
type EnableOptions struct {
	Start                  bool
	DryRun                 bool
	RollbackOnStartFailure bool
	StartTimeout           time.Duration
	PollInterval           time.Duration
	CPGroup                string
	// TmpDir overrides the directory of the CIB scratch file (tests).
	TmpDir string
}

// EnableResult is the outcome of ApplyEnable.
type EnableResult struct {
	Changed     bool
	State       string
	ResourceIDs []string
	Commands    []string
}

func (o EnableOptions) poll() time.Duration {
	if o.PollInterval > 0 {
		return o.PollInterval
	}
	return 2 * time.Second
}

func (o EnableOptions) timeout() time.Duration {
	if o.StartTimeout > 0 {
		return o.StartTimeout
	}
	return 5 * time.Minute
}

func runPCS(ctx context.Context, runner sysutil.CommandRunner, timeout time.Duration, args ...string) ([]byte, error) {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return runner.Run(c, "pcs", args...)
}

func tailOf(out []byte, err error) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 300 {
		s = s[len(s)-300:]
	}
	if s == "" && err != nil {
		return err.Error()
	}
	return s
}

// applyCIB is D4: copy the CIB, run every edit against the file, then push
// the config. Nothing is live before the push, so a failure needs no rollback.
func applyCIB(ctx context.Context, runner sysutil.CommandRunner, tmpDir string, cmds [][]string) error {
	pcsGlobal.Lock()
	defer pcsGlobal.Unlock()
	dir, err := os.MkdirTemp(tmpDir, "webkvm-ha-")
	if err != nil {
		return opErr(CodePCSFailed, "cannot create scratch dir: %v", err)
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "cib.xml")
	if out, err := runPCS(ctx, runner, 30*time.Second, "cluster", "cib", tmp); err != nil {
		return opErr(CodePCSFailed, "pcs cluster cib: %s", tailOf(out, err))
	}
	for _, c := range cmds {
		if out, err := runPCS(ctx, runner, 30*time.Second, append([]string{"-f", tmp}, c...)...); err != nil {
			return opErr(CodePCSFailed, "pcs %s: %s", strings.Join(c[:min(3, len(c))], " "), tailOf(out, err))
		}
	}
	if out, err := runPCS(ctx, runner, 60*time.Second, "cluster", "cib-push", tmp, "--config"); err != nil {
		return opErr(CodePCSFailed, "pcs cluster cib-push: %s", tailOf(out, err))
	}
	return nil
}

// resolve returns the single workload of key.
func resolve(ctx context.Context, runner sysutil.CommandRunner, key WorkloadKey, cpGroup string) (Workload, error) {
	list, err := ManagedWorkloads(ctx, runner, cpGroup)
	if err != nil {
		return Workload{}, opErr(CodePCSFailed, "%v", err)
	}
	found := FindWorkload(list, key)
	switch len(found) {
	case 0:
		return Workload{}, opErr(CodeNotFound, "workload %s is not under Pacemaker", key)
	case 1:
		return found[0], nil
	}
	return Workload{}, opErr(CodeAmbiguous, "several resources claim %s", key)
}

// waitWorkload polls until cond is satisfied, the workload fails, or the
// timeout expires. It returns the last observed workload.
func waitWorkload(ctx context.Context, runner sysutil.CommandRunner, key WorkloadKey, cpGroup string, timeout, interval time.Duration, cond func(Workload) bool) (Workload, bool, error) {
	deadline := time.Now().Add(timeout)
	var last Workload
	for {
		w, err := resolve(ctx, runner, key, cpGroup)
		if err == nil {
			last = w
			if cond(w) {
				return w, true, nil
			}
			if w.State == WorkloadFailed {
				return w, false, nil
			}
		}
		if time.Now().After(deadline) {
			return last, false, nil
		}
		select {
		case <-ctx.Done():
			return last, false, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// ApplyEnable implements D4 with idempotency and start-failure rollback.
func ApplyEnable(ctx context.Context, runner sysutil.CommandRunner, plan Plan, opts EnableOptions) (EnableResult, error) {
	res := EnableResult{ResourceIDs: append([]string{}, plan.Resources...), Commands: plan.Display("<tmp-cib>")}
	if len(plan.Commands) == 0 || plan.VM == "" {
		return res, opErr(CodeInvalid, "empty plan")
	}
	unlock, ok := LockKey(plan.Key.String())
	if !ok {
		return res, opErr(CodeBusy, "another operation on %s is running", plan.Key)
	}
	defer unlock()

	// Idempotency / collision check against the live CIB.
	cib, status, err := readCIBAndStatus(ctx, runner)
	if err != nil {
		return res, opErr(CodePCSFailed, "%v", err)
	}
	existing, perr := ParseWorkloads(cib, status, opts.CPGroup)
	if perr != nil {
		return res, opErr(CodePCSFailed, "%v", perr)
	}
	for _, w := range existing {
		if w.ResourceID == plan.VM {
			if w.Key == plan.Key.String() {
				res.State = w.State
				return res, nil // already in the desired state
			}
			return res, opErr(CodeIDCollision, "resource %s already belongs to %s", plan.VM, w.Key)
		}
		if w.Key == plan.Key.String() {
			return res, opErr(CodeIDCollision, "%s is already managed by resource %s", plan.Key, w.ResourceID)
		}
	}
	if clash := foreignResource(cib, plan.Resources); clash != "" {
		return res, opErr(CodeIDCollision, "resource id %s is already in use", clash)
	}
	res.Changed = true
	if opts.DryRun {
		res.State = WorkloadStopped
		return res, nil
	}
	for _, c := range plan.Pre {
		c2, cancel := context.WithTimeout(ctx, 30*time.Second)
		out, err := runner.Run(c2, c[0], c[1:]...)
		cancel()
		if err != nil {
			return res, opErr(CodePCSFailed, "%s: %s", c[0], tailOf(out, err))
		}
	}
	if err := applyCIB(ctx, runner, opts.TmpDir, plan.Commands); err != nil {
		res.Changed = false
		return res, err
	}
	res.State = WorkloadStopped
	if !opts.Start {
		res.State = WorkloadPaused
		return res, nil
	}
	w, started, werr := waitWorkload(ctx, runner, plan.Key, opts.CPGroup, opts.timeout(), opts.poll(),
		func(w Workload) bool { return w.State == WorkloadStarted })
	if started {
		res.State = w.State
		return res, nil
	}
	if w.State != "" {
		res.State = w.State
	}
	if werr != nil {
		return res, werr
	}
	if opts.RollbackOnStartFailure {
		if leftovers := rollbackEnable(ctx, runner, plan, opts.timeout(), opts.poll()); len(leftovers) > 0 {
			e := opErr(CodeRollbackFailed, "start failed and rollback left resources behind")
			e.Leftovers = leftovers
			return res, e
		}
		res.Changed = false
	}
	return res, opErr(CodeStartTimeout, "%s did not reach Started in %s", plan.Key, opts.timeout())
}

// foreignResource returns a plan resource id that already exists in the CIB.
func foreignResource(cib []byte, ids []string) string {
	text := string(cib)
	for _, id := range ids {
		if strings.Contains(text, `id="`+id+`"`) {
			return id
		}
	}
	return ""
}

// rollbackEnable takes down what a failed start created, with the same safe
// order as ApplyDisable: disable every top resource (group, then the DRBD
// clone), wait until no instance is active on any node, and only then
// delete. If the stack does not stop, nothing is deleted (an active resource
// removed from the CIB becomes an orphan that Pacemaker stops without its
// parameters, fails and fences). It returns the ids it could not remove.
func rollbackEnable(ctx context.Context, runner sysutil.CommandRunner, plan Plan, timeout, interval time.Duration) []string {
	if err := stopStack(ctx, runner, plan.Resources, timeout, interval); err != nil {
		left := []string{}
		for _, id := range plan.Resources {
			left = append(left, id+": not deleted, the stack did not stop ("+err.Error()+")")
		}
		return left
	}
	var left []string
	for _, id := range plan.Resources {
		if out, err := runPCS(ctx, runner, 60*time.Second, "resource", "delete", id); err != nil {
			left = append(left, id+": "+tailOf(out, err))
		}
	}
	return left
}

// stackPrimitives returns the primitive ids inside each top resource (the
// top itself, the members of a group, the primitive of a clone).
func stackPrimitives(root xmlElement, tops []string) []string {
	want := map[string]bool{}
	for _, t := range tops {
		want[t] = true
	}
	var out []string
	for _, p := range cibPrimitives(root) {
		if want[p.ID] || (p.Group != "" && want[p.Group]) || (p.Clone != "" && want[p.Clone]) {
			out = append(out, p.ID)
		}
	}
	return out
}

// elementByID finds the resource element (primitive/group/clone) with id.
func elementByID(e xmlElement, id string) *xmlElement {
	if e.attr("id") == id && e.XMLName.Local != "nvpair" {
		return &e
	}
	for i := range e.Children {
		if found := elementByID(e.Children[i], id); found != nil {
			return found
		}
	}
	return nil
}

// targetStopped reports whether resource id already has target-role=Stopped
// in its own meta attributes (paused or disabled before this operation).
func targetStopped(root xmlElement, id string) bool {
	resources := root.find("resources")
	if resources == nil {
		return false
	}
	e := elementByID(*resources, id)
	if e == nil {
		return false
	}
	return strings.EqualFold(directPairs(*e, "meta_attributes")["target-role"], "Stopped")
}

// errStackUnknown means the runtime state could not be read, so the stack
// can not be proven stopped (fail closed: treated as active).
var errStackUnknown = errors.New("cluster status is not readable")

// activeInStatus returns the primitives of prims with an active instance on
// any node. ok is false when the status has no <resources> section. A
// primitive absent from a readable status is inactive: crm_mon always lists
// active resources (orphans included).
func activeInStatus(statusXML []byte, prims []string) (active []string, ok bool) {
	var root xmlElement
	if len(statusXML) == 0 || xml.Unmarshal(statusXML, &root) != nil {
		return nil, false
	}
	resources := root.find("resources")
	if resources == nil {
		return nil, false
	}
	want := map[string]bool{}
	for _, p := range prims {
		want[p] = true
	}
	seen := map[string]bool{}
	var items []xmlElement
	resources.descendants("resource", &items)
	for _, item := range items {
		id := item.attr("id")
		if i := strings.Index(id, ":"); i > 0 {
			id = id[:i]
		}
		if !want[id] || seen[id] {
			continue
		}
		role := item.attr("role")
		if parseBoolDefault(item.attr("active"), false) || (role != "" && role != "Stopped") {
			seen[id] = true
			active = append(active, id)
		}
	}
	sort.Strings(active)
	return active, true
}

// stackActivity reads the live CIB and status and returns the active
// primitives of the stack formed by tops.
func stackActivity(ctx context.Context, runner sysutil.CommandRunner, tops []string) ([]string, error) {
	cib, err := runReadOnly(ctx, runner, "cluster", "cib")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPacemakerUnavailable, err)
	}
	var root xmlElement
	if err := xml.Unmarshal(cib, &root); err != nil {
		return nil, fmt.Errorf("parse CIB: %w", err)
	}
	prims := stackPrimitives(root, tops)
	if len(prims) == 0 {
		return nil, nil
	}
	status, err := runReadOnly(ctx, runner, "status", "xml")
	if err != nil {
		return nil, errStackUnknown
	}
	active, ok := activeInStatus(status, prims)
	if !ok {
		return nil, errStackUnknown
	}
	return active, nil
}

// waitStackInactive polls until no primitive of tops is active anywhere. An
// unreadable status counts as active. It returns what was last seen active.
func waitStackInactive(ctx context.Context, runner sysutil.CommandRunner, tops []string, timeout, interval time.Duration) ([]string, bool, error) {
	deadline := time.Now().Add(timeout)
	var last []string
	for {
		active, err := stackActivity(ctx, runner, tops)
		if err == nil {
			if len(active) == 0 {
				return nil, true, nil
			}
			last = active
		} else {
			last = []string{"unknown: " + err.Error()}
		}
		if time.Now().After(deadline) {
			return last, false, nil
		}
		select {
		case <-ctx.Done():
			return last, false, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// stopStack disables the top resources one at a time, in order (the group
// before the DRBD clone it depends on), waiting after each until nothing of
// the stack disabled so far is active on any node. On failure it re-enables,
// in reverse order, only what it disabled itself and returns a typed error;
// nothing is ever deleted here.
func stopStack(ctx context.Context, runner sysutil.CommandRunner, tops []string, timeout, interval time.Duration) error {
	// The CIB tells which tops were already disabled (paused) before this
	// operation, so a rollback never re-enables them. Unreadable: change
	// nothing.
	var root xmlElement
	cib, err := runReadOnly(ctx, runner, "cluster", "cib")
	if err == nil {
		err = xml.Unmarshal(cib, &root)
	}
	if err != nil {
		return opErr(CodePCSFailed, "cannot read the CIB (%v); nothing was changed", err)
	}
	var disabledByUs []string
	rollback := func(cause *OpError) error {
		rctx := context.WithoutCancel(ctx)
		var left []string
		for i := len(disabledByUs) - 1; i >= 0; i-- {
			if out, err := runPCS(rctx, runner, 30*time.Second, "resource", "enable", disabledByUs[i]); err != nil {
				left = append(left, disabledByUs[i]+": still disabled: "+tailOf(out, err))
			}
		}
		if len(left) > 0 {
			e := opErr(CodeRollbackFailed, "%s; re-enabling failed", cause.Msg)
			e.Leftovers = left
			return e
		}
		return cause
	}
	for i, top := range tops {
		if !targetStopped(root, top) {
			if out, err := runPCS(ctx, runner, 30*time.Second, "resource", "disable", top); err != nil {
				return rollback(opErr(CodePCSFailed, "pcs resource disable %s: %s; nothing was deleted", top, tailOf(out, err)))
			}
			disabledByUs = append(disabledByUs, top)
		}
		active, stopped, werr := waitStackInactive(ctx, runner, tops[:i+1], timeout, interval)
		if werr != nil {
			return rollback(opErr(CodeStopTimeout, "%v while waiting for %s to stop; nothing was deleted", werr, top))
		}
		if !stopped {
			return rollback(opErr(CodeStopTimeout, "%s still active after %s (%s); nothing was deleted",
				top, timeout, strings.Join(active, ", ")))
		}
	}
	return nil
}

// Disable modes.
const (
	ModeStop        = "stop"
	ModePause       = "pause"
	ModeForget      = "forget"
	ModeKeepRunning = "keep_running"
)

// DisableOptions tunes ApplyDisable.
type DisableOptions struct {
	AllowKeepRunning bool
	StopTimeout      time.Duration
	PollInterval     time.Duration
	CPGroup          string
	TmpDir           string
}

// DisableResult is the outcome of ApplyDisable.
type DisableResult struct {
	Changed  bool
	State    string
	Commands []string
}

// ResumeOptions tunes ApplyResume.
type ResumeOptions struct {
	StartTimeout time.Duration
	PollInterval time.Duration
	CPGroup      string
}

// ApplyResume re-enables a paused workload (target-role=Stopped, set by
// ApplyDisable mode pause or by an enable with start=false) and waits for it
// to start. A workload that is not paused is a no-op (Changed false), so the
// call is idempotent.
func ApplyResume(ctx context.Context, runner sysutil.CommandRunner, key WorkloadKey, opts ResumeOptions) (EnableResult, error) {
	if err := key.Validate(); err != nil {
		return EnableResult{}, opErr(CodeInvalid, "%v", err)
	}
	unlock, ok := LockKey(key.String())
	if !ok {
		return EnableResult{}, opErr(CodeBusy, "another operation on %s is running", key)
	}
	defer unlock()
	w, err := resolve(ctx, runner, key, opts.CPGroup)
	if err != nil {
		return EnableResult{}, err
	}
	res := EnableResult{State: w.State, ResourceIDs: w.ResourceIDs}
	if w.State != WorkloadPaused {
		return res, nil
	}
	top := w.TopResource()
	if out, err := runPCS(ctx, runner, 30*time.Second, "resource", "enable", top); err != nil {
		return res, opErr(CodePCSFailed, "pcs resource enable: %s", tailOf(out, err))
	}
	res.Changed = true
	timeout := opts.StartTimeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	w2, started, werr := waitWorkload(ctx, runner, key, opts.CPGroup, timeout, interval,
		func(w Workload) bool { return w.State == WorkloadStarted })
	if w2.State != "" {
		res.State = w2.State
	}
	if started {
		return res, nil
	}
	if werr != nil {
		return res, werr
	}
	return res, opErr(CodeStartTimeout, "%s did not start in %s", key, timeout)
}

// ApplyDisable takes a workload out of HA.
//
//   - stop: disable the stack top by top (group, then the DRBD clone), wait
//     until no instance of any of its primitives is active on any node, then
//     delete it in one atomic CIB push. If it does not stop within
//     StopTimeout, nothing is deleted, what was disabled is enabled again and
//     CodeStopTimeout is returned.
//   - pause: disable the top resource only (the DRBD clone stays Promoted so
//     a resume is fast); nothing is deleted.
//   - forget / keep_running: remove an already stopped stack without stopping
//     anything. Refused with CodeWorkloadActive while any part of it is
//     active: an active resource removed from the CIB becomes an orphan that
//     Pacemaker stops without its parameters (the v11 incident: the DRBD
//     clone failed its demote/stop on every node and SBD fenced all of them).
//
// The delete is never issued while anything of the stack is active (or its
// state is unknown). An unknown workload is a no-op (changed false) so the
// call is idempotent.
func ApplyDisable(ctx context.Context, runner sysutil.CommandRunner, key WorkloadKey, mode string, optList ...DisableOptions) (DisableResult, error) {
	var opts DisableOptions
	if len(optList) > 0 {
		opts = optList[0]
	}
	if mode == "" {
		mode = ModeStop
	}
	switch mode {
	case ModeStop, ModePause, ModeForget:
	case ModeKeepRunning:
		if !opts.AllowKeepRunning {
			return DisableResult{}, opErr(CodeConfirmation, "keep_running requires an explicit flag")
		}
	default:
		return DisableResult{}, opErr(CodeInvalid, "unknown disable mode")
	}
	if err := key.Validate(); err != nil {
		return DisableResult{}, opErr(CodeInvalid, "%v", err)
	}
	unlock, ok := LockKey(key.String())
	if !ok {
		return DisableResult{}, opErr(CodeBusy, "another operation on %s is running", key)
	}
	defer unlock()
	w, err := resolve(ctx, runner, key, opts.CPGroup)
	if err != nil {
		var oe *OpError
		if errors.As(err, &oe) && oe.Code == CodeNotFound {
			return DisableResult{State: "absent"}, nil
		}
		return DisableResult{}, err
	}
	res := DisableResult{Changed: true}
	top := w.TopResource()
	timeout := opts.StopTimeout
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	interval := opts.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	stack := w.ResourceIDs
	if len(stack) == 0 {
		stack = []string{top}
	}
	switch mode {
	case ModePause:
		if w.State != WorkloadPaused {
			if out, err := runPCS(ctx, runner, 30*time.Second, "resource", "disable", top); err != nil {
				return res, opErr(CodePCSFailed, "pcs resource disable: %s", tailOf(out, err))
			}
		}
		res.State = WorkloadPaused
		return res, nil
	case ModeForget, ModeKeepRunning:
		active, err := stackActivity(ctx, runner, stack)
		if err != nil {
			res.Changed = false
			return res, opErr(CodeWorkloadActive, "cannot prove that %s is stopped (%v); nothing was changed", key, err)
		}
		if len(active) > 0 {
			res.Changed = false
			code, format := CodeWorkloadActive, "%s is still active (%s); stop it first (mode stop). Nothing was changed"
			if w.State == WorkloadPaused {
				code, format = CodeWorkloadPaused, "%s is paused but its storage is still active (%s); resume it and then stop it, or stop it directly (mode stop). Nothing was changed"
			}
			e := opErr(code, format, key, strings.Join(active, ", "))
			e.Leftovers = active
			return res, e
		}
	}
	// stop, and forget/keep_running of a stopped stack: disable everything
	// (so nothing restarts in between), prove it is stopped, then delete.
	if err := stopStack(ctx, runner, stack, timeout, interval); err != nil {
		res.Changed = false
		return res, err
	}
	cmds := [][]string{}
	for _, id := range stack {
		cmds = append(cmds, []string{"resource", "delete", id})
	}
	if err := applyCIB(ctx, runner, opts.TmpDir, cmds); err != nil {
		// The stack is stopped and disabled but still configured: a
		// consistent, safe state (paused); the operator can retry.
		return res, err
	}
	res.State = "absent"
	return res, nil
}

// MoveOptions tunes MoveWorkload.
type MoveOptions struct {
	Timeout      time.Duration
	PollInterval time.Duration
	CPGroup      string
}

// MoveWorkload moves a workload with `pcs resource move`, waits until it is
// Started on the target and always clears the temporary constraint.
func MoveWorkload(ctx context.Context, runner sysutil.CommandRunner, key WorkloadKey, node string, opts MoveOptions) (Workload, error) {
	if !PacemakerNodeRE.MatchString(node) {
		return Workload{}, opErr(CodeInvalid, "invalid node name")
	}
	unlock, ok := LockKey(key.String())
	if !ok {
		return Workload{}, opErr(CodeBusy, "another operation on %s is running", key)
	}
	defer unlock()
	w, err := resolve(ctx, runner, key, opts.CPGroup)
	if err != nil {
		return w, err
	}
	if !w.Managed {
		return w, opErr(CodeMaintenanceActive, "%s is unmanaged (maintenance)", key)
	}
	if !containsStr(w.AllowedNodes, node) {
		return w, opErr(CodeNodeNotAllowed, "node %s is not allowed for %s", node, key)
	}
	if w.State == WorkloadStarted && w.Node == node {
		return w, nil // already there: pcs would answer "Requested item already exists"
	}
	top := w.TopResource()
	if out, err := runPCS(ctx, runner, 60*time.Second, "resource", "move", top, node); err != nil {
		return w, opErr(CodePCSFailed, "pcs resource move: %s", tailOf(out, err))
	}
	timeout, interval := opts.Timeout, opts.PollInterval
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if interval <= 0 {
		interval = 2 * time.Second
	}
	last, reached, werr := waitWorkload(ctx, runner, key, opts.CPGroup, timeout, interval,
		func(w Workload) bool { return w.State == WorkloadStarted && w.Node == node })
	clearOut, clearErr := runPCS(ctx, runner, 30*time.Second, "resource", "clear", top)
	if werr != nil {
		return last, werr
	}
	if !reached {
		return last, opErr(CodeStartTimeout, "%s did not start on %s in %s", key, node, timeout)
	}
	if clearErr != nil {
		return last, opErr(CodePCSFailed, "pcs resource clear: %s", tailOf(clearOut, clearErr))
	}
	return last, nil
}

// ClearFailcount runs `pcs resource cleanup` on the workload.
func ClearFailcount(ctx context.Context, runner sysutil.CommandRunner, key WorkloadKey, cpGroup string) error {
	unlock, ok := LockKey(key.String())
	if !ok {
		return opErr(CodeBusy, "another operation on %s is running", key)
	}
	defer unlock()
	w, err := resolve(ctx, runner, key, cpGroup)
	if err != nil {
		return err
	}
	if out, err := runPCS(ctx, runner, 60*time.Second, "resource", "cleanup", w.TopResource()); err != nil {
		return opErr(CodePCSFailed, "pcs resource cleanup: %s", tailOf(out, err))
	}
	return nil
}

// ApplyPlacement replaces the webkvm-loc-* constraints of a workload with
// allowed_nodes (-INFINITY elsewhere) and an optional preferred node.
// Constraints not starting with "webkvm-" are never touched.
func ApplyPlacement(ctx context.Context, runner sysutil.CommandRunner, key WorkloadKey, allowed []string, preferred, cpGroup, tmpDir string) ([]string, error) {
	if err := validateCommon(allowed, preferred, nil, 0, 0); err != nil {
		return nil, opErr(CodeInvalid, "%v", err)
	}
	unlock, ok := LockKey(key.String())
	if !ok {
		return nil, opErr(CodeBusy, "another operation on %s is running", key)
	}
	defer unlock()
	cib, status, err := readCIBAndStatus(ctx, runner)
	if err != nil {
		return nil, opErr(CodePCSFailed, "%v", err)
	}
	list, _ := ParseWorkloads(cib, status, cpGroup)
	found := FindWorkload(list, key)
	if len(found) != 1 {
		if len(found) == 0 {
			return nil, opErr(CodeNotFound, "workload %s is not under Pacemaker", key)
		}
		return nil, opErr(CodeAmbiguous, "several resources claim %s", key)
	}
	top := found[0].TopResource()
	cluster, _ := clusterNodesOf(cib)
	for _, n := range allowed {
		if !containsStr(cluster, n) {
			return nil, opErr(CodeInvalid, "node %s is not a cluster member", n)
		}
	}
	cmds := [][]string{}
	var root xmlElement
	if xmlErr := unmarshalXML(cib, &root); xmlErr == nil {
		for _, loc := range cibLocations(root) {
			if loc.Rsc == top && strings.HasPrefix(loc.ID, "webkvm-loc-") {
				cmds = append(cmds, []string{"constraint", "remove", loc.ID})
			}
		}
	}
	add, _ := locationCommands(top, cluster, allowed, preferred)
	cmds = append(cmds, add...)
	if len(cmds) == 0 {
		return nil, nil
	}
	if err := applyCIB(ctx, runner, tmpDir, cmds); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, "pcs "+strings.Join(c, " "))
	}
	sort.Strings(out)
	return out, nil
}

func unmarshalXML(data []byte, root *xmlElement) error { return xml.Unmarshal(data, root) }

// clusterNodesOf lists the node names of a CIB.
func clusterNodesOf(cib []byte) ([]string, error) {
	var root xmlElement
	if err := xml.Unmarshal(cib, &root); err != nil {
		return nil, err
	}
	return cibNodeNames(root), nil
}
