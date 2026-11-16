package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/firewall"
	"webkvm/internal/ha"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
	"webkvm/internal/vmsched"
)

func (h *Handler) ListVMs(w http.ResponseWriter, r *http.Request) {
	vms, err := h.compute.ListDomains()
	if err != nil {
		// A degraded local libvirt must not hide the remote nodes: keep
		// going when there is at least one enabled remote node to list.
		if !h.hasEnabledRemoteNode() {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		slog.Warn("list_vms_local_failed", "err", err)
		vms = nil
	}
	// Federation: aggregate domains from every enabled remote node so a
	// single dashboard sees the whole cluster (vm.node labels the source).
	_, role, _ := audit.FromRequest(r)
	// ?scope=local answers with this server's instances only, at once; the
	// UI renders them and merges the full (federated) list when it arrives.
	if r.URL.Query().Get("scope") != "local" {
		vms = h.federateVMsContext(r.Context(), vms, role)
	}
	// ACL & visibility for non-admins (ownership, allowed groups, allowed tags).
	vms = h.filterVMsByACL(r, vms)
	if h.clusterHAEnabled() {
		applyPacemakerManagementGroup(r.Context(), vms, h.cpGroup())
	}
	// Local instances carry the machine's real hostname, not the name that
	// the shared nodes.json happened to persist before a failover.
	localName := localNodeName()
	for i := range vms {
		if vms[i].Node == "" {
			vms[i].Node = localName
		}
	}
	h.applyManaged(vms)
	if vms == nil {
		vms = []models.VM{} // an empty cluster is [], never JSON null
	}
	jsonResp(w, http.StatusOK, vms)
}

func (h *Handler) hasEnabledRemoteNode() bool {
	if h.nodes == nil {
		return false
	}
	for _, n := range h.nodes.List() {
		if !n.IsLocal() && n.Enabled {
			return true
		}
	}
	return false
}

// applyManaged stamps the persisted "managed" flag (default true) on listings.
func (h *Handler) applyManaged(vms []models.VM) {
	ws := h.workloadStore()
	state := ws.all()
	for i := range vms {
		node := vms[i].NodeID
		if node == "" {
			node = vms[i].Node
		}
		mg := true
		if st, ok := state[workloadKey(node, vms[i].Hypervisor, vms[i].ID)]; ok {
			mg = st.Managed
		}
		vms[i].Managed = &mg
	}
}

// Bounds for cluster federation. Variables so tests can shorten them.
var (
	// federationNodeTimeout bounds one remote node (connect + list).
	federationNodeTimeout = 8 * time.Second
	// federationDeadline caps the whole fan-out; slower nodes are skipped.
	federationDeadline = 10 * time.Second
	// federationBackoff keeps a node that just failed out of the next
	// requests, so a dead node does not cost its timeout on every call.
	federationBackoff = 15 * time.Second

	// listRemoteDomains is a test seam; nil means "list through the Pool".
	listRemoteDomains func(context.Context, string) ([]models.VM, error)
	// federationStaleTTL is how long a node's last good listing is served,
	// marked stale, while the node does not answer.
	federationStaleTTL = 60 * time.Second
	// federationFreshTTL: a node listed this recently is answered from its
	// last listing instead of being asked again (8.29: two chained GET
	// /api/vms of ~6 s each after a create). Any mutating request routed to
	// a node, and any create, drops this shortcut (invalidateFreshListings).
	federationFreshTTL = 3 * time.Second
)

type staleListing struct {
	vms []models.VM
	at  time.Time
	// fresh is cleared by invalidateFreshListings; the listing then only
	// serves as the stale fallback.
	fresh bool
}

// freshFor returns the node's last listing when it is younger than
// federationFreshTTL and nothing changed since through this server.
func freshFor(id string) ([]models.VM, bool) {
	staleMu.Lock()
	defer staleMu.Unlock()
	l, ok := staleListings[id]
	if !ok || !l.fresh || time.Since(l.at) >= federationFreshTTL {
		return nil, false
	}
	return append([]models.VM(nil), l.vms...), true
}

// invalidateFreshListings drops the fresh shortcut of the given nodes (all
// when none is given), keeping the listings as stale fallbacks.
func invalidateFreshListings(ids ...string) {
	staleMu.Lock()
	defer staleMu.Unlock()
	if len(ids) == 0 {
		for k, l := range staleListings {
			l.fresh = false
			staleListings[k] = l
		}
		return
	}
	for _, id := range ids {
		if l, ok := staleListings[id]; ok {
			l.fresh = false
			staleListings[id] = l
		}
	}
}

// isMutating: any request that may change what a VM listing shows.
func isMutating(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
}

var (
	staleMu       sync.Mutex
	staleListings = map[string]staleListing{}
)

func rememberListing(id string, vms []models.VM) {
	staleMu.Lock()
	defer staleMu.Unlock()
	staleListings[id] = staleListing{vms: append([]models.VM(nil), vms...), at: time.Now(), fresh: true}
}

// staleFor returns the last good listing of a node (marked stale) while it is
// younger than federationStaleTTL.
func staleFor(id string) []models.VM {
	staleMu.Lock()
	defer staleMu.Unlock()
	l, ok := staleListings[id]
	if !ok || time.Since(l.at) > federationStaleTTL {
		return nil
	}
	out := append([]models.VM(nil), l.vms...)
	for i := range out {
		out[i].Stale = true
	}
	return out
}

// nodeBreakerOpen reports whether a node should be skipped because it just
// failed: the Pool's status when wired, the legacy map otherwise.
func (h *Handler) nodeBreakerOpen(n nodes.Node) bool {
	if h.libvirtPool != nil && n.Driver() != nodes.BackendIncus {
		return h.libvirtPool.Status(n.ID).State == libvirt.PoolStateFailing
	}
	return federationRecentlyFailed(n.ID)
}

var (
	federationFailMu sync.Mutex
	federationFailed = map[string]time.Time{}
)

func federationRecentlyFailed(id string) bool {
	federationFailMu.Lock()
	defer federationFailMu.Unlock()
	at, ok := federationFailed[id]
	if ok && time.Since(at) >= federationBackoff {
		delete(federationFailed, id)
		return false
	}
	return ok
}

func federationNoteResult(id string, failed bool) {
	federationFailMu.Lock()
	defer federationFailMu.Unlock()
	if failed {
		federationFailed[id] = time.Now()
	} else {
		delete(federationFailed, id)
	}
}

// federateVMs appends the domains of each enabled remote node to the local
// list, tagging them with their node name. See federateVMsContext.
func (h *Handler) federateVMs(vms []models.VM, role string) []models.VM {
	return h.federateVMsContext(context.Background(), vms, role)
}

// federateVMsContext queries every enabled remote node IN PARALLEL, each
// bounded by federationNodeTimeout and all by federationDeadline (and the
// request context). Best-effort: a node that is slow, down or unreachable is
// logged, marked unreachable in the registry and skipped, so one dead node
// never hides the fleet nor blocks the response. Results keep node order.
func (h *Handler) federateVMsContext(ctx context.Context, vms []models.VM, role string) []models.VM {
	if h.nodes == nil {
		return vms
	}
	var targets []nodes.Node
	for _, n := range h.nodes.List() {
		// is_self: this machine registered again as a remote; its domains
		// are already in the local list.
		if n.IsLocal() || n.IsSelf || !n.Enabled {
			continue
		}
		if n.Driver() == nodes.BackendIncus && role != models.RoleAdmin {
			continue
		}
		if n.Driver() == nodes.BackendLibvirt && !libvirtURIUsable(n.URI) {
			slog.Warn("federate_skipped_unsafe_uri", "node", n.Name)
			continue
		}
		if cached, ok := freshFor(n.ID); ok {
			for _, v := range cached {
				v.Node, v.NodeID = n.Name, n.ID
				vms = append(vms, v)
			}
			continue
		}
		if h.nodeBreakerOpen(n) {
			// Skipped, but its last listing is still shown (stale).
			for _, v := range staleFor(n.ID) {
				v.Node, v.NodeID = n.Name, n.ID
				vms = append(vms, v)
			}
			continue
		}
		targets = append(targets, n)
	}
	if len(targets) == 0 {
		return vms
	}
	ctx, cancel := context.WithTimeout(ctx, federationDeadline)
	defer cancel()

	type result struct {
		vms []models.VM
		err error
	}
	// Capture the globals once: abandoned goroutines must not read them later.
	nodeTimeout, seam := federationNodeTimeout, listRemoteDomains
	results := make([]chan result, len(targets))
	for i, n := range targets {
		results[i] = make(chan result, 1)
		go func(n nodes.Node, out chan<- result) {
			nodeCtx, nodeCancel := context.WithTimeout(ctx, nodeTimeout)
			defer nodeCancel()
			remote, err := h.listNodeInstances(nodeCtx, n, seam)
			out <- result{remote, err}
		}(n, results[i])
	}
	for i, n := range targets {
		var res result
		select {
		case res = <-results[i]:
		case <-ctx.Done():
			// Deadline reached: keep whatever this node already delivered.
			select {
			case res = <-results[i]:
			default:
				slog.Warn("federate_list_timeout", "node", n.Name)
				federationNoteResult(n.ID, true)
				h.nodes.MarkUnreachable(n.ID, "VM listing timed out")
				vms = appendStale(vms, n)
				continue
			}
		}
		if res.err != nil {
			slog.Warn("federate_list_failed", "node", n.Name, "err", res.err)
			federationNoteResult(n.ID, true)
			if code, pending := nodes.AccessSetupCode(libvirt.ClassifyNodeError(res.err)); pending {
				// Reachable or not, the cause is missing SSH access, not
				// a down node: say so instead of marking it offline.
				h.nodes.MarkAccessNotSetUp(n.ID, code)
			} else {
				h.nodes.MarkUnreachable(n.ID, "VM listing failed: "+res.err.Error())
			}
			vms = appendStale(vms, n)
			continue
		}
		federationNoteResult(n.ID, false)
		for j := range res.vms {
			res.vms[j].Node = n.Name
			res.vms[j].NodeID = n.ID
		}
		rememberListing(n.ID, res.vms)
		vms = append(vms, res.vms...)
	}
	return vms
}

func appendStale(vms []models.VM, n nodes.Node) []models.VM {
	for _, v := range staleFor(n.ID) {
		v.Node, v.NodeID = n.Name, n.ID
		vms = append(vms, v)
	}
	return vms
}

// listLibvirtNode lists a remote libvirt node through the Pool's long-lived
// connector, bounded by ctx.
func (h *Handler) listLibvirtNode(ctx context.Context, n nodes.Node, seam func(context.Context, string) ([]models.VM, error)) ([]models.VM, error) {
	if seam != nil {
		return seam(ctx, n.URI)
	}
	if h.libvirtPool == nil {
		return nil, libvirt.ErrNodeUnreachable
	}
	conn, err := h.libvirtPool.Connector(ctx, n)
	if err != nil {
		return nil, err
	}
	type result struct {
		vms []models.VM
		err error
	}
	done := make(chan result, 1)
	go func() {
		v, err := conn.ListDomains()
		done <- result{v, err}
	}()
	select {
	case res := <-done:
		return res.vms, res.err
	case <-ctx.Done():
		return nil, libvirt.ClassifyNodeError(ctx.Err())
	}
}

// listNodeInstances lists one remote node's instances within ctx.
func (h *Handler) listNodeInstances(ctx context.Context, n nodes.Node, seam func(context.Context, string) ([]models.VM, error)) ([]models.VM, error) {
	if n.Driver() != nodes.BackendIncus {
		return h.listLibvirtNode(ctx, n, seam)
	}
	type result struct {
		vms []models.VM
		err error
	}
	done := make(chan result, 1)
	go func() {
		backend, release, err := h.openIncusNode(ctx, n, remoteIncusListTimeout)
		if err != nil {
			done <- result{nil, err}
			return
		}
		remote, err := backend.ListDomains()
		release()
		done <- result{remote, err}
	}()
	select {
	case res := <-done:
		return res.vms, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (h *Handler) GetVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	node := r.URL.Query().Get("node")
	op, release, err := h.operatorFor(node)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	defer release()
	vm, err := op.GetDomain(id)
	if err != nil {
		slog.Warn("get_vm_failed", "id", id, "node", node, "err", err)
		if errors.Is(err, compute.ErrNotFound) || libvirt.IsDomainNotFound(err) {
			jsonErr(w, http.StatusNotFound, "instance not found")
		} else {
			jsonErr(w, http.StatusInternalServerError, "failed to read instance")
		}
		return
	}
	if h.isRemote() {
		vm.Node, vm.NodeID = h.remoteNodeLabel()
		vm.Hypervisor = "kvm"
		mg := h.workloadStore().Managed(vm.NodeID, "kvm", id)
		vm.Managed = &mg
	} else if !h.isLocalTarget(node) {
		vm.Node = node
	} else {
		local := []models.VM{vm}
		if h.clusterHAEnabled() {
			applyPacemakerManagementGroup(r.Context(), local, h.cpGroup())
		}
		vm = local[0]
		if vm.Node == "" {
			vm.Node = localNodeName()
		}
	}
	jsonResp(w, http.StatusOK, vm)
}

// applyPacemakerManagement marks instances owned by Pacemaker (KVM and Incus)
// so the UI can render them read-only; lifecycle actions are also rejected
// server-side. It uses the default control-plane group; handlers call
// applyPacemakerManagementGroup with the configured one.
func applyPacemakerManagement(ctx context.Context, vms []models.VM) {
	applyPacemakerManagementGroup(ctx, vms, ha.DefaultCPGroup)
}

func applyPacemakerManagementGroup(ctx context.Context, vms []models.VM, cpGroup string) {
	probe := probeHAOwnership(ctx, cpGroup)
	if len(probe.Workloads) == 0 && len(probe.Fallback) == 0 {
		return
	}
	localName := localNodeName()
	for i := range vms {
		hv := vms[i].Hypervisor
		if hv == "" {
			hv = ha.KindKVM
		}
		if hv == ha.KindIncus {
			// Incus workloads are cluster-wide (any member lists them); only
			// instances of a registered remote endpoint are not ours.
			if vms[i].NodeID != "" {
				continue
			}
		} else if vms[i].Node != "" && vms[i].Node != "local" && vms[i].Node != localName &&
			!probe.isMember(nodes.Node{Name: vms[i].Node}) {
			// Remote libvirt nodes outside this Pacemaker cluster have their
			// own view; members share the CIB (and the 409 guard).
			continue
		}
		wl, ok, ambiguous := probe.ownerOf(vms[i].Name, hv)
		if !ok || ambiguous {
			continue
		}
		vms[i].ManagedBy = "pacemaker"
		vms[i].HAKey = wl.Key
		// The workload runs where crm_mon/pcs says, not where the panel runs.
		// Exposed separately: vm.node is the routing key for GetVM and
		// operatorFor and the Pacemaker name may not be in the registry.
		if wl.Node != "" {
			vms[i].PacemakerNode = wl.Node
		}
	}
}

// localNodeName is the identity of the host serving this request. It must be
// read live: after a failover the panel runs on a different member while the
// shared nodes.json still carries the previous hostname.
func localNodeName() string {
	if h, err := os.Hostname(); err == nil {
		if h = strings.TrimSpace(h); h != "" {
			return h
		}
	}
	return "local"
}

// haGuardApplies reports whether this panel's Pacemaker view governs the
// instance a request addresses: Incus workloads (cluster-wide), the local
// node (empty, "local", its hostname, or its registry name/id) and a remote
// libvirt node that is a member of the same Pacemaker cluster (it shares the
// CIB). A node outside the cluster has its own manager.
func (h *Handler) haGuardApplies(r *http.Request, hv string, probe haProbeResult) bool {
	if hv == ha.KindIncus {
		return true
	}
	if h.isRemote() {
		return probe.isMember(h.remote.node)
	}
	node := r.URL.Query().Get("node")
	if isLocalNode(node) {
		return true
	}
	n, ok := h.nodeForTarget(node)
	return ok && n.IsLocal()
}

// haOwnerFor resolves the URL id of an instance (workload name, libvirt
// UUID, or anything else the backend accepts) to the canonical Pacemaker
// workload. Pacemaker workloads are keyed by NAME while the UI addresses KVM
// VMs by UUID: when the id is not a workload name, the domain is looked up
// through the request's backend (local, or the remote node's) and its name is
// matched, with the hypervisor of the resolved domain.
func (h *Handler) haOwnerFor(r *http.Request, id, hv string) (wl ha.Workload, ok, ambiguous bool) {
	probe := probeHAOwnership(r.Context(), h.cpGroup())
	if !h.haGuardApplies(r, hv, probe) {
		return ha.Workload{}, false, false
	}
	if wl, ok, ambiguous = probe.ownerOf(id, hv); ok || probe.empty() || h.compute == nil {
		return wl, ok, ambiguous
	}
	vm, err := h.compute.GetDomain(id)
	if err != nil || vm.Name == "" || vm.Name == id {
		return ha.Workload{}, false, false
	}
	kind := strings.ToLower(vm.Hypervisor)
	if kind != ha.KindIncus {
		kind = ha.KindKVM
	}
	if hv != "" && hv != kind {
		return ha.Workload{}, false, false
	}
	return probe.ownerOf(vm.Name, kind)
}

// isHAOwned reports whether the instance is a Pacemaker workload (managed or
// unmanaged by a maintenance window) that this panel's view governs.
func (h *Handler) isHAOwned(r *http.Request, id string) bool {
	if !h.clusterHAEnabled() {
		return false
	}
	_, ok, _ := h.haOwnerFor(r, id, requestHV(r))
	return ok
}

func writeInstanceRunning(w http.ResponseWriter) {
	jsonResp(w, http.StatusConflict, map[string]any{
		"error": "the instance is running: stop it first, then delete it",
		"code":  "instance_running",
	})
}

// rejectPacemakerManaged returns 409 for an instance whose lifecycle belongs
// to Pacemaker (KVM or Incus), whether it is addressed by name or UUID. A
// workload inside a maintenance window is NOT rejected, so the safe-edit flow
// (maintenance begin, edit, maintenance end) works.
func (h *Handler) rejectPacemakerManaged(w http.ResponseWriter, r *http.Request, id string) bool {
	return h.rejectPacemaker(w, r, id, requestHV(r), false)
}

// rejectPacemakerManagedStrict is rejectPacemakerManaged without the
// maintenance exception: moving or migrating a workload behind Pacemaker's
// back is never allowed, window or not.
// hv forces the hypervisor ("" = take it from the request).
func (h *Handler) rejectPacemakerManagedStrict(w http.ResponseWriter, r *http.Request, id, hv string) bool {
	if hv == "" {
		hv = requestHV(r)
	}
	return h.rejectPacemaker(w, r, id, hv, true)
}

func (h *Handler) rejectPacemaker(w http.ResponseWriter, r *http.Request, id, hv string, strict bool) bool {
	if !h.clusterHAEnabled() {
		return false
	}
	wl, ok, ambiguous := h.haOwnerFor(r, id, hv)
	if !ok {
		return false
	}
	if ambiguous {
		jsonResp(w, http.StatusConflict, map[string]any{
			"error": "a KVM VM and an Incus instance share this name and both are managed by Pacemaker; pass hv=kvm or hv=incus",
			"code":  "ambiguous_workload",
		})
		return true
	}
	if !strict && maintenanceActive(wl, time.Now()) {
		return false
	}
	msg := "instance is managed by Pacemaker; use the HA workload controls (or pcs) to start, stop or remove it"
	if strict {
		msg = "instance is managed by Pacemaker; use POST /api/ha/workloads/" + wl.Kind + "/" + wl.Name + "/move instead"
	}
	jsonResp(w, http.StatusConflict, map[string]any{"error": msg, "code": "pacemaker_managed", "ha_key": wl.Key})
	return true
}

func (h *Handler) CreateVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "create_vm"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	// The next listing must show the new instance: no fresh shortcut.
	defer invalidateFreshListings()
	var req models.CreateVMRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.rejectDrainingPlacement(w, req.Node) {
		return
	}
	if err := validateVMName(req.Name); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.TPMVersion != "" && !validTPMVersions[req.TPMVersion] {
		jsonErr(w, http.StatusBadRequest, "tpm_version must be \"1.2\" or \"2.0\"")
		return
	}
	if req.RAMMB < 0 || req.VCPUs < 0 || req.DiskGB < 0 {
		jsonErr(w, http.StatusBadRequest, "vcpus, ram_mb and disk_gb must be non-negative")
		return
	}
	if req.RAMMB == 0 {
		req.RAMMB = 2048
	}
	// One RAM rule with Edit and the UI (vm_ram.go).
	if e := checkRAMRule(&req.RAMMB, req.MinRAMMB, 0, req.AllowLowRAM); e != nil {
		writeRAMRuleError(w, e)
		return
	}
	// A local VM cannot reserve more RAM than the host has. Without this,
	// absurd values were accepted and only failed later at start time.
	if (req.Node == "" || req.Node == "local") && req.RAMMB > 0 {
		if total := hostMemoryMB(); total > 0 && req.RAMMB > total {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("ram_mb must not exceed this host's total memory (%d MB)", total))
			return
		}
	}
	if req.VCPUs == 0 {
		req.VCPUs = 2
	}
	if req.DiskGB == 0 && req.BaseImageID == "" {
		req.DiskGB = 20
	}

	// The pool has to match what is being created. A container lands in
	// an Incus pool, a KVM disk in a libvirt disk pool, and neither may
	// land in the ISO library — libvirt would accept that last one
	// happily (both are directory pools) and drop a qcow2 among the
	// install media. Applies to admins too: this is storage layout, not
	// permissions. Mirrors the frontend's pool selectors.
	if req.Type == "" && req.Image != "" {
		req.Type = "container"
	}
	isContainer := req.Type == "container" || req.Image != ""
	wantPurpose := compute.PoolPurposeDisk
	if isContainer {
		wantPurpose = compute.PoolPurposeContainer
	}

	// A non-admin container with no pool used to be ACL-checked and
	// quota-charged against the libvirt disk pool (h.defaultPool()) while
	// Incus put the root disk in a container pool of its own choosing, so
	// the ACL guarded the wrong pool and the quota was booked on it.
	// Resolve the effective container pool up front (as DeployAppliance
	// does) and pin the create to it, so ACL, quota and the actual root
	// disk all name the same pool. Admins keep the historical behaviour
	// (Incus resolves the pool from the profile).
	owner, role, _ := audit.FromRequest(r)

	// Remote node deploys are admin-only cluster operations. Incus targets
	// use their own authenticated API and inventory; libvirt targets remain
	// KVM-only and use the remote connector below.
	// The local node addressed by hostname/id, or a remote entry that is
	// this machine again (is_self), is a local create.
	if req.Node != "" && req.Node != "local" && h.nodes != nil {
		if t, ok := h.nodeForTarget(req.Node); ok && t.IsLocal() {
			req.Node = ""
		}
	}
	if req.BaseImageID != "" {
		req.BaseImageID = strings.TrimSpace(req.BaseImageID)
		if isContainer {
			jsonErr(w, http.StatusBadRequest, "cached KVM base images cannot create Incus containers")
			return
		}
		if req.Node != "" && req.Node != "local" {
			jsonErr(w, http.StatusNotImplemented, "cached base images can only create VMs on this server")
			return
		}
		if req.ExistingDiskPool != "" || req.ExistingDiskName != "" {
			jsonErr(w, http.StatusBadRequest, "choose either a cached base image or an existing disk")
			return
		}
		if req.BaseImageID != filepath.Base(req.BaseImageID) ||
			strings.ContainsAny(req.BaseImageID, `/\`) || strings.Contains(req.BaseImageID, "..") {
			jsonErr(w, http.StatusBadRequest, "invalid base image id")
			return
		}
		if req.DiskGB > 1024 {
			jsonErr(w, http.StatusBadRequest, "disk_gb must not exceed 1024 for a cached base image")
			return
		}
		releaseBaseImage := h.acquireDeployLock("base-image:" + req.BaseImageID)
		defer releaseBaseImage()
		path, _, found := h.findCachedBaseImage(req.BaseImageID)
		if !found {
			jsonErr(w, http.StatusNotFound, "cached base image not found")
			return
		}
		if !isQCow2(path) {
			jsonErr(w, http.StatusBadRequest, "cached base image is not a valid qcow2 file")
			return
		}
		req.BaseImagePath = path
	}
	// HA is enabled after creation by this server's Pacemaker, so it cannot
	// follow a VM created on another node.
	if req.HA != nil && req.Node != "" && req.Node != "local" && !h.isLocalTarget(req.Node) {
		jsonErr(w, http.StatusBadRequest, "high availability can only be enabled for VMs created on this server")
		return
	}
	if req.Node != "" && req.Node != "local" {
		if role != models.RoleAdmin {
			jsonErr(w, http.StatusForbidden, "only administrators can create VMs on a remote node")
			return
		}
		if h.nodes != nil {
			if target, ok := h.nodeForTarget(req.Node); ok && target.Driver() == nodes.BackendIncus {
				if !target.Enabled {
					jsonErr(w, http.StatusNotFound, "Incus node is disabled")
					return
				}
				if !isContainer || req.Type != "container" {
					jsonErr(w, http.StatusBadRequest, "remote Incus currently creates containers only")
					return
				}
				h.createRemoteIncusVM(w, r, req, target)
				return
			}
		}
		if isContainer {
			jsonErr(w, http.StatusBadRequest, "containers cannot be created on a remote node")
			return
		}
		target, ok := h.nodeForTarget(req.Node)
		if !ok || !target.Enabled || target.Driver() != nodes.BackendLibvirt {
			jsonErrCode(w, http.StatusNotFound, "node_not_found", fmt.Sprintf("node %q not found", req.Node))
			return
		}
		if req.CloudInit != nil {
			cloudInitRemoteUnsupported(w, target)
			return
		}
		// ISO / virtio ISO / existing disk are volume NAMES of the node's
		// own pools (resolved below through its libvirt connection, never
		// from this host's filesystem). The default bridge is read from
		// this host's /sys, so a remote create needs an explicit bridge.
		if e := validateRemoteMediaNames(&req); e != nil {
			e.write(w, target)
			return
		}
		if req.Network == "" {
			jsonErrCode(w, http.StatusBadRequest, "network_required", "choose a network (bridge) of the target node")
			return
		}
		if !libvirt.ValidRemoteBridgeName(req.Network) {
			jsonErrCode(w, http.StatusBadRequest, "invalid_bridge", "the bridge name must be 1-15 characters of letters, digits, '.', '_' or '-' (for example vmbr0)")
			return
		}
		if h.libvirtPool == nil {
			jsonErrCode(w, http.StatusServiceUnavailable, "pool_unavailable", "remote libvirt connections are not initialized")
			return
		}
		cctx, ccancel := context.WithTimeout(r.Context(), remoteLibvirtConnectTimeout)
		conn, err := h.libvirtPool.Connector(cctx, target)
		ccancel()
		if err != nil {
			writeNodeError(w, target, err)
			return
		}
		if e := resolveRemoteMedia(conn, &req); e != nil {
			e.write(w, target)
			return
		}
		req.Scope = h.mediaScope(r)
		vm, err := conn.CreateDomain(req)
		if err != nil {
			slog.Warn("remote_vm_create_failed", "node", target.ID, "err", err)
			jsonErr(w, http.StatusInternalServerError, "failed to create the VM on the node")
			return
		}
		r = withDispatch(r, dispatchInfo{Node: target.Name, NodeID: target.ID, HV: "kvm"})
		vm.Node = target.Name
		jsonResp(w, http.StatusCreated, map[string]any{"id": vm.ID, "name": vm.Name, "node": vm.Node, "node_id": target.ID})
		h.audit.Log(auditFor(r, "vm.create", vm.ID, map[string]interface{}{"name": vm.Name}))
		return
	}

	// Creating on a specific Incus cluster member other than this one is an
	// administrator operation.
	if req.TargetMember != "" && !isLocalNode(req.TargetMember) && role != models.RoleAdmin {
		jsonErr(w, http.StatusForbidden, "only administrators can create instances on another cluster member")
		return
	}

	if isContainer && strings.TrimSpace(req.StoragePool) == "" && role != models.RoleAdmin {
		pool := h.containerPoolName("")
		if pool == "" {
			// Fail closed: letting Incus pick would bypass the pool ACL
			// and the per-pool quota.
			jsonErr(w, http.StatusServiceUnavailable, "cannot resolve a default container storage pool; specify one explicitly")
			return
		}
		if exists, active, perr := h.poolExistsActive(pool); perr != nil {
			jsonErr(w, http.StatusServiceUnavailable, "cannot verify storage pool: "+perr.Error())
			return
		} else if !exists {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("storage pool %q does not exist", pool))
			return
		} else if !active {
			jsonErr(w, http.StatusBadRequest, fmt.Sprintf("storage pool %q is not active", pool))
			return
		}
		req.StoragePool = pool
	}

	if err := h.assertPoolPurpose(req.StoragePool, wantPurpose); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ExistingDiskPool != "" {
		if err := h.assertPoolPurpose(req.ExistingDiskPool, compute.PoolPurposeDisk); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	// Quota enforcement: the requesting user owns the new VM.
	if role != models.RoleAdmin {
		u, uerr := h.userStore.Get(owner)
		if uerr != nil {
			jsonErr(w, http.StatusUnauthorized, "user not found")
			return
		}
		if err := h.checkQuota(owner, 1, int64(req.VCPUs), req.RAMMB, req.DiskGB); err != nil {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		// Per-pool disk cap (a new blank disk lands on req.StoragePool
		// or the default pool — a container's pool was already resolved
		// into req.StoragePool above; an existing disk is pre-allocated so it
		// does not consume new disk quota). Pool ACL, however, must be
		// checked in both cases: without this, a user restricted to a
		// subset of pools could land a VM on a disallowed pool simply
		// by pointing at an existing disk on it.
		if req.ExistingDiskPool != "" && req.ExistingDiskName != "" {
			if err := assertPoolAllowed(u, req.ExistingDiskPool); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
		} else {
			pool := req.StoragePool
			if pool == "" {
				pool = h.defaultPool()
			}
			if err := assertPoolAllowed(u, pool); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
			if err := h.checkDiskQuota(owner, map[string]int64{pool: req.DiskGB}); err != nil {
				jsonErr(w, http.StatusConflict, err.Error())
				return
			}
		}
		// Network ACL: a restricted user can only land the VM's NIC on
		// an allowed bridge. An empty req.Network falls back to the
		// host's primary bridge (internal/libvirt's unexported
		// mainBridge()) — that fallback isn't checked here since there
		// is no API-layer equivalent of h.defaultPool() for networks
		// yet; a low-risk, documented gap, since the create form always
		// preselects a concrete network.
		if req.Network != "" {
			if err := assertNetworkAllowed(u, req.Network); err != nil {
				jsonErr(w, http.StatusForbidden, err.Error())
				return
			}
		}
	}

	// Drop snippet references the caller may not read before any
	// path (validation, seed build, Incus user-data) resolves them.
	h.scrubCloudInitSnippets(r, req.CloudInit)

	// Fail fast on invalid cloud-init payloads BEFORE the VM is created,
	// so a bad form gets a clean 400 instead of a half-provisioned VM.
	if req.CloudInit != nil {
		if err := (cloudinit.Config{
			User:           req.CloudInit.User,
			Password:       req.CloudInit.Password,
			SSHKey:         req.CloudInit.SSHKey,
			Hostname:       req.CloudInit.Hostname,
			CustomUserData: req.CloudInit.CustomUserData,
			SnippetID:      req.CloudInit.SnippetID,
		}).Validate(); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	if req.VLANTag != nil && (*req.VLANTag < 1 || *req.VLANTag > 4094) {
		jsonErr(w, http.StatusBadRequest, "vlan_tag must be between 1 and 4094")
		return
	}

	// Advanced options validation (Fase 5).
	switch req.BootOrder {
	case "", "disk", "cdrom", "network":
	default:
		jsonErr(w, http.StatusBadRequest, "boot_order must be one of: disk, cdrom, network")
		return
	}
	for _, p := range req.Profiles {
		if p == "" {
			jsonErr(w, http.StatusBadRequest, "incus profile names must not be empty")
			return
		}
	}

	req.Scope = h.mediaScope(r)
	vm, err := h.compute.CreateDomain(req)
	if err != nil {
		if errors.Is(err, compute.ErrForbidden) {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if strings.Contains(err.Error(), "resolve ISO") || strings.Contains(err.Error(), "media file") || strings.Contains(err.Error(), "ambiguous media") {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.Contains(err.Error(), "already exists") {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		if strings.Contains(err.Error(), "Maximum CPUs greater than") {
			jsonErr(w, http.StatusBadRequest, "vcpus exceeds the machine type limit")
			return
		}
		if strings.Contains(err.Error(), "smaller than the base image") ||
			strings.Contains(err.Error(), "standalone qcow2") ||
			strings.Contains(err.Error(), "is not a host Linux bridge") ||
			strings.Contains(err.Error(), "unknown LXD image remote") ||
			strings.Contains(err.Error(), "no local image with fingerprint") ||
			strings.Contains(err.Error(), "is required to create a container") {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Record the owner in VM metadata for quota accounting.
	if owner != "" {
		_, _ = h.compute.UpdateVMMeta(vm.ID, models.VMMetaUpdate{OwnerID: &owner})
	}
	// Optional cloud-init provisioning (user / password / SSH key / hostname).
	var createdPassword string
	if req.CloudInit != nil {
		// Remember the cloud-init username for later password resets.
		if req.CloudInit.User != "" {
			u := req.CloudInit.User
			_, _ = h.compute.UpdateVMMeta(vm.ID, models.VMMetaUpdate{CiUser: &u})
		}
		createdPassword = req.CloudInit.Password
		// LXD containers already received their cloud-init natively at
		// creation (user.user-data / user.network-config config keys) —
		// the NoCloud ISO path is KVM-only and would 501 on a container.
		if vm.Hypervisor != "incus" {
			if err := h.applyCloudInit(vm.ID, vm.Name, req.CloudInit); err != nil {
				resp := map[string]any{
					"id":      vm.ID,
					"name":    vm.Name,
					"warning": "cloud-init failed: " + err.Error(),
				}
				// Do not let Pacemaker start a VM whose seed never
				// attached: it would boot unprovisioned.
				if req.HA != nil {
					resp["ha_warning"] = "high availability was not enabled because cloud-init failed"
				}
				jsonResp(w, http.StatusCreated, resp)
				return
			}
		}
	}
	h.audit.Log(auditFor(r, "vm.create", vm.ID, map[string]interface{}{"name": vm.Name}))
	resp := map[string]any{"id": vm.ID, "name": vm.Name}
	if createdPassword != "" {
		resp["password"] = createdPassword
		resp["password_warning"] = "Save this password! It won't be shown again."
	}
	h.addCreatedHAResult(r, vm, req.HA, resp)
	jsonResp(w, http.StatusCreated, resp)
}

// haWorkloadKeyFor maps an instance to the Pacemaker workload key. The
// local Incus backend always uses the "default" project.
func haWorkloadKeyFor(vm models.VM) ha.WorkloadKey {
	if vm.Hypervisor == "incus" {
		return ha.WorkloadKey{Kind: ha.KindIncus, Project: "default", Name: vm.Name}
	}
	return ha.WorkloadKey{Kind: ha.KindKVM, Name: vm.Name}
}

// addCreatedHAResult enables HA for a freshly created instance when the
// request asked for it. It runs after cloud-init so the first start already
// has its seed. The VM stays created when HA is refused; the reason goes to
// ha_warning so the operator can retry from the HA toggle.
func (h *Handler) addCreatedHAResult(r *http.Request, vm models.VM, opts *models.CreateHAOptions, resp map[string]any) {
	if opts == nil {
		return
	}
	key := haWorkloadKeyFor(vm)
	_, e := h.enableWorkload(r, key, enableRequest{
		AllowedNodes:       opts.AllowedNodes,
		PreferredNode:      opts.PreferredNode,
		Stickiness:         opts.Stickiness,
		MigrationThreshold: opts.MigrationThreshold,
	})
	if e != nil {
		resp["ha_warning"] = "VM created, but high availability was not enabled: " + e.Msg
		return
	}
	resp["ha"] = true
}

func (h *Handler) UpdateVM(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req models.UpdateVMRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name != nil {
		if err := validateVMName(*req.Name); err != nil {
			jsonErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.TPMVersion != nil && !validTPMVersions[*req.TPMVersion] {
		jsonErr(w, http.StatusBadRequest, "tpm_version must be \"1.2\" or \"2.0\"")
		return
	}
	// One RAM rule with create and the UI (vm_ram.go). A floor-only change
	// is checked against the VM's current RAM when it can be read.
	{
		var minRAM, current int64
		if req.MinRAMMB != nil {
			minRAM = *req.MinRAMMB
		}
		if minRAM > 0 && req.RAMMB == nil && h.compute != nil {
			if cur, err := h.compute.GetDomain(id); err == nil {
				current = cur.RAMMB
			}
		}
		if e := checkRAMRule(req.RAMMB, minRAM, current, req.AllowLowRAM); e != nil {
			writeRAMRuleError(w, e)
			return
		}
	}

	// Quota: growing vCPU/RAM on a VM counts against its owner.
	if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		if req.VCPUs != nil || req.RAMMB != nil {
			if owner := h.ownerOf(id); owner != "" {
				cur, err := h.compute.GetDomain(id)
				if err != nil {
					// M-04: fail closed on growth — a failed lookup used to
					// let vCPU/RAM grow silently past quota.
					slog.Error("vm_update_quota_check_failed", "err", err, "vm", id)
					jsonErr(w, http.StatusServiceUnavailable, "cannot verify quota: "+err.Error())
					return
				}
				{
					addVCPU := int64(0)
					addRAM := int64(0)
					if req.VCPUs != nil && *req.VCPUs > cur.VCPUs {
						addVCPU = int64(*req.VCPUs - cur.VCPUs)
					}
					if req.RAMMB != nil && *req.RAMMB > cur.RAMMB {
						addRAM = *req.RAMMB - cur.RAMMB
					}
					if addVCPU > 0 || addRAM > 0 {
						if err := h.checkQuota(owner, 0, addVCPU, addRAM, 0); err != nil {
							jsonErr(w, http.StatusConflict, err.Error())
							return
						}
					}
				}
			}
		}
	}

	if req.BootOrder != nil {
		switch *req.BootOrder {
		case "", "disk", "cdrom", "network":
		default:
			jsonErr(w, http.StatusBadRequest, "boot_order must be one of: disk, cdrom, network")
			return
		}
	}
	for _, p := range req.Profiles {
		if p == "" {
			jsonErr(w, http.StatusBadRequest, "incus profile names must not be empty")
			return
		}
	}

	req.Scope = h.mediaScope(r)
	vm, err := h.compute.UpdateDomain(id, req)
	if err != nil {
		if errors.Is(err, compute.ErrForbidden) {
			jsonErr(w, http.StatusForbidden, err.Error())
			return
		}
		if errors.Is(err, compute.ErrDomainMustBeStoppedToRename) {
			jsonErr(w, http.StatusConflict, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit.Log(auditFor(r, "vm.update", id, nil))
	jsonResp(w, http.StatusOK, vm)
}

func (h *Handler) DeleteVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "delete_vm"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	if h.rejectPacemakerManaged(w, r, id) {
		return
	}
	if h.isRemote() {
		h.deleteRemoteVM(w, r, id)
		return
	}
	// Remote node: the instance lives on another host. Delete it there and
	// return — the disk sweep, firewall and scheduler cleanup below are all
	// local-side concerns owned by the remote node instead.
	if node := r.URL.Query().Get("node"); !isLocalNode(node) {
		op, release, err := h.operatorFor(node)
		if err != nil {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		defer release()
		deleteDisks := r.URL.Query().Get("disks") == "true" ||
			r.URL.Query().Get("delete_disks") == "true" ||
			r.URL.Query().Get("deleteDisks") == "true" ||
			r.URL.Query().Get("disks") == "1"
		// Capture the disks before undefining: their pool and source are
		// only reachable through the domain XML.
		vm, _ := op.GetDomain(id)
		if err := op.DeleteDomain(id); err != nil {
			h.vmActionErr(w, err, nil)
			return
		}
		var deleted, kept []string
		if deleteDisks {
			for _, disk := range vm.Disks {
				if disk.Device != "disk" || disk.Pool == "" || disk.Source == "" || disk.Type == "block" {
					continue
				}
				name := filepath.Base(disk.Source)
				if name == "/" || name == "." || name == "" {
					continue
				}
				if err := op.DeleteStorageVolume(disk.Pool, name); err != nil {
					kept = append(kept, disk.Source)
					continue
				}
				deleted = append(deleted, disk.Source)
			}
		}
		h.audit.Log(auditFor(r, "vm.delete", id, map[string]interface{}{
			"node": node, "disks_deleted": deleted,
		}))
		jsonResp(w, http.StatusOK, map[string]any{
			"status": "deleted", "disks_deleted": deleted, "disks_kept": kept,
		})
		return
	}
	// Capture the VM name BEFORE undefining: the disk-cleanup naming
	// convention (<vm>.qcow2, <vm>-<dev>.qcow2) is name-based.
	vm, _ := h.compute.GetDomain(id)
	// An HA workload in a maintenance window passes the Pacemaker guard, but
	// deleting it while it runs would pull the instance from under its
	// resource agent: ask the operator to stop it first.
	if vm.State == models.VMStateRunning && h.isHAOwned(r, id) {
		writeInstanceRunning(w)
		return
	}
	deleteDisks := r.URL.Query().Get("disks") == "true" ||
		r.URL.Query().Get("delete_disks") == "true" ||
		r.URL.Query().Get("deleteDisks") == "true" ||
		r.URL.Query().Get("disks") == "1"
	if err := h.compute.DeleteDomain(id); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "instance is running") {
			writeInstanceRunning(w)
			return
		}
		h.vmActionErr(w, err, nil)
		return
	}
	var disksDeleted []string
	// Disks the sweep deliberately left behind. Reported rather than
	// only logged: a disk that survives because linked clones are
	// still backed by it is something the operator has to know about,
	// not something to discover later as unexplained used space.
	var disksKept []string
	if deleteDisks && vm.Name != "" && vm.Hypervisor != "incus" {
		// Pass the VM's ACTUAL disk filenames, not just the name-based
		// convention: after a rename the disk keeps its original filename
		// (<oldname>.qcow2), so a name-only sweep would orphan it.
		var diskNames []string
		for _, d := range vm.Disks {
			if d.Device == "disk" && d.Name != "" {
				diskNames = append(diskNames, d.Name)
			}
		}
		var derr error
		disksDeleted, disksKept, derr = h.compute.DeleteVMDiskFiles(vm.Name, diskNames...)
		if derr != nil {
			h.logError("vm_disk_cleanup_failed", derr, vm.Name)
		} else if len(disksKept) > 0 {
			h.logError("vm_disk_cleanup_skipped",
				fmt.Errorf("still in use, locked, or the backing file of a linked clone: %v", disksKept), vm.Name)
		}
		// The cloud-init seed ISO lives outside every storage pool
		// (see cloudinitDir's doc comment) and outside vm.Disks, so
		// DeleteVMDiskFiles' pool sweep never sees it. Confirmed live
		// during an audit: deleting a cloud-init VM with ?disks=true
		// removed the qcow2 but left seed-<vm>.iso behind forever,
		// since nothing else ever references or cleans that path.
		isoPath := filepath.Join(h.cloudinitDir(), cloudInitSeedName(vm.Name))
		if rerr := os.Remove(isoPath); rerr == nil {
			disksDeleted = append(disksDeleted, isoPath)
		} else if !errors.Is(rerr, os.ErrNotExist) {
			h.logError("vm_cloudinit_seed_cleanup_failed", rerr, vm.Name)
		}
	}
	// Clean up any orphaned per-VM state so the stores never keep
	// rules/schedules for a VM that no longer exists.
	if h.fwStore != nil {
		if err := h.fwStore.Set(firewall.VMFirewall{VMID: id}); err != nil {
			h.logError("firewall_cleanup_after_delete_failed", err, id)
		} else if h.fwMgr != nil {
			if _, ferr := h.fwMgr.Apply(); ferr != nil {
				h.logError("firewall_reapply_after_delete_failed", ferr, id)
			}
		}
	}
	if h.vmSchedStore != nil {
		if err := h.vmSchedStore.Set(id, vmsched.Schedule{}); err != nil {
			h.logError("vmsched_cleanup_after_delete_failed", err, id)
		} else if h.vmScheduler != nil {
			h.vmScheduler.Rebuild()
		}
	}
	h.pruneVMCoverFiles(id, "") // uploaded <id>.<ext> covers
	h.audit.Log(auditFor(r, "vm.delete", id, map[string]interface{}{
		"disks_deleted": disksDeleted,
		"disks_kept":    disksKept,
	}))
	jsonResp(w, http.StatusOK, map[string]any{
		"status":        "deleted",
		"disks_deleted": disksDeleted,
		"disks_kept":    disksKept,
	})
}

// deleteRemoteVM deletes a VM of a remote libvirt node. With ?disks=true the
// volumes are removed THROUGH LIBVIRT on that node (never the local-FS sweep
// of DeleteVMDiskFiles, which would act on this host). Local-side cleanup
// (firewall, scheduler, covers) does not apply: those stores belong to the
// node that owns the VM.
func (h *Handler) deleteRemoteVM(w http.ResponseWriter, r *http.Request, id string) {
	vm, _ := h.compute.GetDomain(id)
	q := r.URL.Query()
	deleteDisks := q.Get("disks") == "true" || q.Get("delete_disks") == "true" ||
		q.Get("deleteDisks") == "true" || q.Get("disks") == "1"
	if err := h.compute.DeleteDomain(id); err != nil {
		h.vmActionErr(w, err, nil)
		return
	}
	deleted, kept := []string{}, []string{}
	if deleteDisks {
		for _, d := range vm.Disks {
			if d.Device != "disk" || d.Source == "" {
				continue
			}
			vol := filepath.Base(d.Source)
			if d.Pool == "" || d.Type == "block" {
				kept = append(kept, d.Source)
				continue
			}
			if err := h.compute.DeleteStorageVolume(d.Pool, vol); err != nil {
				h.logError("remote_vm_disk_cleanup_skipped", err, vm.Name)
				kept = append(kept, d.Source)
				continue
			}
			deleted = append(deleted, d.Source)
		}
	}
	h.audit.Log(auditFor(r, "vm.delete", id, map[string]interface{}{"disks_deleted": deleted, "disks_kept": kept}))
	jsonResp(w, http.StatusOK, map[string]any{"status": "deleted", "disks_deleted": deleted, "disks_kept": kept})
}

// ListIncusProfiles returns the profile names available on the Incus
// backend for the container creation form (empty list on KVM-only hosts).
func (h *Handler) ListIncusProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := h.compute.ListIncusProfiles()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if profiles == nil {
		profiles = []string{}
	}
	jsonResp(w, http.StatusOK, map[string][]string{"profiles": profiles})
}

// ListIncusImages returns available Incus container images (official catalog + cached local images).
func (h *Handler) ListIncusImages(w http.ResponseWriter, r *http.Request) {
	arch := runtime.GOARCH
	switch arch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}

	images, err := h.compute.ListIncusImages()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if images == nil {
		images = []models.IncusImageItem{}
	}

	jsonResp(w, http.StatusOK, models.IncusImagesResponse{
		Images:       images,
		HostArch:     arch,
		IncusEnabled: h.cfg.IncusEnabled,
	})
}

// validTPMVersions is the set of TPM versions the VM create/edit forms offer.
var validTPMVersions = map[string]bool{"1.2": true, "2.0": true}

// validVMNameRE matches libvirt-safe VM names: alphanumeric, dots,
// hyphens, underscores, 1-64 chars.
var validVMNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

func validateVMName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if !validVMNameRE.MatchString(name) {
		return fmt.Errorf("invalid VM name: must be 1-64 characters, alphanumeric, dots, hyphens, or underscores")
	}
	return nil
}
