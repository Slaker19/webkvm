package libvirt

import (
	"testing"
	"time"
)

func TestPoolWarnDeduplicatesRepeatedConnectFailures(t *testing.T) {
	p := NewPool(nil)
	defer p.Close()
	clock := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }

	if !p.shouldWarn("n1", "no_key") {
		t.Fatal("the first failure must warn")
	}
	for i := 0; i < 50; i++ { // probed every few seconds
		clock = clock.Add(10 * time.Second)
		if p.shouldWarn("n1", "no_key") {
			t.Fatalf("repeat #%d within the window must not warn", i)
		}
	}
	if !p.shouldWarn("n1", "ssh_auth_denied") {
		t.Fatal("a different failure code is news and must warn")
	}
	if !p.shouldWarn("n2", "no_key") {
		t.Fatal("another node has its own state and must warn")
	}
	clock = clock.Add(poolWarnRepeat + time.Second)
	if !p.shouldWarn("n1", "ssh_auth_denied") {
		t.Fatal("the same failure must warn again after the repeat window")
	}
}

func TestPoolWarnResetsAfterASuccessfulConnect(t *testing.T) {
	p := NewPool(nil)
	defer p.Close()
	clock := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return clock }

	if !p.shouldWarn("n1", "node_unreachable") {
		t.Fatal("first failure must warn")
	}
	p.mu.Lock()
	delete(p.warned, "n1") // what a successful connect does
	p.mu.Unlock()
	clock = clock.Add(time.Second)
	if !p.shouldWarn("n1", "node_unreachable") {
		t.Fatal("after a success a new failure is news again and must warn")
	}
}
