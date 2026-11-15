package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/ha"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// remoteLibvirtConnectTimeout bounds opening a remote libvirt connection.
const remoteLibvirtConnectTimeout = 15 * time.Second

// domainOperator is the subset of the compute.Backend seam used to act on a
// single instance, regardless of whether the target is KVM or Incus.
type domainOperator interface {
	StartDomain(id string) error
	ShutdownDomain(id string) error
	ForceOffDomain(id string) error
	RebootDomain(id string) error
	SuspendDomain(id string) error
	ResumeDomain(id string) error
	DeleteDomain(id string) error
	DeleteStorageVolume(pool, name string) error
	GetDomain(id string) (models.VM, error)
	ListSnapshots(domainID string) ([]models.Snapshot, error)
	GetDomainLog(id string, lines int) (string, error)
}

func (h *Handler) nodeForTarget(target string) (nodes.Node, bool) {
	if h.nodes == nil {
		return nodes.Node{}, false
	}
	list := h.nodes.List()
	for _, n := range list {
		if n.Name == target || n.ID == target {
			if n.IsSelf {
				// A remote entry that is this machine again (registered
				// before the control plane moved here): serve it locally,
				// never over SSH to ourselves.
				for _, l := range list {
					if l.IsLocal() {
						return l, true
					}
				}
			}
			return n, true
		}
	}
	return nodes.Node{}, false
}

// nodeURI resolves an enabled libvirt node (by name or ID) to its URI.
func (h *Handler) nodeURI(target string) (string, bool) {
	n, ok := h.nodeForTarget(target)
	if !ok || !n.Enabled || n.Driver() != nodes.BackendLibvirt {
		return "", false
	}
	if !libvirtURIUsable(n.URI) {
		return "", false
	}
	return n.URI, true
}

// operatorFor resolves the target of an instance operation. Incus nodes use
// the authenticated project-scoped API client; libvirt nodes use a connector.
// The returned release closes a remote client and is a no-op for local.
func (h *Handler) operatorFor(node string) (domainOperator, func(), error) {
	// A per-node clone already carries that node's backend: never reconnect.
	if h.isRemote() {
		return h.compute, func() {}, nil
	}
	if isLocalNode(node) {
		return h.compute, func() {}, nil
	}
	n, ok := h.nodeForTarget(node)
	if ok && n.IsLocal() {
		return h.compute, func() {}, nil
	}
	if !ok || !n.Enabled {
		return nil, nil, fmt.Errorf("node %q not found or disabled", node)
	}
	if n.Driver() == nodes.BackendIncus {
		// The release cancels the client context; the Incus client reuses it
		// for every request made through the operator.
		return h.openIncusNode(context.Background(), n, remoteIncusOperationTimeout)
	}
	c, err := h.openRemoteLibvirt(n)
	if err != nil {
		return nil, nil, err
	}
	return c, func() { c.Close() }, nil
}

// openRemoteLibvirt connects to a remote libvirt node with a bounded wait
// (libvirt's connect has no timeout) and never to a URI carrying options.
func (h *Handler) openRemoteLibvirt(n nodes.Node) (*libvirt.Connector, error) {
	if !libvirtURIUsable(n.URI) {
		return nil, fmt.Errorf("node %q has an unsafe libvirt URI; re-create it with a plain qemu+ssh URI", n.Name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), remoteLibvirtConnectTimeout)
	defer cancel()
	return libvirt.NewRemoteConnectorContext(ctx, n.URI)
}

// isLocalNode reports whether the node selector refers to the local
// hypervisor: the empty string (a VM with no node badge), "local", or this
// machine's own hostname (what ListVMs stamps on local instances). A remote
// node can never share the hostname because the registry's local node owns
// that name.
func isLocalNode(node string) bool {
	return node == "" || node == "local" || node == localNodeName()
}

// isLocalTarget is isLocalNode plus registry resolution: a ?node= naming the
// self-duplicate entry (is_self, by id or name) is this server too, so the
// response gets the local node name and the Pacemaker annotation.
func (h *Handler) isLocalTarget(node string) bool {
	if isLocalNode(node) {
		return true
	}
	n, ok := h.nodeForTarget(node)
	return ok && n.IsLocal()
}

