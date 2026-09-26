package firewall

import (
	"log/slog"
	"strings"
	"testing"
)

func TestBuildRulesetMasqueradeOnFirstApply(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	_ = s.Set(VMFirewall{
		VMID: "vm1",
		Forwards: []Forward{
			{ID: "f1", Proto: "tcp", HostPort: 8080, GuestPort: 80, TargetIP: "192.168.1.50"},
		},
	})
	// Applied is false initially (as on a fresh apply)
	m := NewManager(s, func(id string) string { return "192.168.1.50" }, 8080, slog.Default())
	rs := m.BuildRuleset()
	if !strings.Contains(rs, "dnat to 192.168.1.50:80") {
		t.Fatalf("expected dnat rule:\n%s", rs)
	}
	if !strings.Contains(rs, "masquerade") {
		t.Fatalf("EXPECTED BUG: missing masquerade postrouting chain on first apply:\n%s", rs)
	}
}
