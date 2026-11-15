package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/ha"
	"webkvm/internal/hainstall"
	"webkvm/internal/sysutil"
)

// haClusterState is the lazily created per-process state of the cluster
// wizard: the single-flight setup job and the fencing-test mutex.
type haClusterState struct {
	once   sync.Once
	setup  *ha.SetupManager
	fence  ha.FenceTester
	opLock sync.Mutex // serialises standby/unstandby
}

func (h *Handler) clusterSetup() *ha.SetupManager {
	hs := h.haState()
	hs.once.Do(func() { hs.setup = ha.NewSetupManager(h.dataDir()) })
	return hs.setup
}

// haPackagesReady is true when the packages are installed (or partial with
// nothing missing and no unit that still needs the operator).
func (h *Handler) haPackagesReady(ctx context.Context) bool {
	if h.haInstall == nil {
		return false
	}
	s := h.haInstall.Reconcile(ctx)
	switch s.State {
	case hainstall.StateInstalled:
		return true
	case hainstall.StatePartial:
		return len(s.Missing) == 0 && len(s.UnitsActive) == 0
	}
	return false
}

func (h *Handler) requireHAPackages(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		if !h.haPackagesReady(ctx) {
			jsonErrCode(w, http.StatusConflict, "module_not_installed",
				"the Cluster and HA packages are not installed on this node; install them from Nodes")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clusterEnvTweak lets tests redirect host paths to a temp tree.
var clusterEnvTweak func(ha.ClusterEnv) ha.ClusterEnv

func (h *Handler) clusterEnv() ha.ClusterEnv {
	env := ha.DefaultClusterEnv(sysutil.DefaultRunner)

	if h.settings != nil {
		env.ClusterCIDR = strings.TrimSpace(h.settings.GetString("ha.cluster_cidr"))
	}
	if clusterEnvTweak != nil {
		env = clusterEnvTweak(env)
	}
	return env
}

func (h *Handler) auditHA(r *http.Request, action, resource string, detail map[string]any) {
	if h.audit != nil {
		h.audit.Log(auditFor(r, action, resource, detail))
	}
}

func nodeNames(req ha.ClusterRequest) []string {
	out := make([]string, 0, len(req.Nodes))
	for _, n := range req.Nodes {
		out = append(out, n.Name)
	}
	return out
}

// PlanHACluster validates a cluster layout and probes this node. Read-only.
func (h *Handler) PlanHACluster(w http.ResponseWriter, r *http.Request) {
	var req ha.ClusterRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErrCode(w, http.StatusBadRequest, "invalid_body", "the request body is not valid JSON")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	defer cancel()
	jsonResp(w, http.StatusOK, h.clusterEnv().Plan(ctx, req))
}

// SetupHACluster starts the formation job. Refuses (409) unless the plan is
// clean; cluster_exists is reported with its own code.
func (h *Handler) SetupHACluster(w http.ResponseWriter, r *http.Request) {
	var req ha.ClusterRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErrCode(w, http.StatusBadRequest, "invalid_body", "the request body is not valid JSON")
		return
	}
	if !req.Confirm {
		jsonErrCode(w, http.StatusBadRequest, "confirmation_required", "forming a cluster requires confirm=true")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
	plan := h.clusterEnv().Plan(ctx, req)
	cancel()
	if !plan.OK {
		code := "plan_blocked"
		for _, b := range plan.Blockers {
			if b == ha.CodeClusterExists {
				code = ha.CodeClusterExists
			}
		}
		jsonResp(w, http.StatusConflict, map[string]any{
			"error": "the plan has blockers; nothing was changed", "code": code, "blockers": plan.Blockers,
		})
		return
	}
	actor, role, ip := audit.FromRequest(r)
	detail := map[string]any{"cluster_name": req.ClusterName, "nodes": nodeNames(req), "sbd_enable": req.SBDEnabled(), "enable_on_boot": req.EnableOnBoot}
	mgr := h.clusterSetup()
	err := mgr.Start(h.clusterEnv(), req, func(final ha.SetupStatus) {
		if h.audit == nil {
			return
		}
		d := map[string]any{"cluster_name": req.ClusterName, "state": final.State, "failed_step": final.FailedStep}
		h.audit.Log(audit.Entry{User: actor, Role: role, IP: ip, Action: "ha_cluster.setup.finish", Resource: "ha", Detail: d})
	})
	if err != nil {
		if errors.Is(err, ha.ErrSetupBusy) {
			jsonErrCode(w, http.StatusConflict, "setup_busy", err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, "could not start the setup")
		return
	}
	h.auditHA(r, "ha_cluster.setup.start", "ha", detail)
	jsonResp(w, http.StatusAccepted, mgr.Snapshot())
}

// GetHAClusterStatus returns the state of the formation job.
func (h *Handler) GetHAClusterStatus(w http.ResponseWriter, _ *http.Request) {
	jsonResp(w, http.StatusOK, h.clusterSetup().Snapshot())
}

func refusal(w http.ResponseWriter, status int, msg string, blockers []string) {
	jsonResp(w, status, map[string]any{"error": msg, "code": blockers[0], "blockers": blockers})
}

// TestHAFencing really fences a node (power-cycle). Heavily guarded.
func (h *Handler) TestHAFencing(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Node              string `json:"node"`
		Confirm           string `json:"confirm"`
		AllowControlPlane bool   `json:"allow_control_plane"`
		// AcknowledgeUnreliable lets a watchdog-only direct fence run on a
		// node without resources. Its result is never recorded as proof.
		AcknowledgeUnreliable bool `json:"acknowledge_unreliable"`
	}
	if err := decodeBody(r, &req); err != nil || !ha.PacemakerNodeRE.MatchString(req.Node) {
		jsonErrCode(w, http.StatusBadRequest, "invalid_node_name", "node must be a Pacemaker node name")
		return
	}
	if req.Confirm != ha.FenceConfirmation(req.Node) {
		h.auditHA(r, "ha_fencing.test.refused", "ha", map[string]any{"node": req.Node, "reason": ha.RefConfirmRequired})
		refusal(w, http.StatusConflict, "type the exact confirmation to fence a node", []string{ha.RefConfirmRequired})
		return
	}
	res, blockers, st := h.haState().fence.FenceWith(r.Context(), sysutil.DefaultRunner, req.Node, req.Confirm,
		ha.FenceOptions{AllowControlPlane: req.AllowControlPlane, AcknowledgeUnreliable: req.AcknowledgeUnreliable, CPGroup: h.cpGroup()})
	if len(blockers) > 0 {
		h.auditHA(r, "ha_fencing.test.refused", "ha", map[string]any{"node": req.Node, "blockers": blockers})
		if containsString(blockers, ha.RefWatchdogOnlyUnreliable) {
			jsonResp(w, http.StatusConflict, map[string]any{
				"error":       "with watchdog-only SBD a direct fence of a live node does not reboot it; simulate a real failure instead",
				"code":        ha.RefWatchdogOnlyUnreliable,
				"blockers":    blockers,
				"mode":        ha.FencingMode(st),
				"explanation": ha.WatchdogOnlyExplanation(st, req.Node, req.AcknowledgeUnreliable),
				"node":        req.Node,
				"simulate":    map[string]any{"node": req.Node, "command": ha.SimulateFailureCommand},
			})
			return
		}
		refusal(w, http.StatusConflict, "the fencing test was refused; nothing was fenced", blockers)
		return
	}
	user, _, _ := audit.FromRequest(r)
	// Only a real device fence is recorded. In watchdog_only mode (reachable
	// only with an acknowledged, resource-free node) Pacemaker merely assumes
	// the node self-fenced, so the result proves nothing.
	if res.OK && ha.FencingMode(st) == ha.FencingModeDevice {
		_ = ha.SaveFencingRecord(h.dataDir(), ha.FencingRecord{TestedAt: res.FencedAt, Node: req.Node, ClusterName: st.ClusterName, By: user})
	}
	h.auditHA(r, "ha_fencing.test", "ha", map[string]any{"node": req.Node, "ok": res.OK, "fenced_at": res.FencedAt.Format(time.RFC3339)})
	jsonResp(w, http.StatusOK, res)
}

func (h *Handler) StandbyHANode(w http.ResponseWriter, r *http.Request)   { h.setStandby(w, r, true) }
func (h *Handler) UnstandbyHANode(w http.ResponseWriter, r *http.Request) { h.setStandby(w, r, false) }

func (h *Handler) setStandby(w http.ResponseWriter, r *http.Request, standby bool) {
	node := chi.URLParam(r, "name")
	if !ha.PacemakerNodeRE.MatchString(node) {
		jsonErrCode(w, http.StatusBadRequest, "invalid_node_name", "node must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$")
		return
	}
	h.haState().opLock.Lock()
	defer h.haState().opLock.Unlock()
	st := ha.ProbePacemaker(r.Context(), sysutil.DefaultRunner)
	if st.State != ha.StateObserved && st.State != ha.StateBlocked {
		refusal(w, http.StatusConflict, "Pacemaker state is not readable", []string{ha.RefPacemakerDown})
		return
	}
	known := false
	for _, n := range st.NodeNames {
		known = known || n == node
	}
	for _, n := range st.NodeStates {
		known = known || n.Name == node
	}
	if !known {
		jsonErrCode(w, http.StatusNotFound, ha.RefNodeUnknown, "the node is not in the Pacemaker node list")
		return
	}
	verb, state, action := "unstandby", "online", "ha_node.unstandby"
	if standby {
		verb, state, action = "standby", "standby", "ha_node.standby"
		if blockers := ha.StandbyBlockers(st, node); len(blockers) > 0 {
			h.auditHA(r, action+".refused", "ha", map[string]any{"node": node, "blockers": blockers})
			refusal(w, http.StatusConflict, "refusing: the cluster would lose capacity or quorum", blockers)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if _, err := sysutil.DefaultRunner.Run(ctx, "pcs", "node", verb, node); err != nil {
		h.auditHA(r, action, "ha", map[string]any{"node": node, "ok": false})
		jsonErrCode(w, http.StatusBadGateway, "pcs_failed", "pcs node "+verb+" failed")
		return
	}
	h.auditHA(r, action, "ha", map[string]any{"node": node, "ok": true})
	jsonResp(w, http.StatusOK, map[string]any{"ok": true, "node": node, "state": state})
}

// GetHAFencingEvidence reports how fencing is proven in this cluster. Read-only.
func (h *Handler) GetHAFencingEvidence(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	st := ha.ProbePacemaker(ctx, sysutil.DefaultRunner)
	ev := h.fencingEvidence(st)
	events := ev.Events
	if len(events) > ha.MaxFenceEvents {
		events = events[:ha.MaxFenceEvents]
	}
	var testedAt *string
	if ev.Tested && ev.TestedAt != "" {
		testedAt = &ev.TestedAt
	}
	var recordedBy *string
	if ev.RecordedBy != "" {
		recordedBy = &ev.RecordedBy
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"mode":        ev.Mode,
		"tested":      ev.Tested,
		"tested_at":   testedAt,
		"events":      events,
		"recorded_by": recordedBy,
		"simulate":    map[string]any{"command_template": ha.SimulateFailureCommand},
	})
}
