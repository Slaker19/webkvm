package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

func postUploadDisk(h *Handler, pool, name, content string) int {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("pool", pool)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write([]byte(content))
	mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/storage/upload-disk", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", "admin")
	rr := httptest.NewRecorder()
	h.UploadDisk(rr, req)
	return rr.Code
}

func TestUploadDisk_ConflictAndMaliciousImage(t *testing.T) {
	needQemuImg(t)
	dir := t.TempDir()
	h := newPoolTestHandlerWithPath(dir, models.StoragePool{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk})
	if code := postUploadDisk(h, "webkvm-disks", "a.qcow2", realQcow2(t)); code != http.StatusCreated {
		t.Fatalf("first upload: %d", code)
	}
	// Re-upload of a ghost file (on disk, unknown to libvirt) replaces it
	// instead of failing with a permanent "file exists".
	if code := postUploadDisk(h, "webkvm-disks", "a.qcow2", realQcow2(t)); code != http.StatusCreated {
		t.Fatalf("ghost re-upload: got %d, want 201", code)
	}
	// qcow2 with a backing file: 400 and nothing left behind.
	host := filepath.Join(t.TempDir(), "host")
	os.WriteFile(host, make([]byte, 4096), 0o600)
	if code := postUploadDisk(h, "webkvm-disks", "evil.qcow2", backedQcow2(t, host)); code != http.StatusBadRequest {
		t.Fatalf("malicious upload: got %d, want 400", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.qcow2")); err == nil {
		t.Fatal("malicious image left in the pool")
	}
}

// A volume libvirt knows about must never be overwritten in place: it can
// be a VM disk or a base image every linked clone depends on. The storage
// UI has an explicit delete for those.
func TestUploadDisk_RefusesOverwriteOfRegisteredVolume(t *testing.T) {
	needQemuImg(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.qcow2"), []byte("old"), 0o644)
	h := &Handler{compute: &fakePoolBackend{
		pools:    []models.StoragePool{{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk}},
		poolPath: dir,
		volumes:  map[string]bool{"a.qcow2": true},
	}}
	if code := postUploadDisk(h, "webkvm-disks", "a.qcow2", realQcow2(t)); code != http.StatusConflict {
		t.Fatalf("registered overwrite: got %d, want 409", code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.qcow2")); string(b) != "old" {
		t.Fatal("registered volume was modified")
	}
}

// A ghost file that some domain still references (volume entry gone but the
// XML points at it) is refused too — deleting it would break that VM.
func TestUploadDisk_RefusesOverwriteOfAttachedGhost(t *testing.T) {
	needQemuImg(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.qcow2"), []byte("old"), 0o644)
	h := &Handler{compute: &fakePoolBackend{
		pools:       []models.StoragePool{{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk}},
		poolPath:    dir,
		attachments: map[string][]models.VolumeAttachment{"webkvm-disks/a.qcow2": {{VMName: "vm1"}}},
	}}
	if code := postUploadDisk(h, "webkvm-disks", "a.qcow2", realQcow2(t)); code != http.StatusConflict {
		t.Fatalf("attached ghost overwrite: got %d, want 409", code)
	}
}
