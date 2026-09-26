package libvirt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateDisksReadableAcceptsEmptyDisk covers a check that could
// never succeed: the EOF tolerance was written as
//
//	!errors.Is(err, errors.New("EOF"))
//
// which compares the real io.EOF sentinel against a brand new error
// value. errors.Is can never match that, so the condition was always
// true and reading a 0-byte disk returned "read disk ...: EOF".
//
// A zero-length disk is ordinary: a raw volume that was created but
// never written to, or a placeholder file. Exporting or starting such
// a VM failed with what looked like an I/O fault on the storage.
func TestValidateDisksReadableAcceptsEmptyDisk(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.raw")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(empty); err != nil || fi.Size() != 0 {
		t.Fatalf("fixture is not a 0-byte file: %v", err)
	}

	xml := `<domain><devices><disk type='file' device='disk'>` +
		`<source file='` + empty + `'/><target dev='vda' bus='virtio'/>` +
		`</disk></devices></domain>`

	c := &Connector{}
	if err := c.validateDisksReadable(xml); err != nil {
		t.Fatalf("a 0-byte disk must be accepted, got: %v", err)
	}
}

// TestValidateDisksReadableRejectsMissingDisk keeps the real failure
// path honest: a disk that is not on disk at all must still be an
// error, with an actionable message.
func TestValidateDisksReadableRejectsMissingDisk(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone.qcow2")
	xml := `<domain><devices><disk type='file' device='disk'>` +
		`<source file='` + missing + `'/><target dev='vda' bus='virtio'/>` +
		`</disk></devices></domain>`

	c := &Connector{}
	err := c.validateDisksReadable(xml)
	if err == nil {
		t.Fatal("a missing disk must be rejected")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error should name the offending path, got: %v", err)
	}
}
