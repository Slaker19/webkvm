package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

// coverTestBackend keeps per-VM metadata in memory so the cover handlers
// can be exercised end to end against a real covers directory.
type coverTestBackend struct {
	compute.Backend
	vms map[string]*models.VM
}

func (b *coverTestBackend) ListDomains() ([]models.VM, error) {
	out := make([]models.VM, 0, len(b.vms))
	for _, vm := range b.vms {
		out = append(out, *vm)
	}
	return out, nil
}
func (b *coverTestBackend) GetDomain(id string) (models.VM, error) { return *b.vms[id], nil }
func (b *coverTestBackend) DeleteDomain(id string) error {
	delete(b.vms, id)
	return nil
}
func (b *coverTestBackend) GetVMMeta(id string) (models.VMMeta, error) {
	return models.VMMeta{Cover: b.vms[id].Cover}, nil
}
func (b *coverTestBackend) UpdateVMMeta(id string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	if upd.Cover != nil {
		b.vms[id].Cover = *upd.Cover
	}
	return models.VMMeta{Cover: b.vms[id].Cover}, nil
}

const (
	coverVM1 = "11111111-1111-1111-1111-111111111111"
	coverVM2 = "22222222-2222-2222-2222-222222222222"
)

func setupCoverTest(t *testing.T) (*Handler, *coverTestBackend, string) {
	t.Helper()
	cfg := &config.Config{DataDir: t.TempDir()}
	be := &coverTestBackend{vms: map[string]*models.VM{
		coverVM1: {ID: coverVM1, Name: "one"},
		coverVM2: {ID: coverVM2, Name: "two"},
	}}
	h := &Handler{cfg: cfg, compute: be}
	h.EnsureMediaDirs()
	if err := os.MkdirAll(cfg.CoversDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	return h, be, cfg.CoversDir()
}

func coverReq(method, target, id string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", string(models.RoleAdmin))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func uploadCoverReq(t *testing.T, id string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "c.bin")
	if err != nil {
		t.Fatal(err)
	}
	part.Write(content)
	mw.Close()
	req := coverReq(http.MethodPost, "/api/vms/"+id+"/cover", id, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

var (
	pngBytes  = []byte("\x89PNG\r\n\x1a\n0000000000")
	jpegBytes = []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0}
)

func writeCover(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("img"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertGone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("%s should have been removed (stat err = %v)", filepath.Base(p), err)
	}
}

func assertPresent(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Stat(p); err != nil {
		t.Errorf("%s should still exist: %v", filepath.Base(p), err)
	}
}

// Uploading a PNG then a JPEG used to leave <id>.png behind, and the
// response leaked the absolute on-disk path.
func TestUploadCover_ReplacesOtherFormatAndHidesPath(t *testing.T) {
	h, be, dir := setupCoverTest(t)

	rr := httptest.NewRecorder()
	h.UploadCover(rr, uploadCoverReq(t, coverVM1, pngBytes))
	if rr.Code != http.StatusOK {
		t.Fatalf("png upload: %d %s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	h.UploadCover(rr, uploadCoverReq(t, coverVM1, jpegBytes))
	if rr.Code != http.StatusOK {
		t.Fatalf("jpeg upload: %d %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["path"]; ok {
		t.Errorf("response must not expose the server path: %v", resp)
	}
	if strings.Contains(rr.Body.String(), dir) {
		t.Errorf("response leaks covers dir: %s", rr.Body.String())
	}
	if u, _ := resp["url"].(string); !strings.HasPrefix(u, "/api/covers/"+coverVM1+".jpg?v=") {
		t.Errorf("url = %q", u)
	}
	assertGone(t, filepath.Join(dir, coverVM1+".png"))
	assertPresent(t, filepath.Join(dir, coverVM1+".jpg"))
	if !strings.HasPrefix(be.vms[coverVM1].Cover, "/api/covers/"+coverVM1+".jpg") {
		t.Errorf("meta cover = %q", be.vms[coverVM1].Cover)
	}
}

func TestUploadCover_OversizedReturns413(t *testing.T) {
	h, _, _ := setupCoverTest(t)
	big := append(append([]byte{}, pngBytes...), make([]byte, 9<<20)...)
	rr := httptest.NewRecorder()
	h.UploadCover(rr, uploadCoverReq(t, coverVM1, big))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d %s", rr.Code, rr.Body.String())
	}
}

// Switching to a media-library cover must drop the old per-VM upload.
func TestApplyMediaUsage_VMCoverRemovesUploadedFile(t *testing.T) {
	h, be, dir := setupCoverTest(t)
	old := writeCover(t, dir, coverVM1+".png")
	be.vms[coverVM1].Cover = "/api/covers/" + coverVM1 + ".png?v=1"
	if err := os.WriteFile(filepath.Join(h.cfg.MediaCustomDir(), "lib.png"), pngBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"media_id": "custom:lib.png", "usage": "vm_cover", "vm_id": coverVM1})
	rr := httptest.NewRecorder()
	h.ApplyMediaUsage(rr, coverReq(http.MethodPost, "/api/media/apply-usage", "", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("apply-usage: %d %s", rr.Code, rr.Body.String())
	}
	assertGone(t, old)
}

func TestUpdateVMMeta_CoverChangeRemovesUploadedFile(t *testing.T) {
	h, be, dir := setupCoverTest(t)
	old := writeCover(t, dir, coverVM1+".webp")
	be.vms[coverVM1].Cover = "/api/covers/" + coverVM1 + ".webp?v=1"

	body := `{"cover":"/api/media/custom:lib.png/raw"}`
	rr := httptest.NewRecorder()
	h.UpdateVMMeta(rr, coverReq(http.MethodPut, "/api/vms/"+coverVM1+"/meta", coverVM1, strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("meta: %d %s", rr.Code, rr.Body.String())
	}
	assertGone(t, old)
}

// A clone inherits its source's cover URL; the source's file must
// survive while another VM still references it.
func TestDeleteCover_KeepsFileReferencedByClone(t *testing.T) {
	h, be, dir := setupCoverTest(t)
	f := writeCover(t, dir, coverVM1+".png")
	be.vms[coverVM1].Cover = "/api/covers/" + coverVM1 + ".png?v=1"
	be.vms[coverVM2].Cover = "/api/covers/" + coverVM1 + ".png?v=1"

	// The clone dropping its (inherited) cover must not touch the file.
	rr := httptest.NewRecorder()
	h.DeleteCover(rr, coverReq(http.MethodDelete, "/", coverVM2, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete clone cover: %d", rr.Code)
	}
	assertPresent(t, f)

	be.vms[coverVM2].Cover = "/api/covers/" + coverVM1 + ".png?v=1"
	rr = httptest.NewRecorder()
	h.DeleteCover(rr, coverReq(http.MethodDelete, "/", coverVM1, nil))
	assertPresent(t, f) // still used by the clone

	be.vms[coverVM2].Cover = ""
	h.pruneVMCoverFiles(coverVM1, "")
	assertGone(t, f)
}

func TestDeleteVM_RemovesAllCoverFiles(t *testing.T) {
	h, be, dir := setupCoverTest(t)
	png := writeCover(t, dir, coverVM1+".png")
	jpg := writeCover(t, dir, coverVM1+".jpg")
	other := writeCover(t, dir, coverVM2+".png")
	prefix := writeCover(t, dir, coverVM1+"0.png") // different id sharing a prefix
	be.vms[coverVM1].Cover = "/api/covers/" + coverVM1 + ".jpg?v=1"

	rr := httptest.NewRecorder()
	h.DeleteVM(rr, coverReq(http.MethodDelete, "/api/vms/"+coverVM1, coverVM1, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete vm: %d %s", rr.Code, rr.Body.String())
	}
	assertGone(t, png)
	assertGone(t, jpg)
	assertPresent(t, other)
	assertPresent(t, prefix)
}

func TestPruneVMCoverFiles_RejectsTraversalIDs(t *testing.T) {
	h, _, dir := setupCoverTest(t)
	outside := filepath.Join(filepath.Dir(dir), "secret.png")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", ".", "..", "../secret", "a/b", `..\secret`} {
		h.pruneVMCoverFiles(id, "")
	}
	assertPresent(t, outside)
}
