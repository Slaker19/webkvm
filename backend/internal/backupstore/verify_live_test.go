package backupstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
)

// TestVerifyCorruption_AgainstMinIO is the V13-BCK-04 real corruption
// gate: upload an archive to a live S3 store, verify it (intact → ok),
// then CORRUPT the remote object and prove verifyWrittenPrimary detects
// the mismatch and purges the poisoned object — the exact path a
// VerifyOnWrite job takes. Skipped unless MINIO_TEST_* is set.
func TestVerifyCorruption_AgainstMinIO(t *testing.T) {
	endpoint := os.Getenv("MINIO_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("MINIO_TEST_ENDPOINT not set; skipping live MinIO corruption gate")
	}
	access := os.Getenv("MINIO_TEST_ACCESS")
	secret := os.Getenv("MINIO_TEST_SECRET")

	bucket := "webkvm-bck04-verify"
	prefix := "gate-" + newID("g")
	tgt := Target{
		Type:     TargetS3,
		Bucket:   bucket,
		Endpoint: endpoint,
		Path:     prefix,
		Secret:   TargetSecret{AccessKey: access, SecretKey: secret},
	}
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

	const fname = "webkvm-gatehost-20260101T120000.123456789Z-abc123-config.tar.zst"
	dir := t.TempDir()
	staging := filepath.Join(dir, "staging")
	_ = os.MkdirAll(staging, 0o700)
	local := filepath.Join(staging, fname)
	blob := []byte("the pristine archive produced by the backup job")
	if err := os.WriteFile(local, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	files := []JobFile{{Filename: fname, Size: int64(len(blob))}}
	if _, err := s3UploadRun(ctx, tgt, staging, files); err != nil {
		t.Fatalf("upload: %v", err)
	}

	// Intact copy passes verification (recorded into a temp store).
	storeDir := filepath.Join(dir, "store")
	s, err := New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyWrittenPrimary(s, tgt, staging, fname); err != nil {
		t.Fatalf("intact remote should verify: %v", err)
	}

	// Corrupt the remote object: overwrite with tampered bytes.
	cli, err := s3ClientFor(tgt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.PutObject(ctx, tgt.Bucket, s3ObjectKey(tgt, fname),
		strings.NewReader("TAMPERED: checksum will never match"), -1, minio.PutObjectOptions{}); err != nil {
		t.Fatalf("corrupt overwrite: %v", err)
	}

	// verifyWrittenPrimary must now fail AND purge the poisoned object.
	err = verifyWrittenPrimary(s, tgt, staging, fname)
	if err == nil {
		t.Fatal("expected corrupted remote to fail verification")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error should mention the mismatch: %v", err)
	}
	left, err := s3List(tgt)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("poisoned object was not purged; %d object(s) remain: %+v", len(left), left)
	}
}
