// Package cloudinit generates NoCloud cloud-init seed images (an ISO
// with user-data / meta-data / network-config) that a VM boots to
// provision itself: create a user, inject an SSH key, set the
// hostname.
//
// The ISO is produced with xorriso (standard on Debian/Ubuntu), run
// with argument-separated exec (no shell), so there is no command
// injection surface. All free-text inputs are validated and YAML-
// escaped before being written, so a malicious value cannot break out
// of the seed.
package cloudinit

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tredoe/osutil/user/crypt/sha512_crypt"
)

// NetworkConfig specifies static IP assignment, default gateways and DNS servers for a NIC.
type NetworkConfig struct {
	Interface string   `json:"interface,omitempty"` // e.g. "eth0" or "enp1s0" (default: "eth0")
	IPv4      string   `json:"ipv4,omitempty"`      // CIDR format, e.g. "192.168.1.50/24"
	Gateway4  string   `json:"gateway4,omitempty"`  // IPv4 gateway, e.g. "192.168.1.1"
	IPv6      string   `json:"ipv6,omitempty"`      // CIDR format, e.g. "2001:db8::50/64"
	Gateway6  string   `json:"gateway6,omitempty"`  // IPv6 gateway
	DNS       []string `json:"dns,omitempty"`       // Nameserver IPs, e.g. ["1.1.1.1", "8.8.8.8"]
	Search    []string `json:"search,omitempty"`    // Search domains, e.g. ["lan", "local"]
}

// Config is the operator-supplied provisioning data.
type Config struct {
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	SSHKey   string `json:"ssh_key,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// Networks allows configuring static IP addresses, gateways and DNS servers.
	Networks []NetworkConfig `json:"networks,omitempty"`
	// CustomUserData allows providing a full #cloud-config YAML document
	// or custom shell script directly.
	CustomUserData string `json:"custom_user_data,omitempty"`
	// SnippetID optionally points to a reusable snippet in SnippetStore.
	SnippetID string `json:"snippet_id,omitempty"`
	// SnippetIDs allows selecting multiple reusable snippets to compose together.
	SnippetIDs []string `json:"snippet_ids,omitempty"`
	// ProvisionScript is an optional bash script injected into the seed
	// and executed on first boot (as root). Used by appliance "apps" to
	// install software on a base cloud image.
	ProvisionScript string `json:"-"`
	// SkipGuestAgent skips installing/starting the QEMU guest agent
	// (v1.4 Fase 4.1). Containers have no QEMU guest agent — the LXD
	// backend sets this so the provisioned cloud-init does not try to
	// install an irrelevant package on every container.
	SkipGuestAgent bool `json:"-"`
	// InstanceIDSuffix, when set, is appended to the generated
	// instance-id (webkvm-<hostname>-<suffix>). cloud-init treats a
	// changed instance-id as "new configuration" and re-runs
	// provisioning instead of skipping it as already-applied — needed
	// when reapplying cloud-init to a VM that already booted once with
	// the same hostname/instance-id.
	InstanceIDSuffix string `json:"-"`
}

var (
	userRe      = regexp.MustCompile(`^[a-z_][a-z0-9_\-]{0,31}$`)
	hostnameRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.\-]{0,62}$`)
	ifaceNameRe = regexp.MustCompile(`^[a-zA-Z0-9_\-\*]{1,16}$`)
	// SSH keys must be a single line starting with a known type.
	sshKeyRe = regexp.MustCompile(`^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp256|ecdsa-sha2-nistp384|ecdsa-sha2-nistp521|sk-ssh-ed25519|sk-ecdsa-sha2-nistp256) [A-Za-z0-9+/=]+[ \t]+[^\n]+$`)
	// System groups present in stock Debian/Ubuntu images. Cloud-init runs
	// `useradd <name> --groups sudo,adm` and, because no -g is given,
	// useradd tries to create a PRIMARY group named after the user. If that
	// name already exists as a group, useradd exits with code 9 and the user
	// is never created. Reject those names up front so provisioning cannot
	// silently fail at first boot.
	systemGroupRe = regexp.MustCompile(`(?i)^(root|daemon|bin|sys|sync|games|man|lp|mail|news|uucp|proxy|www-data|backup|list|irc|_apt|nobody|systemd-network|systemd-timesync|dhcpcd|messagebus|syslog|systemd-resolve|uuidd|tss|sshd|pollinate|tcpdump|landscape|fwupd-refresh|polkitd|sudo|adm|admin)$`)
)

