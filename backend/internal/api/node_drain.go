package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// nodeDrainVM is one row of a drain/evacuation plan.
type nodeDrainVM struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	State      string `json:"state"`
	Migratable bool   `json:"migratable"`
	Action     string `json:"action,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type nodeDrainPlan struct {
	Node    nodes.Node    `json:"node"`
	VMs     []nodeDrainVM `json:"vms"`
	Targets []nodes.Node  `json:"targets"`
}

type nodeEvacuateResult struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type nodeEvacuateResponse struct {
	Status   string               `json:"status"`
	Node     string               `json:"node"`
	Target   string               `json:"target,omitempty"`
	Migrated []nodeEvacuateResult `json:"migrated,omitempty"`
	Failed   []nodeEvacuateResult `json:"failed,omitempty"`
	Skipped  []nodeEvacuateResult `json:"skipped,omitempty"`
	Plan     *nodeDrainPlan       `json:"plan,omitempty"`
}

func (h *Handler) drainNodeFromRequest(r *http.Request) (nodes.Node, bool) {
	if h.nodes == nil {
		return nodes.Node{}, false
	}
	n, ok := h.nodeForTarget(chiURLParam(r, "id"))
	if !ok {
		return nodes.Node{}, false
	}
	return n, true
}

// nodeForPlacement resolves an empty/local selector to the local registry
// entry and everything else through the name/id registry lookup.
func (h *Handler) nodeForPlacement(sel string) (nodes.Node, bool) {
	if h.nodes == nil {
		return nodes.Node{}, false
	}
	if isLocalNode(sel) {
		for _, n := range h.nodes.List() {
			if n.IsLocal() {
				return n, true
			}
		}
		return nodes.Node{}, false
	}
	return h.nodeForTarget(sel)
}

// rejectDrainingPlacement blocks new workloads on a draining node. It is
// called by CreateVM before any expensive validation, so a drain does not
// race with a creation that started a moment earlier.
func (h *Handler) rejectDrainingPlacement(w http.ResponseWriter, sel string) bool {
	n, ok := h.nodeForPlacement(sel)
	if !ok || !n.Draining {
		return false
	}
	jsonErrCode(w, http.StatusConflict, "node_draining",
		fmt.Sprintf("node %q is draining; stop the drain before creating new workloads", n.Name))
	return true
}

// DrainNode marks a libvirt node as draining. Admin only.
func (h *Handler) DrainNode(w http.ResponseWriter, r *http.Request) {
	n, ok := h.drainNodeFromRequest(r)
	if !ok {
		jsonErrCode(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if n.Driver() != nodes.BackendLibvirt {
		jsonErrCode(w, http.StatusConflict, "incus_evacuate_unsupported",
			"drain is implemented for libvirt nodes; use the Incus cluster evacuation for Incus members")
		return
	}
	user, _, _ := audit.FromRequest(r)
	updated, err := h.nodes.SetDraining(n.ID, true, user)
	if err != nil {
		nodeErr(w, err, http.StatusInternalServerError)
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "node.drain", n.ID, map[string]any{"name": n.Name}))
	}
	jsonResp(w, http.StatusOK, updated)
}

// UndrainNode clears the maintenance flag. Admin only.
func (h *Handler) UndrainNode(w http.ResponseWriter, r *http.Request) {
	n, ok := h.drainNodeFromRequest(r)
	if !ok {
		jsonErrCode(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	updated, err := h.nodes.SetDraining(n.ID, false, "")
	if err != nil {
		nodeErr(w, err, http.StatusInternalServerError)
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "node.undrain", n.ID, map[string]any{"name": n.Name}))
	}
	jsonResp(w, http.StatusOK, updated)
}

// GetNodeDrainPlan lists the instances on a node and classifies which ones
// can be migrated away. It is read-only and safe to call before draining.
func (h *Handler) GetNodeDrainPlan(w http.ResponseWriter, r *http.Request) {
	n, ok := h.drainNodeFromRequest(r)
	if !ok {
		jsonErrCode(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if n.Driver() != nodes.BackendLibvirt {
		jsonErrCode(w, http.StatusConflict, "incus_evacuate_unsupported",
			"drain is implemented for libvirt nodes; use the Incus cluster evacuation for Incus members")
		return
	}
	vms, err := h.listNodeVMs(r.Context(), n)
	if err != nil {
		jsonErrCode(w, http.StatusBadGateway, "node_list_failed", "could not list instances on the node: "+err.Error())
		return
	}
	plan := nodeDrainPlan{
		Node:    n,
		VMs:     buildDrainPlan(vms),
		Targets: h.drainTargets(n),
	}
	jsonResp(w, http.StatusOK, plan)
}

// EvacuateNode migrates the migratable instances off a draining node.
// POST /api/nodes/{id}/evacuate {target, live, shared_storage, dry_run}
func (h *Handler) EvacuateNode(w http.ResponseWriter, r *http.Request) {
	src, ok := h.drainNodeFromRequest(r)
	if !ok {
		jsonErrCode(w, http.StatusNotFound, "not_found", "node not found")
		return
	}
	if src.Driver() != nodes.BackendLibvirt {
		jsonErrCode(w, http.StatusConflict, "incus_evacuate_unsupported",
			"drain is implemented for libvirt nodes; use the Incus cluster evacuation for Incus members")
		return
	}
	var req struct {
		Target        string `json:"target"`
		Live          bool   `json:"live"`
		SharedStorage bool   `json:"shared_storage"`
		DryRun        bool   `json:"dry_run"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	vms, err := h.listNodeVMs(r.Context(), src)
	if err != nil {
		jsonErrCode(w, http.StatusBadGateway, "node_list_failed", "could not list instances on the node: "+err.Error())
		return
	}
	plan := nodeDrainPlan{
		Node:    src,
		VMs:     buildDrainPlan(vms),
		Targets: h.drainTargets(src),
	}
	if req.DryRun {
		jsonResp(w, http.StatusOK, nodeEvacuateResponse{
			Status: "planned", Node: src.Name, Plan: &plan,
		})
		return
	}
	if !src.Draining {
		jsonErrCode(w, http.StatusConflict, "node_not_draining",
			"put the node in drain mode before evacuating it")
		return
	}
	if req.Target == "" {
		jsonErrCode(w, http.StatusBadRequest, "target_required", "destination node is required")
		return
	}
	dst, ok := h.nodeForTarget(req.Target)
	if !ok || !dst.Enabled || dst.Driver() != nodes.BackendLibvirt {
		jsonErrCode(w, http.StatusNotFound, "target_not_found", fmt.Sprintf("destination node %q not found", req.Target))
		return
	}
	if dst.ID == src.ID {
		jsonErrCode(w, http.StatusBadRequest, "same_node", "source and destination nodes are the same")
		return
	}
	if dst.Draining {
		jsonErrCode(w, http.StatusConflict, "target_draining", "destination node is draining")
		return
	}
	if h.libvirtPool == nil {
		jsonErrCode(w, http.StatusServiceUnavailable, "pool_unavailable", "remote libvirt connections are not initialized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	resp := nodeEvacuateResponse{Status: "completed", Node: src.Name, Target: dst.Name}
	for _, vm := range plan.VMs {
		row := nodeEvacuateResult{ID: vm.ID, Name: vm.Name}
		if !vm.Migratable {
			row.Status = "skipped"
			row.Error = vm.Reason
			resp.Skipped = append(resp.Skipped, row)
			continue
		}
		live := vm.Action == "live_migrate"
		shared := req.SharedStorage
		if vm.Action == "offline_shared_storage" {
			if !req.SharedStorage {
				row.Status = "skipped"
				row.Error = "shared_storage_required"
				resp.Skipped = append(resp.Skipped, row)
				continue
			}
			live = false
		}
		if err := h.libvirtPool.MigrateDomain(ctx, src, dst, vm.ID, live, shared); err != nil {
			row.Status = "failed"
			row.Error = err.Error()
			resp.Failed = append(resp.Failed, row)
			continue
		}
		row.Status = "migrated"
		resp.Migrated = append(resp.Migrated, row)
		if h.audit != nil {
			h.audit.Log(auditFor(r, "vm.migrate", vm.ID, map[string]any{
				"from_node": src.Name, "to_node": dst.Name, "live": live, "shared_storage": shared,
			}))
		}
	}
	if len(resp.Failed) > 0 {
		resp.Status = "partial"
	}
	jsonResp(w, http.StatusOK, resp)
}

func (h *Handler) listNodeVMs(ctx context.Context, n nodes.Node) ([]models.VM, error) {
	if n.IsLocal() {
		if h.compute == nil {
			return nil, errors.New("compute backend unavailable")
		}
		return h.compute.ListDomains()
	}
	return h.listNodeInstances(ctx, n, nil)
}

// buildDrainPlan is pure so the classification is easy to test. Conservative:
// anything that can fail on migration is skipped with a reason instead of
// being attempted blindly.
func buildDrainPlan(vms []models.VM) []nodeDrainVM {
	out := make([]nodeDrainVM, 0, len(vms))
	for _, vm := range vms {
		p := nodeDrainVM{ID: vm.ID, Name: vm.Name, State: string(vm.State)}
		switch {
		case vm.Stale:
			p.Reason = "stale_listing"
		case vm.ManagedBy == "pacemaker":
			p.Reason = "pacemaker_managed"
		case vm.Hypervisor == "incus" || vm.Type == "container":
			p.Reason = "incus_instance"
		case vm.GPU:
			p.Reason = "gpu_passthrough"
		case vm.State == models.VMStateRunning:
			p.Migratable = true
			p.Action = "live_migrate"
		case vm.State == models.VMStateShutoff:
			p.Migratable = true
			p.Action = "offline_shared_storage"
			p.Reason = "requires_shared_storage"
		default:
			p.Migratable = true
			p.Action = "live_migrate"
		}
		out = append(out, p)
	}
	return out
}

func (h *Handler) drainTargets(src nodes.Node) []nodes.Node {
	if h.nodes == nil {
		return nil
	}
	out := make([]nodes.Node, 0)
	for _, n := range h.nodes.List() {
		if n.ID == src.ID || n.IsSelf || !n.Enabled || n.Draining {
			continue
		}
		if n.Driver() != nodes.BackendLibvirt || !libvirtURIUsable(n.URI) {
			continue
		}
		out = append(out, n)
	}
	return out
}
