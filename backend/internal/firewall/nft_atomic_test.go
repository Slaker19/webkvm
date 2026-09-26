package firewall

import (
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	s := NewStore(t.TempDir())
	if err := s.Set(VMFirewall{
		VMID:  "vm1",
		Rules: []Rule{{ID: "r1", Proto: "tcp", Port: 8080, Action: "drop"}},
	}); err != nil {
		t.Fatal(err)
	}
	return NewManager(s, func(id string) string { return "192.168.1.50" }, 8081, slog.Default())
}

// TestRulesetReplacesTableAtomically pins the fix for a bug that could
// leave the host with NO firewall at all.
//
// applyRuleset used to run `nft delete table ip webkvm` itself, before
// the `nft -c` syntax check. When the generated ruleset turned out to
// be invalid, the delete had already landed: every per-VM rule and
// every host input rule was gone, the API answered "nft check failed"
// as if nothing had happened, and the UI kept listing the rules as
// active. The operator had no way to tell the firewall was down.
//
// The teardown now lives inside the ruleset, so nft applies the delete
// and the new definition in a single transaction: a parse error leaves
// the previous ruleset untouched.
func TestRulesetReplacesTableAtomically(t *testing.T) {
	rs := newTestManager(t).BuildRuleset()

	del := strings.Index(rs, "delete table ip webkvm")
	def := strings.Index(rs, "table ip webkvm {")
	if del < 0 {
		t.Fatalf("ruleset must remove the previous table itself:\n%s", rs)
	}
	if def < 0 {
		t.Fatalf("ruleset must define the table:\n%s", rs)
	}
	if del > def {
		t.Errorf("the delete must precede the definition:\n%s", rs)
	}
	// The bare `table ip webkvm` declaration is what makes the delete
	// safe on a host where the table does not exist yet.
	if !strings.Contains(rs[:del], "table ip webkvm\n") {
		t.Errorf("missing the create-if-absent declaration before the delete:\n%s", rs)
	}
}

// TestRulesetIsValidNft feeds the generated ruleset to the real nft
// parser (`nft -c`, check only — it changes nothing). A generator that
// emits syntactically invalid nft is a firewall that silently never
// applies, so this is worth asserting against the actual binary rather
// than against our own idea of the syntax. Skipped where nft is absent
// or not usable unprivileged.
func TestRulesetIsValidNft(t *testing.T) {
	if _, err := exec.LookPath("nft"); err != nil {
		t.Skip("nft not installed")
	}
	if os.Geteuid() != 0 {
		t.Skip("nft -c needs root to read the current ruleset")
	}
	rs := newTestManager(t).BuildRuleset()

	f, err := os.CreateTemp(t.TempDir(), "webkvm-nft-*.rules")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(rs); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if out, err := exec.Command("nft", "-c", "-f", f.Name()).CombinedOutput(); err != nil {
		t.Fatalf("nft rejected the generated ruleset: %v: %s\n---\n%s",
			err, strings.TrimSpace(string(out)), rs)
	}
}
