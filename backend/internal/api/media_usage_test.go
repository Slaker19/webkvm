package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/configstore"
	"webkvm/internal/models"
	"webkvm/internal/user"

	"github.com/go-chi/chi/v5"
)

// setupMediaUsageHandler builds a handler wired to a real user store and a
// real settings store. The existing media tests leave both nil, which is
// exactly why the "applied but not saved" bug could never be caught there:
// with no store there is nothing to assert against.
func setupMediaUsageHandler(t *testing.T) (*Handler, *chi.Mux) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")

	cfg := &config.Config{DataDir: dir}

	authMgr := auth.NewManager("test-jwt-secret-for-media-usage", nil)
	t.Cleanup(func() { authMgr.Close() })

	auditLogger, err := audit.New(cfg.AuditLogFile())
	if err != nil {
		t.Fatalf("audit logger: %v", err)
	}

	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatalf("user store: %v", err)
	}
	settings, err := configstore.New(dir, configstore.DefaultSchema())
	if err != nil {
		t.Fatalf("settings store: %v", err)
	}

	h := &Handler{
		cfg:       cfg,
		auth:      authMgr,
		audit:     auditLogger,
		userStore: us,
		settings:  settings,
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

	return h, r
}

func uploadTestImage(t *testing.T, r *chi.Mux, token, name string) string {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15c4\x00\x00\x00\nIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4\x00\x00\x00\x00IEND\xaeB`\x82"))
	w.Close()

	req := httptest.NewRequest("POST", "/api/media/upload", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: status %d, body %s", rec.Code, rec.Body.String())
	}
	var item models.MediaItem
	if err := json.Unmarshal(rec.Body.Bytes(), &item); err != nil {
		t.Fatalf("unmarshal item: %v", err)
	}
	return item.ID
}

func applyUsage(t *testing.T, r *chi.Mux, token string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/api/media/apply-usage", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// The original bug: apply-usage answered {"status":"applied"} for avatar
// while writing nothing anywhere. A 200 is not evidence; the only proof is
// reading the value back out of the store.
func TestApplyMediaUsage_AvatarIsPersisted(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)

	id := uploadTestImage(t, r, token, "face.png")
	rec := applyUsage(t, r, token, map[string]any{"media_id": id, "usage": "avatar"})
	if rec.Code != http.StatusOK {
		t.Fatalf("apply avatar: status %d, body %s", rec.Code, rec.Body.String())
	}

	u, err := h.userStore.Get("admin")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	want := models.MediaRawURL(id)
	if u.Avatar != want {
		t.Fatalf("avatar not persisted: got %q, want %q", u.Avatar, want)
	}
}

// Same bug for the two branding usages.
func TestApplyMediaUsage_BrandingIsPersisted(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)

	id := uploadTestImage(t, r, token, "brand.png")
	want := models.MediaRawURL(id)

	for _, usage := range []string{"logo", "favicon"} {
		rec := applyUsage(t, r, token, map[string]any{"media_id": id, "usage": usage})
		if rec.Code != http.StatusOK {
			t.Fatalf("apply %s: status %d, body %s", usage, rec.Code, rec.Body.String())
		}
		if got := h.settings.GetString("branding." + usage); got != want {
			t.Errorf("branding.%s not persisted: got %q, want %q", usage, got, want)
		}
	}
}

// Branding is global. A non-admin holding the "media" permission may manage
// their own avatar, but must not restyle the product for everyone else.
func TestApplyMediaUsage_BrandingIsAdminOnly(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	adminToken, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)
	id := uploadTestImage(t, r, adminToken, "brand.png")

	yes := true
	if _, err := h.userStore.Create(models.CreateUserRequest{
		Username: "op", Password: "Str0ng-Pass#2026", Role: models.RoleOperator,
		Permissions: &models.UserPermissions{CanMedia: &yes},
	}); err != nil {
		t.Fatalf("create operator: %v", err)
	}
	opToken, _, _ := h.auth.GenerateTokenWithMustChange("op", models.RoleOperator, false, 0)

	rec := applyUsage(t, r, opToken, map[string]any{"media_id": id, "usage": "logo"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("operator setting the logo: status %d, want 403. Body: %s", rec.Code, rec.Body.String())
	}
	if got := h.settings.GetString("branding.logo"); got != "" {
		t.Fatalf("logo was written by a non-admin: %q", got)
	}
}

// The avatar is taken from the session, never from the request body, so a
// caller cannot set somebody else's picture.
func TestApplyMediaUsage_AvatarAppliesToCallerOnly(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	adminToken, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)
	id := uploadTestImage(t, r, adminToken, "face.png")

	yes := true
	if _, err := h.userStore.Create(models.CreateUserRequest{
		Username: "victim", Password: "Str0ng-Pass#2026", Role: models.RoleOperator,
		Permissions: &models.UserPermissions{CanMedia: &yes},
	}); err != nil {
		t.Fatalf("create victim: %v", err)
	}
	if _, err := h.userStore.Create(models.CreateUserRequest{
		Username: "attacker", Password: "Str0ng-Pass#2026", Role: models.RoleOperator,
		Permissions: &models.UserPermissions{CanMedia: &yes},
	}); err != nil {
		t.Fatalf("create attacker: %v", err)
	}
	attackerToken, _, _ := h.auth.GenerateTokenWithMustChange("attacker", models.RoleOperator, false, 0)

	// "username" is not part of the contract; if it ever were honoured
	// this would silently rewrite another account.
	rec := applyUsage(t, r, attackerToken, map[string]any{
		"media_id": id, "usage": "avatar", "username": "victim",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("apply avatar: status %d, body %s", rec.Code, rec.Body.String())
	}

	victim, err := h.userStore.Get("victim")
	if err != nil {
		t.Fatalf("get victim: %v", err)
	}
	if victim.Avatar != "" {
		t.Fatalf("attacker set the victim's avatar: %q", victim.Avatar)
	}
	attacker, err := h.userStore.Get("attacker")
	if err != nil {
		t.Fatalf("get attacker: %v", err)
	}
	if attacker.Avatar == "" {
		t.Fatal("caller's own avatar was not set")
	}
}

// A reference to a file that does not exist can only ever be diagnosed
// later, as a broken image with no trace of where it came from. Refuse it
// at write time.
func TestApplyMediaUsage_RejectsMissingMedia(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)

	for _, id := range []string{
		"custom:does-not-exist.png",
		"custom:../../../etc/passwd",
		"system:../custom/x.png",
		"bogus:x.png",
		"no-category.png",
	} {
		rec := applyUsage(t, r, token, map[string]any{"media_id": id, "usage": "avatar"})
		if rec.Code == http.StatusOK {
			t.Errorf("apply-usage accepted a bad media_id %q (status 200)", id)
		}
	}
	u, _ := h.userStore.Get("admin")
	if u.Avatar != "" {
		t.Fatalf("a rejected id still reached the store: %q", u.Avatar)
	}
}

// Deleting an image that something still points at leaves a broken image
// with no explanation. The caller must confirm.
func TestDeleteMedia_InUseRequiresForce(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)

	id := uploadTestImage(t, r, token, "face.png")
	if rec := applyUsage(t, r, token, map[string]any{"media_id": id, "usage": "avatar"}); rec.Code != http.StatusOK {
		t.Fatalf("apply avatar: %d", rec.Code)
	}

	del := func(q string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", "/api/media/"+id+q, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	rec := del("")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete of an in-use image: status %d, want 409. Body: %s", rec.Code, rec.Body.String())
	}
	var conflict struct {
		Usages []mediaUsageRef `json:"usages"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conflict); err != nil {
		t.Fatalf("unmarshal conflict: %v", err)
	}
	if len(conflict.Usages) != 1 || conflict.Usages[0].Kind != "avatar" || conflict.Usages[0].Name != "admin" {
		t.Fatalf("conflict must name who uses it, got %+v", conflict.Usages)
	}
	if !h.mediaExists(id) {
		t.Fatal("the file was deleted despite the 409")
	}

	if rec := del("?force=true"); rec.Code != http.StatusOK {
		t.Fatalf("forced delete: status %d, body %s", rec.Code, rec.Body.String())
	}
	if h.mediaExists(id) {
		t.Fatal("forced delete did not remove the file")
	}
}

