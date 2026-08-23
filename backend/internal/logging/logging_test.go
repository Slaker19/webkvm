package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/goleak"
)
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}


func TestInitWithFile_EmptyPathIsStderrOnly(t *testing.T) {
	InitWithFile("text", "info", "")
	// Should not panic, should leave the default logger functional.
	slog.Info("no_file_path")
	// Reset for any later test in the package.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func TestInitWithFile_WritesToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")

	InitWithFile("text", "info", path)
	t.Cleanup(func() {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	})

	slog.Info("hello_file", "k", "v")

	got := readFile(t, path)
	if !strings.Contains(got, "hello_file") {
		t.Fatalf("expected log line in file, got: %q", got)
	}
	if !strings.Contains(got, "k=v") {
		t.Fatalf("expected attr k=v in file, got: %q", got)
	}
}

func TestInitWithFile_AppendsAcrossInits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "append.log")

	InitWithFile("text", "info", path)
	slog.Info("first")
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	InitWithFile("text", "info", path)
	slog.Info("second")
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	got := readFile(t, path)
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("expected both records appended, got: %q", got)
	}
}

func TestInitWithFile_OpensExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre.log")
	if err := os.WriteFile(path, []byte("seed\n"), 0644); err != nil {
		t.Fatal(err)
	}

	InitWithFile("text", "info", path)
	slog.Info("after")
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	got := readFile(t, path)
	if !strings.HasPrefix(got, "seed") {
		t.Fatalf("expected seed to be preserved at start, got: %q", got)
	}
	if !strings.Contains(got, "after") {
		t.Fatalf("expected new record to be appended, got: %q", got)
	}
}

func TestInitWithFile_UnwritablePathDoesNotPanic(t *testing.T) {
	// Point at a path that cannot be created: a regular file
	// whose parent is itself, so os.OpenFile must fail.
	dir := t.TempDir()
	regular := filepath.Join(dir, "regular")
	if err := os.WriteFile(regular, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(regular, "no-such-dir", "x.log")

	// Should not panic, should warn to stderr and continue with
	// stderr-only logging.
	InitWithFile("text", "info", bad)
	slog.Info("still_works")
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func TestTeeHandler_DelegatesEnabled(t *testing.T) {
	var buf bytes.Buffer
	primary := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})
	th := &teeHandler{primary: primary}
	if !th.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("expected primary's Enabled to be reflected")
	}
}

func TestTeeHandler_HandleWritesToBoth(t *testing.T) {
	var primaryBuf, fileBuf bytes.Buffer
	primary := slog.NewTextHandler(&primaryBuf, nil)
	fileH := slog.NewTextHandler(&fileBuf, nil)
	th := &teeHandler{primary: primary, file: fileH}

	rec := slog.Record{Message: "boom", Level: slog.LevelInfo}
	if err := th.Handle(context.Background(), rec); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if !strings.Contains(primaryBuf.String(), "boom") {
		t.Errorf("primary missing: %q", primaryBuf.String())
	}
	if !strings.Contains(fileBuf.String(), "boom") {
		t.Errorf("file missing: %q", fileBuf.String())
	}
}

func TestTeeHandler_WithAttrsPropagates(t *testing.T) {
	var primaryBuf, fileBuf bytes.Buffer
	primary := slog.NewTextHandler(&primaryBuf, nil)
	fileH := slog.NewTextHandler(&fileBuf, nil)
	th := (&teeHandler{primary: primary, file: fileH}).
		WithAttrs([]slog.Attr{slog.String("app", "test")}).(*teeHandler)

	rec := slog.Record{Message: "hi", Level: slog.LevelInfo}
	_ = th.Handle(context.Background(), rec)

	if !strings.Contains(primaryBuf.String(), "app=test") {
		t.Errorf("primary missing attr: %q", primaryBuf.String())
	}
	if !strings.Contains(fileBuf.String(), "app=test") {
		t.Errorf("file missing attr: %q", fileBuf.String())
	}
}

func TestTeeHandler_ConcurrentSafe(t *testing.T) {
	// Smoke test: write from many goroutines to a tee'd handler and
	// ensure the file ends up with one record per log call. The
	// race detector (run with -race) catches any missing locks.
	var fileBuf bytes.Buffer
	primary := slog.NewTextHandler(io.Discard, nil)
	fileH := slog.NewTextHandler(&fileBuf, nil)
	th := &teeHandler{primary: primary, file: fileH}
	logger := slog.New(th)

	var wg sync.WaitGroup
	const n = 200
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("concurrent")
		}()
	}
	wg.Wait()

	if got := strings.Count(fileBuf.String(), "concurrent"); got != n {
		t.Fatalf("expected %d lines, got %d (file may have lost records)", n, got)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":         slog.LevelInfo,
		"INFO":     slog.LevelInfo,
		"debug":    slog.LevelDebug,
		"WARN":     slog.LevelWarn,
		"warning":  slog.LevelWarn,
		"error":    slog.LevelError,
		"err":      slog.LevelError,
		" unknown ": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
