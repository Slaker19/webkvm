package cloudinit

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	valid := []Config{
		{User: "webkvm", Password: "secret1", Hostname: "vm1"},
		{User: "deploy_user", Password: "secret1", SSHKey: "ssh-rsa AAAA test"},
		{Hostname: "my-host.local"},
		{SSHKey: "ssh-ed25519 AAAAx3zaC1yc2EAAA test"},
		// v1.4 Fase 4.1: root-only — password without a dedicated user.
		{Password: "secret1", Hostname: "ct1"},
		{Password: "secret1"},
	}
	for i, c := range valid {
		if err := c.Validate(); err != nil {
			t.Errorf("case %d: unexpected error: %v", i, err)
		}
	}
	invalid := []Config{
		{},                                  // empty
		{User: "Bad Name!"},                 // bad user
		{User: "admin"},                     // collides with a system group
		{User: "deploy_user"},               // user without password (password required)
		{User: "webkvm", Password: "12345"}, // password too short
		{User: "webkvm", Password: "1234567890123"}, // password too long
		{Hostname: "../evil"},                       // bad hostname
		{SSHKey: "garbage key"},                     // not a key
		{SSHKey: "ssh-ed25519 AA\nBB"},              // multiline / newline injection
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
		User:            "webkvm",
		Password:        "secret1",
		ProvisionScript: "#!/bin/bash\napt-get install -y myapp\n",
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

// v1.4 Fase 4.1: a password without a dedicated user must be applied to
// root (users: - name: root ... lock_passwd: false).
func TestBuildUserDataRootPassword(t *testing.T) {
	ud := buildUserData(Config{Password: "secret1", Hostname: "ct1"})
	if !strings.Contains(ud, "  - name: root\n") {
		t.Fatalf("expected root user block, got:\n%s", ud)
	}
	if !strings.Contains(ud, "lock_passwd: false") {
		t.Fatalf("expected lock_passwd: false for root, got:\n%s", ud)
	}
	if !strings.Contains(ud, "passwd: $6$") {
		t.Fatalf("expected a $6$ crypt hash for the root password, got:\n%s", ud)
	}
	if strings.Contains(ud, "- name: webkvm") {
		t.Fatal("must not create a dedicated user in root-only mode")
	}
}

// v1.4 Fase 4.1: SkipGuestAgent removes the qemu-guest-agent package and
// its systemd enable step (containers have no guest agent).
func TestBuildUserDataSkipGuestAgent(t *testing.T) {
	ud := buildUserData(Config{User: "webkvm", Password: "secret1", SkipGuestAgent: true})
	if strings.Contains(ud, "qemu-guest-agent") {
		t.Fatalf("SkipGuestAgent still references qemu-guest-agent:\n%s", ud)
	}
	with := buildUserData(Config{User: "webkvm", Password: "secret1"})
	if !strings.Contains(with, "qemu-guest-agent") {
		t.Fatal("default config must keep installing the guest agent")
	}
}

func TestBuildUserDataMultiDistroNetworkAndGuestAgent(t *testing.T) {
	ud := buildUserData(Config{User: "webkvm", Password: "secret1"})
	if !strings.Contains(ud, "groups: sudo,adm,wheel") {
		t.Fatal("expected wheel group for Arch/RHEL/Fedora compatibility")
	}
	if !strings.Contains(ud, "/etc/systemd/network/20-webkvm-dhcp.network") {
		t.Fatal("expected systemd-networkd DHCP fallback configuration in write_files")
	}
	if !strings.Contains(ud, "systemd-networkd") {
		t.Fatal("expected systemd-networkd activation in runcmd")
	}
	if !strings.Contains(ud, "pacman -Sy --noconfirm qemu-guest-agent") {
		t.Fatal("expected pacman installer fallback in runcmd for Arch Linux")
	}
	if !strings.Contains(ud, "apt-get install -y qemu-guest-agent") {
		t.Fatal("expected apt-get installer fallback in runcmd for Debian/Ubuntu")
	}
	if !strings.Contains(ud, "dnf install -y qemu-guest-agent") {
		t.Fatal("expected dnf installer fallback in runcmd for Fedora/RHEL")
	}
}

// TestBuildUserDataNetworkdRestartsIfAlreadyActive is a regression test
// for F12-05: `systemctl enable --now systemd-networkd` is a no-op on
// cloud images that ship systemd-networkd pre-enabled and already
// running by preset (confirmed live: the official Arch Linux cloud
// image boots with systemd-networkd already active before this runcmd
// entry executes). `enable --now` never restarts an already-running
// unit, so the 20-webkvm-dhcp.network file written by write_files was
// never picked up and the VM was left with no DHCP lease at all
// (reproduced live: `networkctl status eth0` showed "off (unmanaged)"
// with zero addresses, until a manual `systemctl restart
// systemd-networkd` picked up the new config and acquired a lease).
// This asserts the runcmd also pairs it with `try-restart`, which
// reloads the config on an already-active unit and is a safe no-op
// otherwise.
func TestBuildUserDataNetworkdRestartsIfAlreadyActive(t *testing.T) {
	ud := buildUserData(Config{User: "webkvm", Password: "secret1"})
	if !strings.Contains(ud, "systemctl try-restart systemd-networkd") {
		t.Fatal("expected 'systemctl try-restart systemd-networkd' alongside 'enable --now' " +
			"so hosts that ship systemd-networkd already active (e.g. Arch Linux's cloud " +
			"image) actually pick up the DHCP fallback .network file instead of silently " +
			"keeping the interface unconfigured")
	}
}

// TestBuildUserDataNetworkFixRunsInBootcmd is a regression test for
// F12-06: a real boot deadlock, not just a no-op. The DHCP fallback
// used to be applied only from `runcmd`, which cloud-init does not
// execute until the LAST stage (`cloud-final`, via the scripts_user
// module). `cloud-final.service` ships with `After=time-sync.target`,
// and `systemd-time-wait-sync.service` blocks that target FOREVER
// (TimeoutStartSec=infinity) waiting for an NTP sync that itself
// requires working network. On a systemd-networkd distro that ships
// the unit already active by preset (confirmed live: Arch Linux's
// official cloud image) but never restarted to read the new .network
// file, there is no network yet — so NTP never syncs, cloud-final
// never starts, runcmd never runs, and the VM hangs indefinitely.
// Reproduced live: `cloud-init status` stayed "running" and
// `systemd-time-wait-sync` stayed "activating" for 15+ minutes, until
// a *manual* `systemctl restart systemd-networkd` from outside the
// guest broke the cycle and cloud-final completed within seconds.
//
// This asserts the DHCP fallback + restart is ALSO applied from
// `bootcmd`, which cloud-init runs in the pre-network local stage
// (`cloud-init-local.service`, ordered `Before=network-pre.target`) —
// long before anything can block on time-sync or network-online, so
// the race can never occur in the first place.
func TestBuildUserDataNetworkFixRunsInBootcmd(t *testing.T) {
	ud := buildUserData(Config{User: "webkvm", Password: "secret1"})
	const marker = "bootcmd:\n"
	bootcmdIdx := strings.Index(ud, marker)
	if bootcmdIdx < 0 {
		t.Fatal("expected a top-level 'bootcmd:' section so the network fallback runs " +
			"in the pre-network local stage instead of only in runcmd (which can " +
			"deadlock behind time-sync.target on systemd-networkd distros)")
	}
	rest := ud[bootcmdIdx+len(marker):]
	// bootcmd is a single list item on one line ("  - [...]"); the
	// next top-level key starts at the next line that does not begin
	// with whitespace.
	lineEnd := strings.IndexByte(rest, '\n')
	if lineEnd < 0 {
		lineEnd = len(rest)
	}
	bootcmdLine := rest[:lineEnd]
	if !strings.Contains(bootcmdLine, "systemctl restart systemd-networkd") {
		t.Fatal("expected 'systemctl restart systemd-networkd' inside bootcmd " +
			"(unconditional restart, not try-restart: bootcmd runs pre-network, " +
			"before anything could be relying on an existing connection)")
	}
	if !strings.Contains(bootcmdLine, "/etc/systemd/network/20-webkvm-dhcp.network") {
		t.Fatal("expected bootcmd to write the DHCP fallback file itself " +
			"(cannot rely on write_files, which runs in the same 'init' stage " +
			"but is not guaranteed to run before bootcmd's own command)")
	}
	if strings.Count(bootcmdLine, "\n") > 0 {
		t.Fatal("bootcmd command must be single-line: a multi-line quoted YAML " +
			"scalar here would hit the exact same line-folding trap as F12-01")
	}
}

// TestBuildUserDataGuestAgentScriptIsValidShell is a regression test for
// F12-01: the qemu-guest-agent installer used to be embedded as a
// single-quoted multi-line YAML flow scalar inside
// `runcmd: [sh, -c, '...']`. YAML folds line breaks inside a quoted
// scalar into spaces, so cloud-init ran the script with every newline
// collapsed to a space — turning "then\n  if ..." into "then if ...",
// which is a bash syntax error ("syntax error near unexpected token
// `then'"). The guest agent was consequently never installed/started on
// ANY distro. This test extracts the actual script cloud-init would run
// (parsing the write_files `content: |` block, exactly like cloud-init's
// own block-scalar handling) and asserts `bash -n` accepts it — so a
// future regression to the old inline single-quoted form fails loudly
// instead of silently breaking the guest agent everywhere.
func TestBuildUserDataGuestAgentScriptIsValidShell(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	ud := buildUserData(Config{User: "webkvm", Password: "secret1"})

	const marker = "path: /usr/local/bin/webkvm-qga-installer.sh"
	idx := strings.Index(ud, marker)
	if idx < 0 {
		t.Fatal("expected write_files entry for webkvm-qga-installer.sh")
	}
	rest := ud[idx:]
	contentIdx := strings.Index(rest, "content: |")
	if contentIdx < 0 {
		t.Fatal("expected a block-scalar 'content: |' for webkvm-qga-installer.sh")
	}
	lines := strings.Split(rest[contentIdx:], "\n")[1:] // after "content: |"
	var script []string
	for _, line := range lines {
		if strings.HasPrefix(line, "      ") {
			script = append(script, strings.TrimPrefix(line, "      "))
			continue
		}
		break // dedent: end of the block scalar
	}
	if len(script) == 0 {
		t.Fatal("empty guest agent installer script extracted")
	}
	body := strings.Join(script, "\n")
	if !strings.Contains(body, "pacman") || !strings.Contains(body, "qemu-guest-agent") {
		t.Fatalf("extracted script looks wrong: %q", body)
	}

	tmp, err := os.CreateTemp(t.TempDir(), "qga-*.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString(body); err != nil {
		t.Fatal(err)
	}
	tmp.Close()

	out, err := exec.Command("bash", "-n", filepath.Clean(tmp.Name())).CombinedOutput()
	if err != nil {
		t.Fatalf("extracted guest-agent script is not valid bash: %v\n%s\n--- script ---\n%s", err, out, body)
	}
}

func TestGeneratePassword(t *testing.T) {
	length := 16
	pass1 := GeneratePassword(length)
	pass2 := GeneratePassword(length)

	// Check length
	if len(pass1) != length {
		t.Errorf("expected password length %d, got %d", length, len(pass1))
	}
	if len(pass2) != length {
		t.Errorf("expected password length %d, got %d", length, len(pass2))
	}

	// Check for randomness (very unlikely to generate the same password twice)
	if pass1 == pass2 {
		t.Errorf("expected consecutive generated passwords to be different, but both were %s", pass1)
	}

	// Check character set constraints
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	for _, char := range pass1 {
		if !strings.ContainsRune(charset, char) {
			t.Errorf("generated password contains invalid character: %c", char)
		}
	}
}

func TestBuildNetworkConfigStatic(t *testing.T) {
	cfg := Config{
		Networks: []NetworkConfig{
			{
				Interface: "eth0",
				IPv4:      "192.168.1.50/24",
				Gateway4:  "192.168.1.1",
				DNS:       []string{"1.1.1.1", "8.8.8.8"},
				Search:    []string{"lan"},
			},
		},
	}
	nc := buildNetworkConfig(cfg)
	if !strings.Contains(nc, "192.168.1.50/24") {
		t.Errorf("expected static IPv4 in network-config, got:\n%s", nc)
	}
	if !strings.Contains(nc, "gateway4: '192.168.1.1'") {
		t.Errorf("expected gateway4 in network-config, got:\n%s", nc)
	}
	if !strings.Contains(nc, "1.1.1.1") {
		t.Errorf("expected DNS in network-config, got:\n%s", nc)
	}
}

func TestValidateNetworkConfig(t *testing.T) {
	// Valid network config
	validCfg := Config{
		User:     "webkvm",
		Password: "password123",
		Networks: []NetworkConfig{
			{
				Interface: "eth0",
				IPv4:      "192.168.1.100/24",
				Gateway4:  "192.168.1.1",
				DNS:       []string{"1.1.1.1", "8.8.8.8"},
			},
		},
	}
	if err := validCfg.Validate(); err != nil {
		t.Errorf("expected valid config, got error: %v", err)
	}

	// Invalid IPv4 CIDR
	invalidCIDR := validCfg
	invalidCIDR.Networks = []NetworkConfig{{IPv4: "192.168.1.500/24"}}
	if err := invalidCIDR.Validate(); err == nil {
		t.Error("expected error for invalid IPv4 CIDR, got nil")
	}

	// Invalid Gateway4
	invalidGW := validCfg
	invalidGW.Networks = []NetworkConfig{{Gateway4: "192.168.1.500"}}
	if err := invalidGW.Validate(); err == nil {
		t.Error("expected error for invalid Gateway4, got nil")
	}

	// Invalid DNS
	invalidDNS := validCfg
	invalidDNS.Networks = []NetworkConfig{{DNS: []string{"invalid-dns-ip"}}}
	if err := invalidDNS.Validate(); err == nil {
		t.Error("expected error for invalid DNS, got nil")
	}

	// Invalid Interface Name
	invalidIface := validCfg
	invalidIface.Networks = []NetworkConfig{{Interface: "eth0;rm -rf /"}}
	if err := invalidIface.Validate(); err == nil {
		t.Error("expected error for invalid interface name, got nil")
	}
}