// An unused image must still delete without ceremony.
func TestDeleteMedia_UnusedDeletesCleanly(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)

	id := uploadTestImage(t, r, token, "orphan.png")
	req := httptest.NewRequest("DELETE", "/api/media/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("delete unused image: status %d, body %s", rec.Code, rec.Body.String())
	}
	if h.mediaExists(id) {
		t.Fatal("file still on disk after delete")
	}
}

// The frontend sends the id through encodeURIComponent, so the colon
// arrives as %3A. chi hands the parameter back still escaped; before the
// handler decoded it, every delete from the UI answered 404 "media not
// found" and the in-use 409 path could never be reached.
func TestDeleteMedia_PercentEncodedID(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)

	id := uploadTestImage(t, r, token, "face.png")
	if rec := applyUsage(t, r, token, map[string]any{"media_id": id, "usage": "avatar"}); rec.Code != http.StatusOK {
		t.Fatalf("apply avatar: %d", rec.Code)
	}
	encoded := url.PathEscape(id)
	encoded = strings.ReplaceAll(encoded, ":", "%3A")

	del := func(q string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", "/api/media/"+encoded+q, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	rec := del("")
	if rec.Code != http.StatusConflict {
		t.Fatalf("encoded delete of an in-use image: status %d, want 409. Body: %s", rec.Code, rec.Body.String())
	}
	if rec := del("?force=true"); rec.Code != http.StatusOK {
		t.Fatalf("encoded forced delete: status %d, body %s", rec.Code, rec.Body.String())
	}
	if h.mediaExists(id) {
		t.Fatal("encoded forced delete did not remove the file")
	}
}

