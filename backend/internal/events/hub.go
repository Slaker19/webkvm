// Package events provides a simple fan-out event hub for broadcasting
// state changes (e.g. libvirt VM lifecycle events) to multiple subscribers
// (typically SSE HTTP handlers).
//
// Usage:
//
//	hub := events.NewHub()
//	go hub.Run(ctx)  // not strictly required, but useful for graceful shutdown
//
//	id, ch, cancel := hub.Subscribe()
//	defer cancel()
//	for e := range ch {
//	    // handle event
//	}
//
//	hub.Broadcast(events.Event{Type: "vm.state", VmID: "...", State: "running"})
//
// Concurrency: Hub is safe for concurrent use. Broadcast, Subscribe, the
// returned cancel func, and Close may all be called from any goroutine.
//
// The previous implementation closed subscriber channels from cancel()/Close
// while Broadcast could still send on them, panicking with "send on closed
// channel" under concurrent load. Both the send (in Broadcast) and the close
// (in cancel/Close) now run under the same mutex, so they can never overlap.
// The event channel is always safe to range over; it is closed exactly once
// when the subscriber is removed.
package events

import (
	"context"
	"sync"
	"sync/atomic"
)

// Event is the wire format for a single broadcast message.
// Fields are JSON-tagged so they serialize directly into SSE `data:` lines.
type Event struct {
	Type      string `json:"type"`         // e.g. "vm.state", "vm.removed", "vm.metrics"
	VmID      string `json:"vm_id"`        // libvirt UUID
	State     string `json:"state"`        // "running" | "shutoff" | "paused" | "crashed" | "unknown"
	Name      string `json:"name"`         // VM name (convenience for the UI)
	Timestamp int64  `json:"timestamp"`    // unix seconds
	Data      any    `json:"data,omitempty"` // optional payload (e.g. metrics series)
}

// Hub is a thread-safe broadcaster. Zero value is not usable; use NewHub.
type Hub struct {
	mu      sync.Mutex
	clients map[uint64]chan Event
	nextID  uint64
	closed  atomic.Bool
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[uint64]chan Event),
	}
}

// Subscribe registers a new client. The returned channel is buffered; if
// the consumer falls behind, events are dropped (non-blocking send) to
// avoid stalling broadcasters.
//
// The returned cancel function removes the client and closes its channel.
// After cancel returns, ranging over the channel will end. Always call
// cancel (or defer it) to avoid leaking the subscription.
func (h *Hub) Subscribe() (id uint64, ch chan Event, cancel func()) {
	id = atomic.AddUint64(&h.nextID, 1)
	ch = make(chan Event, 16)
	h.mu.Lock()
	h.clients[id] = ch
	h.mu.Unlock()

	cancel = func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if c, ok := h.clients[id]; ok {
			delete(h.clients, id)
			close(c)
		}
	}
	return id, ch, cancel
}

// Broadcast sends an event to every subscribed client. Non-blocking:
// if a client's buffer is full (or it is being torn down) the event is
// dropped for that client.
//
// The whole iteration runs under h.mu so that no subscriber channel can be
// closed (by cancel/Close) while we are sending on it — this is what makes
// the send safe and avoids the historic "send on closed channel" panic.
func (h *Hub) Broadcast(e Event) {
	if h.closed.Load() {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.clients {
		select {
		case ch <- e:
		default:
			// Drop event for slow consumer.
		}
	}
}

// ClientCount returns the number of active subscribers.
func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// Run blocks until ctx is cancelled. It exists so callers can hold a single
// reference to the hub and let it clean up on shutdown. Calling Run is
// optional — Broadcast and Subscribe are safe to call from any goroutine
// at any time.
func (h *Hub) Run(ctx context.Context) {
	<-ctx.Done()
	h.Close()
}

// Close disconnects all clients. After Close, Broadcast is a no-op.
// Safe to call multiple times.
func (h *Hub) Close() {
	if h.closed.Swap(true) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ch := range h.clients {
		delete(h.clients, id)
		close(ch)
	}
}
