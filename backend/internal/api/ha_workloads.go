package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/auth"
	"webkvm/internal/compute"
	"webkvm/internal/ha"
	"webkvm/internal/sysutil"
)

// kvmVMFacts are the facts about a libvirt VM that eligibility needs.
type kvmVMFacts struct {
	Exists    bool
	DiskPaths []string
	XMLPath   string
	Running   bool
	Bridges   []string
}

// workloadFacts gives the handler the VM facts it cannot read from
// Pacemaker. The implementation (against compute.Backend / compute/incus) is
// wired by a later wave through newWorkloadFacts.
type workloadFacts interface {
	KVM(ctx context.Context, name string) (kvmVMFacts, error)
	// Incus returns an inspector for the instance and the names of every
	// member of the Incus cluster.
	Incus(ctx context.Context, project, name string) (ha.IncusInspector, []string, error)
	// NodeBridges maps each cluster node to the bridges it has (best effort).
	NodeBridges(ctx context.Context) (map[string][]string, error)
}

// newWorkloadFacts is set by the wiring wave. While nil, enabling HA answers
// 503 facts_unavailable instead of guessing.
var newWorkloadFacts func(h *Handler) workloadFacts

// workloadTuning lets tests shorten the waits.
var workloadTuning = struct {
	StartTimeout, StopTimeout, MoveTimeout, Poll time.Duration
}{StartTimeout: 5 * time.Minute, StopTimeout: 3 * time.Minute, MoveTimeout: 5 * time.Minute, Poll: 2 * time.Second}

type apiErr struct {
	Status    int
	Code      string
	Msg       string
	Blockers  []string
	Reasons   []ha.Reason
	Leftovers []string
	// Details are extra typed fields (never overriding the ones above).
	Details map[string]any
}

func (e *apiErr) body() map[string]any {
	m := map[string]any{}
	for k, v := range e.Details {
		m[k] = v
	}
	m["error"], m["code"] = e.Msg, e.Code
	if len(e.Blockers) > 0 {
		m["blockers"] = e.Blockers
	}
	if len(e.Reasons) > 0 {
		m["reasons"] = e.Reasons
	}
	if len(e.Leftovers) > 0 {
		m["leftovers"] = e.Leftovers
	}
	return m
}

func writeAPIErr(w http.ResponseWriter, e *apiErr) { jsonResp(w, e.Status, e.body()) }

// opToAPI maps a typed ha operation error to an HTTP error.
func opToAPI(err error) *apiErr {
	var oe *ha.OpError
	if !errors.As(err, &oe) {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return &apiErr{Status: http.StatusGatewayTimeout, Code: "start_timeout", Msg: err.Error()}
		}
		return &apiErr{Status: http.StatusBadGateway, Code: "pcs_failed", Msg: err.Error()}
	}
	e := &apiErr{Code: oe.Code, Msg: oe.Msg, Leftovers: oe.Leftovers, Details: oe.Details}
	switch oe.Code {
	case ha.CodeInvalid:
		e.Status = http.StatusBadRequest
	case ha.CodeNotFound, ha.CodeReleaseNotFound:
		e.Status = http.StatusNotFound
	case ha.CodePCSFailed, ha.CodeDRBDUpFailed, ha.CodeDRBDPromoteFailed, ha.CodeMountFailed, ha.CodeDefineFailed, ha.CodeStartFailed,
		ha.CodeMountNotVisible, ha.CodeXMLLinkFailed:
		e.Status = http.StatusBadGateway
	case ha.CodeReleaseStoreFailed:
		e.Status = http.StatusInternalServerError
	case ha.CodeStartTimeout, ha.CodeStopTimeout:
		e.Status = http.StatusGatewayTimeout
	case ha.CodeRollbackFailed:
		e.Status = http.StatusInternalServerError
	default: // busy, ambiguous, workload_active, maintenance_active, confirmation_required, id_collision, node_not_allowed
		e.Status = http.StatusConflict
	}
	return e
}

func (h *Handler) auditWorkload(r *http.Request, action string, key ha.WorkloadKey, detail map[string]any) {
	if h.audit == nil {
		return
	}
	if detail == nil {
		detail = map[string]any{}
	}
	detail["key"] = key.String()
	h.audit.Log(auditFor(r, action, "ha", detail))
}

