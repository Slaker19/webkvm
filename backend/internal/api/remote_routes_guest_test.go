package api

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Routes whose handlers shell out to the LOCAL virsh binary (guest-agent
// commands) or sample local metrics must answer 501 remote_unsupported on a
// remote node instead of failing with `exec: "virsh": executable file not found`.
func TestLocalProcessGuestRoutesAreClassL(t *testing.T) {
	for _, k := range []string{
		"GET /guest-info", "POST /guest/exec", "POST /guest/sync-time", "POST /clipboard",
		"GET /metrics", "GET /metrics/history",
	} {
		c, ok := vmRouteClasses[k]
		if !ok || c != classL {
			t.Errorf("%s: class = %v (known=%v), want L", k, c, ok)
		}
	}
}

// 8.29 #2: the VM detail page skips these reads for a remote VM
// (frontend/src/lib/utils/remoteRoutes.js REMOTE_UNSUPPORTED_LOADS) because
// they are class L. If one of them ever becomes A/B, the page would hide a
// working panel: this test fails so both lists move together.
func TestFrontendSkippedRemoteLoadsAreClassL(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "lib", "utils", "remoteRoutes.js"))
	if err != nil {
		t.Skipf("frontend source not available: %v", err)
	}
	m := regexp.MustCompile(`REMOTE_UNSUPPORTED_LOADS = \[([^\]]*)\]`).FindSubmatch(src)
	if m == nil {
		t.Fatal("REMOTE_UNSUPPORTED_LOADS not found in remoteRoutes.js")
	}
	names := regexp.MustCompile(`'([a-z-]+)'`).FindAllSubmatch(m[1], -1)
	if len(names) == 0 {
		t.Fatal("REMOTE_UNSUPPORTED_LOADS is empty")
	}
	for _, n := range names {
		k := "GET /" + string(n[1])
		if c, ok := vmRouteClasses[k]; !ok || c != classL {
			t.Errorf("%s: class = %v (known=%v), want L", k, c, ok)
		}
	}
}
