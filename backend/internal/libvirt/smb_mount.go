package libvirt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WebKVM's own SMB/CIFS mounting for AUTHENTICATED shares.
//
// libvirt's netfs storage-pool driver has no supported way to carry
// CIFS credentials: its <source><auth type='...'> element only accepts
// "chap" (iscsi) and "ceph" (rbd) — confirmed against the installed
// libvirt's own storagepool.rng schema (`sourcenetfs` never references
// `sourceinfoauth` at all). Defining a pool with a hand-rolled
// <auth type='cifs'> block is silently accepted by StoragePoolDefineXML
// but then IGNORED when libvirt actually mounts the share, which falls
// back to an anonymous/guest mount — a real, live-confirmed failure
// ("mount error(13): Permission denied") against any share that
// requires a login, not just a theoretical gap.
//
// So for a CIFS pool WITH credentials, WebKVM mounts the share itself
// via a systemd .mount unit (survives reboots, unlike a bare `mount`
// call) plus a root-only credentials file, then defines a plain "dir"
// libvirt pool on top of the already-mounted path — libvirt never sees
// the password at all. A CIFS pool with NO credentials (anonymous/guest
// share) still goes through libvirt's native netfs+cifs pool untouched,
// which works fine since there's no auth to carry.
const smbCredentialsDir = "/etc/webkvm"

func smbCredentialsPath(poolName string) string {
	return filepath.Join(smbCredentialsDir, "smb-creds-"+poolName)
}

// smbMountUnitName asks systemd itself for the exact unit name a mount
// at this path must use — a .mount unit's name is required to be the
// escaped form of its own target path, and hand-rolling systemd's
// escaping rules is more fragile than just asking the real binary.
func smbMountUnitName(mountpoint string) (string, error) {
	out, err := exec.Command("systemd-escape", "--path", "--suffix=mount", mountpoint).Output()
	if err != nil {
		return "", fmt.Errorf("systemd-escape: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// mountSMBShare mounts an authenticated CIFS share at mountpoint via a
// systemd .mount unit and a root-only credentials file. Rolls back
// (removes the credentials file / unit file it already wrote) on any
// failure so a failed attempt never leaves an orphan behind.
func mountSMBShare(poolName, host, shareDir, mountpoint, user, pass string) error {
	if err := os.MkdirAll(mountpoint, 0755); err != nil {
		return fmt.Errorf("create mountpoint: %w", err)
	}
	if err := os.MkdirAll(smbCredentialsDir, 0755); err != nil {
		return fmt.Errorf("create credentials dir: %w", err)
	}
	credPath := smbCredentialsPath(poolName)
	credContent := fmt.Sprintf("username=%s\npassword=%s\n", user, pass)
	if err := os.WriteFile(credPath, []byte(credContent), 0600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}

	unitName, err := smbMountUnitName(mountpoint)
	if err != nil {
		os.Remove(credPath)
		return err
	}
	unitPath := filepath.Join("/etc/systemd/system", unitName)
	share := strings.TrimPrefix(shareDir, "/")
	unitContent := fmt.Sprintf(`[Unit]
Description=WebKVM SMB mount for pool %s

[Mount]
What=//%s/%s
Where=%s
Type=cifs
Options=credentials=%s,uid=0,gid=0,file_mode=0777,dir_mode=0777,vers=3.0

[Install]
WantedBy=multi-user.target
`, poolName, host, share, mountpoint, credPath)

	if err := os.WriteFile(unitPath, []byte(unitContent), 0644); err != nil {
		os.Remove(credPath)
		return fmt.Errorf("write mount unit: %w", err)
	}

	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		os.Remove(unitPath)
		os.Remove(credPath)
		return fmt.Errorf("daemon-reload: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", unitName).CombinedOutput(); err != nil {
		exec.Command("systemctl", "disable", unitName).Run() //nolint:errcheck
		os.Remove(unitPath)
		os.Remove(credPath)
		exec.Command("systemctl", "daemon-reload").Run() //nolint:errcheck
		return fmt.Errorf("mount failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// unmountSMBShare tears down a self-managed SMB mount, if one exists
// at mountpoint (identified by the deterministic unit name derived
// from the path — no separate state file needed, mirroring the
// per-bridge dnsmasq unit pattern elsewhere in this package). A
// mountpoint with no matching unit is silently a no-op.
func unmountSMBShare(poolName, mountpoint string) error {
	unitName, err := smbMountUnitName(mountpoint)
	if err != nil {
		return err
	}
	unitPath := filepath.Join("/etc/systemd/system", unitName)
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return nil // not one of ours
	}
	exec.Command("systemctl", "disable", "--now", unitName).Run() //nolint:errcheck
	os.Remove(unitPath)
	os.Remove(smbCredentialsPath(poolName))
	exec.Command("systemctl", "daemon-reload").Run() //nolint:errcheck
	return nil
}

// isSelfManagedSMBMount reports whether mountpoint has one of our own
// systemd .mount units — used by DeletePool to decide whether to tear
// one down alongside the libvirt pool definition.
func isSelfManagedSMBMount(mountpoint string) bool {
	unitName, err := smbMountUnitName(mountpoint)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join("/etc/systemd/system", unitName))
	return err == nil
}
