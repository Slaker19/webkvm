package netguard

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// The incident this package exists for: one client retrying a failed TLS
// handshake ~83 times/second wrote one journal line per attempt, which is
// ~2 GB/day. This is the regression test for that amplification.
func TestAggregatingWriter_CollapsesHandshakeFlood(t *testing.T) {
	var out bytes.Buffer
	var summaries []string
	w := NewAggregatingWriter(&out, func(msg string, n int, _, _ time.Time) {
		summaries = append(summaries, fmt.Sprintf("%s x%d", msg, n))
	}, time.Hour) // never auto-flush; we flush explicitly
	defer w.Close()

	// 5000 failed handshakes from the same peer, each on a new ephemeral
	// port, exactly as observed on the wire.
	const floods = 5000
	for i := 0; i < floods; i++ {
		line := fmt.Sprintf("http: TLS handshake error from 192.168.1.171:%d: remote error: tls: unknown certificate\n", 34000+i)
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Only the first line may reach the log.
	if got := strings.Count(out.String(), "\n"); got != 1 {
		t.Errorf("passthrough wrote %d lines, want 1 (the flood was not collapsed)", got)
	}

	w.Flush()
	if len(summaries) != 1 {
		t.Fatalf("got %d summaries, want 1: %v", len(summaries), summaries)
	}
	if want := fmt.Sprintf("x%d", floods-1); !strings.Contains(summaries[0], want) {
		t.Errorf("summary %q does not report %s suppressed", summaries[0], want)
	}

	// The whole point: bytes written must not scale with attempts.
	if out.Len() > 200 {
		t.Errorf("wrote %d bytes for %d attempts; amplification not fixed", out.Len(), floods)
	}
}

// Distinct peers are distinct problems and must not be merged, otherwise a
// real outage affecting many clients would be hidden by a noisy one.
func TestAggregatingWriter_SeparatesDistinctPeers(t *testing.T) {
	var out bytes.Buffer
	seen := map[string]int{}
	w := NewAggregatingWriter(&out, func(msg string, n int, _, _ time.Time) {
		seen[msg] = n
	}, time.Hour)
	defer w.Close()

	for i := 0; i < 10; i++ {
		w.Write([]byte("http: TLS handshake error from 192.168.1.171:5000: bad cert\n"))
		w.Write([]byte("http: TLS handshake error from 10.0.0.9:6000: bad cert\n"))
	}
	w.Flush()

	if len(seen) != 2 {
		t.Fatalf("got %d distinct summaries, want 2: %v", len(seen), seen)
	}
	for msg, n := range seen {
		if n != 9 {
			t.Errorf("peer %q reported %d suppressed, want 9", msg, n)
		}
	}
}

// A one-off error must never be delayed or swallowed; only repeats are.
func TestAggregatingWriter_SingleErrorPassesThroughImmediately(t *testing.T) {
	var out bytes.Buffer
	var summaries int
	w := NewAggregatingWriter(&out, func(string, int, time.Time, time.Time) { summaries++ }, time.Hour)
	defer w.Close()

	w.Write([]byte("http: some genuinely rare error\n"))
	if !strings.Contains(out.String(), "genuinely rare") {
		t.Error("a single error must reach the log immediately")
	}
	w.Flush()
	if summaries != 0 {
		t.Errorf("got %d summaries for a non-repeated error, want 0", summaries)
	}
}

// After a quiet period the same message must pass through again, so a
// recurring-but-infrequent problem stays visible.
func TestAggregatingWriter_RepassesAfterFlush(t *testing.T) {
	var out bytes.Buffer
	w := NewAggregatingWriter(&out, func(string, int, time.Time, time.Time) {}, time.Hour)
	defer w.Close()

	w.Write([]byte("http: TLS handshake error from 1.2.3.4:1: x\n"))
	w.Flush()
	w.Write([]byte("http: TLS handshake error from 1.2.3.4:2: x\n"))

	if got := strings.Count(out.String(), "\n"); got != 2 {
		t.Errorf("got %d passthrough lines, want 2 (message did not re-pass after flush)", got)
	}
}

func TestAggregatingWriter_CloseFlushesFinalWindow(t *testing.T) {
	var mu sync.Mutex
	var got int
	w := NewAggregatingWriter(nil, func(_ string, n int, _, _ time.Time) {
		mu.Lock()
		got = n
		mu.Unlock()
	}, time.Hour)

	for i := 0; i < 5; i++ {
		w.Write([]byte("http: TLS handshake error from 9.9.9.9:1: x\n"))
	}
	w.Close() // must emit the pending window rather than drop it

	mu.Lock()
	defer mu.Unlock()
	if got != 4 {
		t.Errorf("Close reported %d suppressed, want 4", got)
	}
}

func TestAggregatingWriter_ConcurrentWritesAreSafe(t *testing.T) {
	w := NewAggregatingWriter(nil, func(string, int, time.Time, time.Time) {}, 5*time.Millisecond)
	defer w.Close()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				w.Write([]byte(fmt.Sprintf("http: TLS handshake error from 192.168.1.%d:%d: x\n", g, i)))
			}
		}(g)
	}
	wg.Wait()
}

