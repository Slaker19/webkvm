package api

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/auth"
	"webkvm/internal/compute"
	"webkvm/internal/ha"
)

// clusterProvider is implemented by compute.Combined (and test doubles).
type clusterProvider interface {
	Cluster() (compute.ClusterBackend, bool)
}

// clusterBackend returns the Incus cluster layer, when there is one.
func (h *Handler) clusterBackend() (compute.ClusterBackend, bool) {
	if cp, ok := h.compute.(clusterProvider); ok {
		return cp.Cluster()
	}
	return nil, false
}

// mountIncusCluster registers the member-level admin endpoints.
func (h *Handler) mountIncusCluster(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Post("/api/cluster/members/{name}/evacuate", h.EvacuateClusterMember)
		r.Post("/api/cluster/members/{name}/restore", h.RestoreClusterMember)
	})
}

// clusterErr maps the compute cluster errors to HTTP. Never raw text.
func clusterErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, compute.ErrMemberNotFound):
		jsonErrCode(w, http.StatusNotFound, "member_not_found", "cluster member not found")
	case errors.Is(err, compute.ErrMemberOffline):
		jsonErrCode(w, http.StatusConflict, "member_offline", "the cluster member is offline")
	case errors.Is(err, compute.ErrNeedsStop):
		jsonErrCode(w, http.StatusConflict, "needs_stop", "the instance must be stopped to move it without live migration")
	case errors.Is(err, compute.ErrSameMember):
		jsonErrCode(w, http.StatusConflict, "same_member", "the instance already runs on that cluster member")
	case errors.Is(err, compute.ErrNotClustered):
		jsonErrCode(w, http.StatusConflict, "not_clustered", "Incus is not clustered")
	case errors.Is(err, compute.ErrNotImplemented):
		jsonErrCode(w, http.StatusNotImplemented, "not_implemented", "not supported by this backend")
	default:
		jsonErrCode(w, http.StatusInternalServerError, "cluster_operation_failed", "the cluster operation failed")
	}
}

// MoveVM moves an Incus instance to another member of the local cluster.
// POST /api/vms/{id}/move {"target_member":"node2","live":false}
func (h *Handler) MoveVM(w http.ResponseWriter, r *http.Request) {
	if h.isRemote() {
		h.remoteUnsupported(w, "")
		return
	}
	id := chi.URLParam(r, "id")
	var req struct {
		TargetMember string `json:"target_member"`
		Live         bool   `json:"live"`
	}
	if err := decodeBody(r, &req); err != nil || req.TargetMember == "" {
		jsonErrCode(w, http.StatusBadRequest, "invalid_request", "target_member is required")
		return
	}
	// A Pacemaker-managed instance is moved with POST /api/ha/workloads/…/move;
	// a direct Incus move would leave Pacemaker fighting the new location.
	if h.rejectPacemakerManagedStrict(w, r, id, ha.KindIncus) {
		return
	}
	cb, ok := h.clusterBackend()
	if !ok {
		jsonErrCode(w, http.StatusConflict, "not_clustered", "Incus is not clustered")
		return
	}
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		if errors.Is(err, compute.ErrNotFound) {
			jsonErrCode(w, http.StatusNotFound, "not_found", "instance not found")
		} else {
			jsonErr(w, http.StatusInternalServerError, "failed to read instance")
		}
		return
	}
	if vm.Hypervisor != "incus" {
		jsonErrCode(w, http.StatusBadRequest, "incus_only", "only Incus instances can be moved between cluster members")
		return
	}
	if err := cb.MoveInstance(r.Context(), id, req.TargetMember, req.Live); err != nil {
		clusterErr(w, err)
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "vm.move", id, map[string]any{"target_member": req.TargetMember, "live": req.Live, "hv": "incus"}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"status": "moved", "target_member": req.TargetMember})
}

var evacuateModeRE = regexp.MustCompile(`^[a-z][a-z-]{0,19}$`)

