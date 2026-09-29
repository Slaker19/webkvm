// Package libvirtbackup — incremental backup support via libvirt checkpoints.
//
// This package is separate from internal/libvirt to avoid an import
// cycle: backupstore needs to call these functions, and libvirt
// already imports backupstore.
//
// The flow (validated by scripts/qa/e2e-incremental.sh):
//
//  1. Create a <domaincheckpoint> with dirty bitmaps on the disks.
//  2. Call dom.BackupBegin with a <domainbackup mode='push'> XML.
//  3. Wait for the backup job to complete (domjobinfo → None).
//  4. Record the checkpoint in the chain store.
package libvirtbackup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// DiskTarget maps a libvirt disk device name to an output file path.
type DiskTarget struct {
	Device string // libvirt device name (e.g. "vda")
	File   string // absolute path on the local filesystem
}

// BuildBackupXML generates the <domainbackup> XML for push mode.
// If incrementalFrom is non-empty, the backup is incremental from
// that checkpoint; otherwise it is a full backup.
func BuildBackupXML(disks []DiskTarget, incrementalFrom string) string {
	var b strings.Builder
	b.WriteString("<domainbackup mode='push'>\n")
	if incrementalFrom != "" {
		fmt.Fprintf(&b, "  <incremental>%s</incremental>\n", incrementalFrom)
	}
	b.WriteString("  <disks>\n")
	for _, d := range disks {
		fmt.Fprintf(&b, "    <disk name='%s' type='file'>\n", d.Device)
		fmt.Fprintf(&b, "      <target file='%s'/>\n", d.File)
		b.WriteString("      <driver type='qcow2'/>\n")
		b.WriteString("    </disk>\n")
	}
	b.WriteString("  </disks>\n")
	b.WriteString("</domainbackup>")
	return b.String()
}

// BuildCheckpointXML generates the <domaincheckpoint> XML with
// dirty bitmaps enabled on the given disk devices.
func BuildCheckpointXML(name string, diskDevs []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<domaincheckpoint>\n  <name>%s</name>\n", name)
	if len(diskDevs) > 0 {
		b.WriteString("  <disks>\n")
		for _, dev := range diskDevs {
			fmt.Fprintf(&b, "    <disk name='%s' checkpoint='bitmap'/>\n", dev)
		}
		b.WriteString("  </disks>\n")
	}
	b.WriteString("</domaincheckpoint>")
	return b.String()
}

// GenerateCheckpointName returns a unique checkpoint name.
func GenerateCheckpointName() string {
	epoch := time.Now().Unix()
	buf := make([]byte, 4)
	_, _ = rand.Read(buf)
	return fmt.Sprintf("chk-%d-%s", epoch, hex.EncodeToString(buf))
}

// BeginBackup starts a push-mode backup with an optional checkpoint.
// If checkpointName is non-empty, a <domaincheckpoint> XML is created
// first and passed to BackupBegin. The function blocks until the
// backup job completes or the context is cancelled.
//
// This is the Go equivalent of:
//
//	virsh backup-begin <domain> --backupxml <file> --checkpointxml <file>
func BeginBackup(ctx context.Context, vmID, backupXML, checkpointXML string) error {
	// Write temp XML files for virsh.
	backupFile, err := writeTempXML(backupXML)
	if err != nil {
		return fmt.Errorf("write backup xml: %w", err)
	}
	defer func() { _ = os.Remove(backupFile) }()

	checkpointFile := ""
	if checkpointXML != "" {
		checkpointFile, err = writeTempXML(checkpointXML)
		if err != nil {
			return fmt.Errorf("write checkpoint xml: %w", err)
		}
		defer func() { _ = os.Remove(checkpointFile) }()
	}

	args := []string{"backup-begin", vmID, "--backupxml", backupFile}
	if checkpointFile != "" {
		args = append(args, "--checkpointxml", checkpointFile)
	}

	cmd := exec.CommandContext(ctx, "virsh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("virsh backup-begin: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Wait for the backup job to complete.
	if err := waitBackupJob(ctx, vmID); err != nil {
		return fmt.Errorf("backup job: %w", err)
	}

	return nil
}

// waitBackupJob polls domjobinfo until the backup job finishes.
func waitBackupJob(ctx context.Context, vmID string) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			out, err := exec.CommandContext(ctx, "virsh", "domjobinfo", vmID).Output()
			if err != nil {
				// Domain may have been destroyed; treat as done.
				return nil
			}
			if strings.Contains(string(out), "Job type:         None") {
				return nil
			}
		}
	}
}

// ListCheckpoints returns the checkpoint names for a domain.
func ListCheckpoints(vmID string) ([]string, error) {
	out, err := exec.Command("virsh", "checkpoint-list", vmID).Output()
	if err != nil {
		return nil, fmt.Errorf("virsh checkpoint-list: %w", err)
	}

	var names []string
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 1 && strings.HasPrefix(fields[0], "chk-") {
			names = append(names, fields[0])
		}
	}
	return names, nil
}

// DeleteCheckpoint removes a checkpoint and its bitmap.
func DeleteCheckpoint(vmID, name string) error {
	out, err := exec.Command("virsh", "checkpoint-delete", vmID, name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("virsh checkpoint-delete: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// writeTempXML writes XML to a temp file and returns its path.
func writeTempXML(xml string) (string, error) {
	dir := os.TempDir()
	f, err := os.CreateTemp(dir, "webkvm-backup-*.xml")
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(xml); err != nil {
		return "", err
	}
	return f.Name(), nil
}
