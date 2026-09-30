//go:build linux

package fsutil

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// DeviceID returns the stat(2) st_dev for path (the underlying block device/filesystem ID).
// Returns 0 if stat fails or path is unreadable.
func DeviceID(path string) uint64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0
	}
	return uint64(st.Dev)
}

// TryReflink attempts to clone src file into dst using the FICLONE ioctl.
// Returns nil if successful.
// If the underlying filesystem does not support reflink (or src and dst are on different devices),
// it returns an error suitable for fallback.
func TryReflink(dst, src *os.File) error {
	return unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
}

// CopyFileFast copies src to dst attempting reflink (FICLONE) first.
// If reflink is unsupported, it falls back to sparse copy or io.Copy.
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

	// 1. Try reflink CoW clone
	if err := TryReflink(out, in); err == nil {
		return out.Sync()
	}

	// 2. Fallback to sparse copy
	if err := CopySparse(out, in, fi.Size()); err != nil {
		return err
	}
	return out.Sync()
}

// CopySparse copies src to dst, skipping holes when supported by the filesystem.
func CopySparse(dst *os.File, src *os.File, size int64) error {
	const (
		seekData = 3
		seekHole = 4
	)
	if err := dst.Truncate(size); err != nil {
		return err
	}
	var offset int64
	for offset < size {
		dataStart, err := syscall.Seek(int(src.Fd()), offset, seekData)
		if err != nil {
			if errors.Is(err, syscall.ENXIO) {
				break
			}
			if _, serr := src.Seek(offset, io.SeekStart); serr != nil {
				return serr
			}
			if _, cerr := io.Copy(dst, src); cerr != nil {
				return cerr
			}
			return dst.Truncate(size)
		}
		dataEnd, err := syscall.Seek(int(src.Fd()), dataStart, seekHole)
		if err != nil {
			dataEnd = size
		}
		if _, err := src.Seek(dataStart, io.SeekStart); err != nil {
			return err
		}
		if _, err := dst.Seek(dataStart, io.SeekStart); err != nil {
			return err
		}
		if _, err := io.CopyN(dst, src, dataEnd-dataStart); err != nil {
			return err
		}
		offset = dataEnd
	}
	return dst.Truncate(size)
}