// --- LimitListener -------------------------------------------------------

func TestLimitListener_CapsConnectionsPerIP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	const limit = 4
	ll := NewLimitListener(ln, limit)
	defer ll.Close()

	var rejected int
	var rmu sync.Mutex
	ll.OnReject = func(string, int) {
		rmu.Lock()
		rejected++
		rmu.Unlock()
	}

	accepted := make(chan net.Conn, 64)
	go func() {
		for {
			c, err := ll.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	// Open well past the limit from one IP (loopback), as the runaway
	// client did.
	var dialed []net.Conn
	for i := 0; i < limit*3; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		dialed = append(dialed, c)
	}
	defer func() {
		for _, c := range dialed {
			c.Close()
		}
	}()

	var held []net.Conn
	deadline := time.After(2 * time.Second)
collect:
	for len(held) < limit {
		select {
		case c := <-accepted:
			held = append(held, c)
		case <-deadline:
			break collect
		}
	}

	if len(held) != limit {
		t.Fatalf("accepted %d connections, want exactly %d", len(held), limit)
	}
	// Nothing beyond the cap may be handed to the server.
	select {
	case c := <-accepted:
		t.Fatalf("accepted a connection past the limit: %v", c.RemoteAddr())
	case <-time.After(150 * time.Millisecond):
	}
	if ll.ActiveFor("127.0.0.1") != limit {
		t.Errorf("ActiveFor=%d, want %d", ll.ActiveFor("127.0.0.1"), limit)
	}

	rmu.Lock()
	gotRejected := rejected
	rmu.Unlock()
	if gotRejected == 0 {
		t.Error("no rejections recorded; the cap was never enforced")
	}
}

// Reaching the cap must not wedge the listener shut: once a held
// connection is closed, the freed slot has to admit a new client. Without
// this, the first burst would lock legitimate users out permanently.
func TestLimitListener_FreedSlotAdmitsNewClient(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	const limit = 2
	ll := NewLimitListener(ln, limit)
	defer ll.Close()

	accepted := make(chan net.Conn, 16)
	go func() {
		for {
			c, err := ll.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()

	// Saturate the cap.
	var clients []net.Conn
	for i := 0; i < limit; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		clients = append(clients, c)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	var held []net.Conn
	for i := 0; i < limit; i++ {
		select {
		case c := <-accepted:
			held = append(held, c)
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d connections accepted", len(held), limit)
		}
	}
	if n := ll.ActiveFor("127.0.0.1"); n != limit {
		t.Fatalf("ActiveFor=%d, want %d", n, limit)
	}

	// A further client is refused while the cap is full.
	extra, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial extra: %v", err)
	}
	defer extra.Close()
	select {
	case c := <-accepted:
		t.Fatalf("accepted past the cap: %v", c.RemoteAddr())
	case <-time.After(150 * time.Millisecond):
	}

	// Free one slot from the server side, which is what net/http does
	// when a handler returns and the connection is torn down.
	held[0].Close()
	waitFor(t, func() bool { return ll.ActiveFor("127.0.0.1") == limit-1 })

	// The freed slot must now admit a brand-new client.
	fresh, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial fresh: %v", err)
	}
	defer fresh.Close()
	select {
	case c := <-accepted:
		c.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("freed slot did not admit a new client; listener is wedged")
	}
}