// Validate checks the config fields. An all-empty config is an error
// (there is nothing to provision).
func (c Config) Validate() error {
	if c.User == "" && c.SSHKey == "" && c.Hostname == "" && c.Password == "" && c.CustomUserData == "" && c.SnippetID == "" && len(c.SnippetIDs) == 0 && len(c.Networks) == 0 {
		return errors.New("cloud-init needs at least a user, a password, an SSH key, a hostname, network configuration, or custom user-data")
	}
	if c.User != "" && !userRe.MatchString(c.User) {
		return fmt.Errorf("invalid cloud-init user %q (letters, digits, _ and - only)", c.User)
	}
	if c.User != "" && systemGroupRe.MatchString(c.User) {
		return fmt.Errorf("user name %q collides with a system group and would fail to provision; choose a different name", c.User)
	}
	// A password is required whenever a user is provisioned, so the VM
	// has a guaranteed access path (serial console) that does not depend
	// on SSH keys.
	if c.User != "" && c.Password == "" {
		return errors.New("password is required when provisioning a cloud-init user")
	}
	if c.Password != "" {
		if len(c.Password) < 6 {
			return fmt.Errorf("password must be at least 6 characters")
		}
		if len(c.Password) > 12 {
			return fmt.Errorf("password must be at most 12 characters")
		}
	}
	if c.Hostname != "" && !hostnameRe.MatchString(c.Hostname) {
		return fmt.Errorf("invalid cloud-init hostname %q", c.Hostname)
	}
	if c.SSHKey != "" && !sshKeyRe.MatchString(c.SSHKey) {
		return errors.New("invalid SSH key: expected a single line like 'ssh-ed25519 AAAA... comment'")
	}
	for i, n := range c.Networks {
		if n.Interface != "" && !ifaceNameRe.MatchString(n.Interface) {
			return fmt.Errorf("network [%d]: invalid interface name %q", i, n.Interface)
		}
		if n.IPv4 != "" {
			ip, _, err := net.ParseCIDR(n.IPv4)
			if err != nil || ip.To4() == nil {
				return fmt.Errorf("network [%d]: invalid ipv4 CIDR %q (e.g. 192.168.1.50/24)", i, n.IPv4)
			}
		}
		if n.Gateway4 != "" {
			gw := net.ParseIP(n.Gateway4)
			if gw == nil || gw.To4() == nil {
				return fmt.Errorf("network [%d]: invalid gateway4 IP %q", i, n.Gateway4)
			}
		}
		if n.IPv6 != "" {
			ip, _, err := net.ParseCIDR(n.IPv6)
			if err != nil || ip.To4() != nil {
				return fmt.Errorf("network [%d]: invalid ipv6 CIDR %q", i, n.IPv6)
			}
		}
		if n.Gateway6 != "" {
			gw := net.ParseIP(n.Gateway6)
			if gw == nil || gw.To4() != nil {
				return fmt.Errorf("network [%d]: invalid gateway6 IP %q", i, n.Gateway6)
			}
		}
		for _, d := range n.DNS {
			d = strings.TrimSpace(d)
			if d != "" && net.ParseIP(d) == nil {
				return fmt.Errorf("network [%d]: invalid DNS server IP %q", i, d)
			}
		}
	}
	return nil
}

