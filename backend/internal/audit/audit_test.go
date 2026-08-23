package audit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreCurrent())
}


func TestNew_CreatesDirectoryAndFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "subdir", "audit.log")

	logger, err := New(path)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = logger.file.Close() }()

	// Directory was created.
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("dir not created: %v", err)
	}
	// File was created.
	info, err := os.Stat(path)
	if err != nil {
		t.Errorf("file not created: %v", err)
	}
	// Permissions: 0600.
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("file perms = %o, want 0600", perm)
	}
}

func TestLog_WritesValidJSONL(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.file.Close() }()

	logger.Log(Entry{
		User:     "admin",
		Role:     "admin",
		Action:   "vm.start",
		Resource: "vm-123",
		IP:       "192.168.1.1",
		Detail:   map[string]interface{}{"foo": "bar"},
	})
	logger.Log(Entry{
		User:   "alice",
		Action: "user.create",
	})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines, got %d:\n%s", len(lines), data)
	}

	// Each line is a parseable JSON object with the expected fields.
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("line %d: invalid JSON: %v\nline=%s", i, err, line)
		}
		if e.Time == "" {
			t.Errorf("line %d: time field empty", i)
		}
		if _, err := time.Parse(time.RFC3339, e.Time); err != nil {
			t.Errorf("line %d: time not RFC3339: %v", i, err)
		}
	}

	// Spot-check first entry.
	var first Entry
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.User != "admin" {
		t.Errorf("user = %q, want admin", first.User)
	}
	if first.Action != "vm.start" {
		t.Errorf("action = %q, want vm.start", first.Action)
	}
	if first.Detail["foo"] != "bar" {
		t.Errorf("detail[foo] = %v, want bar", first.Detail["foo"])
	}
}

func TestLog_SetsTimeWhenEmpty(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.file.Close() }()

	before := time.Now().UTC().Truncate(time.Second)
	logger.Log(Entry{User: "x", Action: "y"})
	after := time.Now().UTC().Add(time.Second)

	data, _ := os.ReadFile(path)
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	got, err := time.Parse(time.RFC3339, e.Time)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	if got.Before(before) || got.After(after) {
		t.Errorf("time %v outside [%v, %v]", got, before, after)
	}
}

func TestLog_PreservesProvidedTime(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.file.Close() }()

	custom := "2020-01-02T03:04:05Z"
	logger.Log(Entry{User: "x", Action: "y", Time: custom})

	data, _ := os.ReadFile(path)
	var e Entry
	_ = json.Unmarshal(data, &e)
	if e.Time != custom {
		t.Errorf("time = %q, want %q", e.Time, custom)
	}
}

func TestLog_NilReceiverIsSafe(t *testing.T) {
	// Must not panic on nil receiver — the auth middleware calls
	// logger.Log on an audit-disabled system.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil receiver panicked: %v", r)
		}
	}()
	var l *Logger
	l.Log(Entry{User: "x", Action: "y"})
}

func TestLog_ConcurrentSafety(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.file.Close() }()

	const writers = 20
	const perWriter = 50

	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				logger.Log(Entry{
					User:   "u",
					Action: "test",
					Detail: map[string]interface{}{"id": id, "j": j},
				})
			}
		}(i)
	}
	wg.Wait()

	// Verify file contents: 1 line per write, all parseable.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != writers*perWriter {
		t.Errorf("got %d lines, want %d", len(lines), writers*perWriter)
	}
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d invalid: %v", i, err)
		}
	}
}

func TestLog_RotatesAt10MB(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file rotation test uses unix stat")
	}
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.file.Close() }()

	// Write enough entries to exceed 10MB. Each entry is ~600B, so
	// 20_000 entries gets us past the limit (12MB+).
	big := strings.Repeat("x", 500)
	for i := 0; i < 20_000; i++ {
		logger.Log(Entry{
			User:   "u",
			Action: "test",
			Detail: map[string]interface{}{"payload": big},
		})
	}

	// The current file should be smaller than 10MB; the rotated
	// .1 file should exist.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() >= maxFileBytes {
		t.Errorf("current file size %d >= %d (no rotation)", info.Size(), maxFileBytes)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Errorf("rotated file missing: %v", err)
	}
}

func TestFromRequest(t *testing.T) {
	tests := []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		wantUser   string
		wantRole   string
		wantIP     string
	}{
		{
			name:       "all headers, IPv4 remote",
			headers:    map[string]string{"X-User": "admin", "X-Role": "admin"},
			remoteAddr: "192.168.1.10:54321",
			wantUser:   "admin",
			wantRole:   "admin",
			wantIP:     "192.168.1.10",
		},
		{
			name:       "IPv6 remote with brackets",
			headers:    map[string]string{"X-User": "bob"},
			remoteAddr: "[::1]:1234",
			wantUser:   "bob",
			wantRole:   "",
			wantIP:     "::1",
		},
		{
			name:       "no headers, no port",
			remoteAddr: "10.0.0.5",
			wantIP:     "10.0.0.5",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tc.remoteAddr
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			user, role, ip := FromRequest(r)
			if user != tc.wantUser {
				t.Errorf("user = %q, want %q", user, tc.wantUser)
			}
			if role != tc.wantRole {
				t.Errorf("role = %q, want %q", role, tc.wantRole)
			}
			if ip != tc.wantIP {
				t.Errorf("ip = %q, want %q", ip, tc.wantIP)
			}
		})
	}
}

func TestNew_DirectoryCreationFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not supported on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, can't test permission denied")
	}
	tmp := t.TempDir()
	// Make tmp read-only so MkdirAll inside it fails.
	if err := os.Chmod(tmp, 0555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(tmp, 0755) }()

	path := filepath.Join(tmp, "no-such-subdir", "audit.log")
	if _, err := New(path); err == nil {
		t.Errorf("expected error from MkdirAll, got nil")
	}
}

func TestNew_OpenFileFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not supported on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, can't test permission denied")
	}
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	// Create a directory with the same name as the audit log file
	// so os.OpenFile(O_CREATE) returns an error.
	if err := os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(path); err == nil {
		t.Errorf("expected error from OpenFile, got nil")
	}
}

func TestLog_RotateErrorAfterThreshold(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod not supported on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, can't test permission denied")
	}
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")

	// Pre-create the rotated file path and make the dir read-only
	// so the rename inside rotation fails. Then the next Log call
	// hits the error path inside rotateIfNeededLocked.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, maxFileBytes), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".1", 0755); err != nil {
		t.Fatal(err)
	}
	// Now any os.Rename(path, path+".1") will fail because path+".1"
	// is a directory.
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.file.Close() }()

	// Add 1 byte to push us over the threshold.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()

	// Log a new entry: rotateIfNeededLocked will fail and Log will
	// return silently (no panic, no error).
	logger.Log(Entry{User: "u", Action: "test"})
}
