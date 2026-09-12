package remotebrowse

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// smbEntryLine matches one smbclient `ls` output line, e.g.:
//
//	  VDI                                 D        0  Fri Sep 11 21:48:16 2026
//	  hello.txt                           N       23  Fri Sep 11 20:55:27 2026
//
// Anchored on the attribute column (a short run of D/A/H/S/R/N letters)
// rather than splitting on whitespace, since names may contain spaces.
// Verified live against smbclient 4.24.7 (Arch) listing a real share.
var smbEntryLine = regexp.MustCompile(`^\s*(.+?)\s+([DAHSRN]+)\s+(\d+)\s+(.+)$`)

// browseSMB lists req.Subpath of the SMB share req.SourceDir on
// req.Host via the smbclient CLI (packaging/standalone/install.sh
// installs it: smbclient on apt/pacman, samba-client on dnf). No mount
// is performed — smbclient can list a share without mounting it, unlike
// NFS.
func browseSMB(ctx context.Context, req Request) ([]Entry, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	share := "//" + req.Host + "/" + strings.TrimPrefix(req.SourceDir, "/")
	script := "ls"
	if sub := strings.Trim(req.Subpath, "/"); sub != "" {
		script = fmt.Sprintf("cd %q; ls", sub)
	}

	args := []string{share}
	if req.Username != "" {
		args = append(args, "-U", req.Username+"%"+req.Password)
	} else {
		args = append(args, "-N")
	}
	args = append(args, "-c", script)

	out, err := exec.CommandContext(ctx, "smbclient", args...).CombinedOutput()
	text := string(out)

	if browseErr := classifySMBError(text, err); browseErr != nil {
		return nil, browseErr
	}
	return parseSMBListing(text), nil
}

// classifySMBError inspects smbclient's combined output for known
// failure signatures. smbclient's exit code alone is NOT reliable here
// (verified live): an unreachable host or a nonexistent `cd` target
// both exit 0 while still printing a clear NT_STATUS_* failure line, so
// every case below is matched on output text first.
func classifySMBError(text string, err error) error {
	trimmed := strings.TrimSpace(text)

	// A failed `cd` (e.g. into a subpath that no longer exists) does not
	// fail the smbclient process — it prints an error line and then
	// `ls` proceeds to list whatever directory it was ALREADY in, which
	// would otherwise silently return the wrong listing. Detect it
	// explicitly rather than trusting a 0 exit code.
	for _, line := range strings.Split(text, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "cd ") && strings.Contains(l, "NT_STATUS_") {
			return fmt.Errorf("folder not found: %s", l)
		}
	}

	switch {
	case strings.Contains(text, "NT_STATUS_LOGON_FAILURE"), strings.Contains(text, "NT_STATUS_ACCESS_DENIED"):
		return fmt.Errorf("authentication failed")
	case strings.Contains(text, "NT_STATUS_BAD_NETWORK_NAME"):
		return fmt.Errorf("share not found: %s", trimmed)
	case strings.Contains(text, "NT_STATUS_HOST_UNREACHABLE"),
		strings.Contains(text, "NT_STATUS_CONNECTION_REFUSED"),
		strings.Contains(text, "do_connect"):
		return fmt.Errorf("could not reach server: %s", trimmed)
	case err != nil:
		return fmt.Errorf("smbclient: %v (%s)", err, trimmed)
	}
	return nil
}

func parseSMBListing(text string) []Entry {
	var out []Entry
	for _, line := range strings.Split(text, "\n") {
		m := smbEntryLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := strings.TrimSpace(m[1])
		attrs := m[2]
		if name == "." || name == ".." {
			continue
		}
		if strings.Contains(attrs, "D") {
			out = append(out, Entry{Name: name})
		}
	}
	if out == nil {
		out = []Entry{}
	}
	return out
}
