package hostsched

import (
	"io"
	"log/slog"
	"testing"
)

func TestValidate(t *testing.T) {
	if err := Validate(Config{}); err != nil {
		t.Fatalf("disabled empty config should be valid: %v", err)
	}
	if err := Validate(Config{Enabled: true, UpgradeCron: "0 3 * * 0"}); err != nil {
		t.Fatalf("valid cron should pass: %v", err)
	}
	if err := Validate(Config{Enabled: true}); err == nil {
		t.Fatal("enabled without cron should be rejected")
	}
	if err := Validate(Config{Enabled: true, UpgradeCron: "not a cron"}); err == nil {
		t.Fatal("malformed cron should be rejected")
	}
}

func TestStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	want := Config{Enabled: true, UpgradeCron: "0 3 * * 0", RebootAfter: true}
	if err := s.Set(want); err != nil {
		t.Fatal(err)
	}
	// Reload from disk into a fresh store.
	s2 := NewStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	got := s2.Get()
	if got != want {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
}

func TestSchedulerSetRun(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.Set(Config{Enabled: true, UpgradeCron: "0 3 * * 0"}); err != nil {
		t.Fatal(err)
	}
	fired := false
	sch := NewScheduler(s, func() { fired = true }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	sch.Start()
	defer sch.Stop()
	// SetRun must wire the callback without panicking; the cron only fires
	// on schedule so fired stays false here.
	sch.SetRun(func() { fired = true })
	if fired {
		t.Fatal("callback must not fire on SetRun")
	}
}