// yamlSingleQuote escapes a value for single-quoted YAML.
func yamlSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func buildNetworkConfig(cfg Config) string {
	if len(cfg.Networks) == 0 {
		return "version: 2\n" +
			"ethernets:\n" +
			"  all-en:\n" +
			"    match:\n" +
			"      name: 'en*'\n" +
			"    dhcp4: true\n" +
			"    dhcp6: true\n" +
			"    optional: true\n" +
			"  all-eth:\n" +
			"    match:\n" +
			"      name: 'eth*'\n" +
			"    dhcp4: true\n" +
			"    dhcp6: true\n" +
			"    optional: true\n" +
			"  all-vi:\n" +
			"    match:\n" +
			"      name: 'vi*'\n" +
			"    dhcp4: true\n" +
			"    dhcp6: true\n" +
			"    optional: true\n"
	}

	var sb strings.Builder
	sb.WriteString("version: 2\nethernets:\n")
	for i, n := range cfg.Networks {
		iface := n.Interface
		if iface == "" {
			iface = fmt.Sprintf("eth%d", i)
		}
		sb.WriteString(fmt.Sprintf("  %s:\n", iface))
		sb.WriteString(fmt.Sprintf("    match:\n      name: %s\n", yamlSingleQuote(iface)))
		if n.IPv4 == "" && n.IPv6 == "" {
			sb.WriteString("    dhcp4: true\n    dhcp6: true\n")
		} else {
			sb.WriteString("    dhcp4: false\n    dhcp6: false\n    addresses:\n")
			if n.IPv4 != "" {
				sb.WriteString(fmt.Sprintf("      - %s\n", yamlSingleQuote(n.IPv4)))
			}
			if n.IPv6 != "" {
				sb.WriteString(fmt.Sprintf("      - %s\n", yamlSingleQuote(n.IPv6)))
			}
			if n.Gateway4 != "" {
				sb.WriteString(fmt.Sprintf("    gateway4: %s\n", yamlSingleQuote(n.Gateway4)))
			}
			if n.Gateway6 != "" {
				sb.WriteString(fmt.Sprintf("    gateway6: %s\n", yamlSingleQuote(n.Gateway6)))
			}
		}
		if len(n.DNS) > 0 || len(n.Search) > 0 {
			sb.WriteString("    nameservers:\n")
			if len(n.DNS) > 0 {
				sb.WriteString("      addresses:\n")
				for _, dns := range n.DNS {
					dns = strings.TrimSpace(dns)
					if dns != "" {
						sb.WriteString(fmt.Sprintf("        - %s\n", yamlSingleQuote(dns)))
					}
				}
			}
			if len(n.Search) > 0 {
				sb.WriteString("      search:\n")
				for _, s := range n.Search {
					s = strings.TrimSpace(s)
					if s != "" {
						sb.WriteString(fmt.Sprintf("        - %s\n", yamlSingleQuote(s)))
					}
				}
			}
		}
		sb.WriteString("    optional: true\n")
	}
	return sb.String()
}

// BuildNoCloudISO renders the seed files and produces the ISO at
// isoPath. Returns the ISO path (== isoPath) on success.
func BuildNoCloudISO(isoPath string, cfg Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}

	// Working directory next to the ISO (same filesystem, cleaned up).
	work := filepath.Join(filepath.Dir(isoPath), ".seed-"+filepath.Base(isoPath)+"-tmp")
	if err := os.MkdirAll(work, 0o700); err != nil {
		return "", err
	}
	defer os.RemoveAll(work)

	// user-data: create the user, inject password/SSH key, grant sudo.
	ud := buildUserData(cfg)

	// meta-data. A changed instance-id tells cloud-init this is new
	// configuration to (re-)apply, instead of skipping it as an
	// instance it already provisioned.
	instanceID := "webkvm-" + strings.ToLower(replaceSpace(cfg.Hostname))
	if cfg.InstanceIDSuffix != "" {
		instanceID += "-" + cfg.InstanceIDSuffix
	}
	md := "instance-id: " + instanceID + "\n"
	if cfg.Hostname != "" {
		md += "local-hostname: " + cfg.Hostname + "\n"
	}

	// network-config: default to DHCP or user-specified static IPs.
	nc := buildNetworkConfig(cfg)

	files := map[string]string{
		"user-data":      ud,
		"meta-data":      md,
		"network-config": nc,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(work, name), []byte(content), 0o644); err != nil {
			return "", err
		}
	}

	// Produce the ISO. xorriso -as mkisofs: -volid cidata is the
	// magic label NoCloud looks for.
	if _, err := exec.LookPath("xorriso"); err != nil {
		return "", errors.New("xorriso is required to generate cloud-init seeds (install xorriso)")
	}
	cmd := exec.Command("xorriso", "-as", "mkisofs",
		"-quiet", "-output", isoPath,
		"-volid", "cidata", "-joliet", "-rock",
		work)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(isoPath)
		return "", fmt.Errorf("xorriso failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if fi, err := os.Stat(isoPath); err != nil || fi.Size() == 0 {
		_ = os.Remove(isoPath)
		return "", errors.New("xorriso produced no ISO")
	}
	return isoPath, nil
}

