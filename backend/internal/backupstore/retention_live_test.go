package backupstore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRetention_AgainstMinIO is the V13-BCK-03 live gate: upload three
// runs to a real S3-compatible store, then verify ApplyRetention prunes
// exactly the old runs (KeepLast=1 keeps only the newest by mtime) and
// the bucket ends clean.
func TestRetention_AgainstMinIO(t *testing.T) {
	endpoint := os.Getenv("MINIO_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("MINIO_TEST_ENDPOINT not set")
	}
	access := os.Getenv("MINIO_TEST_ACCESS")
	secret := os.Getenv("MINIO_TEST_SECRET")

	bucket := "webkvm-retention-test"
	prefix := "gate-" + newID("r")
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

	// Three runs with DIFFERENT run suffixes and mtimes: old1 < old2 < new.
	runs := []struct {
		file string
		at   time.Time
	}{
		{"webkvm-h-20260101T120000.000000000Z-aaa001-config.tar.zst", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)},
		{"webkvm-h-20260102T120000.000000000Z-aaa002-config.tar.zst", time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)},
		{"webkvm-h-20260103T120000.000000000Z-aaa003-config.tar.zst", time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)},
	}
	staging := t.TempDir()
	for _, r := range runs {
		local := filepath.Join(staging, r.file)
		if err := os.WriteFile(local, []byte("run-data"), 0o600); err != nil {
			t.Fatal(err)
		}
		// mtime controls the retention ordering on the object store.
		_ = os.Chtimes(local, r.at, r.at)
		if _, err := s3UploadRun(ctx, tgt, staging, []JobFile{{Filename: r.file, Size: 8}}); err != nil {
			t.Fatal(err)
		}
	}

	// Apply retention: KeepLast=1 -> only the newest run survives.
	tgt.Retention = RetentionPolicy{KeepLast: 1}
	removed, err := ApplyRetention(nil, tgt)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 {
		t.Errorf("removed %d runs, want 2", removed)
	}
	left, err := s3List(tgt)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Fatalf("bucket has %d objects after retention, want 1: %+v", len(left), left)
	}
	if left[0].Filename != runs[2].file {
		t.Errorf("kept %q, want the newest %q", left[0].Filename, runs[2].file)
	}
}
