// Package remotebrowse lists the subdirectories of a remote NFS export or
// SMB/CIFS share, for the "browse folders" helper used by both the
// storage-pool and backup-target creation forms. It is a leaf package:
// it does not import, and is not imported by, internal/libvirt or
// internal/backupstore, which each own their own PERSISTENT mount logic
// (smb_mount.go, net_mount.go). Browsing here is always a throwaway
// operation scoped to a single request.
package remotebrowse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DefaultTimeout bounds a single browse operation (mount/list/unmount for
// NFS, or the smbclient round trip for SMB).
const DefaultTimeout = 8 * time.Second

// Request describes what to list. Subpath is always relative to
// SourceDir, with no leading or trailing slash; empty means "the top of
// SourceDir itself".
type Request struct {
	Format    string // "nfs" | "cifs"
	Host      string
	SourceDir string // NFS export path, or SMB share name
	Subpath   string
	Username  string // cifs only
	Password  string // cifs only
}

// Entry is one subfolder found at the requested level.
type Entry struct {
	Name string `json:"name"`
}

// Browse lists the subfolders of req.SourceDir/req.Subpath on req.Host.
func Browse(ctx context.Context, req Request) ([]Entry, error) {
	if req.Host == "" || req.SourceDir == "" {
		return nil, fmt.Errorf("host and source_dir are required")
	}
	switch strings.ToLower(req.Format) {
	case "nfs":
		return browseNFS(ctx, req)
	case "cifs", "smb":
		return browseSMB(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported format %q", req.Format)
	}
}
