package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/config"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

func setupMediaTestHandler(t *testing.T) (*Handler, *chi.Mux, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir,
	}

	authMgr := auth.NewManager("test-jwt-secret-for-media", nil)
	t.Cleanup(func() { authMgr.Close() })

	auditLogger, err := audit.New(cfg.AuditLogFile())
	if err != nil {
		t.Fatalf("audit logger: %v", err)
	}

	h := &Handler{
		cfg:   cfg,
		auth:  authMgr,
		audit: auditLogger,
	}
	h.EnsureMediaDirs()

	r := chi.NewRouter()
	r.Get("/api/media/{id}/raw", h.GetMediaRaw)
	r.Group(func(r chi.Router) {
		r.Use(authMgr.Middleware)
		r.Route("/api/media", func(r chi.Router) {
			r.Get("/", h.ListMedia)
			r.Post("/upload", h.UploadMedia)
			r.Delete("/{id}", h.DeleteMedia)
			r.Post("/apply-usage", h.ApplyMediaUsage)
		})
	})

	return h, r, dir
}

func TestMedia_SystemAssetsProtected(t *testing.T) {
	h, r, _ := setupMediaTestHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", "admin", false, 0)

	// List media -> should contain default seeded system assets
	req := httptest.NewRequest("GET", "/api/media", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("list media status = %d, want %d", w.Code, http.StatusOK)
	}

	var resp struct {
		Items []models.MediaItem `json:"items"`
		Count int                `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal list media: %v", err)
	}

	if resp.Count == 0 {
		t.Fatalf("expected seeded system assets, got 0")
	}

	var systemItem *models.MediaItem
	for _, it := range resp.Items {
		if it.IsSystem {
			systemItem = &it
			break
		}
	}
	if systemItem == nil {
		t.Fatalf("expected at least one system media item")
	}

	// Try to delete system asset -> must return 403
	delReq := httptest.NewRequest("DELETE", "/api/media/"+systemItem.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delW := httptest.NewRecorder()
	r.ServeHTTP(delW, delReq)

	if delW.Code != http.StatusForbidden {
		t.Fatalf("delete system media status = %d, want 403 Forbidden. Body: %s", delW.Code, delW.Body.String())
	}
}

func TestMedia_UploadAndServeAndCustomDelete(t *testing.T) {
	h, r, dir := setupMediaTestHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", "admin", false, 0)

	// Upload custom image
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "test-avatar.png")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	pngContent := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15c4\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82")
	part.Write(pngContent)
	writer.Close()

	upReq := httptest.NewRequest("POST", "/api/media/upload", &body)
	upReq.Header.Set("Content-Type", writer.FormDataContentType())
	upReq.Header.Set("Authorization", "Bearer "+token)
	upW := httptest.NewRecorder()
	r.ServeHTTP(upW, upReq)

	if upW.Code != http.StatusCreated {
		t.Fatalf("upload media status = %d, want %d. Body: %s", upW.Code, http.StatusCreated, upW.Body.String())
	}

	var item models.MediaItem
	if err := json.Unmarshal(upW.Body.Bytes(), &item); err != nil {
		t.Fatalf("unmarshal created item: %v", err)
	}

	if item.IsSystem {
		t.Errorf("uploaded item should not be marked is_system")
	}
	if !strings.HasPrefix(item.ID, "custom:") {
		t.Errorf("uploaded item id should have custom: prefix, got %s", item.ID)
	}

	// Serve raw image (no auth required)
	rawReq := httptest.NewRequest("GET", "/api/media/"+item.ID+"/raw", nil)
	rawW := httptest.NewRecorder()
	r.ServeHTTP(rawW, rawReq)

	if rawW.Code != http.StatusOK {
		t.Fatalf("get raw media status = %d, want 200", rawW.Code)
	}
	if rawW.Header().Get("Content-Type") != "image/png" {
		t.Errorf("content-type = %q, want image/png", rawW.Header().Get("Content-Type"))
	}

	// Delete custom image
	delReq := httptest.NewRequest("DELETE", "/api/media/"+item.ID, nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delW := httptest.NewRecorder()
	r.ServeHTTP(delW, delReq)

	if delW.Code != http.StatusOK {
		t.Fatalf("delete custom media status = %d, want 200. Body: %s", delW.Code, delW.Body.String())
	}

	// Verify file is gone from disk
	customDir := filepath.Join(dir, "media", "custom")
	entries, _ := os.ReadDir(customDir)
	if len(entries) != 0 {
		t.Errorf("expected custom dir to be empty after delete, got %d files", len(entries))
	}
}
