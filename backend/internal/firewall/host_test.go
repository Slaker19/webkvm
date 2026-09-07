package firewall

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testHostManager(t *testing.T, dir string, rules HostFirewall) (*Manager, *HostStore) {
	t.Helper()
	s := NewStore(dir)
	hs := NewHostStore(dir)
	if !rules.IsEmpty() {
		if err := hs.Set(rules); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(s, func(id string) string { return "192.168.1.50" }, 8080, slog.Default())
	m.SetHostStore(hs)
	// Keep rollback tests fast.
	m.RollbackDeadline(150 * time.Millisecond)
	return m, hs
}

// skipIfNotRoot skips the Safe Apply tests that apply the ruleset to
// the kernel with nft: nftables requires CAP_NET_ADMIN, which the
// non-root GitHub Actions runners do not have (the apply would fail
// with "Operation not permitted"). The pure validation/rollback logic
// is still exercised by the tests that never reach nft.
func skipIfNotRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root / CAP_NET_ADMIN to apply nftables")
	}
}

// V13-C-01 anti-lockout: a drop rule on a protected management port
// (SSH, the UI port, the VNC range) is REJECTED.
func TestValidateHostFirewall_RejectsDropOnProtectedPorts(t *testing.T) {
	for _, port := range []int{22, 8080, 5900, 5903} {
		err := ValidateHostFirewall(HostFirewall{
			Input: []HostInputRule{{ID: "r", Proto: "tcp", Port: port, Action: "drop"}},
		}, 8080)
		if err == nil {
			t.Errorf("port %d: expected anti-lockout rejection, got nil", port)
		}
		if !strings.Contains(err.Error(), "protected") {
			t.Errorf("port %d: error should mention the protected port: %v", port, err)
		}
	}
}

// An allow rule on a protected port is fine (it is redundant with the
// implicit rail, but harmless and explicit).
func TestValidateHostFirewall_AllowsAllowOnProtectedPorts(t *testing.T) {
	if err := ValidateHostFirewall(HostFirewall{
		Input: []HostInputRule{{ID: "r", Proto: "tcp", Port: 22, Action: "allow"}},
	}, 8080); err != nil {
		t.Fatalf("allow on protected port should pass: %v", err)
	}
}

// Invalid proto/port/action/src and forwards without a valid IPv4 are
// rejected.
func TestValidateHostFirewall_RejectsMalformed(t *testing.T) {
	cases := []HostFirewall{
		{Input: []HostInputRule{{ID: "a", Proto: "icmp", Port: 80, Action: "allow"}}},
		{Input: []HostInputRule{{ID: "a", Proto: "tcp", Port: 0, Action: "allow"}}},
		{Input: []HostInputRule{{ID: "a", Proto: "tcp", Port: 65536, Action: "allow"}}},
		{Input: []HostInputRule{{ID: "a", Proto: "tcp", Port: 80, Action: "reject"}}},
		{Input: []HostInputRule{{ID: "a", Proto: "tcp", Port: 80, Action: "allow", Src: "not-an-ip"}}},
		{Input: []HostInputRule{{ID: "a", Proto: "tcp", Port: 80, Action: "allow", Src: "2001:db8::1"}}},
		{Forwards: []HostForwardRule{{ID: "f", Proto: "tcp", HostPort: 80, GuestIP: "", GuestPort: 8080}}},
		{Forwards: []HostForwardRule{{ID: "f", Proto: "tcp", HostPort: 80, GuestIP: "host", GuestPort: 8080}}},
	}
	for _, c := range cases {
		if err := ValidateHostFirewall(c, 8080); err == nil {
			t.Errorf("expected rejection for %+v", c)
		}
	}
	// Valid ones pass.
	if err := ValidateHostFirewall(HostFirewall{
		Input:    []HostInputRule{{ID: "a", Proto: "both", Port: 8085, Action: "drop", Src: "10.0.0.0/8"}},
		Forwards: []HostForwardRule{{ID: "f", Proto: "tcp", HostPort: 80, GuestIP: "192.168.1.50", GuestPort: 8080}},
	}, 8080); err != nil {
		t.Fatalf("valid ruleset rejected: %v", err)
	}
}

