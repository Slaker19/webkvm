package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// remoteTarget identifies the libvirt node a cloned Handler operates on.
type remoteTarget struct {
	node nodes.Node
}

// dispatchInfo is attached to the request context once a request has been
// routed to a non-local target; auditFor reads it so every audit entry of
// that request carries node, node_id and hv.
type dispatchInfo struct {
	Node   string
	NodeID string
	HV     string
}

type dispatchCtxKey struct{}

func dispatchFrom(r *http.Request) (dispatchInfo, bool) {
	if r == nil {
		return dispatchInfo{}, false
	}
	d, ok := r.Context().Value(dispatchCtxKey{}).(dispatchInfo)
	return d, ok
}

func withDispatch(r *http.Request, d dispatchInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), dispatchCtxKey{}, d))
}

// isRemote reports whether h is a per-node clone for a remote libvirt node.
func (h *Handler) isRemote() bool { return h != nil && h.remote != nil }

// remoteNodeLabel is the node name stamped on responses of a remote handler.
func (h *Handler) remoteNodeLabel() (name, id string) {
	if h.remote == nil {
		return "", ""
	}
	return h.remote.node.Name, h.remote.node.ID
}

// forBackend clones the handler for another backend (a remote libvirt node).
// Every field is copied with its shared pointers; the shared state that used
// to be held by value (and carries mutexes) lives behind pointers so the
// copy never duplicates a lock.
func (h *Handler) forBackend(be compute.Backend, lv *libvirt.Connector, node nodes.Node) *Handler {
	// Make sure the lazily created shared state exists BEFORE copying, so the
	// clone and the original share the same instances.
	h.deployState()
	h.haState()
	h.sshTestState()
	h.routerCache()
	h.workloadStore()
	c := *h
	c.compute = be
	c.lv = lv
	c.remote = &remoteTarget{node: node}
	return &c
}

// --- per-node router cache --------------------------------------------------

type remoteRouterEntry struct {
	conn   *libvirt.Connector
	uri    string
	name   string
	uiURL  string
	router http.Handler
}

type remoteRouterCache struct {
	mu sync.Mutex
	m  map[string]*remoteRouterEntry
}

func (h *Handler) routerCache() *remoteRouterCache {
	sharedInitMu.Lock()
	defer sharedInitMu.Unlock()
	if h.remoteRouters == nil {
		h.remoteRouters = &remoteRouterCache{m: map[string]*remoteRouterEntry{}}
	}
	return h.remoteRouters
}

// remoteRouter returns the route tree for node, built ONCE per node ID with
// mountVMRoutes(modeRemote). The libvirt connector comes from the Pool on
// every request; when the pool hands out another connector (reconnect,
// edited node) or the node was edited, the tree is rebuilt around it.
func (h *Handler) remoteRouter(node nodes.Node, conn *libvirt.Connector) http.Handler {
	rc := h.routerCache()
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if e, ok := rc.m[node.ID]; ok && e.conn == conn && e.uri == node.URI && e.name == node.Name && e.uiURL == node.UIURL {
		return e.router
	}
	rh := h.forBackend(compute.NewKVMBackend(conn), conn, node)
	mux := chi.NewRouter()
	rh.mountVMRoutes(mux, modeRemote)
	rc.m[node.ID] = &remoteRouterEntry{conn: conn, uri: node.URI, name: node.Name, uiURL: node.UIURL, router: mux}
	return mux
}

// invalidateNode forgets everything cached for a node: its pooled
// connection and its route tree. Called on edit, delete, enable/disable,
// host-key pinning and key rotation.
func (h *Handler) invalidateNode(id string) {
	if h.libvirtPool != nil {
		h.libvirtPool.Invalidate(id)
	}
	forgetNodeDialer(id)
	rc := h.routerCache()
	rc.mu.Lock()
	delete(rc.m, id)
	rc.mu.Unlock()
}

func (h *Handler) invalidateAllNodes() {
	if h.libvirtPool != nil {
		h.libvirtPool.InvalidateAll()
	}
	forgetAllNodeDialers()
	rc := h.routerCache()
	rc.mu.Lock()
	rc.m = map[string]*remoteRouterEntry{}
	rc.mu.Unlock()
}

// --- errors -------------------------------------------------------------------

// writeNodeError answers a failure talking to a remote node with the typed
// code and status from libvirt/errors.go. The text is always a fixed message:
// raw libvirt or ssh stderr never reaches the client.
func writeNodeError(w http.ResponseWriter, node nodes.Node, err error) {
	var ne *libvirt.NodeError
	if !errors.As(err, &ne) {
		classified := libvirt.ClassifyNodeError(err)
		if !errors.As(classified, &ne) {
			ne = libvirt.ErrNodeUnreachable
		}
	}
	jsonResp(w, ne.HTTPStatus(), map[string]string{
		"error":   ne.Error(),
		"code":    ne.Code(),
		"node":    node.Name,
		"node_id": node.ID,
	})
}

// --- dispatch -------------------------------------------------------------------