// BuildUserData renders the #cloud-config user-data document for a
// config. Exported so the LXD backend can reuse it for the native
// cloud-init path (user.user-data) instead of a NoCloud ISO (v1.4 Fase 2).
func BuildUserData(cfg Config) string {
	return buildUserData(cfg)
}

// buildUserData renders the #cloud-config user-data document. It creates
// the provisioned user, installs and starts the QEMU guest agent, and —
// when a ProvisionScript is supplied — writes it to disk (base64-encoded
// to avoid YAML quoting issues) and runs it on first boot.
func buildUserData(cfg Config) string {
	// If the user provided a full custom user-data document and no basic user overrides:
	if cfg.CustomUserData != "" && cfg.User == "" && cfg.Password == "" && cfg.SSHKey == "" && cfg.ProvisionScript == "" {
		if strings.HasPrefix(strings.TrimSpace(cfg.CustomUserData), "#cloud-config") {
			return strings.TrimSpace(cfg.CustomUserData) + "\n"
		}
		// If custom script (e.g. bash):
		return fmt.Sprintf("#cloud-config\nruncmd:\n  - |\n    %s\n", strings.ReplaceAll(cfg.CustomUserData, "\n", "\n    "))
	}

	var ud strings.Builder
	ud.WriteString("#cloud-config\n")
	if cfg.User != "" {
		fmt.Fprintf(&ud, "users:\n  - name: %s\n", cfg.User)
		if cfg.Password != "" {
			hash := cryptSHA512(cfg.Password, "")
			fmt.Fprintf(&ud, "    passwd: %s\n", hash)
			// cloud-init locks the account unless explicitly told not to;
			// without this the password hash is stored with a leading '!'
			// in /etc/shadow and login is impossible.
			ud.WriteString("    lock_passwd: false\n")
		}
		if cfg.SSHKey != "" {
			ud.WriteString("    ssh_authorized_keys:\n")
			fmt.Fprintf(&ud, "      - %s\n", yamlSingleQuote(cfg.SSHKey))
		}
		ud.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
		ud.WriteString("    groups: sudo,adm,wheel\n")
		ud.WriteString("    shell: /bin/bash\n")
	} else if cfg.Password != "" {
		// Root-only mode (v1.4 Fase 4.1): no dedicated user was
		// requested, so apply the password (and optional SSH key)
		// straight to root. `lock_passwd: false` is mandatory — without
		// it cloud-init stores the hash with a leading '!' and login is
		// impossible.
		ud.WriteString("users:\n")
		ud.WriteString("  - name: root\n")
		hash := cryptSHA512(cfg.Password, "")
		fmt.Fprintf(&ud, "    passwd: %s\n", hash)
		ud.WriteString("    lock_passwd: false\n")
		if cfg.SSHKey != "" {
			ud.WriteString("    ssh_authorized_keys:\n")
			fmt.Fprintf(&ud, "      - %s\n", yamlSingleQuote(cfg.SSHKey))
		}
	} else if cfg.SSHKey != "" {
		// Key-only: still create a default user so the key lands in
		// an account cloud-init manages.
		ud.WriteString("ssh_authorized_keys:\n")
		fmt.Fprintf(&ud, "  - %s\n", yamlSingleQuote(cfg.SSHKey))
	}
	// Install and start the QEMU guest agent so WebKVM can change the
	// VM password (virDomainSetUserPassword) without SSH and display IP
	// in real time. This guarantees a working password-reset and IP path
	// for every cloud-init VM across distros.
	if !cfg.SkipGuestAgent {
		ud.WriteString("packages:\n  - qemu-guest-agent\n")
	}

	// F12-06: boot deadlock on systemd-networkd distros that ship the
	// unit already active by preset (confirmed live: Arch Linux's
	// official cloud image). The DHCP fallback used to be applied only
	// from `runcmd`, which cloud-init does not execute until the
	// `cloud-final` stage (via the scripts_user module) — but
	// `cloud-final.service` has `After=time-sync.target`, and
	// `systemd-time-wait-sync.service` blocks that target FOREVER
	// (TimeoutStartSec=infinity) waiting for an NTP sync that itself
	// requires working network. With no network yet (networkd already
	// running but never restarted to pick up our config),
	// `cloud-final` therefore never starts, the `runcmd` that would
	// fix the network never runs, and the VM hangs indefinitely —
	// confirmed live: `systemctl status systemd-time-wait-sync`
	// stayed "activating" for 15+ minutes, `cloud-init status` stayed
	// "running" forever, until a *manual* `systemctl restart
	// systemd-networkd` from outside the guest broke the cycle and
	// cloud-final completed within seconds. Fix: apply the exact same
	// DHCP fallback file + restart from `bootcmd`, which runs in the
	// pre-network local stage (`cloud-init-local.service`, ordered
	// `Before=network-pre.target`) — far earlier than any time-sync or
	// network-online gating, so the interface has a lease long before
	// anything can block on it. Written as a single-line command (no
	// embedded newlines) specifically to avoid the YAML-folding trap
	// from F12-01.
	ud.WriteString("bootcmd:\n")
	ud.WriteString("  - [sh, -c, 'if command -v systemctl >/dev/null 2>&1; then mkdir -p /etc/systemd/network 2>/dev/null; printf \"%s\\n\" \"[Match]\" \"Name=en* eth* vi*\" \"\" \"[Network]\" \"DHCP=yes\" > /etc/systemd/network/20-webkvm-dhcp.network 2>/dev/null; systemctl enable --now systemd-networkd 2>/dev/null || true; systemctl restart systemd-networkd 2>/dev/null || true; systemctl enable --now systemd-resolved 2>/dev/null || true; systemctl enable --now NetworkManager 2>/dev/null || true; fi']\n")
	// If an app provisioning script was supplied, write it to disk plus a
	// small runner that logs execution to /var/log/webkvm-provision.log and
	// records the outcome in /run/webkvm-provision.status (running | ok |
	// failed:<rc>). Without this, script output only lands deep inside
	// cloud-init-output.log and any failure is invisible from WebKVM.
	// A single write_files: block for every file below — cloud-config
	// is YAML, and a second top-level write_files: key later in the
	// same document would silently shadow (or be shadowed by) this
	// one rather than merging with it, dropping whichever list came
	// first.
	ud.WriteString("write_files:\n")
	if cfg.ProvisionScript != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(cfg.ProvisionScript))
		ud.WriteString("  - path: /usr/local/bin/webkvm-provision.sh\n")
		ud.WriteString("    content: !!binary |\n")
		ud.WriteString("      " + encoded + "\n")
		ud.WriteString("    permissions: '0755'\n")
		ud.WriteString("  - path: /usr/local/bin/webkvm-run-provision\n")
		ud.WriteString("    content: |\n")
		ud.WriteString("      #!/bin/bash\n")
		ud.WriteString("      echo running > /run/webkvm-provision.status\n")
		ud.WriteString("      bash /usr/local/bin/webkvm-provision.sh \\\n")
		ud.WriteString("        >/var/log/webkvm-provision.log 2>&1\n")
		ud.WriteString("      rc=$?\n")
		ud.WriteString("      if [ \"$rc\" -eq 0 ]; then\n")
		ud.WriteString("        echo ok > /run/webkvm-provision.status\n")
		ud.WriteString("      else\n")
		ud.WriteString("        echo \"failed:$rc\" > /run/webkvm-provision.status\n")
		ud.WriteString("      fi\n")
		ud.WriteString("      exit 0\n")
		ud.WriteString("    permissions: '0755'\n")
	}
	// Network fallback for systemd-networkd distributions (Arch, Alpine, minimal Linux):
	// automatically enable DHCP on all physical/virtual ethernet interfaces.
	const networkdConfig = `[Match]
Name=en* eth* vi*

[Network]
DHCP=yes
`
	ud.WriteString("  - path: /etc/systemd/network/20-webkvm-dhcp.network\n")
	ud.WriteString("    content: |\n")
	for _, line := range strings.Split(strings.TrimRight(networkdConfig, "\n"), "\n") {
		ud.WriteString("      " + line + "\n")
	}
	ud.WriteString("    permissions: '0644'\n")

	// Serial-console usability: cloud images boot getty on ttyS0 with
	// TERM unset/linux and a 0x0 winsize, so modern TUIs (btop/htop/mc)
	// draw garbage and exit. Ship a tiny profile hook that pins a
	// terminal web terminals understand and fixes the grid per login.
	const termHook = `#!/bin/sh
# Added by WebKVM: usable serial console (btop/htop/mc)
if [ -t 0 ]; then
  case "$TERM" in ""|linux|vt100) TERM=xterm-256color; export TERM ;; esac
  stty rows 24 cols 80 2>/dev/null || true
fi
`
	ud.WriteString("  - path: /etc/profile.d/zz-webkvm-term.sh\n")
	ud.WriteString("    content: |\n")
	for _, line := range strings.Split(strings.TrimRight(termHook, "\n"), "\n") {
		ud.WriteString("      " + line + "\n")
	}
	ud.WriteString("    permissions: '0644'\n")

	if !cfg.SkipGuestAgent {
		// Multi-distro resilient installer for qemu-guest-agent (covers Arch, Debian/Ubuntu, RHEL/Fedora, SUSE, Alpine).
		// F12-01: this used to be embedded as a single-quoted multi-line
		// scalar inside a `runcmd: [sh, -c, '...']` flow entry. YAML folds
		// line breaks inside a quoted scalar into spaces, so cloud-init
		// executed the script with every newline collapsed — turning
		// "then\n  if ..." into "then if ..." on one line, which bash
		// rejects with "syntax error near unexpected token `then'". The
		// guest agent was therefore NEVER installed/started on ANY
		// distro via this path (confirmed live: `virsh domifaddr
		// --source agent` fails on Arch and Debian VMs deployed from the
		// catalog). Fix: ship the script as its own write_files entry
		// (a `|` block scalar, which preserves newlines verbatim) and
		// invoke it as a plain one-line command from runcmd instead of
		// embedding the multi-line body inline.
		const gaInstaller = `#!/bin/sh
if ! command -v qemu-ga >/dev/null 2>&1; then
  if command -v pacman >/dev/null 2>&1; then
    pacman -Sy --noconfirm qemu-guest-agent 2>/dev/null || true
  elif command -v apt-get >/dev/null 2>&1; then
    apt-get update -y && apt-get install -y qemu-guest-agent 2>/dev/null || true
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y qemu-guest-agent 2>/dev/null || true
  elif command -v yum >/dev/null 2>&1; then
    yum install -y qemu-guest-agent 2>/dev/null || true
  elif command -v zypper >/dev/null 2>&1; then
    zypper --non-interactive install qemu-guest-agent 2>/dev/null || true
  elif command -v apk >/dev/null 2>&1; then
    apk add qemu-guest-agent 2>/dev/null || true
  fi
fi
if command -v systemctl >/dev/null 2>&1; then
  systemctl enable --now qemu-guest-agent 2>/dev/null || systemctl start qemu-guest-agent 2>/dev/null || true
elif command -v rc-service >/dev/null 2>&1; then
  rc-update add qemu-guest-agent default 2>/dev/null || true
  rc-service qemu-guest-agent start 2>/dev/null || true
fi
`
		ud.WriteString("  - path: /usr/local/bin/webkvm-qga-installer.sh\n")
		ud.WriteString("    content: |\n")
		for _, line := range strings.Split(strings.TrimRight(gaInstaller, "\n"), "\n") {
			ud.WriteString("      " + line + "\n")
		}
		ud.WriteString("    permissions: '0755'\n")
	}

	// Single runcmd block: ensure network services and guest agent are enabled,
	// apply the hook to the ROOT console too (root logins skip profile.d on some images),
	// and run the app provisioning runner if a script was supplied.
	ud.WriteString("runcmd:\n")
	// Universal network activation for systemd-based distros (Arch, Debian, Ubuntu, Fedora, etc.)
	//
	// F12-05: `systemctl enable --now X` is a no-op on the "start" half
	// when X is already active — and cloud images that ship
	// systemd-networkd pre-enabled by preset (confirmed live: Arch
	// Linux's official cloud image) boot with it already running
	// BEFORE this runcmd executes, so `enable --now` never restarts
	// it. The 20-webkvm-dhcp.network file written above by write_files
	// then sits on disk unread, and the interface never gets a DHCP
	// lease — the VM is silently left with no usable network. Fixed
	// by pairing `enable --now` (handles "not running yet") with
	// `try-restart` (handles "already running": restarts it so the
	// new .network file is actually picked up; a no-op if the unit
	// isn't running, since `enable --now` already started it fresh).
	ud.WriteString("  - [sh, -c, \"if command -v systemctl >/dev/null 2>&1; then systemctl enable --now systemd-networkd 2>/dev/null || true; systemctl try-restart systemd-networkd 2>/dev/null || true; systemctl enable --now systemd-resolved 2>/dev/null || true; systemctl enable --now NetworkManager 2>/dev/null || true; fi\"]\n")

	if !cfg.SkipGuestAgent {
		ud.WriteString("  - [sh, /usr/local/bin/webkvm-qga-installer.sh]\n")
	}
	ud.WriteString("  - [sh, -c, \"grep -q zz-webkvm-term /root/.bashrc || printf '%s\\n' '. /etc/profile.d/zz-webkvm-term.sh' >> /root/.bashrc\"]\n")
	if cfg.ProvisionScript != "" {
		ud.WriteString("  - [/usr/local/bin/webkvm-run-provision]\n")
	}
	ud.WriteString("package_update: true\n")
	if cfg.ProvisionScript != "" {
		// Visible completion marker in the console once every cloud-init
		// module — including app provisioning — has finished.
		ud.WriteString("final_message: \"WEBKVM provisioning finished after $UPTIME seconds\"\n")
	}
	if cfg.CustomUserData != "" {
		cleaned := strings.TrimSpace(cfg.CustomUserData)
		cleaned = strings.TrimPrefix(cleaned, "#cloud-config")
		ud.WriteString("\n# --- Custom Snippet / User Data ---\n")
		ud.WriteString(cleaned)
		ud.WriteString("\n")
	}
	return ud.String()
}