// mountHAWorkloads registers the per-workload HA endpoints. The caller mounts
// it under /api/ha behind requireClusterHA; every route is admin-only.
func (h *Handler) mountHAWorkloads(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		h.registerHAWorkloadRoutes(r)
	})
}

// registerHAWorkloadRoutes holds the route table without the role guard so
// tests can exercise the handlers directly.
func (h *Handler) registerHAWorkloadRoutes(r chi.Router) {
	// Any write may change who owns an instance: drop the cached ownership
	// view so the VM routes see it at once instead of after the probe TTL.
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req)
			if req.Method != http.MethodGet {
				invalidateHAProbe()
			}
		})
	})
	{
		r.Get("/workloads", h.ListHAWorkloads)
		r.Post("/workloads/bulk", h.BulkHAWorkloads)
		r.Get("/workloads/{kind}/{name}", h.GetHAWorkload)
		r.Post("/workloads/{kind}/{name}/enable", h.EnableHAWorkload)
		r.Delete("/workloads/{kind}/{name}", h.DisableHAWorkload)
		r.Put("/workloads/{kind}/{name}/placement", h.PlaceHAWorkload)
		r.Post("/workloads/{kind}/{name}/move", h.MoveHAWorkload)
		r.Post("/workloads/{kind}/{name}/clear-failcount", h.ClearHAWorkloadFailcount)
		r.Post("/workloads/{kind}/{name}/maintenance", h.MaintenanceHAWorkload)
		r.Get("/released", h.ListReleasedVMs)
		r.Post("/released/{name}/restore", h.RestoreReleasedVM)
		r.Delete("/released/{name}", h.DiscardReleasedVM)
	}
}

type haPreconditions struct {
	OK       bool     `json:"ok"`
	Blockers []string `json:"blockers"`
}

type haWorkloadSummary struct {
	Managed    int `json:"managed"`
	Eligible   int `json:"eligible"`
	Ineligible int `json:"ineligible"`
	Failed     int `json:"failed"`
}

// preconditions evaluates the cluster-wide requirements for enabling HA.
// pacemaker is returned so callers can reuse it.
func (h *Handler) preconditions(ctx context.Context) (haPreconditions, ha.PacemakerStatus) {
	blockers := []string{}
	st := ha.ProbePacemaker(ctx, sysutil.DefaultRunner)
	if st.State != ha.StateObserved && st.State != ha.StateBlocked {
		return haPreconditions{Blockers: []string{"pacemaker_unavailable"}}, st
	}
	if !h.fencingRecorded(st) {
		blockers = append(blockers, "fencing_test_required")
	}
	if !(st.WatchdogFencing != nil && *st.WatchdogFencing) && st.FenceDevices == 0 {
		blockers = append(blockers, "sbd_not_active")
	}
	if st.Quorum == nil || !*st.Quorum {
		blockers = append(blockers, "quorum_missing")
	}
	active := 0
	for _, n := range st.NodeStates {
		if n.Online && !n.Standby {
			active++
		}
	}
	if active < 2 {
		blockers = append(blockers, "not_enough_nodes")
	}
	if threshold, ok := incusHealingProbe(ctx); ok && threshold > 0 {
		blockers = append(blockers, "incus_healing_enabled")
	}
	checks, err := ha.ControlPlaneChecks(ctx, sysutil.DefaultRunner, h.cpGroup(), h.dataDir())
	if err != nil || len(checks) > 0 {
		blockers = append(blockers, "control_plane_unhealthy")
	}
	return haPreconditions{OK: len(blockers) == 0, Blockers: blockers}, st
}