// Decoding must not open a traversal path: escapes that decode to a
// separator or a dot-segment, and malformed escapes, are refused before
// anything touches the filesystem. An encoded system id stays protected.
func TestDeleteMedia_RejectsBadEncodings(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)

	cases := map[string]int{
		"custom%3A..%2Fsecret.png": http.StatusBadRequest,
		"custom%3A%2E%2E":          http.StatusBadRequest,
		"..":                       http.StatusBadRequest,
		"custom%3Aa%5Cb.png":       http.StatusBadRequest,
		"system%3Alogo.png":        http.StatusForbidden,
	}
	for path, want := range cases {
		req := httptest.NewRequest("DELETE", "/api/media/"+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("DELETE %s: status %d, want %d. Body: %s", path, rec.Code, want, rec.Body.String())
		}
	}

	// A malformed escape cannot go through httptest.NewRequest (it
	// refuses to parse it), so set the raw path chi routes on directly.
	req := httptest.NewRequest("DELETE", "/api/media/x", nil)
	req.URL.Path = "/api/media/custom:bad%ZZ"
	req.URL.RawPath = "/api/media/custom%3Abad%ZZ"
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed escape: status %d, want 400. Body: %s", rec.Code, rec.Body.String())
	}
}

// The raw endpoint accepts both spellings too.
func TestGetMediaRaw_PercentEncodedID(t *testing.T) {
	h, r := setupMediaUsageHandler(t)
	token, _, _ := h.auth.GenerateTokenWithMustChange("admin", models.RoleAdmin, false, 0)

	id := uploadTestImage(t, r, token, "face.png")
	req := httptest.NewRequest("GET", "/api/media/"+strings.ReplaceAll(id, ":", "%3A")+"/raw", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("encoded raw: status %d", rec.Code)
	}
}

// Branding keys are rendered by the frontend as <img src>. Free-form URLs
// there would be a stored XSS vector, so the settings store itself must
// refuse them even when written through the generic settings API.
func TestSettings_BrandingRejectsNonMediaValues(t *testing.T) {
	h, _ := setupMediaUsageHandler(t)

	for _, bad := range []string{
		"javascript:alert(1)",
		"https://evil.example/pixel.png",
		"data:text/html;base64,PHNjcmlwdD4=",
		"/api/media/custom:../../etc/passwd/raw",
	} {
		_, failed, err := h.settings.SetMany(configstore.Set{"branding.logo": bad})
		if err == nil && len(failed) == 0 {
			t.Errorf("settings accepted branding.logo = %q", bad)
		}
		if got := h.settings.GetString("branding.logo"); got != "" {
			t.Fatalf("branding.logo was written with %q", got)
		}
	}
}

type coverListBackend struct {
	compute.Backend
	vms []models.VM
}

func (b *coverListBackend) ListDomains() ([]models.VM, error) { return b.vms, nil }

// Bug 13: every media-library reference ends in "/raw", so comparing
// covers by filepath.Base matched every library cover against every
// media item, and deleting any item returned 409 media_in_use.
func TestMediaUsage_VMCoverMatchesByMediaID(t *testing.T) {
	h := &Handler{compute: &coverListBackend{vms: []models.VM{
		{ID: "vm1", Name: "one", Cover: models.MediaRawURL("custom:a.png")},
		{ID: "vm2", Name: "two", Cover: models.MediaRawURL("system:b.png") + "?v=1"},
		{ID: "vm3", Name: "three", Cover: "/api/covers/vm3.png?v=1"},
	}}}

	if got := h.mediaUsage("custom:unrelated.png"); len(got) != 0 {
		t.Fatalf("unrelated media reported in use: %+v", got)
	}
	got := h.mediaUsage("custom:a.png")
	if len(got) != 1 || got[0].ID != "vm1" {
		t.Fatalf("custom:a.png usage = %+v, want only vm1", got)
	}
	got = h.mediaUsage("system:b.png")
	if len(got) != 1 || got[0].ID != "vm2" {
		t.Fatalf("system:b.png usage = %+v, want only vm2", got)
	}
}
