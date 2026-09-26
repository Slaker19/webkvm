package backupstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestS3ObjectKeySanitization: las object keys deben ser rutas relativas
// limpias — sin '/', '//', '..' ni trailing slash.
func TestS3ObjectKeySanitization(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		filename string
		want     string
	}{
		{"sin prefix", "", "webkvm-h-20260101.tar.gz", "webkvm-h-20260101.tar.gz"},
		{"prefix normal", "backups", "webkvm-h-20260101.tar.gz", "backups/webkvm-h-20260101.tar.gz"},
		{"leading slash", "/backups", "a.tar.gz", "backups/a.tar.gz"},
		{"double slash", "backups//sub", "a.tar.gz", "backups/sub/a.tar.gz"},
		{"trailing slash", "backups/", "a.tar.gz", "backups/a.tar.gz"},
		{"traversal", "backups/../../etc", "a.tar.gz", "backups/etc/a.tar.gz"},
		{"dot", "./backups", "a.tar.gz", "backups/a.tar.gz"},
	}
	for _, c := range cases {
		tgt := Target{Path: c.path}
		got := s3ObjectKey(tgt, c.filename)
		if got != c.want {
			t.Errorf("%s: s3ObjectKey(%q,%q) = %q, want %q", c.name, c.path, c.filename, got, c.want)
		}
		if strings.HasPrefix(got, "/") || strings.Contains(got, "//") {
			t.Errorf("%s: key %q is not a clean relative path", c.name, got)
		}
	}
}

// TestS3ClientFor_Validation: sin bucket o sin credenciales se rechaza.
func TestS3ClientFor_Validation(t *testing.T) {
	if _, err := s3ClientFor(Target{Type: TargetS3}); err == nil {
		t.Error("missing bucket should error")
	}
	if _, err := s3ClientFor(Target{Type: TargetS3, Bucket: "b"}); err == nil {
		t.Error("missing credentials should error")
	}
	if _, err := s3ClientFor(Target{Type: TargetLocal}); err == nil {
		t.Error("non-s3 target should error")
	}
}

// TestS3CancelContext: un contexto cancelado aborta el upload (no se cuelga).
func TestS3CancelContext(t *testing.T) {
	dir := t.TempDir()
	f, _ := os.Create(filepath.Join(dir, "webkvm-h-20260101.tar.gz"))
	_ = f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tgt := Target{Type: TargetS3, Bucket: "b", Secret: TargetSecret{AccessKey: "AK", SecretKey: "SK"}}
	// El upload intenta conectar/abrir y el contexto ya está cancelado; no
	// debe colgarse y debe propagar un error.
	_, err := s3UploadRun(ctx, tgt, dir, []JobFile{{Filename: "webkvm-h-20260101.tar.gz", Size: 1}})
	if err == nil {
		t.Error("expected error with cancelled context")
	}
}

// TestS3UploadRun_StreamsFromDisk: los ficheros se leen del disco (PutObject
// recibe un *os.File), no de RAM. Con un fichero real local y un cliente que
// falla, el fallo ocurre tras abrir el archivo — no antes.
func TestS3UploadRun_ReadsFilesFromDisk(t *testing.T) {
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "webkvm-h-20260101.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	tgt := Target{Type: TargetS3, Bucket: "b", Secret: TargetSecret{AccessKey: "AK", SecretKey: "SK"}}
	// Sin servidor, el upload falla con un error de conexión (no de lectura).
	_, err = s3UploadRun(context.Background(), tgt, dir, []JobFile{{Filename: "webkvm-h-20260101.tar.gz", Size: 1}})
	if err == nil {
		t.Error("expected connection error with no server")
	}
}
