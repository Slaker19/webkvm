package ha

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LocalInstallStatus describes whether this host's DATA_DIR belongs to the
// Pacemaker-managed control plane or is a host-local copy that would diverge
// from the shared volume (the .239 case). Nothing is deleted or moved: the
// operator decides.
type LocalInstallStatus struct {
	DataDir      string   `json:"data_dir"`
	Shared       bool     `json:"shared"`
	Verified     bool     `json:"verified"`
	DivergentJWT bool     `json:"divergent_jwt"`
	Critical     bool     `json:"critical"`
	DRBDMounts   []string `json:"drbd_mounts,omitempty"`
	Details      []string `json:"details"`
}

// PacemakerOwnsWebKVM reports whether Pacemaker runs (or knows) a member of
// the control-plane group. cpGroup is optional and defaults to
// DefaultCPGroup; the match is exact on the group id, never a substring of a
// resource id.
func PacemakerOwnsWebKVM(pacemaker PacemakerStatus, cpGroup ...string) bool {
	group := normCPGroup(firstGroup(cpGroup))
	for _, resource := range pacemaker.Resources {
		if resource.Group == group {
			return true
		}
	}
	return false
}

type mountEntry struct {
	target string
	source string
}

func listMounts(ctx context.Context, runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}) ([]mountEntry, error) {
	if runner == nil {
		return nil, errors.New("command runner unavailable")
	}
	commandCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := runner.Run(commandCtx, "findmnt", "-n", "-o", "TARGET,SOURCE", "--list")
	if err != nil {
		return nil, err
	}
	entries := make([]mountEntry, 0, 16)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		entries = append(entries, mountEntry{target: fields[0], source: fields[1]})
	}
	return entries, nil
}

func jwtFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8]), nil
}

// ProbeLocalInstall inspects the live mounts (read-only). It flags:
//
//   - critical: DATA_DIR is host-local while Pacemaker owns WebKVM resources.
//     Starting WebKVM there would serve a different user/secret database and
//     could write into the standby's copy.
//   - divergent_jwt: another mounted DRBD volume contains a jwt.key whose
//     fingerprint differs from this DATA_DIR's key.
func ProbeLocalInstall(ctx context.Context, runner interface {
	Run(context.Context, string, ...string) ([]byte, error)
}, dataDir string, pacemaker PacemakerStatus, cpGroup ...string) LocalInstallStatus {
	status := LocalInstallStatus{
		DataDir:    dataDir,
		DRBDMounts: []string{},
		Details:    []string{},
	}
	if dataDir == "" {
		status.Details = append(status.Details, "DATA_DIR is empty")
		return status
	}
	entries, err := listMounts(ctx, runner)
	if err != nil {
		status.Details = append(status.Details, "could not list mounts: "+err.Error())
		return status
	}
	status.Verified = true

	var sharedJWT string
	for _, entry := range entries {
		if !strings.Contains(entry.source, "/dev/drbd") {
			continue
		}
		status.DRBDMounts = append(status.DRBDMounts, entry.target)
		if filepath.Clean(entry.target) == filepath.Clean(dataDir) {
			status.Shared = true
			if fp, err := jwtFingerprint(filepath.Join(entry.target, "jwt.key")); err == nil {
				sharedJWT = fp
			}
		}
	}

	if !status.Shared {
		if _, err := os.Stat(filepath.Join(dataDir, "jwt.key")); err == nil && PacemakerOwnsWebKVM(pacemaker, cpGroup...) {
			status.Critical = true
			status.Details = append(status.Details,
				"DATA_DIR is host-local but Pacemaker manages WebKVM resources; this is an out-of-cluster install")
		} else {
			status.Details = append(status.Details,
				"DATA_DIR is not on a DRBD volume; no Pacemaker-managed WebKVM data was detected")
		}
	}

	if status.Shared {
		if sharedJWT == "" {
			status.Details = append(status.Details, "jwt.key not readable on the shared DATA_DIR")
			return status
		}
		for _, target := range status.DRBDMounts {
			if filepath.Clean(target) == filepath.Clean(dataDir) {
				continue
			}
			fp, err := jwtFingerprint(filepath.Join(target, "jwt.key"))
			if err != nil || fp == sharedJWT {
				continue
			}
			status.DivergentJWT = true
			status.Details = append(status.Details,
				"a mounted DRBD copy at "+target+" has a different jwt.key; it must not start WebKVM")
		}
	}
	return status
}
