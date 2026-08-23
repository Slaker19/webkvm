package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// EventsSSE serves a Server-Sent Events stream of VM state changes.
//
// Auth: the global JWT middleware validates the token. For this SSE endpoint
// the token is passed via the `?token=` query param because EventSource
// cannot set request headers. That query-param path is ONLY honored for
// /api/events; all other routes require the Authorization header.
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
