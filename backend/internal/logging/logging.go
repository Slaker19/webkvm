// Package logging configures structured logging for the backend.
//
// All backend code should use log/slog via the default logger (set up by
// Init) or via the per-request logger returned by FromContext. The
// standard library's log package is reserved for compatibility with
// third-party libs and should not be used in webkvm code.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

type ctxKey struct{}

// Init configures the global slog default logger. Call once from main.
//
//   - format: "json" (default) for machine-readable, or "text" for humans
//   - level:  "debug" | "info" | "warn" | "error" (default: "info")
//
// Output goes to stderr so containers and systemd can route it normally.
func Init(format, level string) {
	InitWithFile(format, level, "")
}

// InitWithFile is like Init but additionally tees every log record to
// the file at filePath (appended, with the same format as stderr).
// Pass filePath="" to disable file output. The file format is text
// when stderr is text and json when stderr is json, so the file is
// always readable by humans tailing it and parseable by the same
// tools that parse the container/journal stream.
//
// If the file cannot be opened, a warning is written to stderr and
// logging continues without the file — startup is never blocked by
// a log destination problem.
func InitWithFile(format, level, filePath string) {
	lvl := parseLevel(level)
	opts := &slog.HandlerOptions{Level: lvl}
	currentLevel = lvl
	currentFilePath = filePath

	format = strings.ToLower(strings.TrimSpace(format))
	var stderrFormat string
	switch format {
	case "text":
		stderrFormat = "text"
	default:
		stderrFormat = "json"
	}
	stderrHandler := buildHandler(os.Stderr, stderrFormat, opts)

	var h slog.Handler = stderrHandler
	if filePath != "" {
		f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "logging: cannot open log file %q: %v (continuing with stderr only)\n", filePath, err)
		} else {
			fileHandler := buildHandler(f, stderrFormat, opts)
			h = &teeHandler{primary: stderrHandler, file: fileHandler, f: f}
		}
	}
	slog.SetDefault(slog.New(h))
}

// currentFilePath is the path of the log file (or "" for stderr-only)
// the most recent Init* call set. SetLevel/SetFormat rebuild the
// handler using the same destination.
var (
	currentFilePath string
	currentLevel    slog.Level
)

// SetLevel swaps the global log level at runtime, without restart.
// Subsequent records below the new level are dropped. Safe to call
// from any goroutine — slog.SetDefault handles the atomic swap.
func SetLevel(level string) {
	cur := slog.Default()
	h := cur.Handler()
	// Re-build the same handler type with the new level. We can't
	// reach inside a *slog.JSONHandler / *slog.TextHandler to mutate
	// the level (no public setter), so we wrap in a levelFilter
	// that drops records below the new threshold. This is a small
	// per-record branch on the hot path; acceptable for a
	// settings-driven control surface that's never a bottleneck.
	currentLevel = parseLevel(level)
	slog.SetDefault(slog.New(&levelFilter{inner: h, min: currentLevel}))
}

// SetFormat swaps the log format at runtime, without restart. Only
// safe to call when no file tee is configured (i.e. logs go to
// stderr only) — swapping the format with a file tee open would
// leave the file half-rewritten. For our use case the UI exposes
// format as a restart-only setting, so this is a guard against
// operator misuse, not a normal code path.
func SetFormat(format string) {
	if currentFilePath != "" {
		// Refuse: SetFormat with a tee is unsafe. The caller is
		// expected to have flagged the field as restart-only.
		return
	}
	stderrFormat := "json"
	if strings.ToLower(strings.TrimSpace(format)) == "text" {
		stderrFormat = "text"
	}
	slog.SetDefault(slog.New(buildHandler(os.Stderr, stderrFormat, &slog.HandlerOptions{Level: currentLevel})))
}

// levelFilter drops records below min. Used by SetLevel to avoid
// re-allocating the underlying handler.
type levelFilter struct {
	inner slog.Handler
	min   slog.Level
}

func (f *levelFilter) Enabled(_ context.Context, l slog.Level) bool {
	return l >= f.min
}
func (f *levelFilter) Handle(ctx context.Context, r slog.Record) error {
	return f.inner.Handle(ctx, r)
}
func (f *levelFilter) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelFilter{inner: f.inner.WithAttrs(attrs), min: f.min}
}
func (f *levelFilter) WithGroup(name string) slog.Handler {
	return &levelFilter{inner: f.inner.WithGroup(name), min: f.min}
}

func buildHandler(w io.Writer, format string, opts *slog.HandlerOptions) slog.Handler {
	if format == "text" {
		return slog.NewTextHandler(w, opts)
	}
	return slog.NewJSONHandler(w, opts)
}

// teeHandler writes each record to two underlying handlers. It owns
// the file handle so it can close it on shutdown.
type teeHandler struct {
	primary slog.Handler
	file    slog.Handler
	f       *os.File
}

func (t *teeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return t.primary.Enabled(ctx, l)
}

// Handle is required by the slog.Handler interface to take the
// record by value (not pointer) — changing it would break the
// contract slog.Logger relies on. gocritic's hugeParam flags the
// 288-byte struct; that's expected, hence the nolint.
func (t *teeHandler) Handle(ctx context.Context, r slog.Record) error { //nolint:gocritic
	if err := t.primary.Handle(ctx, r); err != nil {
		return err
	}
	return t.file.Handle(ctx, r)
}

func (t *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{
		primary: t.primary.WithAttrs(attrs),
		file:    t.file.WithAttrs(attrs),
		f:       t.f,
	}
}

func (t *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{
		primary: t.primary.WithGroup(name),
		file:    t.file.WithGroup(name),
		f:       t.f,
	}
}

// Close releases the file handle. Safe to call multiple times.
// The slog handler spec requires Handle to be safe for concurrent
// use, so we don't lock there; Close is intended to be called from
// a shutdown signal after the logger has stopped being used.
func (t *teeHandler) Close() error {
	if t == nil || t.f == nil {
		return nil
	}
	err := t.f.Close()
	t.f = nil
	return err
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithContext stores a logger in ctx. Use FromContext to retrieve it.
func WithContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the request-scoped logger if one is set, otherwise
// the default logger. The returned logger always has at least the
// request_id attribute set when one is present in the context.
func FromContext(ctx context.Context) *slog.Logger {
	if v, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && v != nil {
		return v
	}
	return slog.Default()
}

// FromRequest is a convenience that calls FromContext with r.Context().
func FromRequest(r *http.Request) *slog.Logger {
	return FromContext(r.Context())
}
