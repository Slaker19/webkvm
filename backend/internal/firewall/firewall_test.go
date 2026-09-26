package firewall

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildRulesetIncludesSafetyRails(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	_ = s.Set(VMFirewall{
		VMID: "vm1",
		Rules: []Rule{
			{ID: "r1", Proto: "tcp", Port: 8080, Action: "drop"},
		},
	})
	m := NewManager(s, func(id string) string { return "192.168.1.50" }, 8080, slog.Default())
	rs := m.BuildRuleset()
	// Safety rail: SSH must always be accepted before the drop rule.
	if !strings.Contains(rs, "tcp dport 22 accept") {
		t.Fatalf("ruleset missing SSH safety rail:\n%s", rs)
	}
	if !strings.Contains(rs, "tcp dport 8080 drop") {
		t.Fatalf("ruleset missing the drop rule:\n%s", rs)
	}
	// The drop rule must come AFTER the accept rail.
	if strings.Index(rs, "tcp dport 22 accept") > strings.Index(rs, "tcp dport 8080 drop") {
		t.Fatalf("safety rail not before drop rule:\n%s", rs)
	}
}

func TestBuildRulesetEmptyReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	m := NewManager(s, nil, 8080, slog.Default())
	if rs := m.BuildRuleset(); rs != "" {
		t.Fatalf("expected empty ruleset for no rules, got %q", rs)
	}
}

func TestBuildRulesetSkipsPendingForward(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	_ = s.Set(VMFirewall{
		VMID: "vm-off",
		Forwards: []Forward{
			{ID: "f1", Proto: "tcp", HostPort: 8080, GuestPort: 80},
		},
	})
	// Resolver returns "" (VM off) -> forward must be skipped.
	m := NewManager(s, func(id string) string { return "" }, 8080, slog.Default())
	rs := m.BuildRuleset()
	if strings.Contains(rs, "dnat to") {
		t.Fatalf("forward with no IP should not emit a dnat rule:\n%s", rs)
	}
}

func TestStorePersists(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	fw := VMFirewall{
		VMID:     "vm1",
		Rules:    []Rule{{ID: "r1", Proto: "tcp", Port: 8080, Action: "drop"}},
		Forwards: []Forward{{ID: "f1", Proto: "tcp", HostPort: 8080, GuestPort: 80}},
	}
	if err := s.Set(fw); err != nil {
		t.Fatal(err)
	}
	// Reload from disk.
	s2 := NewStore(dir)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	got := s2.Get("vm1")
	if len(got.Rules) != 1 || len(got.Forwards) != 1 {
		t.Fatalf("reloaded rules mismatch: %+v", got)
	}
}

func TestStoreEmptyDeletes(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	_ = s.Set(VMFirewall{VMID: "vm1", Rules: []Rule{{ID: "r1", Proto: "tcp", Port: 80, Action: "allow"}}})
	if err := s.Set(VMFirewall{VMID: "vm1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "firewall.json")); err != nil {
		t.Fatal("firewall.json should still exist after clearing a VM's rules")
	}
	if got := s.Get("vm1"); len(got.Rules) != 0 {
		t.Fatalf("expected empty rules after delete, got %+v", got)
	}
}
