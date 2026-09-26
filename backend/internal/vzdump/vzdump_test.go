package vzdump_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"webkvm/internal/vzdump"
)

// buildVzdumpTar writes a minimal vzdump LXC tar into w with the given
// compression ("gzip", "zstd", or "" for plain).
func buildVzdumpTar(t *testing.T, compress string) []byte {
	t.Helper()
	var buf bytes.Buffer

	var tw *tar.Writer
	var gw *gzip.Writer
	var zw *zstd.Encoder

	switch compress {
	case "gzip":
		gw = gzip.NewWriter(&buf)
		tw = tar.NewWriter(gw)
	case "zstd":
		var err error
		zw, err = zstd.NewWriter(&buf)
		if err != nil {
			t.Fatalf("zstd.NewWriter: %v", err)
		}
		tw = tar.NewWriter(zw)
	default:
		tw = tar.NewWriter(&buf)
	}

	// pct.conf
	pctConf := "hostname: test-ct\ncores: 2\nmemory: 1024\narch: amd64\nostype: ubuntu\n"
	writeEntry(t, tw, "./etc/vzdump/pct.conf", pctConf)

	// rootfs entries (simulating a minimal container filesystem)
	writeEntry(t, tw, "./rootfs/", "")
	writeEntry(t, tw, "./rootfs/etc/", "")
	writeEntry(t, tw, "./rootfs/etc/hostname", "test-ct\n")
	writeEntry(t, tw, "./rootfs/etc/os-release", "ID=ubuntu\nVERSION_ID=24.04\n")

	if err := tw.Close(); err != nil {
		t.Fatalf("tw.Close: %v", err)
	}
	if gw != nil {
		if err := gw.Close(); err != nil {
			t.Fatalf("gw.Close: %v", err)
		}
	}
	if zw != nil {
		if err := zw.Close(); err != nil {
			t.Fatalf("zw.Close: %v", err)
		}
	}

	return buf.Bytes()
}

func writeEntry(t *testing.T, tw *tar.Writer, name, content string) {
	t.Helper()
	typ := tar.TypeReg
	if strings.HasSuffix(name, "/") {
		typ = tar.TypeDir
	}
	hdr := &tar.Header{
		Name:     name,
		Typeflag: byte(typ),
		Mode:     0644,
		Size:     int64(len(content)),
		ModTime:  time.Now(),
	}
	if typ == tar.TypeDir {
		hdr.Mode = 0755
		hdr.Size = 0
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("WriteHeader %s: %v", name, err)
	}
	if content != "" {
		if _, err := io.WriteString(tw, content); err != nil {
			t.Fatalf("Write %s: %v", name, err)
		}
	}
}

// TestIsVzdumpLXC_Gzip verifies detection of gzip-compressed vzdump archives.
func TestIsVzdumpLXC_Gzip(t *testing.T) {
	data := buildVzdumpTar(t, "gzip")
	tmp := writeTmpFile(t, data)
	ok, err := vzdump.IsVzdumpLXC(tmp)
	if err != nil {
		t.Fatalf("IsVzdumpLXC: %v", err)
	}
	if !ok {
		t.Error("expected IsVzdumpLXC=true for gzip vzdump, got false")
	}
}

// TestIsVzdumpLXC_Zstd verifies detection of zstd-compressed vzdump archives.
func TestIsVzdumpLXC_Zstd(t *testing.T) {
	data := buildVzdumpTar(t, "zstd")
	tmp := writeTmpFile(t, data)
	ok, err := vzdump.IsVzdumpLXC(tmp)
	if err != nil {
		t.Fatalf("IsVzdumpLXC: %v", err)
	}
	if !ok {
		t.Error("expected IsVzdumpLXC=true for zstd vzdump, got false")
	}
}

// TestIsVzdumpLXC_NotVzdump verifies that a plain tar without pct.conf is not
// detected as a vzdump archive.
func TestIsVzdumpLXC_NotVzdump(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeEntry(t, tw, "domain.xml", "<domain/>")
	writeEntry(t, tw, "manifest.txt", "name=test")
	_ = tw.Close()
	tmp := writeTmpFile(t, buf.Bytes())
	ok, err := vzdump.IsVzdumpLXC(tmp)
	if err != nil {
		t.Fatalf("IsVzdumpLXC: %v", err)
	}
	if ok {
		t.Error("expected IsVzdumpLXC=false for WebKVM backup, got true")
	}
}