// rejectUnsupportedRemoteVMRoute is the guard for REGISTERED REMOTE INCUS
// endpoints (nodeDispatch calls it for them): their requests fall through to
// the local handlers, which resolve the remote operator themselves, so the
// list of routes must stay narrow. Remote libvirt nodes do not come through
// here: they get their own route tree (see remoteRouter).
func (h *Handler) rejectUnsupportedRemoteVMRoute(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node := r.URL.Query().Get("node")
		if isLocalNode(node) {
			next.ServeHTTP(w, r)
			return
		}
		target, found := h.nodeForTarget(node)
		if found && target.IsLocal() {
			// The local node addressed by its hostname/id (what vm.node
			// carries for local instances) is still local.
			next.ServeHTTP(w, r)
			return
		}
		// Any remote node (libvirt or Incus) is an administrator-only
		// surface. The per-VM ACL that requireVMOwnership applies matches
		// by name/id of a LOCAL domain and says nothing about a domain with
		// the same name on another node, so it must never authorize this.
		if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
			jsonErr(w, http.StatusForbidden, "remote node access is restricted to administrators")
			return
		}
		if !found || !target.Enabled {
			jsonErr(w, http.StatusNotFound, fmt.Sprintf("node %q not found or disabled", node))
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/api/vms/")
		if i := strings.IndexByte(path, '/'); i >= 0 {
			path = path[i:]
		} else {
			path = "/"
		}
		allowed := false
		switch r.Method {
		case http.MethodGet:
			allowed = path == "/" || path == "/disks" || path == "/networks"
		case http.MethodDelete:
			allowed = path == "/"
		case http.MethodPost:
			allowed = path == "/start" || path == "/shutdown" || path == "/forceoff" || path == "/reboot" || path == "/suspend" || path == "/resume"
		}
		if !allowed {
			jsonErr(w, http.StatusNotImplemented, fmt.Sprintf("this operation is not yet supported for remote %s instances", target.Driver()))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// resolveMigrationNode maps a node selector ("", "local", hostname, name or
// ID) to its registry entry; the local node resolves to the registry's local
// entry.
func (h *Handler) resolveMigrationNode(sel string) (nodes.Node, bool) {
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

// MigrateVM moves a VM to another cluster node. The destination is the
// request body's "node"; the source is the node the VM currently lives on
// (?node= query param, empty = local). Live migration requires the domain
// running and, without shared storage, uses block migration. Orchestrated by
// this process through Pool.MigrateDomain, in any direction.
func (h *Handler) MigrateVM(w http.ResponseWriter, r *http.Request) {
	if err := h.requirePermission(r, "control_power"); err != nil {
		jsonErr(w, http.StatusForbidden, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	// Same rule as the lifecycle handlers: a domain whose lifecycle belongs
	// to Pacemaker is never moved behind the cluster's back (local only), not
	// even inside a maintenance window: use POST /api/ha/workloads/…/move.
	if h.rejectPacemakerManagedStrict(w, r, id, ha.KindKVM) {
		return
	}
	var req struct {
		Node          string `json:"node"`
		Live          bool   `json:"live"`
		SharedStorage bool   `json:"shared_storage"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Node == "" {
		jsonErr(w, http.StatusBadRequest, "destination node is required")
		return
	}
	if !req.Live && !req.SharedStorage {
		jsonErr(w, http.StatusBadRequest, "offline migration only moves the VM definition; set shared_storage=true only when both nodes see the disk paths")
		return
	}
	// Resolve both ends as registry nodes. A per-node clone (remote router)
	// is always the source; otherwise ?node= names it (empty = local).
	var srcNode, dstNode nodes.Node
	var ok bool
	if h.isRemote() {
		srcNode = h.remote.node
	} else if srcNode, ok = h.resolveMigrationNode(r.URL.Query().Get("node")); !ok {
		jsonErrCode(w, http.StatusNotFound, "node_not_found", fmt.Sprintf("source node %q not found", r.URL.Query().Get("node")))
		return
	}
	sourceLocal := srcNode.IsLocal()
	destLocal := isLocalNode(req.Node)
	if dn, found := h.nodeForTarget(req.Node); found && dn.IsLocal() {
		destLocal = true
	}
	// Migration moves a VM between hosts; anything that touches a remote
	// node is an administrator operation, whatever the caller's local
	// control_power permission or ownership of a same-named local VM.
	if !sourceLocal || !destLocal {
		if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
			jsonErr(w, http.StatusForbidden, "migrating to or from a remote node is restricted to administrators")
			return
		}
	}
	if dstNode, ok = h.resolveMigrationNode(req.Node); !ok || !dstNode.Enabled || dstNode.Driver() != nodes.BackendLibvirt {
		jsonErrCode(w, http.StatusNotFound, "node_not_found", fmt.Sprintf("node %q not found", req.Node))
		return
	}
	if !srcNode.Enabled || srcNode.Driver() != nodes.BackendLibvirt {
		jsonErrCode(w, http.StatusNotFound, "node_not_found", fmt.Sprintf("source node %q not found or disabled", srcNode.Name))
		return
	}
	if srcNode.ID == dstNode.ID {
		jsonErrCode(w, http.StatusBadRequest, "same_node", "source and destination nodes are the same")
		return
	}
	if h.libvirtPool == nil {
		jsonErrCode(w, http.StatusServiceUnavailable, "pool_unavailable", "remote libvirt connections are not initialized")
		return
	}
	// Client-orchestrated: this process talks to both libvirtds, so any
	// direction works (remote->local included) with no node-to-node SSH.
	if err := h.libvirtPool.MigrateDomain(r.Context(), srcNode, dstNode, id, req.Live, req.SharedStorage); err != nil {
		var ne *libvirt.NodeError
		if errors.As(err, &ne) {
			writeNodeError(w, srcNode, err)
			return
		}
		slog.Warn("vm_migrate_failed", "vm_id", id, "from", srcNode.Name, "to", dstNode.Name, "err", err)
		if errors.Is(err, context.Canceled) {
			jsonErrCode(w, 499, "migration_cancelled", "the request was cancelled and the migration was aborted")
			return
		}
		if errors.Is(err, compute.ErrNotImplemented) || errors.Is(err, compute.ErrDomainNotRunning) ||
			errors.Is(err, compute.ErrDomainNotPaused) || errors.Is(err, compute.ErrDomainAlreadyPaused) ||
			errors.Is(err, compute.ErrDomainAlreadyRunning) {
			h.vmActionErr(w, err, nil)
			return
		}
		status, code, msg := migrateErrorResponse(err)
		jsonErrCode(w, status, code, msg)
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "vm.migrate", id, map[string]any{"to_node": dstNode.Name, "to_node_id": dstNode.ID, "live": req.Live, "shared_storage": req.SharedStorage}))
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "migrated", "node": dstNode.Name, "node_id": dstNode.ID})
}

// migrateErrorResponse turns a raw libvirt/QEMU migration failure into an
// HTTP status, a stable machine code and a short operator-facing message.
// The raw text (paths, daemon internals) is only logged, never returned.
func migrateErrorResponse(err error) (int, string, string) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "no storage pool with matching target path"),
		strings.Contains(msg, "Could not open") && strings.Contains(msg, "No such file or directory"),
		strings.Contains(msg, "Cannot access storage file"),
		strings.Contains(msg, "Failed to create storage"):
		return http.StatusConflict, "migration_dest_storage_missing",
			"The destination node does not have the VM's disk at the same path. Without shared storage, create or copy the disk there first (same path and size), then try again."
	case strings.Contains(msg, "cannot acquire state change lock"),
		strings.Contains(msg, "another operation is in progress"),
		strings.Contains(msg, "domain is already running a job"):
		return http.StatusConflict, "vm_busy", "The VM is busy with another operation. Wait for it to finish and try again."
	case strings.Contains(msg, "unable to connect to server"),
		strings.Contains(msg, "Connection refused"),
		strings.Contains(msg, "Connection timed out"),
		strings.Contains(msg, "No route to host"),
		strings.Contains(msg, "Name or service not known"):
		return http.StatusBadGateway, "migration_dest_unreachable",
			"The source node could not reach the destination node's migration port. Check that both nodes can reach each other (ports 49152-49215) and that no firewall blocks them."
	}
	return http.StatusInternalServerError, "migration_failed", "The migration failed. The VM was left on the source node; see the server log for details."
}
