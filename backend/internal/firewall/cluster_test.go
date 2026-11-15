package firewall

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultClusterProfileDisabled(t *testing.T) {
	if DefaultClusterProfile().Enabled {
		t.Fatal("the cluster firewall must ship disabled")
	}
}

func TestRenderClusterRulesetMatrix(t *testing.T) {
	rs, err := RenderClusterRuleset(testClusterProfile())
	if err != nil {
		t.Fatal(err)
	}
	wants := []string{
		"table inet webkvm_cluster",
		"policy drop;",
		"ct state established,related accept",
		"iif lo accept",
		"icmpv6 type {",
		"nd-neighbor-solicit",
		"nd-router-advert",
		`iifname "incusbr0" udp dport 67 accept`,
		`iifname "incusbr0" tcp dport 53 accept`,
		`iifname "virbr0" udp dport 53 accept`,
		`iifname "virbr0" udp dport 547 accept`,
		"tcp dport { 8443, 2224, 3366, 3370, 7000 }",
		"udp dport 5405",
		"49152-49215",
		"tcp dport { 22, 8080 }",
	}
	for _, want := range wants {
		if !strings.Contains(rs, want) {
			t.Fatalf("ruleset missing %q:\n%s", want, rs)
		}
	}
	for _, forbidden := range []string{"dport 111", "dport 5900"} {
		if strings.Contains(rs, forbidden) {
			t.Fatalf("ruleset must not open %q:\n%s", forbidden, rs)
		}
	}
}

func TestRenderClusterDeterministic(t *testing.T) {
	a := testClusterProfile()
	a.ManagementCIDRs = []string{"203.0.113.7/32", "10.20.30.0/24", "203.0.113.0/24"}
	a.Bridges = []string{"virbr0", "incusbr0"}
	b := testClusterProfile()
	b.ManagementCIDRs = []string{"203.0.113.0/24", "203.0.113.7/32", "10.20.30.0/24"}
	b.Bridges = []string{"incusbr0", "virbr0"}
	ra, err := RenderClusterRuleset(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := RenderClusterRuleset(b)
	if err != nil {
		t.Fatal(err)
	}
	if ra != rb {
		t.Fatalf("render is not deterministic:\n--- a\n%s\n--- b\n%s", ra, rb)
	}
}

func TestValidateClusterProfile(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*ClusterProfile)
	}{
		{"bad cluster cidr", func(p *ClusterProfile) { p.ClusterCIDR = "nope" }},
		{"empty mgmt", func(p *ClusterProfile) { p.ManagementCIDRs = nil }},
		{"bad bridge", func(p *ClusterProfile) { p.Bridges = []string{"incusbr0;id"} }},
		{"bad migration", func(p *ClusterProfile) { p.MigrationPorts = "99999" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := testClusterProfile()
			tc.mut(&p)
			if err := ValidateClusterProfile(p); err == nil {
				t.Fatalf("expected validation error for %s", tc.name)
			}
		})
	}
}