// nodeDispatch routes a /api/vms/{id}/... request by its ?node= and ?hv=
// selectors:
//
//   - local (or an Incus cluster member of the local daemon): handled by the
//     local handlers, untouched;
//   - an enabled remote libvirt node: served by that node's cached router;
//   - a registered remote Incus endpoint: the legacy narrow allow-list.
//
// hv=kvm|incus disambiguates a name that is both a KVM node and an Incus
// cluster member (they usually share the hostname). A member that is not in
// the node registry resolves as local: the local Incus socket already sees
// every member. Remote KVM stays admin-only, checked before existence (403
// before 404).
func (h *Handler) nodeDispatch(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		sel := q.Get("node")
		hv := strings.ToLower(strings.TrimSpace(q.Get("hv")))
		if hv != "" && hv != "kvm" && hv != "incus" {
			jsonErrCode(w, http.StatusBadRequest, "invalid_hv", "hv must be kvm or incus")
			return
		}
		target, found := h.nodeForTarget(sel)

		if hv == "incus" && !(found && !target.IsLocal() && target.Driver() == nodes.BackendIncus) {
			// An Incus cluster member (or the local daemon): local handlers.
			if sel != "" && !isLocalNode(sel) && !(found && target.IsLocal()) {
				q.Del("node")
				r.URL.RawQuery = q.Encode()
				r = withDispatch(r, dispatchInfo{Node: sel, HV: "incus"})
			}
			next.ServeHTTP(w, r)
			return
		}
		if isLocalNode(sel) || (found && target.IsLocal()) {
			next.ServeHTTP(w, r)
			return
		}
		// Any remote node is an administrator-only surface. The per-VM ACL
		// matches by name/id of a LOCAL domain and says nothing about a
		// domain with the same name elsewhere.
		if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
			jsonErr(w, http.StatusForbidden, "remote node access is restricted to administrators")
			return
		}
		if !found {
			jsonErrCode(w, http.StatusNotFound, "node_not_found", fmt.Sprintf("node %q not found", sel))
			return
		}
		if !target.Enabled {
			jsonResp(w, http.StatusNotFound, map[string]string{
				"error": "the node is disabled", "code": "node_disabled", "node": target.Name, "node_id": target.ID,
			})
			return
		}
		if isMutating(r) {
			// The node's next listing must reflect this change (vms.go
			// federationFreshTTL): drop the shortcut before and after.
			invalidateFreshListings(target.ID)
			defer invalidateFreshListings(target.ID)
		}
		if target.Driver() == nodes.BackendIncus {
			h.rejectUnsupportedRemoteVMRoute(next).ServeHTTP(w, r)
			return
		}
		h.serveRemoteLibvirt(w, r, target)
	})
}

// serveRemoteLibvirt serves the request through the node's cached router.
func (h *Handler) serveRemoteLibvirt(w http.ResponseWriter, r *http.Request, target nodes.Node) {
	if h.libvirtPool == nil {
		jsonErrCode(w, http.StatusServiceUnavailable, "pool_unavailable", "remote libvirt connections are not initialized")
		return
	}
	if !libvirtURIUsable(target.URI) {
		jsonResp(w, http.StatusConflict, map[string]string{
			"error": "the node URI is not a plain libvirt URI; edit the node", "code": "unsafe_uri",
			"node": target.Name, "node_id": target.ID,
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteLibvirtConnectTimeout)
	conn, err := h.libvirtPool.Connector(ctx, target)
	cancel()
	if err != nil {
		slog.Warn("remote_node_connect_failed", "node", target.ID, "err", libvirt.ClassifyNodeError(err))
		writeNodeError(w, target, err)
		return
	}
	r = withDispatch(r, dispatchInfo{Node: target.Name, NodeID: target.ID, HV: "kvm"})
	// Long-lived streams (VNC/SPICE/serial websockets) are not "replies".
	if !isStreamRequest(r) {
		started := time.Now()
		defer func() { h.libvirtPool.ObserveCall(target.ID, "request", time.Since(started)) }()
	}
	h.remoteRouter(target, conn).ServeHTTP(w, r)
}

// --- route classes ---------------------------------------------------------------

type routeClass string

const (
	// classA: served unchanged on a remote node.
	classA routeClass = "A"
	// classB: served remotely with an adaptation inside the handler.
	classB routeClass = "B"
	// classL: local only; answers 501 remote_unsupported on a remote node.
	classL routeClass = "L"
)

// remoteUnsupported answers 501 for a route (or a part of one) that cannot
// act on a remote node. Hint open_node_ui tells the UI to offer opening that
// node's own WebKVM.
func (h *Handler) remoteUnsupported(w http.ResponseWriter, reason string) {
	name, id := h.remoteNodeLabel()
	body := map[string]string{
		"error":   "this operation is not available for remote nodes; open that node in its own WebKVM",
		"code":    "remote_unsupported",
		"hint":    "open_node_ui",
		"node":    name,
		"node_id": id,
	}
	if reason != "" {
		body["reason"] = reason
	}
	if h.remote != nil && h.remote.node.UIURL != "" {
		body["ui_url"] = h.remote.node.UIURL
	}
	jsonResp(w, http.StatusNotImplemented, body)
}

func (h *Handler) remoteUnsupportedHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { h.remoteUnsupported(w, "") }
}

// isStreamRequest reports whether a request is a long-lived stream (a
// websocket upgrade or the console/export streams), whose duration says
// nothing about the node's reply time.
func isStreamRequest(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return true
	}
	p := r.URL.Path
	for _, s := range []string{"/vnc", "/spice-ws", "/serial", "/export"} {
		if strings.HasSuffix(p, s) {
			return true
		}
	}
	return false
}
