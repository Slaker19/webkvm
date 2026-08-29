package cloudinit

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	valid := []Config{
		{User: "webkvm", Password: "secret1", Hostname: "vm1"},
		{User: "deploy_user", Password: "secret1", SSHKey: "ssh-rsa AAAA test"},
		{Hostname: "my-host.local"},
		{SSHKey: "ssh-ed25519 AAAAx3zaC1yc2EAAA test"},
	}
	for i, c := range valid {
		if err := c.Validate(); err != nil {
			t.Errorf("case %d: unexpected error: %v", i, err)
		}
	}
	invalid := []Config{
		{},                             // empty
		{User: "Bad Name!"},            // bad user
		{User: "admin"},                // collides with a system group
		{User: "deploy_user"},          // user without password (password required)
		{User: "webkvm", Password: "12345"},        // password too short
		{User: "webkvm", Password: "1234567890123"}, // password too long
		{Hostname: "../evil"},          // bad hostname
		{SSHKey: "garbage key"},        // not a key
		{SSHKey: "ssh-ed25519 AA\nBB"}, // multiline / newline injection
	}
	for i, c := range invalid {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: expected error, got none for %+v", i, c)
		}
	}
}

func TestYamlSingleQuote(t *testing.T) {
	if got := yamlSingleQuote("it's"); got != "'it''s'" {
		t.Fatalf("expected doubled single quote, got %s", got)
	}
}

func TestBuildUserDataProvisionScript(t *testing.T) {
	cfg := Config{
		User:             "webkvm",
		Password:         "secret1",
		ProvisionScript:  "#!/bin/bash\napt-get install -y myapp\n",
	}
	ud := buildUserData(cfg)

	// The script must be embedded as base64 under write_files.
	if !strings.Contains(ud, "/usr/local/bin/webkvm-provision.sh") {
		t.Fatal("expected provision script path in user-data")
	}
	if !strings.Contains(ud, "!!binary |") {
		t.Fatal("expected base64 literal for the provision script")
	}
	// The base64-encoded script must be present.
	encoded := base64.StdEncoding.EncodeToString([]byte(cfg.ProvisionScript))
	if !strings.Contains(ud, encoded) {
		t.Fatal("expected base64-encoded provision script in user-data")
	}
	// The script must be executed on first boot via the logging runner.
	if !strings.Contains(ud, "/usr/local/bin/webkvm-run-provision") {
		t.Fatal("expected provision runner in user-data")
	}
	// The runner must record execution status and log output.
	if !strings.Contains(ud, "/run/webkvm-provision.status") {
		t.Fatal("expected provisioning status file in user-data")
	}
	if !strings.Contains(ud, "/var/log/webkvm-provision.log") {
		t.Fatal("expected provisioning log file in user-data")
	}

	// Without a script, no write_files/execution block should be emitted.
	plain := buildUserData(Config{User: "webkvm", Password: "secret1"})
	if strings.Contains(plain, "webkvm-provision.sh") {
		t.Fatal("provision script block should be absent when no script is set")
	}
}

// Regression test: buildUserData must emit exactly one top-level
// write_files: key. A second one (previously emitted unconditionally
// for the serial-console terminal hook, after the provisioning
// script's own write_files: block) is invalid YAML at the document
// level and silently drops one of the two lists when parsed, which
// used to make the provisioning script vanish whenever a VM combined
// ProvisionScript with the always-on terminal hook.
func TestBuildUserDataSingleWriteFilesBlock(t *testing.T) {
	cfg := Config{
		User:            "webkvm",
		Password:        "secret1",
		ProvisionScript: "#!/bin/bash\necho hi\n",
	}
	ud := buildUserData(cfg)

	count := strings.Count(ud, "write_files:")
	if count != 1 {
		t.Fatalf("expected exactly 1 top-level write_files: key, got %d in:\n%s", count, ud)
	}
	if !strings.Contains(ud, "/usr/local/bin/webkvm-provision.sh") {
		t.Fatal("expected provision script entry under the single write_files: block")
	}
	if !strings.Contains(ud, "/etc/profile.d/zz-webkvm-term.sh") {
		t.Fatal("expected terminal-hook entry under the single write_files: block")
	}
}