func replaceSpace(s string) string {
	return strings.NewReplacer(" ", "-", ".", "-").Replace(s)
}

// cryptRounds is the iteration count used for SHA-512 crypt hashes.
// 4096 is a widely-used default (the glibc default is 5000; 4096 keeps
// cloud-init first-boot fast while remaining strong).
const cryptRounds = 4096

// cryptSHA512 returns a standard $6$ SHA-512 crypt hash of password using the
// given salt (or a freshly generated one when salt is empty). It delegates to
// the well-tested tredoe sha512_crypt implementation. NOTE: earlier versions
// of this function reimplemented the algorithm by hand and produced hashes
// that Linux/libcrypt could NOT verify (login always failed with "Login
// incorrect"). The correct, standards-compliant implementation must be used.
func cryptSHA512(password, salt string) string {
	c := sha512_crypt.New()

	// When no salt is provided, generate one using the same prefix/rounds the
	// crypt format expects so the output carries "rounds=N".
	if salt == "" {
		s := sha512_crypt.GetSalt()
		saltBytes := s.GenerateWRounds(sha512_crypt.SaltLenMax, cryptRounds)
		salt = string(saltBytes)
	}

	hash, err := c.Generate([]byte(password), []byte(salt))
	if err != nil {
		// This only happens for a malformed salt; our salt is always well-formed.
		panic("sha512_crypt failed: " + err.Error())
	}
	return hash
}

// GeneratePassword returns a random alphanumeric password of the given length.
func GeneratePassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}
