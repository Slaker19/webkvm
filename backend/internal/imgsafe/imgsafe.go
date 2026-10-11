// Package imgsafe inspects and converts UNTRUSTED disk images (uploads, OVA
// payloads, archive members) with qemu-img without letting the image
// redirect root-owned I/O to host files (audit #6).
//
// Dangerous image features: a qcow2/qed backing file, a qcow2 external
// data-file, and VMDK extents that point at paths other than the image
// itself (e.g. /dev/sda). Format probing is also disabled: every call passes
// -f with the format the caller declared, so a file cannot pretend to be
// another format than the one it was uploaded as.
//
// TODO(audit #6): qemu-img still runs with the privileges of the WebKVM
// process. Running it as an unprivileged system user (syscall.Credential +
// rlimits) would survive a bug in this validator, but needs a dedicated
// user and a staging directory owned by it, so it is not done here.
package imgsafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"webkvm/internal/fsutil"
)

// Formats that may be declared for untrusted input.
var okFormats = map[string]bool{
	"raw": true, "qcow2": true, "vmdk": true, "vpc": true, "vhdx": true, "vdi": true, "qed": true,
}

// FormatFromExt maps a file extension to the qemu-img format name. It returns
// "" for extensions that are not accepted.
func FormatFromExt(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".qcow2":
		return "qcow2"
	case ".img", ".raw":
		return "raw"
	case ".vmdk":
		return "vmdk"
	case ".vdi":
		return "vdi"
	case ".vhdx":
		return "vhdx"
	case ".vhd":
		return "vpc"
	case ".qed":
		return "qed"
	}
	return ""
}

// Info is the subset of `qemu-img info --output=json` we rely on.
type Info struct {
	Format      string `json:"format"`
	VirtualSize int64  `json:"virtual-size"`
	Backing     string `json:"backing-filename"`
	FullBacking string `json:"full-backing-filename"`
	BackingFmt  string `json:"backing-filename-format"`
	Spec        struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"format-specific"`
}

const infoTimeout = 2 * time.Minute

func run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "qemu-img", args...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("qemu-img %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// Available reports whether qemu-img is installed.
func Available() bool {
	_, err := exec.LookPath("qemu-img")
	return err == nil
}

// Inspect runs qemu-img info on path forcing the declared format and rejects
// every feature that references other host files.
func Inspect(ctx context.Context, path, declared string) (*Info, error) {
	if !okFormats[declared] {
		return nil, fmt.Errorf("unsupported image format %q", declared)
	}
	real, err := fsutil.ResolveRegular(path)
	if err != nil {
		return nil, fmt.Errorf("image path: %w", err)
	}
	ictx, cancel := context.WithTimeout(ctx, infoTimeout)
	defer cancel()
	// -U: do not take image locks (the file may be in use); -f: never probe.
	out, err := run(ictx, "info", "--output=json", "-U", "-f", declared, "--", real)
	if err != nil {
		return nil, err
	}
	return check(out, real, declared)
}

// check validates the JSON produced by qemu-img info. Split out for tests.
func check(out []byte, real, declared string) (*Info, error) {
	var i Info
	if err := json.Unmarshal(out, &i); err != nil {
		return nil, fmt.Errorf("parse qemu-img info: %w", err)
	}
	if i.Format != declared {
		return nil, fmt.Errorf("image format mismatch: declared %s, detected %s", declared, i.Format)
	}
	if i.Backing != "" || i.FullBacking != "" {
		return nil, errors.New("image has a backing file, which is not allowed for untrusted images")
	}
	var raw map[string]any
	if len(i.Spec.Data) > 0 {
		_ = json.Unmarshal(i.Spec.Data, &raw)
	}
	if v, _ := raw["data-file"].(string); v != "" {
		return nil, errors.New("qcow2 external data-file is not allowed")
	}
	if i.Format == "vmdk" {
		exts, _ := raw["extents"].([]any)
		for _, e := range exts {
			m, _ := e.(map[string]any)
			fn, _ := m["filename"].(string)
			if fn == "" || (filepath.Clean(fn) != filepath.Clean(real) && !sameFile(fn, real)) {
				return nil, fmt.Errorf("vmdk extent %q points outside the image", fn)
			}
		}
	}
	return &i, nil
}

func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}

// Convert validates src (declared format) and converts it to a qcow2 file at
// dst. dst is created exclusively: an existing file is never overwritten
// (the returned error wraps os.ErrExist). On failure dst is removed.
func Convert(ctx context.Context, src, dst, declared string, timeout time.Duration) error {
	if _, err := Inspect(ctx, src, declared); err != nil {
		return err
	}
	f, err := fsutil.CreateExclusive(filepath.Dir(dst), filepath.Base(dst), 0o666)
	if err != nil {
		return err
	}
	f.Close()
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := run(cctx, "convert", "-f", declared, "-O", "qcow2", "-q", "--", src, dst); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return nil
}

// CheckChainInside verifies that every file reachable through backing-file
// references from path lives inside root (after resolving symlinks). It is
// meant for disks restored from a backup archive, where snapshot overlays
// legitimately have backing files, but only ones this import wrote.
func CheckChainInside(ctx context.Context, path, declared, root string) error {
	rr, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	cur := path
	for depth := 0; depth < 64; depth++ {
		// Lexical check first so a reference to a file that does not
		// exist (yet) outside the pool is still refused.
		if depth > 0 && !strings.HasPrefix(filepath.Clean(cur), strings.TrimRight(rr, "/")+"/") {
			return fmt.Errorf("backing chain leaves the pool: %s", cur)
		}
		real, err := fsutil.ResolveRegular(cur)
		if err != nil {
			return fmt.Errorf("backing chain: %w", err)
		}
		if !fsutil.InRoots(real, []string{rr}) {
			return fmt.Errorf("backing chain leaves the pool: %s", real)
		}
		args := []string{"info", "--output=json", "-U"}
		if declared != "" { // "" = format unknown, let qemu-img probe like QEMU would
			args = append(args, "-f", declared)
		}
		ictx, cancel := context.WithTimeout(ctx, infoTimeout)
		out, err := run(ictx, append(args, "--", real)...)
		cancel()
		if err != nil {
			return err
		}
		var i Info
		if err := json.Unmarshal(out, &i); err != nil {
			return err
		}
		if declared != "" && i.Format != declared {
			return fmt.Errorf("image format mismatch: declared %s, detected %s", declared, i.Format)
		}
		var raw map[string]any
		_ = json.Unmarshal(i.Spec.Data, &raw)
		if v, _ := raw["data-file"].(string); v != "" {
			return errors.New("qcow2 external data-file is not allowed")
		}
		if i.Backing == "" {
			return nil
		}
		next := i.FullBacking
		if next == "" {
			next = i.Backing
		}
		if !filepath.IsAbs(next) {
			next = filepath.Join(filepath.Dir(real), next)
		}
		cur = next
		declared = ""
		if okFormats[i.BackingFmt] {
			declared = i.BackingFmt
		}
	}
	return errors.New("backing chain too long")
}