// net/http may Close a connection more than once; the counter must not
// drift negative or the effective limit would grow over time.
func TestLimitListener_DoubleCloseDoesNotDriftCounter(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	ll := NewLimitListener(ln, 8)
	defer ll.Close()

	got := make(chan net.Conn, 1)
	go func() {
		c, err := ll.Accept()
		if err == nil {
			got <- c
		}
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	srv := <-got
	srv.Close()
	srv.Close() // second close must be a no-op for accounting

	if n := ll.ActiveFor("127.0.0.1"); n != 0 {
		t.Errorf("ActiveFor=%d after double Close, want 0", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

// An exempt source (loopback / trusted reverse proxy) must bypass the
// per-IP cap: behind a proxy every user shares one IP.
func TestLimitListener_ExemptBypassesCap(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	const limit = 2
	ll := NewLimitListener(ln, limit)
	ll.Exempt = func(ip string) bool { return ip == "127.0.0.1" }
	defer ll.Close()

	accepted := make(chan net.Conn, 64)
	go func() {
		for {
			c, err := ll.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	const want = limit * 4
	var dialed []net.Conn
	for i := 0; i < want; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		dialed = append(dialed, c)
	}
	defer func() {
		for _, c := range dialed {
			c.Close()
		}
	}()
	got := 0
	deadline := time.After(2 * time.Second)
	for got < want {
		select {
		case c := <-accepted:
			defer c.Close()
			got++
		case <-deadline:
			t.Fatalf("exempt source capped: accepted %d of %d", got, want)
		}
	}
	if n := ll.ActiveFor("127.0.0.1"); n != 0 {
		t.Errorf("exempt connections must not be tracked, ActiveFor=%d", n)
	}
}

// An exempt source must also skip the handshake cooldown: 127.0.0.1 or a
// trusted proxy tripping it would lock every user out (observed after ~20
// failed handshakes from a local probe). Non-exempt IPs stay blocked.
func TestLimitListener_ExemptSkipsCooldown(t *testing.T) {
	run := func(t *testing.T, exempt bool) (accepted bool, drops int) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		ll := NewLimitListener(ln, 4)
		cd := NewCooldown(1, time.Minute)
		cd.RecordFailure("127.0.0.1")
		if !cd.Blocked("127.0.0.1") {
			t.Fatal("precondition: cooldown should block 127.0.0.1")
		}
		ll.Cooldown = cd
		var mu sync.Mutex
		ll.OnCooldownDrop = func(string) { mu.Lock(); drops++; mu.Unlock() }
		if exempt {
			ll.Exempt = func(ip string) bool { return ip == "127.0.0.1" }
		}
		defer ll.Close()

		got := make(chan net.Conn, 1)
		go func() {
			c, err := ll.Accept()
			if err == nil {
				got <- c
			}
		}()
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer c.Close()
		select {
		case ac := <-got:
			ac.Close()
			accepted = true
		case <-time.After(300 * time.Millisecond):
		}
		mu.Lock()
		defer mu.Unlock()
		return accepted, drops
	}
	if ok, drops := run(t, true); !ok || drops != 0 {
		t.Errorf("exempt source: accepted=%v drops=%d, want accepted and no drops", ok, drops)
	}
	if ok, drops := run(t, false); ok || drops != 1 {
		t.Errorf("non-exempt source: accepted=%v drops=%d, want refused with 1 drop", ok, drops)
	}
}
