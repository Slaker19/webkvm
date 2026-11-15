package firewall

import (
	"log/slog"
	"net"
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
	m := NewManager(s, func(id string) string { return "192.168.50.50" }, 8080, slog.Default())
	m.SetManagementCIDRs([]string{"192.168.1.0/24"})
	rs := m.BuildRuleset()
	// SSH is no longer accepted globally; the management drop restricts it.
	if strings.Contains(rs, "tcp dport 22 accept") {
		t.Fatalf("SSH must not be accepted globally:\n%s", rs)
	}
	if !strings.Contains(rs, "tcp dport { 22, 8080 } ip saddr != @mgmt drop") {
		t.Fatalf("ruleset missing the management-port drop:\n%s", rs)
	}
	if !strings.Contains(rs, "tcp dport 8080 drop") {
		t.Fatalf("ruleset missing the VM drop rule:\n%s", rs)
	}
	if !strings.Contains(rs, "192.168.1.0/24") {
		t.Fatalf("ruleset missing the management CIDR:\n%s", rs)
	}
}

func TestBuildRulesetAlwaysCreatesJailSet(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	m := NewManager(s, nil, 8080, slog.Default())
	m.SetManagementCIDRs([]string{"192.168.1.0/24"})
	rs := m.BuildRuleset()
	if rs == "" {
		t.Fatal("the ruleset must exist even with no per-VM rules")
	}
	for _, want := range []string{"set jail_blacklist", "ip saddr @jail_blacklist drop", "policy accept;"} {
		if !strings.Contains(rs, want) {
			t.Fatalf("ruleset missing %q:\n%s", want, rs)
		}
	}
}

func TestBuildRulesetManagementCIDRsIncludeClusterAndProxy(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(NewStore(dir), nil, 8080, slog.Default())
	// The proxy is a /32 inside the LAN /24; auto-merge keeps nft happy with
	// the overlap while still listing the explicit proxy address.
	m.SetManagementCIDRs([]string{"192.168.1.0/24", "192.168.1.8/32", "10.20.30.0/24"})
	rs := m.BuildRuleset()
	for _, want := range []string{"auto-merge", "192.168.1.8/32", "10.20.30.0/24"} {
		if !strings.Contains(rs, want) {
			t.Fatalf("management set missing %q:\n%s", want, rs)
		}
	}
}

func TestBuildRulesetWithoutManagementCIDRsKeepsPortsOpen(t *testing.T) {
	old := listInterfaces
	listInterfaces = func() ([]net.Interface, error) { return nil, nil }
	defer func() { listInterfaces = old }()
	m := NewManager(NewStore(t.TempDir()), nil, 8080, slog.Default())
	m.SetManagementCIDRs(nil)
	rs := m.BuildRuleset()
	if strings.Contains(rs, "@mgmt") {
		t.Fatalf("empty management set must not emit a drop:\n%s", rs)
	}
	if !strings.Contains(rs, "policy accept;") {
		t.Fatalf("expected the open policy:\n%s", rs)
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