func newTestClusterManager(t *testing.T) (*ClusterManager, *[]string, *int) {
	t.Helper()
	m, err := NewClusterManager(filepath.Join(t.TempDir(), "cluster-firewall.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	applied := []string{}
	flushed := 0
	m.applyFn = func(s string) error { applied = append(applied, s); return nil }
	m.flushFn = func() error { flushed++; return nil }
	m.readFn = func() string { return "" }
	return m, &applied, &flushed
}

func TestClusterStageRefusesDisabled(t *testing.T) {
	m, applied, _ := newTestClusterManager(t)
	p := testClusterProfile() // Enabled=false
	if _, err := m.Stage(p); !errors.Is(err, ErrClusterFirewallDisabled) {
		t.Fatalf("Stage error = %v, want ErrClusterFirewallDisabled", err)
	}
	if len(*applied) != 0 {
		t.Fatal("a disabled profile must not reach nft")
	}
}

func TestClusterSafeApplyRollback(t *testing.T) {
	m, applied, flushed := newTestClusterManager(t)
	p := testClusterProfile()
	p.Enabled = true
	deadline, err := m.Stage(p)
	if err != nil {
		t.Fatal(err)
	}
	if !deadline.After(time.Now()) {
		t.Fatal("deadline must be in the future")
	}
	if len(*applied) != 1 {
		t.Fatalf("apply calls = %d, want 1", len(*applied))
	}
	rolled, err := m.Rollback()
	if err != nil || !rolled {
		t.Fatalf("Rollback = (%v,%v), want (true,nil)", rolled, err)
	}
	if *flushed != 1 {
		t.Fatalf("flush calls = %d, want 1 (no previous ruleset)", *flushed)
	}
	if rolled, _ := m.Rollback(); rolled {
		t.Fatal("second Rollback must be a no-op")
	}
}

func TestClusterConfirmPersists(t *testing.T) {
	m, applied, _ := newTestClusterManager(t)
	p := testClusterProfile()
	p.Enabled = true
	if _, err := m.Stage(p); err != nil {
		t.Fatal(err)
	}
	if err := m.Confirm(); err != nil {
		t.Fatal(err)
	}
	if len(*applied) != 1 {
		t.Fatalf("apply calls = %d, want 1", len(*applied))
	}
	if got := m.Profile().Enabled; !got {
		t.Fatal("confirmed profile must persist enabled=true")
	}
	if _, err := os.Stat(m.path); err != nil {
		t.Fatalf("profile was not persisted: %v", err)
	}
}

func TestClusterApplyInteractiveConfirm(t *testing.T) {
	m, applied, _ := newTestClusterManager(t)
	p := testClusterProfile()
	p.Enabled = true
	confirm := filepath.Join(t.TempDir(), "confirm")
	rollback := filepath.Join(t.TempDir(), "rollback")
	go func() {
		time.Sleep(150 * time.Millisecond)
		_ = os.WriteFile(confirm, []byte("ok"), 0o600)
	}()
	if err := m.ApplyInteractive(p, confirm, rollback, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if len(*applied) != 1 {
		t.Fatalf("apply calls = %d, want 1", len(*applied))
	}
	if !m.Profile().Enabled {
		t.Fatal("confirmed profile must persist enabled=true")
	}
}

func TestClusterApplyInteractiveTimeout(t *testing.T) {
	m, applied, flushed := newTestClusterManager(t)
	p := testClusterProfile()
	p.Enabled = true
	confirm := filepath.Join(t.TempDir(), "confirm")
	rollback := filepath.Join(t.TempDir(), "rollback")
	err := m.ApplyInteractive(p, confirm, rollback, 100*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if len(*applied) != 1 {
		t.Fatalf("apply calls = %d, want 1", len(*applied))
	}
	if *flushed != 1 {
		t.Fatalf("flush calls = %d, want 1 after rollback", *flushed)
	}
}

func TestDefaultClusterProfileHasNoLabNetworks(t *testing.T) {
	p := DefaultClusterProfile()
	if p.ClusterCIDR != "" || len(p.ManagementCIDRs) != 0 {
		t.Fatalf("default profile must not ship lab networks: %+v", p)
	}
}

func TestValidateClusterProfileRequiresConfiguredNetworks(t *testing.T) {
	err := ValidateClusterProfile(DefaultClusterProfile())
	if err == nil || !strings.Contains(err.Error(), "cluster_cidr is required") {
		t.Fatalf("empty cluster_cidr must be rejected: %v", err)
	}
	p := DefaultClusterProfile()
	p.ClusterCIDR = "10.20.30.0/24"
	err = ValidateClusterProfile(p)
	if err == nil || !strings.Contains(err.Error(), "management_cidrs must contain") {
		t.Fatalf("empty management_cidrs must be rejected: %v", err)
	}
}
