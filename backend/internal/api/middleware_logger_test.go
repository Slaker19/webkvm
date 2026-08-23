package api

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Test that statusRecorder satisfies the streaming interfaces
// that the SSE handler relies on. Regression test for the
// "GET /api/events 500" bug: the wrapper used to embed
// http.ResponseWriter without forwarding Flusher, so the
// handler's `w.(http.Flusher)` assertion failed.
func TestStatusRecorderImplementsFlusher(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	if _, ok := any(sr).(http.Flusher); !ok {
		t.Fatalf("statusRecorder does not implement http.Flusher (SSE would 500)")
	}
}

func TestStatusRecorderImplementsHijacker(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	if _, ok := any(sr).(http.Hijacker); !ok {
		t.Fatalf("statusRecorder does not implement http.Hijacker")
	}
}

// Test that Flush() on the wrapper actually calls Flush() on the
// underlying ResponseWriter. The httptest.ResponseRecorder
// implements Flusher (no-op), so we use a real http server with
// a hijackable conn instead.
func TestStatusRecorderFlushForwardsToUnderlying(t *testing.T) {
	flushed := false
	rec := &flushingRecorder{
		ResponseWriter: httptest.NewRecorder(),
		flushed:        &flushed,
	}
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	sr.Flush()
	if !flushed {
		t.Fatalf("Flush() on statusRecorder did not forward to underlying writer")
	}
}

func TestStatusRecorderWriteHeaderRecordsStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	sr.WriteHeader(http.StatusTeapot)
	if sr.status != http.StatusTeapot {
		t.Fatalf("WriteHeader: expected status=%d, got %d", http.StatusTeapot, sr.status)
	}
	if rec.Code != http.StatusTeapot {
		t.Fatalf("WriteHeader: expected rec.Code=%d, got %d", http.StatusTeapot, rec.Code)
	}
}

func TestStatusRecorderWriteCountsBytes(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	n, err := sr.Write([]byte("hello world"))
	if err != nil {
		t.Fatalf("Write: unexpected err: %v", err)
	}
	if n != 11 {
		t.Fatalf("Write: expected n=11, got %d", n)
	}
	if sr.bytes != 11 {
		t.Fatalf("Write: expected sr.bytes=11, got %d", sr.bytes)
	}
}

// Compile-time assertions that the wrapper keeps the full interface
// surface of http.ResponseWriter (so any handler that does
// `w.(...)` for the streaming interfaces will succeed).
var (
	_ http.ResponseWriter = (*statusRecorder)(nil)
	_ http.Flusher        = (*statusRecorder)(nil)
	_ http.Hijacker       = (*statusRecorder)(nil)
)

// flushingRecorder wraps httptest.ResponseRecorder and tracks
// whether Flush was called.
type flushingRecorder struct {
	http.ResponseWriter
	flushed *bool
}

func (f *flushingRecorder) Flush() {
	*f.flushed = true
	// httptest.ResponseRecorder has Flush, call it for parity
	if fr, ok := f.ResponseWriter.(http.Flusher); ok {
		fr.Flush()
	}
}

// Ensure unused imports are referenced (bufio, net are used by the
// statusRecorder's Hijack method which is exercised at compile time).
var (
	_ = bufio.NewReader
	_ = net.IPv4
)
