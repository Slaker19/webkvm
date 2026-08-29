package libvirt

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"libvirt.org/go/libvirt"
	"webkvm/internal/events"
)

// StartEventLoop registers a libvirt domain-event callback that broadcasts
// VM lifecycle changes to the supplied hub, and runs the libvirt event loop
// in a goroutine.
//
// If libvirt event registration fails (e.g. on older libvirt builds that
// lack the event API), StartEventLoop falls back to polling ListDomains()
// every pollInterval and diffing states — broadcasting only on change.
// This guarantees the SSE channel receives updates even when the native
// event API is unavailable.
//
// The returned function stops the loop. It is safe to call multiple times.
func (c *Connector) StartEventLoop(ctx context.Context, hub *events.Hub, pollInterval time.Duration) (stop func()) {
	if pollInterval <= 0 {
		pollInterval = 4 * time.Second
	}

	stopCh := make(chan struct{})
	stopped := atomic.Bool{}

	// Try to use the native event API
	conn := c.Get()
	if conn == nil {
		slog.Info("event_loop_no_libvirt_polling")
	} else {
		callbackID, err := conn.DomainEventLifecycleRegister(nil,
			func(_ *libvirt.Connect, d *libvirt.Domain, event *libvirt.DomainEventLifecycle) {
				if d == nil || event == nil {
					return
				}
				name, _ := d.GetName()
				uuid, _ := d.GetUUIDString()
				hub.Broadcast(events.Event{
					Type:      "vm.state",
					VmID:      uuid,
					State:     stateToString(event.Event),
					Name:      name,
					Timestamp: time.Now().Unix(),
				})
			})
		if err != nil {
			slog.Warn("event_loop_libvirt_register_failed_polling", "err", err)
		} else {
			slog.Info("event_loop_active", "callback_id", callbackID)
			// Run the libvirt event loop in a goroutine
			go func() {
				if err := libvirt.EventRunDefaultImpl(); err != nil {
					slog.Warn("event_loop_default_impl_exited", "err", err)
				}
			}()

			// Stop function deregisters the callback
			return func() {
				if !stopped.CompareAndSwap(false, true) {
					return
				}
				close(stopCh)
				if conn := c.Get(); conn != nil {
					if err := conn.DomainEventDeregister(callbackID); err != nil {
						slog.Warn("event_loop_deregister_failed", "err", err)
					}
				}
			}
		}
	}

	// Polling fallback (also runs as a backup alongside native events)
	go pollLoop(ctx, hub, c, pollInterval, stopCh)

	return func() {
		if !stopped.CompareAndSwap(false, true) {
			return
		}
		close(stopCh)
	}
}

func pollLoop(ctx context.Context, hub *events.Hub, c *Connector, interval time.Duration, stop <-chan struct{}) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	last := map[string]string{} // vm uuid -> state
	for {
		select {
		case <-ctx.Done():
			return
		case <-stop:
			return
		case <-ticker.C:
			if !c.IsConnected() {
				continue
			}
			conn := c.Get()
			if conn == nil {
				continue
			}
			doms, err := conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_ACTIVE | libvirt.CONNECT_LIST_DOMAINS_INACTIVE)
			if err != nil {
				continue
			}
			current := map[string]string{}
			for _, dom := range doms {
				uuid, _ := dom.GetUUIDString()
				name, _ := dom.GetName()
				state, _, _ := dom.GetState()
				s := stateToStringFromState(state)
				current[uuid] = s

				if prev, ok := last[uuid]; !ok || prev != s {
					hub.Broadcast(events.Event{
						Type:      "vm.state",
						VmID:      uuid,
						State:     s,
						Name:      name,
						Timestamp: time.Now().Unix(),
					})
				}
				dom.Free()
			}
			// Broadcast "removed" for VMs that disappeared
			for uuid := range last {
				if _, ok := current[uuid]; !ok {
					hub.Broadcast(events.Event{
						Type:      "vm.removed",
						VmID:      uuid,
						Timestamp: time.Now().Unix(),
					})
				}
			}
			last = current
		}
	}
}

// stateToString maps a libvirt DomainEventType to the canonical state
// string the frontend uses ("running" | "shutoff" | "paused" | "crashed").
func stateToString(t libvirt.DomainEventType) string {
	switch t {
	case libvirt.DOMAIN_EVENT_STARTED:
		return "running"
	case libvirt.DOMAIN_EVENT_STOPPED, libvirt.DOMAIN_EVENT_SHUTDOWN:
		return "shutoff"
	case libvirt.DOMAIN_EVENT_SUSPENDED, libvirt.DOMAIN_EVENT_PMSUSPENDED:
		return "paused"
	case libvirt.DOMAIN_EVENT_RESUMED:
		return "running"
	default:
		return "unknown"
	}
}

func stateToStringFromState(s libvirt.DomainState) string {
	switch s {
	case libvirt.DOMAIN_RUNNING, libvirt.DOMAIN_BLOCKED:
		return "running"
	case libvirt.DOMAIN_SHUTOFF, libvirt.DOMAIN_SHUTDOWN:
		return "shutoff"
	case libvirt.DOMAIN_PAUSED:
		return "paused"
	case libvirt.DOMAIN_CRASHED:
		return "crashed"
	default:
		return "unknown"
	}
}
