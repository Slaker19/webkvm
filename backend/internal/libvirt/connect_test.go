package libvirt

import "testing"

// testConnURI is libvirt's built-in fake driver — a real (in-process)
// connection with no hypervisor required, so these tests exercise the
// actual libvirt-go connection lifecycle instead of mocking it away.
const testConnURI = "test:///default"

func TestOpen_ConnectsSuccessfully(t *testing.T) {
	c := NewConnector(testConnURI, nil)
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	if !c.IsConnected() {
		t.Error("IsConnected() = false after a successful Open()")
	}
}

func TestOpen_IsIdempotent(t *testing.T) {
	c := NewConnector(testConnURI, nil)
	if err := c.Open(); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	defer c.Close()

	// Calling Open() again (e.g. a reconnect attempt) must not leak or
	// panic on the previous handle — it should just replace it.
	if err := c.Open(); err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if !c.IsConnected() {
		t.Error("IsConnected() = false after re-Open()")
	}
}

func TestEnsureConnected_RecoversFromADeadConnection(t *testing.T) {
	c := NewConnector(testConnURI, nil)
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	if !c.IsConnected() {
		t.Fatal("precondition failed: not connected after Open()")
	}

	// Simulate a mid-runtime drop (libvirtd restart, network blip, ...)
	// by killing the underlying handle directly, the same way a dead
	// socket would surface: IsAlive() starts failing.
	c.mu.Lock()
	c.conn.Close()
	c.mu.Unlock()

	if c.IsConnected() {
		t.Fatal("precondition failed: still reports connected after killing the handle")
	}

	// Before the fix, ensureConnected only ever checked liveness and
	// never reopened — this would have returned an error forever.
	if err := c.ensureConnected(); err != nil {
		t.Fatalf("ensureConnected did not recover: %v", err)
	}
	if !c.IsConnected() {
		t.Error("IsConnected() = false after ensureConnected() recovered")
	}
}

func TestEnsureConnected_NoOpWhenAlreadyAlive(t *testing.T) {
	c := NewConnector(testConnURI, nil)
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	before := c.Get()
	if err := c.ensureConnected(); err != nil {
		t.Fatalf("ensureConnected: %v", err)
	}
	after := c.Get()
	if before != after {
		t.Error("ensureConnected reopened a connection that was already alive")
	}
}

func TestEnsureConnected_FailsCleanlyWithNoURIAndNoConnection(t *testing.T) {
	c := NewConnector("test:///nonexistent-driver-path", nil)
	if err := c.ensureConnected(); err == nil {
		t.Error("expected an error reconnecting to a bogus URI, got nil")
	}
}

func TestIsConnected_FalseBeforeOpen(t *testing.T) {
	c := NewConnector(testConnURI, nil)
	if c.IsConnected() {
		t.Error("IsConnected() = true before Open() was ever called")
	}
}

func TestClose_IsSafeToCallTwice(t *testing.T) {
	c := NewConnector(testConnURI, nil)
	if err := c.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	c.Close()
	c.Close() // must not panic
	if c.IsConnected() {
		t.Error("IsConnected() = true after Close()")
	}
}