// Host input rules appear in the ruleset AFTER the safety rails, and
// host forwards produce a dnat rule + masquerade target.
func TestBuildRuleset_IncludesHostRules(t *testing.T) {
	dir := t.TempDir()
	m, _ := testHostManager(t, dir, HostFirewall{
		Input:    []HostInputRule{{ID: "r1", Proto: "tcp", Port: 8443, Action: "allow", Src: "10.0.0.0/8"}},
		Forwards: []HostForwardRule{{ID: "f1", Proto: "tcp", HostPort: 2222, GuestIP: "192.168.1.77", GuestPort: 22}},
	})
	rs := m.BuildRuleset()
	if !strings.Contains(rs, "ip saddr 10.0.0.0/8 tcp dport 8443 accept") {
		t.Errorf("missing host input rule with source:\n%s", rs)
	}
	if !strings.Contains(rs, "tcp dport 2222 dnat to 192.168.1.77:22") {
		t.Errorf("missing host forward dnat:\n%s", rs)
	}
	if !strings.Contains(rs, "ip daddr { 192.168.1.77 } masquerade") {
		t.Errorf("missing masquerade target for host forward:\n%s", rs)
	}
	// Rails still first.
	if strings.Index(rs, "tcp dport 22 accept") > strings.Index(rs, "tcp dport 8443 accept") {
		t.Errorf("safety rail not before host rule:\n%s", rs)
	}
}

// The manager reports a ruleset even when the per-VM store is empty
// (host rules alone must produce a table).
func TestBuildRuleset_HostRulesAloneNonEmpty(t *testing.T) {
	dir := t.TempDir()
	m, _ := testHostManager(t, dir, HostFirewall{
		Input: []HostInputRule{{ID: "r", Proto: "tcp", Port: 8085, Action: "drop"}},
	})
	if rs := m.BuildRuleset(); rs == "" {
		t.Fatal("expected non-empty ruleset for host-only rules")
	}
}

// Safe Apply: StageHostApply returns a deadline and marks a pending
// apply; ConfirmHostApply persists the rules and clears the pending
// state.
func TestSafeApply_ConfirmPersists(t *testing.T) {
	skipIfNotRoot(t)
	dir := t.TempDir()
	m, hs := testHostManager(t, dir, HostFirewall{}) // empty baseline
	next := HostFirewall{Input: []HostInputRule{{ID: "r", Proto: "tcp", Port: 8085, Action: "drop"}}}

	prev, deadline, err := m.StageHostApply(next)
	if err != nil {
		t.Fatal(err)
	}
	if prev.IsEmpty() == false && len(prev.Input) != 0 {
		t.Fatalf("prev should be the empty baseline, got %+v", prev)
	}
	if !deadline.After(time.Now()) {
		t.Fatal("deadline should be in the future")
	}
	if _, _, ok := m.PendingApply(); !ok {
		t.Fatal("expected a pending apply after staging")
	}

	if err := m.ConfirmHostApply(); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := m.PendingApply(); ok {
		t.Fatal("pending apply should be cleared after confirm")
	}
	got := hs.Get()
	if len(got.Input) != 1 || got.Input[0].Port != 8085 {
		t.Fatalf("confirmed rules not persisted: %+v", got)
	}
}

// Safe Apply: staging with a drop on a protected port is refused before
// anything touches the kernel or the store.
func TestSafeApply_AntiLockoutRefusedBeforeApply(t *testing.T) {
	dir := t.TempDir()
	m, hs := testHostManager(t, dir, HostFirewall{})
	bad := HostFirewall{Input: []HostInputRule{{ID: "r", Proto: "tcp", Port: 22, Action: "drop"}}}
	if _, _, err := m.StageHostApply(bad); err == nil {
		t.Fatal("expected anti-lockout rejection")
	}
	if _, _, ok := m.PendingApply(); ok {
		t.Fatal("no pending apply should exist after a rejected stage")
	}
	if got := hs.Get(); !got.IsEmpty() {
		t.Fatalf("store must be untouched: %+v", got)
	}
}

