package backupstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateS3Target_ValidMinIO: an S3 target with a compatible endpoint
// (MinIO/R2/B2) needs no Region, only Bucket + credentials.
func TestCreateS3Target_ValidMinIO(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := s.CreateTargetOpts("minio", "", TargetS3, "all", nil, TargetOptions{
		Bucket:    "webkvm-backups",
		Endpoint:  "http://minio.local:9000",
		AccessKey: "AKIAEXAMPLE",
		SecretKey: "super-secret-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if tgt.Type != TargetS3 || tgt.Bucket != "webkvm-backups" {
		t.Fatalf("bad s3 target: %+v", tgt)
	}
	if tgt.Endpoint != "http://minio.local:9000" {
		t.Errorf("endpoint = %q", tgt.Endpoint)
	}
	// El secreto se guardó aparte.
	sec, ok := s.secrets[tgt.ID]
	if !ok || sec.AccessKey != "AKIAEXAMPLE" || sec.SecretKey != "super-secret-key" {
		t.Fatalf("s3 secret not persisted correctly: %+v", sec)
	}
}

// TestCreateS3Target_NativeAWSRequiresRegion: sin endpoint custom, la
// region es obligatoria.
func TestCreateS3Target_NativeAWSRequiresRegion(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, err := s.CreateTargetOpts("aws", "", TargetS3, "all", nil, TargetOptions{
		Bucket: "webkvm", AccessKey: "AK", SecretKey: "SK",
	})
	if err == nil || !strings.Contains(err.Error(), "region is required") {
		t.Fatalf("expected region-required error, got %v", err)
	}
}

// TestCreateS3Target_MissingBucket: bucket is required.
func TestCreateS3Target_MissingBucket(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, err := s.CreateTargetOpts("nobucket", "", TargetS3, "all", nil, TargetOptions{
		Endpoint: "http://minio:9000", AccessKey: "AK", SecretKey: "SK",
	})
	if err == nil || !strings.Contains(err.Error(), "bucket is required") {
		t.Fatalf("expected bucket-required error, got %v", err)
	}
}

// TestCreateS3Target_MissingCredentials: sin AccessKey/SecretKey se
// rechaza.
func TestCreateS3Target_MissingCredentials(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	_, err := s.CreateTargetOpts("nokey", "", TargetS3, "all", nil, TargetOptions{
		Bucket: "webkvm", Region: "eu-west-1",
	})
	if err == nil || !strings.Contains(err.Error(), "access key and secret key are required") {
		t.Fatalf("expected credentials error, got %v", err)
	}
}

// TestSecretsNeverSerialized: un target S3, al serializarse (API, JSON),
// NO debe exponer AccessKey/SecretKey. El struct Target usa json:"-" para
// Secret y el SecretKey vive solo en el fichero de secrets.
func TestSecretsNeverSerialized(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, err := s.CreateTargetOpts("secure", "", TargetS3, "all", nil, TargetOptions{
		Bucket: "webkvm", Region: "us-east-1", AccessKey: "AKIA-SUPERSECRET", SecretKey: "sk-live-very-secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1) The target serialized to JSON (what the API returns) carries no secrets.
	b, err := json.Marshal(tgt)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(b)
	for _, secret := range []string{"AKIA-SUPERSECRET", "sk-live-very-secret"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("secret %q leaked into serialized target", secret)
		}
	}

	// 2) The targets.json file also carries no secrets.
	targetsRaw, err := os.ReadFile(filepath.Join(dir, "backup", "targets.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"AKIA-SUPERSECRET", "sk-live-very-secret"} {
		if strings.Contains(string(targetsRaw), secret) {
			t.Fatalf("secret %q leaked into targets.json", secret)
		}
	}

	// 3) The secrets ARE in the separate secrets file (0600).
	secRaw, err := os.ReadFile(filepath.Join(dir, "backup", "sftp-secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(secRaw), "sk-live-very-secret") {
		t.Fatal("secret not found in secrets file")
	}
}

// TestS3TargetPersistsAcrossReopen: el target S3 sobrevive a recargar el
// store (retrocompat con local/nfs/smb/sftp intacta).
func TestS3TargetPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	if _, err := s.CreateTargetOpts("s3t", "", TargetS3, "all", nil, TargetOptions{
		Bucket: "webkvm", Region: "us-east-1", AccessKey: "AK", SecretKey: "SK",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTarget("loc1", filepath.Join(dir, "loc-mount"), TargetLocal, "all", nil); err != nil {
		t.Fatal(err)
	}

	s2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Find the s3 target by name (its ID is generated).
	var s3id string
	for _, tg := range s2.ListTargets() {
		if tg.Name == "s3t" {
			s3id = tg.ID
		}
	}
	got, ok := s2.GetTarget(s3id)
	if !ok {
		t.Fatal("s3 target lost on reopen")
	}
	if got.Type != TargetS3 || got.Bucket != "webkvm" || got.Region != "us-east-1" {
		t.Fatalf("s3 target not preserved: %+v", got)
	}
	// Retrocompat: el target local sigue ahí (buscar por nombre).
	var locOK bool
	for _, tg := range s2.ListTargets() {
		if tg.Name == "loc1" {
			locOK = true
		}
	}
	if !locOK {
		t.Fatal("existing local target lost on reopen")
	}
	// Los secrets S3 se recargan.
	sec, ok := s2.secrets[got.ID]
	if !ok || sec.SecretKey != "SK" {
		t.Fatalf("s3 secret lost on reopen: %+v", sec)
	}
}

// TestUpdateS3Target: se pueden actualizar los campos S3 y las
// credenciales por separado.
func TestUpdateS3Target(t *testing.T) {
	dir := t.TempDir()
	s, _ := New(dir)
	tgt, err := s.CreateTargetOpts("s3u", "", TargetS3, "all", nil, TargetOptions{
		Bucket: "b1", Region: "us-east-1", AccessKey: "AK1", SecretKey: "SK1",
	})
	if err != nil {
		t.Fatal(err)
	}
	newBucket := "b2"
	upd, err := s.UpdateTarget(tgt.ID, nil, nil, nil, nil, nil, nil, &TargetOptions{
		Bucket: newBucket,
	})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Bucket != "b2" {
		t.Errorf("bucket = %q, want b2", upd.Bucket)
	}
	// Credentials updated.
	_, err = s.UpdateTarget(tgt.ID, nil, nil, nil, nil, nil, nil, &TargetOptions{
		AccessKey: "AK2", SecretKey: "SK2",
	})
	if err != nil {
		t.Fatal(err)
	}
	sec := s.secrets[tgt.ID]
	if sec.AccessKey != "AK2" || sec.SecretKey != "SK2" {
		t.Fatalf("secret not updated: %+v", sec)
	}
}
