package fsutil

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideRoots is returned by ResolveInRoots when a regular file exists
// but lives outside every allowed root.
var ErrOutsideRoots = errors.New("path is outside the allowed storage pools")

// ErrNotRegular is returned when the resolved path is not a regular file
// (device node, socket, FIFO, directory...).
var ErrNotRegular = errors.New("not a regular file")

// ResolveRegular returns the canonical (symlink-free) path of p if it is an
// absolute path to a regular file. Device nodes, sockets, FIFOs and
// directories are rejected. It applies no root confinement.
func ResolveRegular(p string) (string, error) {
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) || strings.Contains(clean, "..") || strings.Contains(p, "..") {
		return "", errors.New("absolute path without traversal required")
	}
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", err
	}
	cleanReal := filepath.Clean(real)
	if !filepath.IsAbs(cleanReal) || strings.Contains(cleanReal, "..") {
		return "", errors.New("resolved path traversal not allowed")
	}
	fi, err := os.Lstat(cleanReal)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s", ErrNotRegular, cleanReal)
	}
	return cleanReal, nil
}

// ResolveInRoots returns the canonical path of p if, and only if, it is a
// regular file located inside one of roots (after resolving symlinks on
// both sides).
func ResolveInRoots(p string, roots []string) (string, error) {
	real, err := ResolveRegular(p)
	if err != nil {
		return "", err
	}
	if !InRoots(real, roots) {
		return "", ErrOutsideRoots
	}
	return real, nil
}

// InRoots reports whether the (already canonical) path real is strictly
// inside one of roots.
func InRoots(real string, roots []string) bool {
	sep := string(os.PathSeparator)
	for _, r := range roots {
		if r == "" {
			continue
		}
		rr, err := filepath.EvalSymlinks(r)
		if err != nil {
			continue
		}
		if strings.HasPrefix(real, strings.TrimRight(rr, sep)+sep) {
			return true
		}
	}
	return false
}

// CreateExclusive creates a NEW file named name inside dir without
// following symlinks out of dir, and never overwrites an existing file
// (an error wrapping os.ErrExist is returned in that case).
func CreateExclusive(dir, name string, perm os.FileMode) (*os.File, error) {
	if name == "" || name != filepath.Base(name) || name == "." || name == ".." {
		return nil, errors.New("invalid file name")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
}
