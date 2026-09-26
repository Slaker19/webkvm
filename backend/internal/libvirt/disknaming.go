package libvirt

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// VMDiskPath returns a unique file path in poolPath for a disk
// belonging to VM newName. The first call for a given (newName,
// ext) pair returns "<pool>/<newName><ext>"; subsequent calls
// (for re-imports of the same VM) return "<newName>-2<ext>",
// "<newName>-3<ext>", and so on.
//
// ext must include the leading dot (e.g. ".qcow2"). The function
// only counts existing files matching the pattern
// "<newName>[-N]?<ext>" as collisions; unrelated files in the
// pool directory do not bump the counter. This means a file
// named "myvm.bak" sitting next to a future "myvm.qcow2" will
// not cause the new disk to be numbered "-2".
//
// The returned path is the file the caller should write to. It
// is not created by this function; the caller is responsible for
// actually producing the file (e.g. via os.OpenFile or
// qemu-img convert + os.Rename).
func VMDiskPath(poolPath, newName, ext string) (string, error) {
	if newName == "" {
		return "", fmt.Errorf("vmDiskPath: newName is required")
	}
	if ext == "" {
		return "", fmt.Errorf("vmDiskPath: ext is required (include the leading dot)")
	}
	if !strings.HasPrefix(ext, ".") {
		return "", fmt.Errorf("vmDiskPath: ext must start with '.', got %q", ext)
	}

	// Pattern: <name>[-<N>]?<ext>
	// Anchored to the file basename (no directory component).
	pattern := fmt.Sprintf(`^%s(-\d+)?%s$`, regexp.QuoteMeta(newName), regexp.QuoteMeta(ext))
	re := regexp.MustCompile(pattern)

	maxN := 0
	seen := false

	entries, err := os.ReadDir(poolPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Pool directory doesn't exist yet — first import
			// will create it. Return the canonical name.
			return filepath.Join(poolPath, newName+ext), nil
		}
		return "", fmt.Errorf("read pool dir %s: %w", poolPath, err)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !re.MatchString(e.Name()) {
			continue
		}
		seen = true
		// Extract the optional -N suffix.
		base := e.Name()
		// Strip the ext suffix first.
		base = base[:len(base)-len(ext)]
		// base is now "<name>" or "<name>-<N>".
		if base == newName {
			// The bare name exists. Counter starts at 2.
			if maxN < 1 {
				maxN = 1
			}
			continue
		}
		// base[len(name)+1:] is "-<N>" (always starts with "-").
		nStr := base[len(newName)+1:]
		n, err := strconv.Atoi(nStr)
		if err != nil {
			// Defensive: should not happen given the regex.
			continue
		}
		if n > maxN {
			maxN = n
		}
	}

	if !seen {
		return filepath.Join(poolPath, newName+ext), nil
	}
	return filepath.Join(poolPath, fmt.Sprintf("%s-%d%s", newName, maxN+1, ext)), nil
}
