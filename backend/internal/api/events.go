package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/auth"
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
	tk, err := auth.IssueTicket(user, role)
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
