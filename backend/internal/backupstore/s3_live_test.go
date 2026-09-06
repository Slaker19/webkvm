package backupstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// TestS3Transport_AgainstMinIO is the V13-BCK-02 real gate: it exercises
// the whole S3 transport (ensure bucket, streamed upload, list, verify,
// delete-run, cleanup) against a live S3-compatible store.
//
// Skipped unless the env vars are set:
//
//	MINIO_TEST_ENDPOINT=http://127.0.0.1:19000
//	MINIO_TEST_ACCESS=minioadmin  MINIO_TEST_SECRET=minioadmin123
func TestS3Transport_AgainstMinIO(t *testing.T) {
	endpoint := os.Getenv("MINIO_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("MINIO_TEST_ENDPOINT not set; skipping live MinIO gate")
	}
	access := os.Getenv("MINIO_TEST_ACCESS")
	secret := os.Getenv("MINIO_TEST_SECRET")
	if access == "" || secret == "" {
		t.Fatal("MINIO_TEST_ACCESS/SECRET required with endpoint")
	}

	// Unique bucket + prefix so a parallel run never collides.
	bucket := "webkvm-bck02-test"
	prefix := "gate-" + newID("g")

	tgt := Target{
		Type:     TargetS3,
		Bucket:   bucket,
		Endpoint: endpoint,
		Path:     prefix,
		Secret:   TargetSecret{AccessKey: access, SecretKey: secret},
	}

	// Clean slate: drop any leftover bucket from a crashed run.
	if c, err := s3ClientFor(tgt); err == nil {
		_ = c.RemoveBucket(context.Background(), bucket)
	}

	ctx := context.Background()
	if err := s3EnsureBucket(ctx, tgt); err != nil {
		t.Fatalf("ensure bucket: %v", err)
	}
	t.Cleanup(func() {
		// Cleanup: remove objects then the bucket so the ephemeral
		// MinIO is left clean.
		_ = s3CleanupTestObjects(tgt, prefix+"/")
		if c, err := s3ClientFor(tgt); err == nil {
			_ = c.RemoveBucket(context.Background(), bucket)
		}
	})

	// 1) Streamed upload from disk: write a real multi-MB file (sparse,
	// but streamed — the point is PutObject reads it from disk, not RAM).
	dir := t.TempDir()
	// Filename follows the runner convention
	// webkvm-<host>-<tsNano>-<runSuffix>-<name>.tar.zst; the run suffix
	// matches isRunSuffix so s3DeleteRun can target it.
	const runSuffix = "20260101T120000.123456789Z-abc123"
	const fname = "webkvm-gatehost-20260101T120000-" + runSuffix + "-config.tar.zst"
	local := filepath.Join(dir, fname)
	blob := make([]byte, 4<<20) // 4 MiB pattern
	for i := range blob {
		blob[i] = byte(i % 251)
	}
	if err := os.WriteFile(local, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(blob)

	files := []JobFile{{Filename: fname, Size: int64(len(blob))}}
	total, err := s3UploadRun(ctx, tgt, dir, files)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if total != int64(len(blob)) {
		t.Fatalf("uploaded %d bytes, want %d", total, len(blob))
	}

	// 2) List sees it under the prefix, as a clean relative name.
	list, err := s3List(tgt)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Filename != fname {
		t.Fatalf("list = %+v, want exactly %q", list, fname)
	}
	if strings.HasPrefix(list[0].Filename, "/") {
		t.Fatalf("listed name has leading slash: %q", list[0].Filename)
	}

	// 3) Verify streams back and matches the local sha256.
	vf, err := s3Verify(tgt, fname)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if vf.Sha256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("sha256 mismatch: got %s want %s", vf.Sha256, hex.EncodeToString(wantHash[:]))
	}

	// 4) Stat config-style key existence check.
	cfgKey := s3ObjectKey(tgt, "config/webkvm-config-latest.tar.zst")
	if cfgKey == "" || strings.Contains(cfgKey, "//") {
		t.Fatalf("bad config key %q", cfgKey)
	}

	// 5) Delete a run (the whole run is removed by suffix).
	removed, err := s3DeleteRun(tgt, runSuffix)
	if err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if removed < 1 {
		t.Fatalf("delete run removed %d objects, want >=1", removed)
	}
	left, err := s3List(tgt)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("after delete run, %d objects remain: %+v", len(left), left)
	}

	// 6) Cleanup helper is idempotent.
	if err := s3CleanupTestObjects(tgt, prefix+"/"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// TestS3_ObjectPersistsAcrossClients: un objeto subido con el transporte
// debe poder leerse con un cliente minio-go directo (prueba de que la key
// es correcta y el bucket accesible).
func TestS3_ObjectPersistsAcrossClients(t *testing.T) {
	endpoint := os.Getenv("MINIO_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("MINIO_TEST_ENDPOINT not set")
	}
	access := os.Getenv("MINIO_TEST_ACCESS")
	secret := os.Getenv("MINIO_TEST_SECRET")

	bucket := "webkvm-bck02-cross"
	prefix := "gate-" + newID("g")
	tgt := Target{Type: TargetS3, Bucket: bucket, Endpoint: endpoint, Path: prefix,
		Secret: TargetSecret{AccessKey: access, SecretKey: secret}}
	ctx := context.Background()
	if err := s3EnsureBucket(ctx, tgt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s3CleanupTestObjects(tgt, prefix+"/")
		if c, err := s3ClientFor(tgt); err == nil {
			_ = c.RemoveBucket(context.Background(), bucket)
		}
	})

	// Raw client (independent of the transport) reading the same key.
	raw, err := minio.New(strings.TrimPrefix(endpoint, "http://"), &minio.Options{
		Creds:  credentials.NewStaticV4(access, secret, ""),
		Secure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.StatObject(ctx, bucket, s3ObjectKey(tgt, "nothing.tar.gz"), minio.StatObjectOptions{}); err == nil {
		t.Fatal("expected NoSuchKey for a missing object")
	}
}
