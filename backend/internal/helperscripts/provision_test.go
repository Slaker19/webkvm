package helperscripts

import (
	"strings"
	"testing"
)

func TestValidSlug(t *testing.T) {
	for _, s := range []string{"adguard", "actualbudget", "paperless-ngx", "2fauth", "alpine-cinny"} {
		if !ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = false, want true", s)
		}
	}
	// A slug is interpolated straight into a URL that is downloaded and
	// executed as root. Anything that could redirect that fetch to
	// another path or another host has to be refused outright.
	for _, s := range []string{
		"../../etc/passwd",
		"a/b",
		"app.sh",
		"app%2e%2e",
		"Adguard",
		"-leading",
		"",
		"http://evil.test/x",
		strings.Repeat("a", 65),
	} {
		if ValidSlug(s) {
			t.Errorf("ValidSlug(%q) = true, want false", s)
		}
	}
}

func TestBuildProvisionScript_RejectsBadSlug(t *testing.T) {
	if _, err := BuildProvisionScript(Script{Slug: "../evil", Name: "x"}); err == nil {
		t.Fatal("a traversal slug must not produce a script")
	}
}

func TestBuildProvisionScript_Structure(t *testing.T) {
	out, err := BuildProvisionScript(Script{
		Slug: "adguard", Name: "AdGuard Home", Port: 3000, WebPath: "/admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"#!/usr/bin/env bash",
		InstallURL("adguard"),
		LauncherURL("adguard"),
		"export APP='AdGuard Home'",
		"community-scripts",
		"port 3000/admin",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated script is missing %q", want)
		}
	}
	// The shim must be embedded, not referenced: the container has no
	// access to WebKVM's filesystem.
	if !strings.Contains(out, "_webkvm_bootstrap_prereqs") {
		t.Error("the shim must be inlined into the generated script")
	}
	// catch_errors has to come from upstream's error handler. A lenient
	// local reimplementation once turned a failed Node.js setup into a
	// successful-looking deploy with a dead service.
	if !strings.Contains(out, "error_handler.func") {
		t.Error("the shim must load error_handler.func for catch_errors")
	}
}

// App names come from upstream APP= and routinely contain spaces and
// punctuation; an unquoted assignment would split or, worse, execute.
func TestBuildProvisionScript_QuotesName(t *testing.T) {
	out, err := BuildProvisionScript(Script{Slug: "x", Name: `Bob's "App" & co; rm -rf /`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `export APP='Bob'\''s "App" & co; rm -rf /'`) {
		t.Errorf("name was not shell-quoted safely:\n%s", firstLines(out, 14))
	}
}

func TestShimIsEmbedded(t *testing.T) {
	s := Shim()
	if len(s) < 1000 {
		t.Fatalf("shim looks empty (%d bytes)", len(s))
	}
	for _, want := range []string{
		"setting_up_container",
		"network_check",
		"update_os",
		"motd_ssh",
		"customize",
		"verb_ip6",
		"_webkvm_bootstrap_prereqs",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("shim does not define %q", want)
		}
	}
	// Defining catch_errors locally is what masked a hard failure.
	if strings.Contains(s, "\ncatch_errors() {") {
		t.Error("the shim must not define its own catch_errors")
	}
}

func TestInstallAndLauncherURLs(t *testing.T) {
	if got := InstallURL("adguard"); got != rawBase+"/install/adguard-install.sh" {
		t.Errorf("InstallURL = %q", got)
	}
	if got := LauncherURL("adguard"); got != rawBase+"/ct/adguard.sh" {
		t.Errorf("LauncherURL = %q", got)
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	if len(parts) > n {
		parts = parts[:n]
	}
	return strings.Join(parts, "\n")
}
