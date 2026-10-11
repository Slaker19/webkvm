package compute

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type importRecorder struct {
	fakeBackend
	called bool
}

func (r *importRecorder) ImportDomain(string, string, string, ImportOpts) (string, string, []string, error) {
	r.called = true
	return "id", "name", nil, nil
}

func incusArchive(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "backup.tar")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	body := "container: {}\n"
	tw.WriteHeader(&tar.Header{Name: "backup/index.yaml", Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	f.Close()
	return p
}

// A native Incus backup carries a config the daemon applies verbatim, so
// only administrators (or WebKVM-generated archives) may import it (audit #3).
func TestCombinedImportIncusBackupIsAdminOnly(t *testing.T) {
	kvm := &fakeBackend{}
	lxd := &importRecorder{}
	c := NewCombined(kvm, lxd)
	arc := incusArchive(t)

	if _, _, _, err := c.ImportDomain(arc, "x", "p", ImportOpts{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("non-admin: err = %v, want ErrForbidden", err)
	}
	if lxd.called {
		t.Fatal("secondary backend reached for a non-admin")
	}
	if _, _, _, err := c.ImportDomain(arc, "x", "p", ImportOpts{Admin: true}); err != nil || !lxd.called {
		t.Fatalf("admin import failed: %v called=%v", err, lxd.called)
	}
	lxd.called = false
	if _, _, _, err := c.ImportDomain(arc, "x", "p", ImportOpts{GeneratedConfig: true}); err != nil || !lxd.called {
		t.Fatalf("WebKVM-generated archive refused: %v", err)
	}
}
