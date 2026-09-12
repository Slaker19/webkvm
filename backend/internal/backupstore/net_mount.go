package backupstore

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WebKVM's own NFS/SMB mounting for backup targets — mirrors
// internal/libvirt/smb_mount.go's approach (used for storage pools) for
// the same reasons, but independent of libvirt: a backup target's local
// path is not a libvirt storage pool, so there's no netfs pool driver to
// delegate to here at all. Every nfs/smb target with a Host set is
// mounted by WebKVM itself via a systemd .mount unit (survives reboots)
// and, for SMB with credentials, a root-only credentials file — the
// password never appears in a command line, a config file readable by
// non-root, or anywhere in targets.json.
const backupCredentialsDir = "/etc/webkvm"

// backupCredentialsPath builds the root-only credentials file path for a
// self-managed SMB backup target. targetID is always newID()-generated
// by Store.CreateTarget, never user-supplied text.
func backupCredentialsPath(targetID string) string {
	return filepath.Join(backupCredentialsDir, "backup-smb-creds-"+targetID) // lgtm[go/path-injection] - targetID always internally generated, see func comment
}

// mountUnitName asks systemd itself for the exact unit name a mount at
// this path must use — a .mount unit's name is required to be the
// escaped form of its own target path.
func mountUnitName(mountpoint string) (string, error) {
	out, err := exec.Command("systemd-escape", "--path", "--suffix=mount", mountpoint).Output()
	if err != nil {
		return "", fmt.Errorf("systemd-escape: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func writeMountUnit(unitName, unitContent string) error {
	unitPath := filepath.Join("/etc/systemd/system", unitName)
	if err := os.WriteFile(unitPath, []byte(unitContent), 0644); err != nil {
		return fmt.Errorf("write mount unit: %w", err)
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		os.Remove(unitPath)
		return fmt.Errorf("daemon-reload: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", unitName).CombinedOutput(); err != nil {
		exec.Command("systemctl", "disable", unitName).Run() //nolint:errcheck
		os.Remove(unitPath)
		exec.Command("systemctl", "daemon-reload").Run() //nolint:errcheck
		return fmt.Errorf("mount failed: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// mountNFSTarget mounts an NFS export at mountpoint via a systemd
// .mount unit. NFS has no per-connection credentials to carry (access
// control is the remote server's own doing — its exports file plus
// Unix permissions), so there's no secrets file here. mountpoint is
// always validated by ValidateTargetPath in the only caller
// (Store.CreateTarget), before this function ever runs.
func mountNFSTarget(host, remoteDir, mountpoint string) error {
	if err := os.MkdirAll(mountpoint, 0755); err != nil { // lgtm[go/path-injection] - mountpoint validated, see func comment
		return fmt.Errorf("create mountpoint: %w", err)
	}
	unitName, err := mountUnitName(mountpoint)
	if err != nil {
		return err
	}
	unitContent := fmt.Sprintf(`[Unit]
Description=WebKVM NFS backup target mount

[Mount]
What=%s:%s
Where=%s
Type=nfs

[Install]
WantedBy=multi-user.target
`, host, remoteDir, mountpoint)
	return writeMountUnit(unitName, unitContent)
}

// mountSMBTarget mounts a CIFS share at mountpoint via a systemd .mount
// unit. With a username, credentials go in a root-only file (never a
// command line or targets.json); with no username, it's an anonymous
// guest mount. mountpoint is always validated by ValidateTargetPath in
// the only caller (Store.CreateTarget); targetID is always
// newID()-generated there too, never user input.
func mountSMBTarget(targetID, host, share, mountpoint, user, pass string) error {
	if err := os.MkdirAll(mountpoint, 0755); err != nil { // lgtm[go/path-injection] - mountpoint validated, see func comment
		return fmt.Errorf("create mountpoint: %w", err)
	}
	share = strings.TrimPrefix(share, "/")

	options := "uid=0,gid=0,file_mode=0600,dir_mode=0700,vers=3.0"
	if user != "" {
		if err := os.MkdirAll(backupCredentialsDir, 0755); err != nil {
			return fmt.Errorf("create credentials dir: %w", err)
		}
		credPath := backupCredentialsPath(targetID)
		credContent := fmt.Sprintf("username=%s\npassword=%s\n", user, pass)
		if err := os.WriteFile(credPath, []byte(credContent), 0600); err != nil {
			return fmt.Errorf("write credentials: %w", err)
		}
		options = fmt.Sprintf("credentials=%s,%s", credPath, options)
	} else {
		options = "guest," + options
	}

	unitName, err := mountUnitName(mountpoint)
	if err != nil {
		if user != "" {
			os.Remove(backupCredentialsPath(targetID))
		}
		return err
	}
	unitContent := fmt.Sprintf(`[Unit]
Description=WebKVM SMB backup target mount

[Mount]
What=//%s/%s
Where=%s
Type=cifs
Options=%s

[Install]
WantedBy=multi-user.target
`, host, share, mountpoint, options)

	if err := writeMountUnit(unitName, unitContent); err != nil {
		if user != "" {
			os.Remove(backupCredentialsPath(targetID))
		}
		return err
	}
	return nil
}

// unmountTarget tears down a self-managed NFS/SMB mount at mountpoint,
// if one exists (identified by the deterministic unit name — no
// separate state file needed). A mountpoint with no matching unit is
// silently a no-op: it just wasn't one of ours (e.g. a plain local
// target, or an nfs/smb target the operator mounted by hand before
// this feature existed).
func unmountTarget(targetID, mountpoint string) error {
	unitName, err := mountUnitName(mountpoint)
	if err != nil {
		return err
	}
	unitPath := filepath.Join("/etc/systemd/system", unitName)
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		return nil
	}
	exec.Command("systemctl", "disable", "--now", unitName).Run() //nolint:errcheck
	os.Remove(unitPath)
	os.Remove(backupCredentialsPath(targetID))
	exec.Command("systemctl", "daemon-reload").Run() //nolint:errcheck
	return nil
}

// isSelfManagedMount reports whether mountpoint has one of our own
// systemd .mount units.
func isSelfManagedMount(mountpoint string) bool {
	unitName, err := mountUnitName(mountpoint)
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join("/etc/systemd/system", unitName))
	return err == nil
}
