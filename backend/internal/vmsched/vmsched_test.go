package vmsched

import "testing"

func TestValidate(t *testing.T) {
	valid := []Schedule{
		{StartCron: "0 8 * * *", StopCron: "0 22 * * *"},
		{StartCron: "30 7 * * 1-5"}, // weekdays only
		{StopCron: "0 0 * * *"},
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
	if err := s.Set("vm1", sch); err != nil {
		t.Fatal(err)
	}
	s2 := NewStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	got := s2.Get("vm1")
	if got.StartCron != "0 8 * * *" || got.StopCron != "0 22 * * *" {
		t.Fatalf("reload mismatch: %+v", got)
	}
	// Clearing removes the entry.
	if err := s.Set("vm1", Schedule{}); err != nil {
		t.Fatal(err)
	}
	if s.Get("vm1").Enabled() {
		t.Fatal("schedule should be cleared")
	}
}
