package api

import (
	"regexp"
	"strings"
	"testing"
)

func directive(csp, name string) string {
	for _, d := range strings.Split(csp, ";") {
		d = strings.TrimSpace(d)
		if strings.HasPrefix(d, name+" ") {
			return d
		}
	}
	return ""
}

// #25: connect-src must not allow arbitrary ws:/wss: hosts.
func TestCSP_ConnectSrcIsSelfOnly(t *testing.T) {
	if got := directive(contentSecurityPolicy, "connect-src"); got != "connect-src 'self'" {
		t.Fatalf("SPA connect-src = %q", got)
	}
	rr, _ := renderConsolePage(t)
	csp := rr.Header().Get("Content-Security-Policy")
	if got := directive(csp, "connect-src"); got != "connect-src 'self'" {
		t.Fatalf("console connect-src = %q", got)
	}
}

// #25: the console page authorises its inline script with a per-response
// nonce rather than 'unsafe-inline'.
func TestConsolePage_ScriptUsesNonce(t *testing.T) {
	rr1, body1 := renderConsolePage(t)
	rr2, _ := renderConsolePage(t)
	csp1 := rr1.Header().Get("Content-Security-Policy")
	if strings.Contains(directive(csp1, "script-src"), "unsafe-inline") {
		t.Fatalf("script-src must not allow unsafe-inline: %s", csp1)
	}
	m := regexp.MustCompile(`'nonce-([^']+)'`).FindStringSubmatch(directive(csp1, "script-src"))
	if m == nil {
		t.Fatalf("no nonce in script-src: %s", csp1)
	}
	if !strings.Contains(body1, `<script type="module" nonce="`+m[1]+`">`) {
		t.Fatal("inline script does not carry the nonce from the header")
	}
	if strings.Count(body1, "<script") != strings.Count(body1, `nonce="`+m[1]+`"`) {
		t.Fatal("every inline <script> must carry the nonce")
	}
	if csp1 == rr2.Header().Get("Content-Security-Policy") {
		t.Fatal("nonce must differ per response")
	}
}
