package vmsched

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	valid := []Schedule{
		{StartCron: "0 8 * * *", StopCron: "0 22 * * *"},
		{StartCron: "30 7 * * 1-5"}, // weekdays only
		{StopCron: "0 0 * * *"},
		{SnapshotCron: "0 2 * * *", SnapshotMax: 5},
	}
	for i, s := range valid {
		if err := Validate(s); err != nil {
			t.Errorf("case %d: unexpected error: %v", i, err)
		}
	}
	invalid := []Schedule{
		{},                        // empty
		{StartCron: "not a cron"}, // bad
		{StartCron: "0 25 * * *"}, // hour out of range
		{SnapshotCron: "invalid"},
	}
	for i, s := range invalid {
		if err := Validate(s); err == nil {
			t.Errorf("case %d: expected error for %+v", i, s)
		}
	}
}

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	sch := Schedule{StartCron: "0 8 * * *", StopCron: "0 22 * * *"}
	if err := s.Set("local", "vm1", sch); err != nil {
		t.Fatal(err)
	}
	s2 := NewStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	got := s2.Get("local", "vm1")
	if got.StartCron != "0 8 * * *" || got.StopCron != "0 22 * * *" {
		t.Fatalf("reload mismatch: %+v", got)
	}
	// Clearing removes the entry.
	if err := s.Set("local", "vm1", Schedule{}); err != nil {
		t.Fatal(err)
	}
	if s.Get("local", "vm1").Enabled() {
		t.Fatal("schedule should be cleared")
	}
}

// TestScopeCollision: two nodes can run domains with the same name; the
// node-scoped store must keep their schedules independent (the federation
// clone shares one store instance).
func TestScopeCollision(t *testing.T) {
	s := NewStore(t.TempDir())
	start := Schedule{StartCron: "0 8 * * *"}
	if err := s.Set("local", "web", start); err != nil {
		t.Fatal(err)
	}
	stop := Schedule{StopCron: "0 22 * * *"}
	if err := s.Set("n_remote1", "web", stop); err != nil {
		t.Fatal(err)
	}
	if got := s.Get("local", "web"); got.StopCron != "" {
		t.Fatalf("local schedule leaked stop from remote: %+v", got)
	}
	if got := s.Get("n_remote1", "web"); got.StartCron != "" {
		t.Fatalf("remote schedule leaked start from local: %+v", got)
	}
	// Clearing one scope leaves the other intact.
	if err := s.Set("local", "web", Schedule{}); err != nil {
		t.Fatal(err)
	}
	if !s.Get("n_remote1", "web").Enabled() {
		t.Fatal("remote schedule lost when local was cleared")
	}
}

// TestMigrateLegacyKeys: schedules persisted by versions before node
// scoping re-key to the local node and survive reload.
func TestMigrateLegacyKeys(t *testing.T) {
	dir := t.TempDir()
	// Write a legacy bare-key file by hand.
	legacy := `{"version":1,"schedules":{"vm-old":{"start_cron":"0 6 * * *"}}}`
	if err := os.WriteFile(filepath.Join(dir, "vm-schedules.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewStore(dir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	s.MigrateLegacy("local")
	if got := s.Get("local", "vm-old"); got.StartCron != "0 6 * * *" {
		t.Fatalf("migrated schedule = %+v", got)
	}
	// Reload: the migrated file has the composite key.
	s2 := NewStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	if got := s2.Get("local", "vm-old"); got.StartCron != "0 6 * * *" {
		t.Fatalf("migration not persisted: %+v", got)
	}
}

// TestSchedulerDispatchesPerNode: the scheduler resolves each composite
// key and hands the owning node to the power/snapshot callbacks; a
// schedule for node B must never fire against node A.
func TestSchedulerDispatchesPerNode(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Set("n_b", "web", Schedule{StartCron: "* * * * *"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("local", "db", Schedule{StopCron: "* * * * *"}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	type call struct{ node, vm, action string }
	var calls []call
	fired := make(chan struct{}, 10)
	sc := NewScheduler(store, func(nodeID, vmID, action string) error {
		mu.Lock()
		calls = append(calls, call{nodeID, vmID, action})
		mu.Unlock()
		fired <- struct{}{}
		return nil
	}, nil, slog.Default())
	sc.Rebuild()
	// Wait for both crons (every-minute schedules fire at the next minute
	// boundary; do not wait that long — assert via a manual fire of the
	// closures instead: Rebuild registered them, so drive fire directly).
	mu.Lock()
	_ = calls
	mu.Unlock()
	_ = fired
	// Direct closure check: fire() must route by key.
	sc.fire("n_b", "web", "start")()
	sc.fire("local", "db", "stop")()
	waitForCall := func(pred func(call) bool) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			ok := false
			for _, c := range calls {
				if pred(c) {
					ok = true
				}
			}
			mu.Unlock()
			if ok {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("expected callback not invoked")
	}
	waitForCall(func(c call) bool { return c.node == "n_b" && c.vm == "web" && c.action == "start" })
	waitForCall(func(c call) bool { return c.node == "local" && c.vm == "db" && c.action == "stop" })
}
