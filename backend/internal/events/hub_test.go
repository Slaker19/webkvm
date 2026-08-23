package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}

func TestHub_SubscribeReceiveOne(t *testing.T) {
	h := NewHub()
	defer h.Close()

	id, ch, cancel := h.Subscribe()
	defer cancel()

	want := Event{Type: "vm.state", VmID: "vm-1", State: "running"}
	go h.Broadcast(want)

	select {
	case got := <-ch:
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}

	if id == 0 {
		t.Errorf("id should be non-zero")
	}
	if h.ClientCount() != 1 {
		t.Errorf("ClientCount = %d, want 1", h.ClientCount())
	}
}

func TestHub_MultipleSubscribersAllReceive(t *testing.T) {
	h := NewHub()
	defer h.Close()

	const n = 10
	chs := make([]chan Event, n)
	cancels := make([]func(), n)
	for i := 0; i < n; i++ {
		_, chs[i], cancels[i] = h.Subscribe()
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	want := Event{Type: "vm.removed", VmID: "vm-1"}
	h.Broadcast(want)

	for i, ch := range chs {
		select {
		case got := <-ch:
			if got != want {
				t.Errorf("subscriber %d: got %+v, want %+v", i, got, want)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d timeout", i)
		}
	}
}

func TestHub_UnsubscribeStopsDelivery(t *testing.T) {
	h := NewHub()
	defer h.Close()

	_, ch, cancel := h.Subscribe()
	cancel() // unsubscribe immediately

	// Channel should be closed.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected closed channel after unsubscribe")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after unsubscribe")
	}

	// ClientCount should drop to 0.
	if h.ClientCount() != 0 {
		t.Errorf("ClientCount = %d, want 0", h.ClientCount())
	}

	// Calling cancel twice is a no-op (idempotent).
	cancel()
}

func TestHub_SlowConsumerDropsEvents(t *testing.T) {
	h := NewHub()
	defer h.Close()

	_, ch, cancel := h.Subscribe()
	defer cancel()

	// Fill the buffer (16) plus more — extras should be dropped.
	for i := 0; i < 100; i++ {
		h.Broadcast(Event{Type: "vm.metrics", VmID: "vm-1"})
	}

	// We expect ~16 events, not 100. Drain with a short timeout.
	got := 0
drain:
	for {
		select {
		case <-ch:
			got++
		case <-time.After(50 * time.Millisecond):
			break drain
		}
	}
	if got == 0 {
		t.Error("expected some events, got 0")
	}
	if got > 16 {
		t.Errorf("got %d events, want at most 16 (buffer size)", got)
	}
}

func TestHub_CloseDisconnectsAll(t *testing.T) {
	h := NewHub()
	_, ch1, c1 := h.Subscribe()
	_, ch2, c2 := h.Subscribe()
	_, ch3, c3 := h.Subscribe()
	defer c1()
	defer c2()
	defer c3()

	if h.ClientCount() != 3 {
		t.Errorf("ClientCount = %d, want 3", h.ClientCount())
	}

	h.Close()

	// All channels closed.
	for i, ch := range []chan Event{ch1, ch2, ch3} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("channel %d: expected closed", i)
			}
		case <-time.After(time.Second):
			t.Errorf("channel %d: not closed", i)
		}
	}

	if h.ClientCount() != 0 {
		t.Errorf("ClientCount after close = %d, want 0", h.ClientCount())
	}
}

func TestHub_BroadcastAfterCloseIsNoop(t *testing.T) {
	h := NewHub()
	_, ch, cancel := h.Subscribe()
	defer cancel()
	h.Close()

	// Should not panic, should not deliver.
	h.Broadcast(Event{Type: "x"})

	select {
	case ev, ok := <-ch:
		if ok {
			t.Errorf("got event after close: %+v", ev)
		}
	case <-time.After(50 * time.Millisecond):
		// OK
	}
}

func TestHub_CloseIdempotent(t *testing.T) {
	h := NewHub()
	_, _, cancel := h.Subscribe()
	defer cancel()

	h.Close()
	h.Close() // should not panic or block
}

func TestHub_RunRespectsContext(t *testing.T) {
	h := NewHub()
	_, _, cancel := h.Subscribe()
	defer cancel()

	ctx, ctxCancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Run(ctx)
		close(done)
	}()

	ctxCancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}
}

func TestHub_ConcurrentSubscribeBroadcast(t *testing.T) {
	h := NewHub()
	defer h.Close()

	const subs = 10
	const broadcasts = 100

	var wg sync.WaitGroup
	wg.Add(subs + 1)

	var received int64

	// Start subscribers.
	cancels := make([]func(), subs)
	for i := 0; i < subs; i++ {
		_, ch, c := h.Subscribe()
		cancels[i] = c
		go func() {
			defer wg.Done()
			for range ch {
				atomic.AddInt64(&received, 1)
			}
		}()
	}

	// Start broadcaster.
	go func() {
		defer wg.Done()
		for i := 0; i < broadcasts; i++ {
			h.Broadcast(Event{Type: "vm.state", VmID: "vm-1", State: "running"})
		}
	}()

	// Let things settle.
	time.Sleep(50 * time.Millisecond)
	for _, c := range cancels {
		c()
	}
	wg.Wait()

	// We expect at least 1 event per subscriber (subs), and at most
	// subs*broadcasts (every broadcast hit every sub).
	got := atomic.LoadInt64(&received)
	if got < int64(subs) {
		t.Errorf("got %d events, want >= %d", got, subs)
	}
	if got > int64(subs*broadcasts) {
		t.Errorf("got %d events, want <= %d", got, subs*broadcasts)
	}
}
