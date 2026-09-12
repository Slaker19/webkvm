package remotebrowse

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// runDir is the tmpfs root under which throwaway NFS browse mounts are
// created. Deliberately NOT /etc/webkvm, which internal/libvirt's
// smb_mount.go and internal/backupstore's net_mount.go reserve for
// PERSISTENT credentials and unit files — a browse mount has no
// persistent state at all, it lives only for the duration of one HTTP
// request.
const runDir = "/run/webkvm"

// browseNFS mounts req.Host:req.SourceDir read-only under a throwaway
// tmpfs directory, lists req.Subpath within it, and unmounts. A plain
// mount/umount round trip is used instead of the systemd .mount-unit
// machinery in net_mount.go/smb_mount.go: that machinery exists so
// persistent pool/target mounts survive reboots and stay independently
// discoverable later, which a mount that is torn down before the
// handler returns never needs — paying for daemon-reload/enable/disable
// four times over would only add latency and a stray-unit leak risk for
// no benefit here.
func browseNFS(ctx context.Context, req Request) ([]Entry, error) {
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("prepare %s: %w", runDir, err)
	}
	tmp, err := os.MkdirTemp(runDir, "browse-nfs-")
	if err != nil {
		return nil, fmt.Errorf("create temp mountpoint: %w", err)
	}
	defer os.RemoveAll(tmp)
	// Lazy-unmount safety net in case the explicit umount below is never
	// reached (panic, early return) — never leaves a stray live mount.
	defer func() { _ = exec.Command("umount", "-l", tmp).Run() }()

	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	what := req.Host + ":" + path.Join("/", req.SourceDir)
	// soft,timeo=50,retrans=1: bound the in-kernel NFS RPC wait itself
	// (roughly 5s) on top of the outer context timeout — killing the
	// `mount` process does not unblock an in-flight kernel-level RPC
	// call, so the context deadline alone cannot be relied on to bound
	// this against an unreachable/firewalled server.
	mountArgs := []string{"-t", "nfs", "-o", "ro,soft,timeo=50,retrans=1", what, tmp}
	out, err := exec.CommandContext(ctx, "mount", mountArgs...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("mount %s: %v (%s)", what, err, strings.TrimSpace(string(out)))
	}

	listDir := filepath.Join(tmp, filepath.Clean("/"+req.Subpath))
	entries, readErr := os.ReadDir(listDir)

	umountOut, umErr := exec.CommandContext(ctx, "umount", tmp).CombinedOutput()
	if umErr != nil {
		// Non-fatal for the caller if the listing already succeeded — the
		// deferred lazy unmount above is the real safety net.
		slog.Warn("remotebrowse_nfs_umount_failed", "err", umErr, "out", strings.TrimSpace(string(umountOut)))
	}

	if readErr != nil {
		return nil, fmt.Errorf("list %s: %w", listDir, readErr)
	}
	return dirsOnly(entries), nil
}

// dirsOnly keeps only real subdirectories: never surface regular files
// as browsable folders, and never follow symlinks (a symlink pointing
// outside the export has no business being offered as a folder to
// descend into).
func dirsOnly(entries []os.DirEntry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, Entry{Name: e.Name()})
		}
	}
	return out
}
