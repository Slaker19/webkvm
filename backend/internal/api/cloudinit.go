package api

import (
	"os"
	"path/filepath"
	"strings"

	"webkvm/internal/cloudinit"
	"webkvm/internal/models"
)

// cloudinitDir returns the directory that hosts cloud-init seed ISOs
// ({dataDir}/cloudinit). Kept outside the ISO pool so seeds never
// show up in the ISO library or the config backup.
func (h *Handler) cloudinitDir() string {
	return filepath.Join(h.cfg.DataDir, "cloudinit")
}

// applyCloudInit generates a NoCloud seed ISO for vmName and attaches
// it to the VM as a SATA cdrom. Returns the ISO path on success.
// A nil/empty config is a no-op. Failures are returned so the caller
// can surface them (provisioning is optional; a bad seed is not fatal
// to the VM itself, but the operator should know).
func (h *Handler) applyCloudInit(vmID, vmName string, req *models.CloudInitRequest) error {
	if req == nil {
		return nil
	}
	// Trim whitespace/newlines: a pasted SSH key often ends with a
	// trailing newline, which would otherwise be rejected.
	cfg := cloudinit.Config{
		User:             strings.TrimSpace(req.User),
		Password:         req.Password,
		SSHKey:           strings.TrimSpace(req.SSHKey),
		Hostname:         strings.TrimSpace(req.Hostname),
		ProvisionScript:  req.ProvisionScript,
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	dir := h.cloudinitDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	isoPath := filepath.Join(dir, "seed-"+sanitizeFilename(vmName)+".iso")
	if _, err := cloudinit.BuildNoCloudISO(isoPath, cfg); err != nil {
		return err
	}
	attach := models.AttachDiskRequest{
		Device: "cdrom",
		Bus:    "sata",
		Source: isoPath,
	}
	if err := h.lv.AttachDisk(vmID, attach); err != nil {
		_ = os.Remove(isoPath)
		return err
	}
	return nil
}

// sanitizeFilename makes a name safe to use as a filename component.
func sanitizeFilename(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		out = []byte("vm")
	}
	return string(out)
}
