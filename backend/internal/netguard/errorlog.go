// Package netguard hardens the public HTTP listener against abusive or
// misconfigured clients.
//
// It exists because of a real incident: a browser on the LAN rejected the
// self-signed certificate and retried the TLS handshake ~83 times per
// second, 24/7. Go's http.Server logs one line per failed handshake, so a
// single unauthenticated client produced ~2 GB of journal per day and kept
// the process at 25% CPU. Neither of those is acceptable for a client that
// never gets past the TLS layer.
//
// Two independent mitigations live here:
//
//   - AggregatingWriter collapses repeated, identical log messages into a
//     periodic summary, so a hostile peer cannot amplify its connection
//     rate into unbounded disk writes.
//   - LimitListener caps how many connections a single remote IP may hold
//     open at once, so one peer cannot exhaust file descriptors or crowd
//     out legitimate users.
package netguard

import (
	"bytes"
	"io"
	"log"
	"regexp"
	"sync"
	"time"
)

// DefaultFlushInterval is how often AggregatingWriter emits a summary for
// messages it has been suppressing.
const DefaultFlushInterval = 30 * time.Second

// variablePattern matches the parts of a log line that differ between
// otherwise identical events, so they collapse to the same key.
//
// A TLS handshake error looks like:
//
//	http: TLS handshake error from 192.168.1.171:34122: remote error: ...
//
// The ephemeral source port changes on every retry, so it must be stripped
// for the messages to group. The IP itself is kept: distinct peers are
// genuinely distinct problems and are reported separately.
var variablePattern = regexp.MustCompile(`:\d{0,5}\b`)

// Sink receives the aggregated summary for a group of suppressed messages.
// It is called from the flush goroutine, never while holding the lock.
type Sink func(msg string, count int, firstSeen, lastSeen time.Time)

// AggregatingWriter is an io.Writer suitable for http.Server.ErrorLog.
//
// The first occurrence of a message passes through immediately, so a
// genuine one-off error is never delayed or hidden. Subsequent occurrences
// of the *same* message within the flush window are counted, not written.
// When the window closes, a single summary reports how many were
// suppressed. A quiet server therefore behaves exactly as before; only a
// flood is collapsed.
type AggregatingWriter struct {
	mu       sync.Mutex
	counts   map[string]*entry
	passthru io.Writer
	sink     Sink
	interval time.Duration

	stop     chan struct{}
	stopped  chan struct{}
	stopOnce sync.Once
	// now is injectable so tests control time instead of sleeping.
	now func() time.Time

	// Observer, when set, sees every message before aggregation. It is
	// how the handshake cooldown learns which peers are failing, since
	// net/http reports handshake failures through ErrorLog only.
	Observer func(msg string)
}

type entry struct {
	// suppressed counts occurrences after the first one that was
	// written through, i.e. the number of lines we did NOT emit.
	suppressed int
	firstSeen  time.Time
	lastSeen   time.Time
	sample     string
}

// NewAggregatingWriter returns a writer that passes the first instance of
// each distinct message to passthru and reports repeats to sink every
// interval. Pass interval <= 0 to use DefaultFlushInterval.
func NewAggregatingWriter(passthru io.Writer, sink Sink, interval time.Duration) *AggregatingWriter {
	if interval <= 0 {
		interval = DefaultFlushInterval
	}
	w := &AggregatingWriter{
		counts:   map[string]*entry{},
		passthru: passthru,
		sink:     sink,
		interval: interval,
		stop:     make(chan struct{}),
		stopped:  make(chan struct{}),
		now:      time.Now,
	}
	go w.loop()
	return w
}

// Write implements io.Writer. It never returns an error: an ErrorLog that
// fails would be logged by the caller, which is exactly the amplification
// we are trying to avoid.
func (w *AggregatingWriter) Write(p []byte) (int, error) {
	n := len(p)
	msg := string(bytes.TrimRight(p, "\n"))
	if msg == "" {
		return n, nil
	}
	if w.Observer != nil {
		// Called before aggregation: suppressed repeats still have to
		// count towards the cooldown, otherwise a flood would be
		// invisible to it after the first line.
		w.Observer(msg)
	}
	key := variablePattern.ReplaceAllString(msg, ":")

	w.mu.Lock()
	e, seen := w.counts[key]
	if !seen {
		now := w.now()
		w.counts[key] = &entry{firstSeen: now, lastSeen: now, sample: msg}
		w.mu.Unlock()
		// First occurrence goes straight through, unsuppressed.
		if w.passthru != nil {
			_, _ = w.passthru.Write(p)
		}
		return n, nil
	}
	e.suppressed++
	e.lastSeen = w.now()
	w.mu.Unlock()
	return n, nil
}

// Flush reports every message with suppressed repeats and resets the
// window. Messages that occurred only once (already written through) are
// dropped from the map so a recurrence after a quiet period is again
// treated as new and passes through immediately.
func (w *AggregatingWriter) Flush() {
	type report struct {
		msg         string
		count       int
		first, last time.Time
	}
	var pending []report

	w.mu.Lock()
	for key, e := range w.counts {
		if e.suppressed > 0 {
			pending = append(pending, report{e.sample, e.suppressed, e.firstSeen, e.lastSeen})
		}
		delete(w.counts, key)
	}
	w.mu.Unlock()

	// The sink is called without the lock so a slow logger cannot block
	// the listener goroutines calling Write.
	for _, r := range pending {
		if w.sink != nil {
			w.sink(r.msg, r.count, r.first, r.last)
		}
	}
}

func (w *AggregatingWriter) loop() {
	defer close(w.stopped)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			w.Flush() // do not lose the final window on shutdown
			return
		case <-t.C:
			w.Flush()
		}
	}
}

// Close stops the flush goroutine after emitting one final summary, so
// counts accumulated just before shutdown are still reported. Safe to
// call more than once.
func (w *AggregatingWriter) Close() {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.stopped
}

// NewErrorLog wraps an AggregatingWriter in the *log.Logger that
// http.Server.ErrorLog expects. Flags are cleared because slog already
// timestamps every record downstream.
func NewErrorLog(w *AggregatingWriter) *log.Logger {
	return log.New(w, "", 0)
}
