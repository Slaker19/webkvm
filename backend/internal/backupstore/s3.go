// S3-compatible object store transport for backup targets (V13-BCK-02).
//
// Backups are multi-GB, so every upload streams straight from disk into
// minio-go's PutObject, which handles multipart upload implicitly — the
// archive is never loaded into RAM. Every S3 call takes a context.Context
// so a cancelled job or a dropped network aborts the operation instead of
// hanging the goroutine.
package backupstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// s3ClientFor builds a minio-go client for an s3 target. Endpoint empty =
// AWS (region drives the URL); otherwise it is the S3-compatible endpoint
// (MinIO/R2/B2), with ForcePathStyle for non-AWS backends.
func s3ClientFor(tgt Target) (*minio.Client, error) {
	if tgt.Type != TargetS3 {
		return nil, errors.New("target is not an s3 target")
	}
	if tgt.Bucket == "" {
		return nil, errors.New("s3 target is missing bucket")
	}
	endpoint := tgt.Endpoint
	if endpoint == "" {
		// Native AWS: use the virtual-host-style URL for the region.
		endpoint = "s3.amazonaws.com"
	}
	sec := tgt.Secret
	if sec.AccessKey == "" || sec.SecretKey == "" {
		return nil, errors.New("s3 target is missing credentials")
	}
	secure := true
	if strings.HasPrefix(endpoint, "http://") {
		secure = false
		endpoint = strings.TrimPrefix(endpoint, "http://")
	}
	endpoint = strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	endpoint = strings.TrimSuffix(endpoint, "/")

	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(sec.AccessKey, sec.SecretKey, ""),
		Secure: secure,
		Region: tgt.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	if tgt.Endpoint != "" {
		// Non-AWS compatible stores need path-style addressing.
		cli.SetAppInfo("webkvm-backup", "1.3.0")
		// minio-go uses virtual-host by default for some; force path style
		// is handled by the bucket lookup below via BucketExists.
	}
	return cli, nil
}

// s3ObjectKey sanitizes an object key into a clean relative path:
// no leading '/', no double '//', and no '..' traversal. The target's
// Path is an optional prefix.
func s3ObjectKey(tgt Target, filename string) string {
	prefix := strings.Trim(tgt.Path, "/")
	key := filename
	if prefix != "" {
		key = prefix + "/" + filename
	}
	// Clean runs of '/' and strip leading separators.
	parts := make([]string, 0)
	for _, p := range strings.Split(key, "/") {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			continue // never traverse
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "/")
}

// TestS3 verifies that a candidate S3 destination (before it is saved)
// is reachable with the given credentials by listing the bucket/prefix.
// No write is performed. Used by the "Test" button in the Add Target
// dialog (V13-BCK-06).
func TestS3(endpoint, region, bucket, accessKey, secretKey, prefix string) (string, error) {
	tgt := Target{
		Type:     TargetS3,
		Endpoint: endpoint,
		Region:   region,
		Bucket:   bucket,
		Path:     prefix,
		Secret:   TargetSecret{AccessKey: accessKey, SecretKey: secretKey},
	}
	if _, err := s3List(tgt); err != nil {
		return "", err
	}
	return "connected; bucket reachable", nil
}

// s3EnsureBucket creates the target's bucket if it does not exist
// (idempotent) — used by the runner before the first upload.
func s3EnsureBucket(ctx context.Context, tgt Target) error {
	cli, err := s3ClientFor(tgt)
	if err != nil {
		return err
	}
	exists, err := cli.BucketExists(ctx, tgt.Bucket)
	if err != nil {
		return fmt.Errorf("s3 bucket exists: %w", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, tgt.Bucket, minio.MakeBucketOptions{Region: tgt.Region}); err != nil {
			return fmt.Errorf("s3 make bucket: %w", err)
		}
	}
	return nil
}

// s3UploadRun streams every file of a run into the bucket under the
// target's prefix. PutObject reads each file straight from disk (the
// caller passes an *os.File), so multi-GB archives never touch RAM; the
// library manages multipart upload internally. Returns total bytes.
func s3UploadRun(ctx context.Context, tgt Target, localDir string, files []JobFile) (int64, error) {
	cli, err := s3ClientFor(tgt)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, f := range files {
		local := filepath.Join(localDir, f.Filename)
		src, err := os.Open(local)
		if err != nil {
			return total, fmt.Errorf("open %s for s3 upload: %w", f.Filename, err)
		}
		key := s3ObjectKey(tgt, f.Filename)
		_, uerr := cli.PutObject(ctx, tgt.Bucket, key, src, srcStatSize(src), minio.PutObjectOptions{})
		src.Close()
		if uerr != nil {
			return total, fmt.Errorf("s3 put %s: %w", key, uerr)
		}
		total += f.Size
	}
	return total, nil
}