// EvacuateClusterMember evacuates an Incus member (admin).
// POST /api/cluster/members/{name}/evacuate {"mode":"migrate"}
// With the HA module on, it answers 409 healing_managed_by_pacemaker while a
// Pacemaker-managed Incus workload runs on the member (override: force=true,
// audited).
func (h *Handler) EvacuateClusterMember(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req struct {
		Mode  string `json:"mode"`
		Force bool   `json:"force"`
	}
	_ = decodeBody(r, &req)
	if req.Mode != "" && !evacuateModeRE.MatchString(req.Mode) {
		jsonErrCode(w, http.StatusBadRequest, "invalid_mode", "invalid evacuation mode")
		return
	}
	if h.rejectMemberOpHAManaged(w, r, "evacuate", name, req.Force) {
		return
	}
	cb, ok := h.clusterBackend()
	if !ok {
		jsonErrCode(w, http.StatusConflict, "not_clustered", "Incus is not clustered")
		return
	}
	if err := cb.EvacuateMember(r.Context(), name, req.Mode); err != nil {
		clusterErr(w, err)
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "cluster.member_evacuate", name, map[string]any{"mode": req.Mode, "hv": "incus"}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"status": "evacuated", "member": name})
}

// RestoreClusterMember restores an evacuated member (admin).
func (h *Handler) RestoreClusterMember(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req struct {
		Force bool `json:"force"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		_ = decodeBody(r, &req)
	}
	if h.rejectMemberOpHAManaged(w, r, "restore", name, req.Force) {
		return
	}
	cb, ok := h.clusterBackend()
	if !ok {
		jsonErrCode(w, http.StatusConflict, "not_clustered", "Incus is not clustered")
		return
	}
	if err := cb.RestoreMember(r.Context(), name); err != nil {
		clusterErr(w, err)
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "cluster.member_restore", name, map[string]any{"hv": "incus"}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"status": "restored", "member": name})
}

// SetWorkloadManaged persists the managed flag of a workload.
// PUT /api/vms/{id}/managed?node=&hv= {"managed":false}
func (h *Handler) SetWorkloadManaged(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Managed *bool `json:"managed"`
	}
	if err := decodeBody(r, &req); err != nil || req.Managed == nil {
		jsonErrCode(w, http.StatusBadRequest, "invalid_request", "managed (boolean) is required")
		return
	}
	node, hv := r.URL.Query().Get("node"), r.URL.Query().Get("hv")
	if d, ok := dispatchFrom(r); ok {
		node, hv = d.NodeID, d.HV
		if node == "" {
			node = d.Node
		}
	}
	if h.isRemote() {
		_, node = h.remoteNodeLabel()
		hv = "kvm"
	}
	if err := h.workloadStore().SetManaged(node, hv, id, *req.Managed); err != nil {
		jsonErrCode(w, http.StatusInternalServerError, "state_write_failed", "could not save the workload state")
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "vm.managed_set", id, map[string]any{"managed": *req.Managed}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"managed": *req.Managed})
}

// rejectMemberOpHAManaged answers 409 healing_managed_by_pacemaker when the
// HA module is on and a Pacemaker-managed Incus workload runs on member:
// evacuating or restoring it would move instances behind Pacemaker's back.
// force (body field or ?force=true) lets an administrator through; the
// override is audited with the affected workloads.
func (h *Handler) rejectMemberOpHAManaged(w http.ResponseWriter, r *http.Request, op, member string, force bool) bool {
	if !h.clusterHAEnabled() {
		return false
	}
	var located []string
	for _, wl := range probeHAOwnership(r.Context(), h.cpGroup()).Workloads {
		if wl.Kind == ha.KindIncus && wl.Node != "" && strings.EqualFold(wl.Node, member) {
			located = append(located, wl.Key)
		}
	}
	if len(located) == 0 {
		return false
	}
	if force || r.URL.Query().Get("force") == "true" {
		if h.audit != nil {
			h.audit.Log(auditFor(r, "cluster.member_"+op+".forced", member, map[string]any{"workloads": located, "hv": "incus"}))
		}
		return false
	}
	jsonResp(w, http.StatusConflict, map[string]any{
		"error":     "Pacemaker manages Incus workloads on this member; move them with /api/ha/workloads/…/move, or pass force=true to override",
		"code":      "healing_managed_by_pacemaker",
		"workloads": located,
	})
	return true
}
