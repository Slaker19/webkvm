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
	// Same name again must not overwrite: 409.
	if code := postUploadDisk(h, "webkvm-disks", "a.qcow2", realQcow2(t)); code != http.StatusConflict {
		t.Fatalf("second upload: got %d, want 409", code)
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
