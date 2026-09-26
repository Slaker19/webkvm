package libvirt

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// A linked clone is a qcow2 overlay whose backing file is another
// image. The dependency is recorded inside the overlay's own header,
// not in any VM definition, so the attachment guards used elsewhere
// ("is this volume attached to a VM?") cannot see it: the disk attached
// to the VM is the overlay, never the image underneath.
//
// Deleting or moving that image leaves every overlay built on it
// unopenable — the clone does not degrade, it simply stops existing.
// Templates instantiated as linked clones, appliances deployed
// copy-on-write and cached base images are all backed this way, so the
// paths that destroy or relocate a file have to ask who depends on it
// first.

// ErrBackingCheckUnavailable is returned when the dependency question
// could not be answered at all. It is deliberately not "no dependants":
// the guard exists to prevent silent data loss, so a check that cannot
// run must block the destructive path rather than wave it through.
var ErrBackingCheckUnavailable = errors.New("cannot determine whether this image is a backing file: qemu-img is not available")

// backingDependents returns the images across dirs whose backing file
// is target, as display names.
//
// Detection shells out to qemu-img rather than parsing qcow2 headers
// here: the header layout is qemu's business, and an image can chain
// through several levels. A file qemu-img cannot read is skipped rather
// than treated as a dependant — a corrupt unrelated image must not make
// an unrelated delete impossible.
func backingDependents(target string, dirs []string) ([]string, error) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		return nil, ErrBackingCheckUnavailable
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		targetAbs = target
	}

	// Gather the candidates first, then probe them concurrently: each
	// probe is a process spawn, and a populated pool has hundreds of
	// images. Done in series this turns a delete into a visible stall.
	seen := map[string]bool{}
	var cands []string
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			cand := filepath.Join(dir, e.Name())
			if cand == targetAbs || seen[cand] {
				continue
			}
			// Only a qcow2 can carry a backing file, and the magic
			// number is the only reliable way to know that: libvirt
			// volumes are frequently named without any extension at
			// all, and a ".img" is as likely to be raw as qcow2.
			if !isQcow2(cand) {
				continue
			}
			seen[cand] = true
			cands = append(cands, cand)
		}
	}

	found := make([]string, len(cands))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, cand := range cands {
		wg.Add(1)
		go func(i int, cand string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if backingFileOf(cand) == targetAbs {
				found[i] = filepath.Base(cand)
			}
		}(i, cand)
	}
	wg.Wait()

	var out []string
	for _, name := range found {
		if name != "" {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// isQcow2 reports whether the file starts with the qcow2 magic number.
// Cheap enough to run on every file in a pool, and it keeps the process
// spawns to the images that could actually have a backing file.
func isQcow2(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var magic [4]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil {
		return false
	}
	return magic == [4]byte{'Q', 'F', 'I', 0xfb}
}

// backingFileOf returns the absolute backing file path recorded in an
// image's header, or "" when it has none or cannot be read.
func backingFileOf(path string) string {
	// qemu-img can block on a network-backed pool, and this runs in a
	// loop over a whole directory, so it gets a deadline.
	cmd := exec.Command("qemu-img", "info", "--output=json", "--force-share", path)
	done := make(chan struct{})
	var out []byte
	var err error
	go func() {
		out, err = cmd.Output()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return ""
	}
	if err != nil {
		return ""
	}
	var info struct {
		BackingFilename string `json:"backing-filename"`
		FullBacking     string `json:"full-backing-filename"`
	}
	if json.Unmarshal(out, &info) != nil {
		return ""
	}
	b := info.FullBacking
	if b == "" {
		b = info.BackingFilename
	}
	if b == "" {
		return ""
	}
	if !filepath.IsAbs(b) {
		b = filepath.Join(filepath.Dir(path), b)
	}
	abs, err := filepath.Abs(b)
	if err != nil {
		return b
	}
	return abs
}

// BackingDependents reports which images are layered on top of the
// volume, as display names. Empty means the volume can be removed or
// relocated without breaking anything else.
//
// Every disk pool is searched, not just the volume's own: a linked
// clone can perfectly well live in a different pool from the image it
// is backed by.
func (c *Connector) BackingDependents(poolName, volName string) ([]string, error) {
	vol, err := c.GetStorageVolume(poolName, volName)
	if err != nil {
		return nil, err
	}
	if vol.Path == "" {
		return nil, nil
	}
	return c.BackingDependentsOfPath(vol.Path)
}

// BackingDependentsOfPath answers the same question for a file that is
// not a pool volume — an image cached outside any pool, for instance.
func (c *Connector) BackingDependentsOfPath(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	return backingDependents(path, c.allPoolDirs())
}

// allPoolDirs lists the filesystem directory of every storage pool, so
// a dependency search covers clones that live in a different pool from
// the image they are backed by — which is the normal arrangement when
// an operator keeps templates on one disk and instances on another.
func (c *Connector) allPoolDirs() []string {
	pools, err := c.ListStoragePools()
	if err != nil {
		return nil
	}
	var dirs []string
	for _, p := range pools {
		dir, derr := c.GetPoolPath(p.Name)
		if derr != nil || dir == "" {
			continue
		}
		dirs = append(dirs, dir)
	}
	return dirs
}
