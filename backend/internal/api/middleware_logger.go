package api

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"webkvm/internal/logging"
)

const requestIDHeader = "X-Request-ID"

// requestLogger is middleware that:
//
//  1. Assigns a request_id (echoing X-Request-ID from the client if present).
//  2. Builds a request-scoped logger with request_id, method, path, remote_ip.
//  3. Stores the logger in the request context for handlers.
//  4. Logs one structured line at the end with method, path, status,
//     duration_ms, user (if auth ran), and request_id.
//  5. Echoes the request_id back in the X-Request-ID response header.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rid := r.Header.Get(requestIDHeader)
		if rid == "" {
			rid = newRequestID()
		}

		// Stash the request_id in the response header for the client.
		w.Header().Set(requestIDHeader, rid)

		// Build a base logger with the attributes that don't change
		// during the request lifetime.
		ip := logging.ClientIP(r)
		logger := slog.Default().With(
			"request_id", rid,
			"method", r.Method,
			"path", r.URL.Path,
			"remote_ip", ip,
		)

		// Inject into the request context so handlers can call
		// logging.FromRequest(r) without having to re-thread the logger.
		ctx := logging.WithContext(r.Context(), logger)
		r = r.WithContext(ctx)

		// Wrap the response writer so we can capture the status code.
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		var rw http.ResponseWriter

		f, isFlusher := w.(http.Flusher)
		h, isHijacker := w.(http.Hijacker)

		if isFlusher && isHijacker {
			rw = &statusRecorderFull{statusRecorder: sr, Flusher: f, Hijacker: h}
		} else if isFlusher {
			rw = &statusRecorderFlusher{statusRecorder: sr, Flusher: f}
		} else if isHijacker {
			rw = &statusRecorderHijacker{statusRecorder: sr, Hijacker: h}
		} else {
			rw = sr
		}

		next.ServeHTTP(rw, r)

		// Re-read the logger from the (possibly decorated) context in
		// case a downstream middleware (auth) added attributes
		// (e.g. user/role). Use the same ctx we passed downstream.
		l := logging.FromContext(ctx)
		l.LogAttrs(ctx, slog.LevelInfo, "http_request",
			slog.Int("status", sr.status),
			slog.Int64("duration_ms", time.Since(start).Milliseconds()),
			slog.Int("bytes", sr.bytes),
		)
	})
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand is documented to never fail on Linux; if it does
		// we'd rather log "unavailable" than panic the request.
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

type statusRecorderFlusher struct {
	*statusRecorder
	http.Flusher
}

type statusRecorderHijacker struct {
	*statusRecorder
	http.Hijacker
}

type statusRecorderFull struct {
	*statusRecorder
	http.Flusher
	http.Hijacker
}