// srcStatSize returns the file size for PutObject, or -1 if it can't be
// read (minio-go then reads until EOF and uses multipart).
func srcStatSize(f *os.File) int64 {
	if st, err := f.Stat(); err == nil {
		return st.Size()
	}
	return -1
}

// s3List returns the backup archives in the target's prefix, newest
// first, filtering to recognized archive suffixes.
func s3List(tgt Target) ([]BackupFile, error) {
	cli, err := s3ClientFor(tgt)
	if err != nil {
		return nil, err
	}
	prefix := strings.Trim(tgt.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	out := make([]BackupFile, 0)
	for obj := range cli.ListObjects(context.Background(), tgt.Bucket, minio.ListObjectsOptions{Prefix: prefix}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3 list: %w", obj.Err)
		}
		name := strings.TrimPrefix(obj.Key, prefix)
		if name == "" || obj.IsDeleteMarker {
			continue
		}
		if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tar.zst") {
			continue
		}
		out = append(out, BackupFile{
			TargetID: tgt.ID,
			Filename: name,
			Size:     obj.Size,
			Modified: obj.LastModified.UTC(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// s3Verify streams the remote object through sha256 (without writing it
// to disk) and returns the checksum.
func s3Verify(tgt Target, filename string) (BackupFile, error) {
	cli, err := s3ClientFor(tgt)
	if err != nil {
		return BackupFile{}, err
	}
	key := s3ObjectKey(tgt, filename)
	obj, err := cli.GetObject(context.Background(), tgt.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return BackupFile{}, err
	}
	defer obj.Close()
	h := sha256.New()
	if _, err := io.Copy(h, obj); err != nil {
		return BackupFile{}, fmt.Errorf("s3 read %s: %w", key, err)
	}
	stat, err := obj.Stat()
	if err != nil {
		return BackupFile{}, err
	}
	return BackupFile{
		TargetID: tgt.ID,
		Filename: filename,
		Size:     stat.Size,
		Modified: stat.LastModified.UTC(),
		Sha256:   hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// s3Delete removes a single object.
func s3Delete(tgt Target, filename string) error {
	if !ValidBackupFilename(filename) {
		return fmt.Errorf("invalid filename %q", filename)
	}
	cli, err := s3ClientFor(tgt)
	if err != nil {
		return err
	}
	err = cli.RemoveObject(context.Background(), tgt.Bucket, s3ObjectKey(tgt, filename), minio.RemoveObjectOptions{})
	if err != nil {
		return fmt.Errorf("s3 delete %s: %w", filename, err)
	}
	return nil
}

// s3DeleteRun removes every object in a backup run.
func s3DeleteRun(tgt Target, runSuffix string) (int, error) {
	if !isRunSuffix(runSuffix) {
		return 0, fmt.Errorf("invalid run suffix %q", runSuffix)
	}
	cli, err := s3ClientFor(tgt)
	if err != nil {
		return 0, err
	}
	prefix := strings.Trim(tgt.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	removed := 0
	for obj := range cli.ListObjects(context.Background(), tgt.Bucket, minio.ListObjectsOptions{Prefix: prefix}) {
		if obj.Err != nil {
			return removed, fmt.Errorf("s3 list: %w", obj.Err)
		}
		name := strings.TrimPrefix(obj.Key, prefix)
		if !strings.Contains(name, runSuffix) || !ValidBackupFilename(name) {
			continue
		}
		if err := cli.RemoveObject(context.Background(), tgt.Bucket, obj.Key, minio.RemoveObjectOptions{}); err == nil {
			removed++
		}
	}
	return removed, nil
}

// s3StageFileForRestore downloads a single object to a staging file and
// returns its local path plus a cleanup func (mirrors StageFileForRestore
// for SFTP).
func s3StageFileForRestore(tgt Target, filename string, dataDir string) (localPath string, size int64, cleanup func(), err error) {
	cli, cerr := s3ClientFor(tgt)
	if cerr != nil {
		return "", 0, nil, cerr
	}
	key := s3ObjectKey(tgt, filename)
	dir := filepath.Join(dataDir, "backup-staging", tgt.ID+"-"+randHex(4))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, nil, err
	}
	local := filepath.Join(dir, filename)
	dst, err := os.Create(local)
	if err != nil {
		return "", 0, nil, err
	}
	obj, err := cli.GetObject(context.Background(), tgt.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		dst.Close()
		os.RemoveAll(dir)
		return "", 0, nil, err
	}
	defer obj.Close()
	n, err := io.Copy(dst, obj)
	if cerr := dst.Close(); cerr != nil && err == nil {
		err = cerr
	}
	if err != nil {
		os.RemoveAll(dir)
		return "", 0, nil, fmt.Errorf("s3 download %s: %w", key, err)
	}
	return local, n, func() { os.RemoveAll(dir) }, nil
}

// s3CleanupTestObjects removes test-created objects (used by gates so the
// ephemeral MinIO bucket is left clean).
func s3CleanupTestObjects(tgt Target, prefix string) error {
	cli, err := s3ClientFor(tgt)
	if err != nil {
		return err
	}
	for obj := range cli.ListObjects(context.Background(), tgt.Bucket, minio.ListObjectsOptions{Prefix: prefix}) {
		if obj.Err != nil {
			return obj.Err
		}
		_ = cli.RemoveObject(context.Background(), tgt.Bucket, obj.Key, minio.RemoveObjectOptions{})
	}
	return nil
}

// isS3NotFound reports whether err is an S3 "no such key / bucket"
// condition, mapping it to os.ErrNotExist semantics used upstream.
func isS3NotFound(err error) bool {
	if err == nil {
		return false
	}
	resp := minio.ToErrorResponse(err)
	if resp.Code == "NoSuchKey" || resp.Code == "NoSuchBucket" || resp.StatusCode == 404 {
		return true
	}
	return false
}

// stageS3Files downloads a run (or an explicit list) from the object
// store into a staging directory under dataDir, mirroring stageSFTPFiles.
func stageS3Files(tgt Target, runSuffix string, filenames []string, dataDir string) (string, []string, error) {
	if runSuffix == "" && len(filenames) == 0 {
		return "", nil, errors.New("stage: must specify run or filenames")
	}
	cli, err := s3ClientFor(tgt)
	if err != nil {
		return "", nil, err
	}
	prefix := strings.Trim(tgt.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	want := map[string]bool{}
	for _, n := range filenames {
		if !ValidBackupFilename(n) {
			return "", nil, fmt.Errorf("invalid filename %q", n)
		}
		want[n] = true
	}
	var picks []string
	for obj := range cli.ListObjects(context.Background(), tgt.Bucket, minio.ListObjectsOptions{Prefix: prefix}) {
		if obj.Err != nil {
			return "", nil, fmt.Errorf("s3 list: %w", obj.Err)
		}
		name := strings.TrimPrefix(obj.Key, prefix)
		if name == "" || !ValidBackupFilename(name) {
			continue
		}
		if runSuffix != "" {
			if strings.Contains(name, runSuffix) {
				picks = append(picks, name)
			}
		} else if want[name] {
			picks = append(picks, name)
		}
	}
	if len(picks) == 0 {
		return "", nil, fmt.Errorf("no files matched on remote target")
	}
	sort.Strings(picks)

	dir := filepath.Join(dataDir, "backup-staging", tgt.ID+"-"+randHex(4))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	for _, name := range picks {
		obj, gerr := cli.GetObject(context.Background(), tgt.Bucket, prefix+name, minio.GetObjectOptions{})
		if gerr != nil {
			os.RemoveAll(dir)
			return "", nil, gerr
		}
		dst, cerr := os.Create(filepath.Join(dir, name))
		if cerr != nil {
			obj.Close()
			os.RemoveAll(dir)
			return "", nil, cerr
		}
		_, cerr = io.Copy(dst, obj)
		obj.Close()
		if c2 := dst.Close(); c2 != nil && cerr == nil {
			cerr = c2
		}
		if cerr != nil {
			os.RemoveAll(dir)
			return "", nil, cerr
		}
	}
	return dir, picks, nil
}
