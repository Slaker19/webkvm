package libvirt

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// makeImage creates a real qcow2, optionally backed by another one.
// The guard reads actual image headers, so there is nothing useful to
// fake here.
func makeImage(t *testing.T, path, backing string) {
	t.Helper()
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img is not installed")
	}
	args := []string{"create", "-f", "qcow2"}
	if backing != "" {
		args = append(args, "-b", backing, "-F", "qcow2")
	} else {
		args = append(args, "-o", "preallocation=full")
	}
	args = append(args, path, "8M")
	if out, err := exec.Command("qemu-img", args...).CombinedOutput(); err != nil {
		t.Fatalf("create %s: %v: %s", path, err, out)
	}
}

func TestBackingDependentsFindsClone(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")
	makeImage(t, filepath.Join(dir, "clone.qcow2"), base)

	deps, err := backingDependents(base, []string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 1 || deps[0] != "clone.qcow2" {
		t.Errorf("dependants = %v, want [clone.qcow2]", deps)
	}
}

// libvirt volumes are routinely named with no extension at all, and a
// ".img" is as likely to be raw as qcow2. Filtering candidates by
// filename would make those clones invisible and let the guard approve
// a delete that destroys them.
func TestBackingDependentsIgnoresFilenames(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")
	makeImage(t, filepath.Join(dir, "no-extension"), base)
	makeImage(t, filepath.Join(dir, "looks-like-raw.img"), base)

	deps, err := backingDependents(base, []string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 2 {
		t.Fatalf("dependants = %v, want both clones regardless of their names", deps)
	}
}

// A clone very often lives in a different pool from the image it is
// backed by: templates on one disk, instances on another. Searching
// only the image's own directory would miss exactly that case.
func TestBackingDependentsSearchesEveryDir(t *testing.T) {
	poolA, poolB := t.TempDir(), t.TempDir()
	base := filepath.Join(poolA, "base.qcow2")
	makeImage(t, base, "")
	makeImage(t, filepath.Join(poolB, "remote-clone.qcow2"), base)

	deps, err := backingDependents(base, []string{poolA, poolB})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 1 || deps[0] != "remote-clone.qcow2" {
		t.Errorf("dependants = %v, want the clone in the other pool", deps)
	}
}

// An unrelated image must not block anything, or every delete in a
// populated pool would be refused.
func TestBackingDependentsIgnoresUnrelated(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")
	other := filepath.Join(dir, "other.qcow2")
	makeImage(t, other, "")
	makeImage(t, filepath.Join(dir, "clone-of-other.qcow2"), other)
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	deps, err := backingDependents(base, []string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("dependants = %v, want none", deps)
	}
}

// A corrupt or unreadable image is skipped, not counted: one broken
// file in a pool must not make every unrelated delete impossible.
func TestBackingDependentsSkipsUnreadable(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")
	// qcow2 magic followed by garbage: passes the cheap pre-filter,
	// fails to parse.
	if err := os.WriteFile(filepath.Join(dir, "corrupt.qcow2"),
		append([]byte{'Q', 'F', 'I', 0xfb}, make([]byte, 64)...), 0o644); err != nil {
		t.Fatal(err)
	}

	deps, err := backingDependents(base, []string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("dependants = %v, want none", deps)
	}
}

// The target itself is never its own dependant, however the caller
// spelled the path.
func TestBackingDependentsExcludesItself(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")

	deps, err := backingDependents(filepath.Join(dir, ".", "base.qcow2"), []string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("dependants = %v, want none", deps)
	}
}

// A missing directory is not an error: an inactive pool's path may
// simply not exist, and that says nothing about dependants elsewhere.
func TestBackingDependentsToleratesMissingDir(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")
	makeImage(t, filepath.Join(dir, "clone.qcow2"), base)

	deps, err := backingDependents(base, []string{"/nonexistent-pool", dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 1 {
		t.Errorf("dependants = %v, want the clone from the directory that does exist", deps)
	}
}

// Without qemu-img the question cannot be answered. Reporting "no
// dependants" would let the destructive path proceed on a guess, which
// is the exact failure the guard exists to prevent.
func TestBackingDependentsFailsClosed(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")

	t.Setenv("PATH", t.TempDir())
	deps, err := backingDependents(base, []string{dir})
	if !errors.Is(err, ErrBackingCheckUnavailable) {
		t.Errorf("err = %v, want ErrBackingCheckUnavailable", err)
	}
	if deps != nil {
		t.Errorf("dependants = %v, want nil", deps)
	}
}

func TestIsQcow2(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "a.qcow2")
	makeImage(t, img, "")
	if !isQcow2(img) {
		t.Error("a real qcow2 was not recognised")
	}

	raw := filepath.Join(dir, "b.raw")
	if err := os.WriteFile(raw, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if isQcow2(raw) {
		t.Error("a raw image was mistaken for qcow2")
	}

	// Shorter than the magic number: must not panic or read past it.
	tiny := filepath.Join(dir, "tiny")
	if err := os.WriteFile(tiny, []byte{'Q'}, 0o644); err != nil {
		t.Fatal(err)
	}
	if isQcow2(tiny) {
		t.Error("a one-byte file was mistaken for qcow2")
	}
	if isQcow2(filepath.Join(dir, "does-not-exist")) {
		t.Error("a missing file was mistaken for qcow2")
	}
}

// backingFileOf must resolve a relative backing path against the
// overlay's own directory, or a pool-relative chain would compare
// unequal to the absolute path the caller passed in and the dependency
// would go unnoticed.
func TestBackingFileOfResolvesRelative(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")
	clone := filepath.Join(dir, "clone.qcow2")
	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2",
		"-b", "base.qcow2", "-F", "qcow2", clone, "8M").CombinedOutput(); err != nil {
		t.Skipf("relative backing not supported here: %v: %s", err, out)
	}
	if got := backingFileOf(clone); got != base {
		t.Errorf("backingFileOf = %q, want %q", got, base)
	}
	if got := backingFileOf(base); got != "" {
		t.Errorf("an image with no backing file returned %q", got)
	}
}

// Backing chains can be several levels deep (base <- mid <- top).
// Only the image directly on top counts as a dependant here: deleting
// the base breaks mid, and the guard names the file that has to be
// dealt with first rather than the whole chain.
func TestBackingDependentsChain(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	mid := filepath.Join(dir, "mid.qcow2")
	top := filepath.Join(dir, "top.qcow2")
	makeImage(t, base, "")
	makeImage(t, mid, base)
	makeImage(t, top, mid)

	deps, err := backingDependents(base, []string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 1 || deps[0] != "mid.qcow2" {
		t.Errorf("dependants of base = %v, want [mid.qcow2]", deps)
	}

	// And the middle of the chain is itself protected.
	deps, err = backingDependents(mid, []string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 1 || deps[0] != "top.qcow2" {
		t.Errorf("dependants of mid = %v, want [top.qcow2]", deps)
	}

	// The leaf blocks nothing, so it stays deletable.
	deps, err = backingDependents(top, []string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 0 {
		t.Errorf("dependants of top = %v, want none", deps)
	}
}

// Several clones on one base must all be named: an operator told about
// one, who deletes it and retries, should not meet the same refusal
// again for a different file.
func TestBackingDependentsNamesAllClones(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")
	for _, n := range []string{"web-01.qcow2", "web-02.qcow2", "web-03.qcow2"} {
		makeImage(t, filepath.Join(dir, n), base)
	}

	deps, err := backingDependents(base, []string{dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 3 {
		t.Fatalf("dependants = %v, want all three clones", deps)
	}
	// Sorted, so the message does not reshuffle between identical calls.
	for i := 1; i < len(deps); i++ {
		if deps[i-1] > deps[i] {
			t.Errorf("dependants are not in a stable order: %v", deps)
			break
		}
	}
}

// The same directory listed twice must not double-count a clone.
func TestBackingDependentsDeduplicates(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.qcow2")
	makeImage(t, base, "")
	makeImage(t, filepath.Join(dir, "clone.qcow2"), base)

	deps, err := backingDependents(base, []string{dir, dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(deps) != 1 {
		t.Errorf("dependants = %v, want the clone listed once", deps)
	}
}