// Safe Apply ROLLBACK: the timer fires and restores the previous
// ruleset when Confirm is not called within the window.
func TestSafeApply_TimeoutRollsBack(t *testing.T) {
	skipIfNotRoot(t)
	dir := t.TempDir()
	baseline := HostFirewall{Input: []HostInputRule{{ID: "r0", Proto: "tcp", Port: 8085, Action: "drop"}}}
	m, hs := testHostManager(t, dir, baseline)
	next := HostFirewall{Input: []HostInputRule{{ID: "r1", Proto: "tcp", Port: 8443, Action: "allow"}}}

	if _, _, err := m.StageHostApply(next); err != nil {
		t.Fatal(err)
	}
	// During the window the staged rules are live for building.
	rs := m.BuildRuleset()
	if !strings.Contains(rs, "tcp dport 8443 accept") {
		t.Fatalf("staged rules should be live during the window:\n%s", rs)
	}
	// The timer (150ms) fires and rolls back.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, ok := m.PendingApply(); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pending apply never rolled back")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Store still holds the baseline (never confirmed).
	got := hs.Get()
	if len(got.Input) != 1 || got.Input[0].Port != 8085 {
		t.Fatalf("store should hold the baseline after rollback: %+v", got)
	}
	// The kernel-facing ruleset is back to the baseline.
	rs = m.BuildRuleset()
	if strings.Contains(rs, "tcp dport 8443 accept") || !strings.Contains(rs, "tcp dport 8085 drop") {
		t.Fatalf("ruleset not restored to baseline after rollback:\n%s", rs)
	}
}

// Confirm/Rollback with no pending apply returns ErrNoPendingApply /
// false.
func TestSafeApply_NoPendingConfirmRollback(t *testing.T) {
	skipIfNotRoot(t)
	dir := t.TempDir()
	m, _ := testHostManager(t, dir, HostFirewall{})
	if err := m.ConfirmHostApply(); err != ErrNoPendingApply {
		t.Fatalf("confirm without pending: got %v, want ErrNoPendingApply", err)
	}
	if rolled, _ := m.RollbackHostApply(); rolled {
		t.Fatal("rollback without pending should report false")
	}
}

// Two staged applies in flight are refused.
func TestSafeApply_SingleFlight(t *testing.T) {
	skipIfNotRoot(t)
	dir := t.TempDir()
	m, _ := testHostManager(t, dir, HostFirewall{})
	one := HostFirewall{Input: []HostInputRule{{ID: "r", Proto: "tcp", Port: 8085, Action: "drop"}}}
	two := HostFirewall{Input: []HostInputRule{{ID: "r", Proto: "tcp", Port: 8081, Action: "allow"}}}
	if _, _, err := m.StageHostApply(one); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.StageHostApply(two); err == nil {
		t.Fatal("second stage while one is pending should be refused")
	}
	if err := m.ConfirmHostApply(); err != nil {
		t.Fatal(err)
	}
}

// HostStore persists and reloads.
func TestHostStorePersists(t *testing.T) {
	dir := t.TempDir()
	fw := HostFirewall{
		Input:    []HostInputRule{{ID: "r", Proto: "tcp", Port: 8085, Action: "drop"}},
		Forwards: []HostForwardRule{{ID: "f", Proto: "tcp", HostPort: 80, GuestIP: "192.168.1.9", GuestPort: 8080}},
	}
	hs := NewHostStore(dir)
	if err := hs.Set(fw); err != nil {
		t.Fatal(err)
	}
	hs2 := NewHostStore(dir)
	if err := hs2.Load(); err != nil {
		t.Fatal(err)
	}
	got := hs2.Get()
	if len(got.Input) != 1 || len(got.Forwards) != 1 {
		t.Fatalf("reload mismatch: %+v", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, "firewall-host.json")); err != nil {
		t.Fatal(err)
	} else if fi.Mode().Perm() != 0o600 {
		t.Errorf("firewall-host.json mode = %v, want 0600", fi.Mode().Perm())
	}
}
