package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/models"
)

// EventsTicket issues a short-lived, single-use ticket the frontend
// exchanges for SSE access (?ticket=... on /api/events), the same
// mechanism used for the VM console/serial WebSocket endpoints. This
// keeps the caller's long-lived JWT out of the request URL — a raw
// bearer token in a query string ends up durably logged (reverse-proxy
// access logs, this backend's own request logger, browser history)
// where anyone with log access could replay it until it expires.
func (h *Handler) EventsTicket(w http.ResponseWriter, r *http.Request) {
	user, role, _ := audit.FromRequest(r)
	tk, err := auth.IssueTicket(user, role, auth.TokenEpoch(r))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "issue ticket: "+err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"ticket": tk, "expires_in": 30})
}

// EventsSSE serves a Server-Sent Events stream of VM state changes.
//
// Auth: EventSource cannot set request headers, so the caller first
// exchanges its JWT for a short-lived ticket via EventsTicket, then
// connects here with `?ticket=...`. The global JWT middleware's
// generic ticket branch validates and burns it before this handler runs.
//
// Wire format: each event is one SSE message:
//
//	id: <auto>
//	event: vm.state
//	data: {"type":"vm.state","vm_id":"...","state":"running",...}
//
// A trailing keep-alive comment is sent every 25 seconds to keep proxies
// from closing the connection.
func (h *Handler) EventsSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonErr(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	id, ch, cancel := h.hub.Subscribe()
	defer cancel()

	// Send a hello event so the client knows it's connected
	fmt.Fprintf(w, "event: connected\ndata: {\"ok\":true}\n\n")
	flusher.Flush()

	// seen records every VM this subscriber was allowed to see. A
	// vm.removed event arrives after the domain (and its metadata) is
	// gone, so requireVMAccess can no longer resolve the owner and would
	// drop it; membership here is the proof the caller had access.
	// Seeded with the caller's current fleet so a VM that never changed
	// state during this connection still gets its removal delivered.
	seen := h.initialVisibleVMs(r)

	keepAlive := time.NewTicker(25 * time.Second)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			// SSE comment line keeps the connection open
			fmt.Fprintf(w, ": keep-alive\n\n")
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			// Multi-tenant isolation: drop events for instances the caller
			// is not authorized to see.
			if e.VmID != "" {
				if e.Type == "vm.removed" && seen[e.VmID] {
					delete(seen, e.VmID)
				} else if h.requireVMAccess(r, e.VmID) != nil {
					// Access was revoked since the VM was seen (owner,
					// group or tag change): forget it, or its later
					// vm.removed would still be delivered.
					if e.Type != "vm.removed" {
						delete(seen, e.VmID)
					}
					continue
				} else if e.Type != "vm.removed" {
					seen[e.VmID] = true
				}
			}
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			evt := e.Type
			if evt == "" {
				evt = "message"
			}
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", id, evt, data)
			flusher.Flush()
		}
	}
}

// initialVisibleVMs returns the IDs of the VMs the caller can currently
// see (nil-safe; admins pass requireVMAccess for every event anyway, so
// they skip the listing).
func (h *Handler) initialVisibleVMs(r *http.Request) map[string]bool {
	seen := map[string]bool{}
	if _, role, _ := audit.FromRequest(r); role == models.RoleAdmin || h.compute == nil {
		return seen
	}
	vms, err := h.compute.ListDomains()
	if err != nil {
		return seen
	}
	for _, vm := range h.filterVMsByACL(r, vms) {
		seen[vm.ID] = true
	}
	return seen
}
