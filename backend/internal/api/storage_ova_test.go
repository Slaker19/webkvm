package api

import (
	"archive/tar"
	"os"
	"path/filepath"
	"testing"
)

// writeOVA builds a minimal .ova (tar) with the given entries:
// name -> (body, typeflag, linkname).
func writeOVA(t *testing.T, path string, entries map[string]ovaEntry) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	defer tw.Close()
	for name, e := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o600,
			Size:     int64(len(e.body)),
			Typeflag: e.typeflag,
			Linkname: e.linkname,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
}

type ovaEntry struct {
	body     string
	typeflag byte
	linkname string
}

func reg(body string) ovaEntry { return ovaEntry{body: body, typeflag: tar.TypeReg} }

// TestExtractOVAAdversarialEntries proves zip-slip containment, one
// single-entry archive per case (deterministic — the extractor stops at
// the first disk-like entry, so mixed archives would be order-dependent).
// No case may create anything outside pool, whatever the entry name is.
func TestExtractOVAAdversarialEntries(t *testing.T) {
	cases := map[string]ovaEntry{
		"traversal_dotdot":     reg("bogus"),
		"absolute_path":        reg("bogus"),
		"nested_dirs":          reg("bogus"),
		"legit_qcow2_passthru": reg("bogus"),
	}
	names := map[string]string{
		"traversal_dotdot":     "../escape.vmdk",
		"absolute_path":        "/abs.vmdk",
		"nested_dirs":          "sub/dir/nested.vmdk",
		"legit_qcow2_passthru": "disk.qcow2",
	}
	for tc, entry := range cases {
		t.Run(tc, func(t *testing.T) {
			parent := t.TempDir()
			pool := filepath.Join(parent, "pool")
			if err := os.MkdirAll(pool, 0o755); err != nil {
				t.Fatal(err)
			}
			ova := filepath.Join(parent, "case.ova")
			writeOVA(t, ova, map[string]ovaEntry{names[tc]: entry})

			finalPath, finalName, _, err := extractAndConvertOVA(ova, pool, "case.ova")
			if tc == "legit_qcow2_passthru" {
				// .qcow2 entries take the rename branch (no qemu-img
				// needed): must land inside the pool, nothing else.
				if err != nil {
					t.Fatalf("legit entry must extract: %v", err)
				}
				if finalName != "case.qcow2" {
					t.Fatalf("unexpected output name %q", finalName)
				}
				if st, serr := os.Stat(finalPath); serr != nil || st.Size() == 0 {
					t.Fatalf("output missing/empty: %v", serr)
				}
			} else if err != nil {
				t.Logf("extract returned (acceptable, content is bogus): %v", err)
			}

			// Nothing escaped: parent holds exactly pool/ + case.ova,
			// and no .webkvm-* workdir leaked.
			dirents, derr := os.ReadDir(parent)
			if derr != nil {
				t.Fatal(derr)
			}
			for _, e := range dirents {
				if e.Name() != "pool" && e.Name() != "case.ova" {
					t.Fatalf("file escaped pool dir: %s", e.Name())
				}
			}
		})
	}
}

// TestExtractOVASymlinkOnlyArchive proves an archive with no regular disk
// image is rejected instead of extracting link metadata.
func TestExtractOVASymlinkOnlyArchive(t *testing.T) {
	parent := t.TempDir()
	pool := filepath.Join(parent, "pool")
	if err := os.MkdirAll(pool, 0o755); err != nil {
		t.Fatal(err)
	}
	ova := filepath.Join(parent, "links.ova")
	writeOVA(t, ova, map[string]ovaEntry{
		"sneaky.vmdk": {body: "", typeflag: tar.TypeSymlink, linkname: "/etc/passwd"},
	})
	if _, _, _, err := extractAndConvertOVA(ova, pool, "links.ova"); err == nil {
		t.Fatal("expected error for symlink-only archive, got nil")
	}
}
