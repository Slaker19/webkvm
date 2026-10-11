package api

import (
	"strings"
	"testing"
)

// #10: the host terminal must not inherit the root service's environment.
func TestHostTerminalEnv_IsAllowListOnly(t *testing.T) {
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "super-secret")
	t.Setenv("JWT_SECRET", "another-secret")
	env := hostTerminalEnv()
	allowed := map[string]bool{"TERM": true, "COLORTERM": true, "LANG": true, "LC_ALL": true}
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		if !allowed[k] {
			t.Fatalf("unexpected variable %q in host terminal env", k)
		}
		if strings.Contains(e, "secret") {
			t.Fatalf("secret leaked: %q", e)
		}
	}
	if len(env) != len(allowed) {
		t.Fatalf("env = %v", env)
	}
}
