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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tredoe/osutil/user/crypt/sha512_crypt"
)

// Config is the operator-supplied provisioning data.
type Config struct {
	User     string `json:"user,omitempty"`
	Password string `json:"password,omitempty"`
	SSHKey   string `json:"ssh_key,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	// ProvisionScript is an optional bash script injected into the seed
	// and executed on first boot (as root). Used by appliance "apps" to
	// install software on a base cloud image.
	ProvisionScript string `json:"-"`
}

var (
	userRe     = regexp.MustCompile(`^[a-z_][a-z0-9_\-]{0,31}$`)
	hostnameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.\-]{0,62}$`)
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
	if c.User == "" && c.SSHKey == "" && c.Hostname == "" {
		return errors.New("cloud-init needs at least a user, an SSH key or a hostname")
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
	return nil
}

// yamlSingleQuote escapes a value for single-quoted YAML.
func yamlSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
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

	// meta-data.
	md := "instance-id: webkvm-" + strings.ToLower(replaceSpace(cfg.Hostname)) + "\n"
	if cfg.Hostname != "" {
		md += "local-hostname: " + cfg.Hostname + "\n"
	}

	// network-config: default to DHCP on all NICs.
	nc := "version: 2\nethernets:\n  all:\n    match:\n      name: en*\n    dhcp4: true\n"

	files := map[string]string{
		"user-data":       ud,
		"meta-data":       md,
		"network-config":  nc,
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

// buildUserData renders the #cloud-config user-data document. It creates
// the provisioned user, installs and starts the QEMU guest agent, and —
// when a ProvisionScript is supplied — writes it to disk (base64-encoded
// to avoid YAML quoting issues) and runs it on first boot.
func buildUserData(cfg Config) string {
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
		ud.WriteString("    groups: sudo,adm\n")
		ud.WriteString("    shell: /bin/bash\n")
	} else if cfg.SSHKey != "" {
		// Key-only: still create a default user so the key lands in
		// an account cloud-init manages.
		ud.WriteString("ssh_authorized_keys:\n")
		fmt.Fprintf(&ud, "  - %s\n", yamlSingleQuote(cfg.SSHKey))
	}
	// Install and start the QEMU guest agent so WebKVM can change the
	// VM password (virDomainSetUserPassword) without SSH. This guarantees
	// a working password-reset path for every cloud-init VM.
	ud.WriteString("packages:\n  - qemu-guest-agent\n")
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

	// Single runcmd block: enable the guest agent, apply the hook to the
	// ROOT console too (root logins skip profile.d on some images), and
	// run the app provisioning runner if a script was supplied.
	ud.WriteString("runcmd:\n")
	ud.WriteString("  - [systemctl, enable, --now, qemu-guest-agent]\n")
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
