package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"webkvm/internal/audit"
	"webkvm/internal/compute"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// withNodeSelector makes a read-only listing handler (storage pools, virtual
// networks, ...) honour `?node=` the same way /api/vms/{id}/... does: no
// selector or the local node runs `pick(h)`; an enabled remote libvirt node
// runs the SAME handler on a per-node clone whose compute backend is that
// node's libvirt connection. Anything else answers a typed error. Remote
// nodes are an administrator-only surface (403 before 404, like nodeDispatch).
func (h *Handler) withNodeSelector(pick func(*Handler) http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target, ok := h.resolveListNode(w, r)
		if !ok {
			return
		}
		if target == nil {
			pick(h)(w, r)
			return
		}
		rh, ok := h.cloneForRemoteList(w, r, *target)
		if !ok {
			return
		}
		pick(rh)(w, r)
	}
}

// listNodeLocalOnly is for listings that read the local filesystem (host NICs):
// a remote selector answers 501 remote_unsupported instead of silently
// describing the local host.
func (h *Handler) listNodeLocalOnly(local http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		target, ok := h.resolveListNode(w, r)
		if !ok {
			return
		}
		if target != nil {
			jsonResp(w, http.StatusNotImplemented, unsupportedListingBody(*target, "this listing is only available on the node itself"))
			return
		}
		local(w, r)
	}
}

// resolveListNode returns (nil, true) for local requests, (&node, true) for an
// enabled remote node, and (nil, false) after writing the HTTP error.
func (h *Handler) resolveListNode(w http.ResponseWriter, r *http.Request) (*nodes.Node, bool) {
	sel := r.URL.Query().Get("node")
	if sel == "" || h.isRemote() || isLocalNode(sel) {
		return nil, true
	}
	target, found := h.nodeForTarget(sel)
	if found && target.IsLocal() {
		return nil, true
	}
	if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
		jsonErr(w, http.StatusForbidden, "remote node access is restricted to administrators")
		return nil, false
	}
	if !found {
		jsonErrCode(w, http.StatusNotFound, "node_not_found", fmt.Sprintf("node %q not found", sel))
		return nil, false
	}
	if !target.Enabled {
		jsonResp(w, http.StatusNotFound, map[string]string{
			"error": "the node is disabled", "code": "node_disabled", "node": target.Name, "node_id": target.ID,
		})
		return nil, false
	}
	if target.Driver() == nodes.BackendIncus {
		jsonResp(w, http.StatusNotImplemented, unsupportedListingBody(target, "this listing is not available for standalone Incus nodes"))
		return nil, false
	}
	return &target, true
}

// cloneForRemoteList opens (through the pool) the node's libvirt connection and
// returns a handler clone bound to it.
func (h *Handler) cloneForRemoteList(w http.ResponseWriter, r *http.Request, target nodes.Node) (*Handler, bool) {
	if h.libvirtPool == nil {
		jsonErrCode(w, http.StatusServiceUnavailable, "pool_unavailable", "remote libvirt connections are not initialized")
		return nil, false
	}
	if !libvirtURIUsable(target.URI) {
		jsonResp(w, http.StatusConflict, map[string]string{
			"error": "the node URI is not a plain libvirt URI; edit the node", "code": "unsafe_uri",
			"node": target.Name, "node_id": target.ID,
		})
		return nil, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), remoteLibvirtConnectTimeout)
	conn, err := h.libvirtPool.Connector(ctx, target)
	cancel()
	if err != nil {
		slog.Warn("remote_node_connect_failed", "node", target.ID, "err", libvirt.ClassifyNodeError(err))
		writeNodeError(w, target, err)
		return nil, false
	}
	return h.forBackend(compute.NewKVMBackend(conn), conn, target), true
}

// unsupportedListingBody is the 501 remote_unsupported body for listings,
// with ui_url when the operator configured the node's address.
func unsupportedListingBody(n nodes.Node, msg string) map[string]string {
	b := map[string]string{
		"error": msg, "code": "remote_unsupported", "hint": "open_node_ui",
		"node": n.Name, "node_id": n.ID,
	}
	if n.UIURL != "" {
		b["ui_url"] = n.UIURL
	}
	return b
}