// TestImportFromProxmox verifies that a vzdump LXC gzip archive is correctly
// converted to an Incus-compatible backup tar.
func TestImportFromProxmox(t *testing.T) {
	data := buildVzdumpTar(t, "gzip")
	srcFile := writeTmpFile(t, data)
	destFile := filepath.Join(t.TempDir(), "incus-backup.tar")

	if err := vzdump.ImportFromProxmox(srcFile, destFile, "restored-ct", "webkvm-incus"); err != nil {
		t.Fatalf("ImportFromProxmox: %v", err)
	}

	// Open the output tar and check required entries.
	df, err := os.Open(destFile)
	if err != nil {
		t.Fatalf("open dest: %v", err)
	}
	defer df.Close()

	entries := map[string]bool{}
	tr := tar.NewReader(df)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("iterate output tar: %v", err)
		}
		name := strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./")
		entries[name] = true

		// Check backup.yaml contains the new container name
		if name == "backup/container/backup.yaml" {
			data, _ := io.ReadAll(tr)
			if !strings.Contains(string(data), "restored-ct") {
				t.Errorf("backup.yaml does not contain 'restored-ct':\n%s", data)
			}
		}
	}

	for _, required := range []string{"backup/container/backup.yaml", "backup/index.yaml"} {
		if !entries[required] {
			t.Errorf("output tar missing required entry: %s", required)
		}
	}
	// rootfs entries should be under backup/container/rootfs/
	found := false
	for k := range entries {
		if strings.HasPrefix(k, "backup/container/rootfs/") {
			found = true
			break
		}
	}
	if !found {
		t.Error("output tar has no backup/container/rootfs/ entries")
	}
}

// TestImportFromProxmox_Zstd verifies the same for a zstd-compressed source.
func TestImportFromProxmox_Zstd(t *testing.T) {
	data := buildVzdumpTar(t, "zstd")
	srcFile := writeTmpFile(t, data)
	destFile := filepath.Join(t.TempDir(), "incus-backup.tar")

	if err := vzdump.ImportFromProxmox(srcFile, destFile, "ct-zstd", ""); err != nil {
		t.Fatalf("ImportFromProxmox (zstd): %v", err)
	}

	df, err := os.Open(destFile)
	if err != nil {
		t.Fatalf("open dest: %v", err)
	}
	defer df.Close()

	tr := tar.NewReader(df)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("iterate: %v", err)
		}
		if strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./") == "backup/container/backup.yaml" {
			found = true
		}
	}
	if !found {
		t.Error("output tar missing backup/container/backup.yaml")
	}
}

// TestVzdumpFilename verifies the generated filename format.
func TestVzdumpFilename(t *testing.T) {
	ts := time.Date(2026, 9, 21, 15, 4, 5, 0, time.UTC)
	got := vzdump.VzdumpFilename("my-container", ts)
	want := "vzdump-lxc-my-container-2026_09_21-15_04_05.tar.zst"
	if got != want {
		t.Errorf("VzdumpFilename: got %q, want %q", got, want)
	}
}

// TestVzdumpFilename_SpecialChars verifies that special chars are sanitized.
func TestVzdumpFilename_SpecialChars(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got := vzdump.VzdumpFilename("hello world/test", ts)
	if strings.Contains(got, " ") || strings.Contains(got, "/") {
		t.Errorf("VzdumpFilename contains unsafe chars: %q", got)
	}
}

func writeTmpFile(t *testing.T, data []byte) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "vzdump-test-*")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		t.Fatalf("Write tmpfile: %v", err)
	}
	f.Close()
	return f.Name()
}

// yamlFromArchive returns the contents of one entry of a converted tar.
func yamlFromArchive(t *testing.T, path, entry string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("iterate tar: %v", err)
		}
		if strings.TrimPrefix(filepath.ToSlash(hdr.Name), "./") == entry {
			b, _ := io.ReadAll(tr)
			return string(b)
		}
	}
	t.Fatalf("entry %q not found in %s", entry, path)
	return ""
}

// TestImportFromProxmoxPool pins the storage pool written into the
// generated YAML.
//
// Both files used to hardcode "default", which only resolves on an Incus
// install that happens to have a pool by that name. WebKVM names its
// pool after the volume backing it, so the import declared a pool that
// did not exist.
func TestImportFromProxmoxPool(t *testing.T) {
	for name, tc := range map[string]struct{ pool, want string }{
		"explicit": {"Lexar-Contenedores", "Lexar-Contenedores"},
		"empty":    {"", "default"},
		"blank":    {"   ", "default"},
	} {
		t.Run(name, func(t *testing.T) {
			src := writeTmpFile(t, buildVzdumpTar(t, "gzip"))
			dest := filepath.Join(t.TempDir(), "out.tar")
			if err := vzdump.ImportFromProxmox(src, dest, "ct", tc.pool); err != nil {
				t.Fatalf("convert: %v", err)
			}

			backup := yamlFromArchive(t, dest, "backup/container/backup.yaml")
			// Three separate pool references: the root device, the
			// expanded root device, and the volume.
			if got := strings.Count(backup, "pool: "+tc.want); got != 3 {
				t.Errorf("backup.yaml has %d %q references, want 3:\n%s", got, tc.want, backup)
			}
			// The "default" PROFILE is unrelated to the pool and must
			// survive verbatim; an over-eager substitution would break
			// every imported container's networking.
			if !strings.Contains(backup, "profiles:\n  - default") {
				t.Errorf("default profile lost from backup.yaml:\n%s", backup)
			}

			index := yamlFromArchive(t, dest, "backup/index.yaml")
			if !strings.Contains(index, "pool: "+tc.want+"\n") {
				t.Errorf("index.yaml pool = %q, want %q:\n%s", index, tc.want, index)
			}
		})
	}
}
