package ha

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// SBDPath picks the SBD configuration file: the first existing candidate,
// else /etc/sysconfig/sbd when /etc/sysconfig exists, else the first-listed
// Debian-style path.
func (e ClusterEnv) SBDPath() string {
	for _, p := range e.SBDPaths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, p := range e.SBDPaths {
		if _, err := os.Stat(filepath.Dir(p)); err == nil {
			return p
		}
	}
	if len(e.SBDPaths) > 0 {
		return e.SBDPaths[len(e.SBDPaths)-1]
	}
	return "/etc/default/sbd"
}

// RewriteSBDConfig sets the given KEY=value pairs in an SBD file, keeping
// every unrelated line (comments included). The first active assignment of a
// key is replaced in place, later duplicates are dropped, and missing keys
// are appended.
func RewriteSBDConfig(existing string, values [][2]string) string {
	lines := strings.Split(strings.TrimRight(existing, "\n"), "\n")
	if existing == "" {
		lines = nil
	}
	done := map[string]bool{}
	out := make([]string, 0, len(lines)+len(values))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		replaced := false
		if !strings.HasPrefix(trimmed, "#") {
			for _, kv := range values {
				if strings.HasPrefix(trimmed, kv[0]+"=") {
					replaced = true
					if !done[kv[0]] {
						done[kv[0]] = true
						out = append(out, kv[0]+"="+kv[1])
					}
					break
				}
			}
		}
		if !replaced {
			out = append(out, line)
		}
	}
	for _, kv := range values {
		if !done[kv[0]] {
			out = append(out, kv[0]+"="+kv[1])
		}
	}
	return strings.Join(out, "\n") + "\n"
}

// WriteFileAtomic writes via a temp file in the same directory and renames.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// WriteSBDConfig applies RewriteSBDConfig to the SBD file of this node.
func (e ClusterEnv) WriteSBDConfig(device string, timeout string) (string, error) {
	path := e.SBDPath()
	perm := os.FileMode(0o644)
	var existing string
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		existing = string(data)
		if st, serr := os.Stat(path); serr == nil {
			perm = st.Mode().Perm()
		}
	case errors.Is(err, fs.ErrNotExist):
	default:
		return path, err
	}
	next := RewriteSBDConfig(existing, [][2]string{
		{"SBD_PACEMAKER", "yes"},
		{"SBD_WATCHDOG_DEV", device},
		{"SBD_WATCHDOG_TIMEOUT", timeout},
	})
	return path, WriteFileAtomic(path, []byte(next), perm)
}
