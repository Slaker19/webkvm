package hainstall

import (
	"os"
	"path/filepath"
	"testing"
)

// New("") must be ephemeral: it may not create ha-module.json in the working
// directory (a test run once left backend/cmd/cli/ha-module.json behind).
func TestNewWithEmptyDataDirDoesNotWriteIntoWorkingDir(t *testing.T) {
	wd := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(wd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	m := New("", nil)
	m.mu.Lock()
	m.saveLocked()
	m.mu.Unlock()

	if _, err := os.Stat(filepath.Join(wd, "ha-module.json")); !os.IsNotExist(err) {
		t.Fatalf("New(\"\") wrote ha-module.json into the working directory (stat err=%v)", err)
	}
}
