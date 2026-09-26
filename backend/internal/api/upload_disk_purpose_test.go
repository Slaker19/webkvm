package api

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// uploadDiskBody builds a multipart body targeting pool with a single
// disk file, matching what the frontend sends to POST /storage/upload-disk.
func uploadDiskBody(t *testing.T, pool, filename string) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("pool", pool); err != nil {
		t.Fatalf("write pool field: %v", err)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := fw.Write([]byte("fake-qcow2-content")); err != nil {
		t.Fatalf("write file content: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

// TestUploadDisk_RejectsISOPool is the core Task 2 guard: a disk image
// must never land in a pool tagged purpose=iso.
func TestUploadDisk_RejectsISOPool(t *testing.T) {
	h := newPoolTestHandler(
		models.StoragePool{Name: "ISOS", Purpose: compute.PoolPurposeISO},
		models.StoragePool{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
	)

	body, contentType := uploadDiskBody(t, "ISOS", "test.qcow2")
	req := httptest.NewRequest(http.MethodPost, "/api/storage/upload-disk", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", "admin")
	rr := httptest.NewRecorder()

	h.UploadDisk(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

// TestUploadDisk_AcceptsDiskPool proves the guard didn't break the happy
// path: a disk pool still accepts the upload and writes the file.
func TestUploadDisk_AcceptsDiskPool(t *testing.T) {
	tmpDir := t.TempDir()
	h := newPoolTestHandlerWithPath(tmpDir,
		models.StoragePool{Name: "webkvm-disks", Purpose: compute.PoolPurposeDisk},
	)

	body, contentType := uploadDiskBody(t, "webkvm-disks", "test.qcow2")
	req := httptest.NewRequest(http.MethodPost, "/api/storage/upload-disk", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", "admin")
	rr := httptest.NewRecorder()

	h.UploadDisk(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body = %s", rr.Code, rr.Body.String())
	}
}
