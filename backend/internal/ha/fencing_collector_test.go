package ha

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu    sync.Mutex
	chans chan chan time.Time
	waits []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{chans: make(chan chan time.Time, 16)} }
func (f *fakeClock) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	f.waits = append(f.waits, d)
	f.mu.Unlock()
	ch := make(chan time.Time, 1)
	f.chans <- ch
	return ch
}
func (f *fakeClock) fire(t *testing.T) {
	t.Helper()
	select {
	case ch := <-f.chans:
		ch <- time.Now()
	case <-time.After(2 * time.Second):
		t.Fatal("collector did not wait on the clock")
	}
}

func TestCollectorMergesWithoutReadersAndStops(t *testing.T) {
	dir := t.TempDir()
	clk := newFakeClock()
	var mu sync.Mutex
	probes := 0
	c := &FencingCollector{DataDir: dir, Enabled: func() bool { return true }, After: clk.After,
		ProbeStatus: func(context.Context) PacemakerStatus {
			mu.Lock()
			probes++
			mu.Unlock()
			st := ledgerSt("c", okEv("n2", "2026-11-12 10:00:00 +00:00"))
			st.State = StateObserved
			return st
		}}
	ctx, cancel := context.WithCancel(context.Background())
	done := c.Start(ctx)
	clk.fire(t) // first run
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(FencingLedgerPath(dir)); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if led := loadLedger(FencingLedgerPath(dir)); len(led.Events) != 1 {
		t.Fatalf("%+v", led)
	}
	// Single-flight: a second Start is a no-op.
	select {
	case <-c.Start(ctx):
	case <-time.After(time.Second):
		t.Fatal("second Start must return a closed channel")
	}
	// No write when nothing changed.
	fi1, _ := os.Stat(FencingLedgerPath(dir))
	clk.fire(t)
	clk.fire(t)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("collector did not stop on cancel")
	}
	fi2, _ := os.Stat(FencingLedgerPath(dir))
	if !fi1.ModTime().Equal(fi2.ModTime()) {
		t.Fatal("ledger rewritten without change")
	}
	if clk.waits[0] != 10*time.Second || clk.waits[1] != 60*time.Second {
		t.Fatalf("%v", clk.waits)
	}
}

func TestCollectorDisabledExecsNothing(t *testing.T) {
	called := false
	c := &FencingCollector{DataDir: t.TempDir(), Enabled: func() bool { return false },
		ProbeStatus: func(context.Context) PacemakerStatus { called = true; return PacemakerStatus{} }}
	if c.Tick(context.Background()) || called {
		t.Fatal("disabled module must not probe")
	}
	c.Enabled = nil
	c.Tick(context.Background())
	if called {
		t.Fatal("nil gate must not probe")
	}
}

func TestCollectorBackoff(t *testing.T) {
	c := &FencingCollector{}
	c.defaults()
	var got []time.Duration
	for i := 0; i < 6; i++ {
		got = append(got, c.next(true))
	}
	if got[0] != 60*time.Second || got[1] != 60*time.Second || got[2] != 120*time.Second || got[5] != 5*time.Minute {
		t.Fatalf("%v", got)
	}
	if d := c.next(false); d != 60*time.Second || c.failures != 0 {
		t.Fatalf("%v", d)
	}
}