// ListHAWorkloads GET /workloads.
func (h *Handler) ListHAWorkloads(w http.ResponseWriter, r *http.Request) {
	pre, _ := h.preconditions(r.Context())
	list, err := ha.ManagedWorkloads(r.Context(), sysutil.DefaultRunner, h.cpGroup())
	if err != nil {
		list = []ha.Workload{}
		if !contains(pre.Blockers, "pacemaker_unavailable") {
			pre.Blockers = append(pre.Blockers, "pacemaker_unavailable")
			pre.OK = false
		}
	}
	sum := haWorkloadSummary{}
	for _, wl := range list {
		sum.Managed++
		if wl.Eligibility.Eligible {
			sum.Eligible++
		} else {
			sum.Ineligible++
		}
		if wl.State == ha.WorkloadFailed {
			sum.Failed++
		}
	}
	jsonResp(w, http.StatusOK, map[string]any{"preconditions": pre, "summary": sum, "workloads": list})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// keyFromRequest validates {kind}/{name}?project= into a WorkloadKey.
func keyFromRequest(r *http.Request) (ha.WorkloadKey, *apiErr) {
	key := ha.WorkloadKey{Kind: chi.URLParam(r, "kind"), Name: chi.URLParam(r, "name"), Project: r.URL.Query().Get("project")}
	return key, validKey(key)
}

func validKey(key ha.WorkloadKey) *apiErr {
	if err := key.Validate(); err != nil {
		return &apiErr{Status: http.StatusBadRequest, Code: "invalid_body", Msg: err.Error()}
	}
	return nil
}

// currentWorkload looks the key up in Pacemaker. found is false (with a nil
// error) when it is not managed.
func (h *Handler) currentWorkload(ctx context.Context, key ha.WorkloadKey) (wl ha.Workload, found bool, e *apiErr) {
	list, err := ha.ManagedWorkloads(ctx, sysutil.DefaultRunner, h.cpGroup())
	if err != nil {
		return wl, false, &apiErr{Status: http.StatusBadGateway, Code: "pcs_failed", Msg: err.Error()}
	}
	m := ha.FindWorkload(list, key)
	switch len(m) {
	case 0:
		return wl, false, nil
	case 1:
		return m[0], true, nil
	}
	return wl, false, &apiErr{Status: http.StatusConflict, Code: ha.CodeAmbiguous, Msg: "several resources claim " + key.String()}
}

// GetHAWorkload GET /workloads/{kind}/{name}. For a VM not yet under HA it
// returns managed:false with its eligibility.
func (h *Handler) GetHAWorkload(w http.ResponseWriter, r *http.Request) {
	key, bad := keyFromRequest(r)
	if bad != nil {
		writeAPIErr(w, bad)
		return
	}
	wl, found, e := h.currentWorkload(r.Context(), key)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	if found {
		jsonResp(w, http.StatusOK, wl)
		return
	}
	elig, ev, ferr := h.evaluate(r.Context(), key)
	if ferr != nil {
		if ferr.Code == "workload_not_found" || ferr.Status == http.StatusServiceUnavailable {
			writeAPIErr(w, ferr)
			return
		}
	}
	state := ev.state
	if state == "" {
		state = ha.WorkloadStopped
	}
	jsonResp(w, http.StatusOK, ha.Workload{
		Key: key.String(), Kind: key.Kind, Name: key.Name, Project: key.Project, State: state, Node: ev.node,
		ResourceIDs: []string{}, Failures: []ha.WorkloadFailure{}, AllowedNodes: []string{}, Eligibility: elig,
	})
}

type evaluated struct {
	kvmSpec   ha.KVMSpec
	nodeNames []string
	// state/node are the live facts of an instance (started|stopped and the
	// member it runs on); empty when the backend cannot tell.
	state, node string
}

// instanceStateInspector is optionally implemented by an IncusInspector that
// knows the live status and cluster member of the instance.
type instanceStateInspector interface {
	InstanceState(inst string) (status, location string)
}

// factsErr maps a facts failure to the documented API error.
func factsErr(err error, what string) *apiErr {
	switch {
	case errors.Is(err, errNotOnThisNode):
		return &apiErr{Status: http.StatusConflict, Code: "vm_not_on_this_node", Msg: "only VMs defined on this node can be put under HA in v1"}
	case errors.Is(err, compute.ErrNotFound):
		return &apiErr{Status: http.StatusNotFound, Code: ha.CodeNotFound, Msg: what + " not found"}
	case errors.Is(err, compute.ErrNotImplemented):
		return &apiErr{Status: http.StatusServiceUnavailable, Code: "facts_unavailable", Msg: "this hypervisor backend is not available on this node"}
	}
	return &apiErr{Status: http.StatusBadGateway, Code: "facts_failed", Msg: "could not read the " + what + " facts"}
}

// evaluate gathers the facts and computes eligibility. The returned
// evaluated carries what the plan needs.
func (h *Handler) evaluate(ctx context.Context, key ha.WorkloadKey) (ha.Eligibility, evaluated, *apiErr) {
	var ev evaluated
	if newWorkloadFacts == nil {
		return ha.Eligibility{Reasons: []ha.Reason{}}, ev, &apiErr{Status: http.StatusServiceUnavailable, Code: "facts_unavailable", Msg: "workload facts are not wired on this node"}
	}
	if key.Kind == ha.KindKVM && h.isRemote() {
		return ha.Eligibility{Reasons: []ha.Reason{}}, ev, factsErr(errNotOnThisNode, "VM")
	}
	facts := newWorkloadFacts(h)
	st := ha.ProbePacemaker(ctx, sysutil.DefaultRunner)
	ev.nodeNames = st.NodeNames
	existing, _ := ha.ManagedWorkloads(ctx, sysutil.DefaultRunner, h.cpGroup())
	if key.Kind == ha.KindIncus {
		insp, members, err := facts.Incus(ctx, key.Project, key.Name)
		if err != nil || insp == nil {
			if err == nil || errors.Is(err, compute.ErrNotFound) {
				return ha.Eligibility{Reasons: []ha.Reason{}}, ev, &apiErr{Status: http.StatusNotFound, Code: ha.CodeNotFound, Msg: "Incus instance not found"}
			}
			return ha.Eligibility{Reasons: []ha.Reason{}}, ev, factsErr(err, "Incus instance")
		}
		if si, ok := insp.(instanceStateInspector); ok {
			status, loc := si.InstanceState(key.Name)
			ev.state, ev.node = liveWorkloadState(strings.EqualFold(status, "running")), loc
		}
		return ha.EvalIncus(insp, ha.IncusInputs{Name: key.Name, Project: key.Project, Members: members, Existing: existing}), ev, nil
	}
	vm, err := facts.KVM(ctx, key.Name)
	if err != nil {
		return ha.Eligibility{Reasons: []ha.Reason{}}, ev, factsErr(err, "VM")
	}
	if !vm.Exists {
		return ha.Eligibility{Reasons: []ha.Reason{}}, ev, &apiErr{Status: http.StatusNotFound, Code: ha.CodeNotFound, Msg: "VM not found"}
	}
	ev.state = liveWorkloadState(vm.Running)
	if vm.Running {
		ev.node = localNodeName()
	}
	bridges, _ := facts.NodeBridges(ctx)
	// Only nodes whose bridges are known can be checked; unknown peers are
	// not reported as missing a bridge.
	checked := []string{}
	for _, n := range st.NodeNames {
		if _, ok := bridges[n]; ok {
			checked = append(checked, n)
		}
	}
	elig, kf := ha.EvalKVM(ctx, sysutil.DefaultRunner, ha.KVMInputs{
		Name: key.Name, DiskPaths: vm.DiskPaths, XMLPath: vm.XMLPath, Running: vm.Running,
		ClusterNodes: checked, Bridges: vm.Bridges, NodeBridges: bridges, Existing: existing,
	})
	ev.kvmSpec = ha.KVMSpec{Name: key.Name, DRBDResource: kf.DRBDResource, Device: kf.Device, MountDir: kf.MountDir,
		ConfigPath: vm.XMLPath, FSType: kf.FSType, ClusterNodes: st.NodeNames}
	return elig, ev, nil
}

func liveWorkloadState(running bool) string {
	if running {
		return ha.WorkloadStarted
	}
	return ha.WorkloadStopped
}

type enableRequest struct {
	DryRun             bool     `json:"dry_run"`
	Start              *bool    `json:"start"`
	AllowedNodes       []string `json:"allowed_nodes"`
	PreferredNode      string   `json:"preferred_node"`
	Stickiness         int      `json:"stickiness"`
	MigrationThreshold int      `json:"migration_threshold"`
}

type enableResponse struct {
	Changed     bool     `json:"changed"`
	State       string   `json:"state"`
	ResourceIDs []string `json:"resource_ids"`
	Commands    []string `json:"commands"`
}

// reasonBlockers maps eligibility reason codes to precondition blockers.
func reasonBlockers(reasons []ha.Reason) []string {
	out := []string{"not_eligible"}
	for _, r := range reasons {
		switch r.Code {
		case ha.ReasonDRBDNotUpToDate:
			out = append(out, "drbd_not_uptodate")
		case ha.ReasonMemberOffline:
			out = append(out, "incus_member_not_online")
		}
	}
	seen := map[string]bool{}
	uniq := out[:0]
	for _, b := range out {
		if !seen[b] {
			seen[b] = true
			uniq = append(uniq, b)
		}
	}
	return uniq
}

func validateNodes(nodes []string, preferred string, cluster []string) *apiErr {
	all := append(append([]string{}, nodes...), preferred)
	for _, n := range all {
		if n == "" {
			continue
		}
		if !ha.PacemakerNodeRE.MatchString(n) || (len(cluster) > 0 && !contains(cluster, n)) {
			return &apiErr{Status: http.StatusBadRequest, Code: "invalid_node_name", Msg: "unknown or invalid node name"}
		}
	}
	return nil
}

func (h *Handler) enableWorkload(r *http.Request, key ha.WorkloadKey, req enableRequest) (enableResponse, *apiErr) {
	ctx := r.Context()
	if bad := validKey(key); bad != nil {
		return enableResponse{}, bad
	}
	if wl, found, e := h.currentWorkload(ctx, key); e != nil {
		return enableResponse{}, e
	} else if found {
		if !wl.Managed && wl.MaintUntil != "" {
			h.auditWorkload(r, "ha_workload.maintenance.refused", key, map[string]any{"op": "enable"})
			return enableResponse{}, &apiErr{Status: http.StatusConflict, Code: ha.CodeMaintenanceActive, Msg: "workload is in maintenance"}
		}
		// A paused workload (disable mode=pause, or enable with start=false) is
		// resumed by enabling it again; anything else is already enabled.
		if wl.State == ha.WorkloadPaused && !req.DryRun && (req.Start == nil || *req.Start) {
			pre, _ := h.preconditions(ctx)
			if len(pre.Blockers) > 0 {
				h.auditWorkload(r, "ha_workload.resume", key, map[string]any{"result": "precondition_failed", "blockers": pre.Blockers})
				return enableResponse{}, &apiErr{Status: http.StatusConflict, Code: "precondition_failed", Msg: "HA cannot be resumed yet", Blockers: pre.Blockers}
			}
			res, err := ha.ApplyResume(ctx, sysutil.DefaultRunner, key, ha.ResumeOptions{
				CPGroup: h.cpGroup(), StartTimeout: workloadTuning.StartTimeout, PollInterval: workloadTuning.Poll,
			})
			if err != nil {
				h.auditWorkload(r, "ha_workload.resume", key, map[string]any{"result": "error", "error": err.Error()})
				return enableResponse{}, opToAPI(err)
			}
			h.auditWorkload(r, "ha_workload.resume", key, map[string]any{"result": "ok", "changed": res.Changed})
			return enableResponse{Changed: res.Changed, State: res.State, ResourceIDs: res.ResourceIDs, Commands: []string{"pcs resource enable " + wl.TopResource()}}, nil
		}
		return enableResponse{Changed: false, State: wl.State, ResourceIDs: wl.ResourceIDs, Commands: []string{}}, nil
	}
	pre, st := h.preconditions(ctx)
	blockers := append([]string{}, pre.Blockers...)
	elig, ev, ferr := h.evaluate(ctx, key)
	if ferr != nil {
		return enableResponse{}, ferr
	}
	// boot.autostart=true is not a blocker: the enable plan sets it to false
	// before the CIB push, so only the other reasons stop the request.
	var reasons []ha.Reason
	for _, rs := range elig.Reasons {
		if rs.Code != ha.ReasonBootAutostart {
			reasons = append(reasons, rs)
		}
	}
	if len(reasons) > 0 {
		blockers = append(blockers, reasonBlockers(reasons)...)
	}
	if len(blockers) > 0 {
		h.auditWorkload(r, "ha_workload.enable", key, map[string]any{"result": "precondition_failed", "blockers": blockers})
		return enableResponse{}, &apiErr{Status: http.StatusConflict, Code: "precondition_failed", Msg: "HA cannot be enabled yet", Blockers: blockers, Reasons: reasons}
	}
	if bad := validateNodes(req.AllowedNodes, req.PreferredNode, st.NodeNames); bad != nil {
		return enableResponse{}, bad
	}
	start := req.Start == nil || *req.Start
	var plan ha.Plan
	var perr error
	if key.Kind == ha.KindIncus {
		plan, perr = ha.PlanEnableIncus(ha.IncusSpec{Name: key.Name, Project: key.Project, ClusterNodes: st.NodeNames,
			AllowedNodes: req.AllowedNodes, PreferredNode: req.PreferredNode, Stickiness: req.Stickiness, MigrationThreshold: req.MigrationThreshold}, start)
	} else {
		spec := ev.kvmSpec
		spec.AllowedNodes, spec.PreferredNode, spec.Stickiness, spec.MigrationThreshold = req.AllowedNodes, req.PreferredNode, req.Stickiness, req.MigrationThreshold
		plan, perr = ha.PlanEnableKVM(spec, start)
	}
	if perr != nil {
		return enableResponse{}, opToAPI(perr)
	}
	res, err := ha.ApplyEnable(ctx, sysutil.DefaultRunner, plan, ha.EnableOptions{
		Start: start, DryRun: req.DryRun, RollbackOnStartFailure: true, CPGroup: h.cpGroup(),
		StartTimeout: workloadTuning.StartTimeout, PollInterval: workloadTuning.Poll,
	})
	if err != nil {
		h.auditWorkload(r, "ha_workload.enable", key, map[string]any{"result": "error", "error": err.Error(), "commands": res.Commands})
		return enableResponse{}, opToAPI(err)
	}
	if !req.DryRun {
		h.auditWorkload(r, "ha_workload.enable", key, map[string]any{"result": "ok", "changed": res.Changed, "commands": res.Commands})
	}
	return enableResponse{Changed: res.Changed, State: res.State, ResourceIDs: res.ResourceIDs, Commands: res.Commands}, nil
}

// EnableHAWorkload POST /workloads/{kind}/{name}/enable.
func (h *Handler) EnableHAWorkload(w http.ResponseWriter, r *http.Request) {
	key, bad := keyFromRequest(r)
	if bad != nil {
		writeAPIErr(w, bad)
		return
	}
	var req enableRequest
	if r.ContentLength != 0 {
		if err := decodeBody(r, &req); err != nil {
			jsonErrCode(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
			return
		}
	}
	res, e := h.enableWorkload(r, key, req)
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	jsonResp(w, http.StatusOK, res)
}

type disableResponse struct {
	Changed  bool               `json:"changed"`
	State    string             `json:"state"`
	Released *ha.ReleasedVMView `json:"released,omitempty"`
}

// releasesStack reports whether a disable mode removes the Pacemaker stack
// (and so the transient KVM domain and its storage).
func releasesStack(mode string) bool {
	return mode == ha.ModeStop || mode == ha.ModeForget || mode == ha.ModeKeepRunning
}

func (h *Handler) disableWorkload(r *http.Request, key ha.WorkloadKey, mode, confirm string, allowKeep, acceptUnrestorable bool) (disableResponse, *apiErr) {
	if bad := validKey(key); bad != nil {
		return disableResponse{}, bad
	}
	if mode == "" {
		mode = ha.ModeStop
	}
	if mode != ha.ModeStop && mode != ha.ModePause && mode != ha.ModeForget && mode != ha.ModeKeepRunning {
		return disableResponse{}, &apiErr{Status: http.StatusBadRequest, Code: "invalid_body", Msg: "mode must be stop, pause or forget"}
	}
	if (mode == ha.ModeForget || mode == ha.ModeKeepRunning) && confirm != key.String() {
		return disableResponse{}, &apiErr{Status: http.StatusConflict, Code: ha.CodeConfirmation, Msg: "type the workload key as confirmation"}
	}
	wl, found, e := h.currentWorkload(r.Context(), key)
	if e != nil {
		return disableResponse{}, e
	}
	if found && !wl.Managed && wl.MaintUntil != "" {
		h.auditWorkload(r, "ha_workload.maintenance.refused", key, map[string]any{"op": "disable"})
		return disableResponse{}, &apiErr{Status: http.StatusConflict, Code: ha.CodeMaintenanceActive, Msg: "workload is in maintenance"}
	}
	// KVM: save what a Restore needs BEFORE the stack is touched (8.20).
	var capture *releaseCapture
	if found && key.Kind == ha.KindKVM && releasesStack(mode) {
		if mode == ha.ModeKeepRunning && !allowKeep {
			return disableResponse{}, &apiErr{Status: http.StatusConflict, Code: ha.CodeConfirmation, Msg: "keep_running requires an explicit flag"}
		}
		c, ce := h.captureRelease(r, key, wl, mode, acceptUnrestorable)
		if ce != nil {
			return disableResponse{}, ce
		}
		capture = c
	}
	res, err := ha.ApplyDisable(r.Context(), sysutil.DefaultRunner, key, mode, ha.DisableOptions{
		AllowKeepRunning: allowKeep, StopTimeout: workloadTuning.StopTimeout, PollInterval: workloadTuning.Poll, CPGroup: h.cpGroup(),
	})
	if err != nil {
		if !res.Changed {
			h.undoCapture(key, capture)
		}
		h.auditWorkload(r, "ha_workload.disable", key, map[string]any{"mode": mode, "result": "error", "error": err.Error()})
		return disableResponse{}, opToAPI(err)
	}
	h.auditWorkload(r, "ha_workload.disable", key, map[string]any{"mode": mode, "result": "ok", "changed": res.Changed})
	out := disableResponse{Changed: res.Changed, State: res.State}
	if capture != nil {
		v := capture.entry.View()
		out.Released = &v
	}
	return out, nil
}

// DisableHAWorkload DELETE /workloads/{kind}/{name}?mode=&confirm=&allow_keep_running=&accept_unrestorable=.
func (h *Handler) DisableHAWorkload(w http.ResponseWriter, r *http.Request) {
	key, bad := keyFromRequest(r)
	if bad != nil {
		writeAPIErr(w, bad)
		return
	}
	q := r.URL.Query()
	res, e := h.disableWorkload(r, key, q.Get("mode"), q.Get("confirm"), q.Get("allow_keep_running") == "true", q.Get("accept_unrestorable") == "true")
	if e != nil {
		writeAPIErr(w, e)
		return
	}
	jsonResp(w, http.StatusOK, res)
}

// PlaceHAWorkload PUT /workloads/{kind}/{name}/placement.
func (h *Handler) PlaceHAWorkload(w http.ResponseWriter, r *http.Request) {
	key, bad := keyFromRequest(r)
	if bad != nil {
		writeAPIErr(w, bad)
		return
	}
	var req struct {
		AllowedNodes  []string `json:"allowed_nodes"`
		PreferredNode string   `json:"preferred_node"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErrCode(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	st := ha.ProbePacemaker(r.Context(), sysutil.DefaultRunner)
	if bad := validateNodes(req.AllowedNodes, req.PreferredNode, st.NodeNames); bad != nil {
		writeAPIErr(w, bad)
		return
	}
	if wl, found, e := h.currentWorkload(r.Context(), key); e != nil {
		writeAPIErr(w, e)
		return
	} else if found && !wl.Managed && wl.MaintUntil != "" {
		h.auditWorkload(r, "ha_workload.maintenance.refused", key, map[string]any{"op": "placement"})
		writeAPIErr(w, &apiErr{Status: http.StatusConflict, Code: ha.CodeMaintenanceActive, Msg: "workload is in maintenance"})
		return
	}
	cmds, err := ha.ApplyPlacement(r.Context(), sysutil.DefaultRunner, key, req.AllowedNodes, req.PreferredNode, h.cpGroup(), "")
	if err != nil {
		h.auditWorkload(r, "ha_workload.placement", key, map[string]any{"result": "error", "error": err.Error()})
		writeAPIErr(w, opToAPI(err))
		return
	}
	h.auditWorkload(r, "ha_workload.placement", key, map[string]any{"result": "ok", "commands": cmds, "allowed_nodes": req.AllowedNodes, "preferred_node": req.PreferredNode})
	if cmds == nil {
		cmds = []string{}
	}
	jsonResp(w, http.StatusOK, map[string]any{"changed": len(cmds) > 0, "commands": cmds})
}

// MoveHAWorkload POST /workloads/{kind}/{name}/move.
func (h *Handler) MoveHAWorkload(w http.ResponseWriter, r *http.Request) {
	key, bad := keyFromRequest(r)
	if bad != nil {
		writeAPIErr(w, bad)
		return
	}
	var req struct {
		Node string `json:"node"`
	}
	if err := decodeBody(r, &req); err != nil || !ha.PacemakerNodeRE.MatchString(req.Node) {
		jsonErrCode(w, http.StatusBadRequest, "invalid_node_name", "node must be a Pacemaker node name")
		return
	}
	wl, err := ha.MoveWorkload(r.Context(), sysutil.DefaultRunner, key, req.Node, ha.MoveOptions{
		Timeout: workloadTuning.MoveTimeout, PollInterval: workloadTuning.Poll, CPGroup: h.cpGroup(),
	})
	if err != nil {
		e := opToAPI(err)
		if e.Code == ha.CodeMaintenanceActive {
			h.auditWorkload(r, "ha_workload.maintenance.refused", key, map[string]any{"op": "move"})
		}
		h.auditWorkload(r, "ha_workload.move", key, map[string]any{"node": req.Node, "result": "error", "code": e.Code})
		writeAPIErr(w, e)
		return
	}
	h.auditWorkload(r, "ha_workload.move", key, map[string]any{"node": req.Node, "result": "ok"})
	jsonResp(w, http.StatusOK, wl)
}

// ClearHAWorkloadFailcount POST /workloads/{kind}/{name}/clear-failcount.
func (h *Handler) ClearHAWorkloadFailcount(w http.ResponseWriter, r *http.Request) {
	key, bad := keyFromRequest(r)
	if bad != nil {
		writeAPIErr(w, bad)
		return
	}
	if err := ha.ClearFailcount(r.Context(), sysutil.DefaultRunner, key, h.cpGroup()); err != nil {
		writeAPIErr(w, opToAPI(err))
		return
	}
	h.auditWorkload(r, "ha_workload.clear_failcount", key, map[string]any{"result": "ok"})
	jsonResp(w, http.StatusOK, map[string]any{"ok": true})
}

type bulkItem struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Project string `json:"project"`
}

type bulkRequest struct {
	Action string     `json:"action"`
	Items  []bulkItem `json:"items"`
	DryRun bool       `json:"dry_run"`
	Mode   string     `json:"mode"`
}

type bulkResult struct {
	Key     string `json:"key"`
	OK      bool   `json:"ok"`
	Changed bool   `json:"changed"`
	Code    string `json:"code,omitempty"`
	Error   string `json:"error,omitempty"`
}

const maxBulkItems = 50

// BulkHAWorkloads POST /workloads/bulk. Items run sequentially; one failure
// never stops the rest.
func (h *Handler) BulkHAWorkloads(w http.ResponseWriter, r *http.Request) {
	var req bulkRequest
	if err := decodeBody(r, &req); err != nil || (req.Action != "enable" && req.Action != "disable") ||
		len(req.Items) == 0 || len(req.Items) > maxBulkItems {
		jsonErrCode(w, http.StatusBadRequest, "invalid_body", "action must be enable|disable with 1 to 50 items")
		return
	}
	if req.Action == "disable" && (req.Mode == ha.ModeForget || req.Mode == ha.ModeKeepRunning) {
		jsonErrCode(w, http.StatusConflict, ha.CodeConfirmation, "forget and keep_running are not available in bulk")
		return
	}
	results := make([]bulkResult, 0, len(req.Items))
	for _, it := range req.Items {
		key := ha.WorkloadKey{Kind: it.Kind, Name: it.Name, Project: it.Project}
		res := bulkResult{Key: key.String()}
		var e *apiErr
		if req.Action == "enable" {
			var out enableResponse
			out, e = h.enableWorkload(r, key, enableRequest{DryRun: req.DryRun})
			res.Changed = out.Changed
		} else {
			var out disableResponse
			out, e = h.disableWorkload(r, key, req.Mode, "", false, false)
			res.Changed = out.Changed
		}
		if e != nil {
			res.Code, res.Error = e.Code, e.Msg
		} else {
			res.OK = true
		}
		results = append(results, res)
	}
	jsonResp(w, http.StatusOK, map[string]any{"results": results})
}
