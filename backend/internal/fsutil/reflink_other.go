//go:build !linux

package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var ErrReflinkUnsupported = errors.New("reflink is not supported on this platform")

// TryReflink is a no-op on non-Linux platforms.
func TryReflink(dst, src *os.File) error {
	return ErrReflinkUnsupported
}

// CopyFileFast copies src to dst using io.Copy on non-Linux platforms.
func CopyFileFast(src, dst string) error {
	if strings.Contains(src, "..") || strings.Contains(dst, "..") {
		return fmt.Errorf("invalid path: traversal not allowed")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	fi, err := in.Stat()
	if err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// CopySparse falls back to io.Copy on non-Linux platforms.
func CopySparse(dst *os.File, src *os.File, size int64) error {
	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Truncate(size)
}
