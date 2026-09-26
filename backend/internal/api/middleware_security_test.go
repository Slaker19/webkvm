package api

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/frontend"
)

func headersFor(t *testing.T, target string, tls bool) http.Header {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if tls {
		req.TLS = &tlsStateStub
	}
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)
	return rec.Result().Header
}

func TestSecurityHeaders_Baseline(t *testing.T) {
	h := headersFor(t, "/api/vms", false)
	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "SAMEORIGIN",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if h.Get("Permissions-Policy") == "" {
		t.Error("Permissions-Policy missing")
	}
	if h.Get("Content-Security-Policy") == "" {
		t.Fatal("CSP missing on an API route")
	}
}

// HSTS over plain HTTP would pin the host to a scheme that may not be
// served, locking the operator out of their own panel.
func TestSecurityHeaders_HSTSOnlyOverTLS(t *testing.T) {
	if got := headersFor(t, "/", false).Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS set over plain HTTP: %q", got)
	}
	if got := headersFor(t, "/", true).Get("Strict-Transport-Security"); !strings.Contains(got, "max-age=") {
		t.Errorf("HSTS = %q over TLS, want a max-age", got)
	}
}

// The non-CSP headers apply to the console path like anywhere else; the
// CSP itself is covered by the two tests below, which pin the tricky
// part: never two policies, but never zero either.
func TestSecurityHeaders_ConsolePathKeepsOtherHeaders(t *testing.T) {
	h := headersFor(t, "/console/abc-123", false)
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Error("the console page must still get the non-CSP headers")
	}
	if got := h.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q on the console path", got)
	}
}

// A request rejected before the console handler runs (401, rate limit)
// never reaches the code that sets the console's own policy. It used to
// go out with no CSP at all — the one response most likely to be shown
// to an unauthenticated caller.
func TestSecurityHeaders_ConsoleErrorStillGetsCSP(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/console/abc-123", nil)
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})).ServeHTTP(rec, req)

	if got := rec.Result().Header.Get("Content-Security-Policy"); got == "" {
		t.Error("an error response on the console path went out with no CSP")
	}
}

// ...but when the console page does render, its own policy must survive
// untouched: two CSP headers are intersected and would break it.
func TestSecurityHeaders_ConsoleOwnPolicyWins(t *testing.T) {
	const own = "default-src 'self'; script-src 'self' 'unsafe-inline'"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/console/abc-123", nil)
	securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", own)
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	got := rec.Result().Header.Values("Content-Security-Policy")
	if len(got) != 1 || got[0] != own {
		t.Errorf("CSP = %v, want exactly the handler's own policy", got)
	}
}

// A CSP that keeps 'unsafe-inline' for scripts documents an intention
// rather than enforcing one.
func TestCSP_NoUnsafeInlineScript(t *testing.T) {
	csp := contentSecurityPolicy
	scriptDirective := ""
	for _, part := range strings.Split(csp, ";") {
		if strings.HasPrefix(strings.TrimSpace(part), "script-src") {
			scriptDirective = part
		}
	}
	if scriptDirective == "" {
		t.Fatal("no script-src directive")
	}
	if strings.Contains(scriptDirective, "unsafe-inline") || strings.Contains(scriptDirective, "unsafe-eval") {
		t.Errorf("script-src = %q, want no unsafe-* source", scriptDirective)
	}
	for _, d := range []string{"object-src 'none'", "base-uri 'none'", "frame-ancestors 'self'", "form-action 'self'"} {
		if !strings.Contains(csp, d) {
			t.Errorf("CSP missing %q", d)
		}
	}
}

// The whole point of hashing is that the hash matches the script that
// actually ships. If index.html changes and this is not recomputed, the
// UI breaks on load with a blocked script — so assert the link.
func TestCSP_HashesMatchBuiltIndex(t *testing.T) {
	distFS, err := fs.Sub(frontend.FS, "dist")
	if err != nil {
		t.Skip("frontend not embedded in this build")
	}
	data, err := fs.ReadFile(distFS, "index.html")
	if err != nil {
		t.Skip("no built index.html in this build")
	}
	var inline []string
	for _, m := range inlineScriptRe.FindAllStringSubmatch(string(data), -1) {
		if strings.TrimSpace(m[1]) != "" {
			inline = append(inline, m[1])
		}
	}
	if len(inline) == 0 {
		t.Skip("index.html has no inline script")
	}
	if len(indexScriptHashes) != len(inline) {
		t.Fatalf("hashed %d inline scripts, index.html has %d", len(indexScriptHashes), len(inline))
	}
	for _, body := range inline {
		sum := sha256.Sum256([]byte(body))
		want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		if !strings.Contains(contentSecurityPolicy, want) {
			t.Errorf("CSP does not allow the inline script shipped in index.html (%s)", want)
		}
	}
}

// A non-nil ConnectionState is all the middleware inspects.
var tlsStateStub = tls.ConnectionState{}
