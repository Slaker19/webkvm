package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"webkvm/internal/auth"
	"webkvm/internal/config"
	"webkvm/internal/models"
	"webkvm/internal/user"

	"github.com/go-chi/chi/v5"
)

func setupTestWebAuthnHandler(t *testing.T) (*Handler, *user.Store) {
	t.Helper()
	dir := t.TempDir()
	us, err := user.NewStore(dir + "/users.json")
	if err != nil {
		t.Fatal(err)
	}

	// Create test user
	_, err = us.Create(models.CreateUserRequest{
		Username: "alice",
		Role:     models.RoleAdmin,
		Password: "StrongPassword123!",
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		WebAuthnRPID:      "localhost",
		WebAuthnRPOrigins: []string{"http://localhost:8080", "https://localhost:8081"},
	}

	wa := auth.NewWebAuthnManager(cfg)
	authMgr := auth.NewManager("test-secret-key-32-chars-long!!", nil)
	t.Cleanup(authMgr.Close)

	h := &Handler{
		userStore: us,
		webauthn:  wa,
		auth:      authMgr,
		cfg:       cfg,
	}

	return h, us
}

func reqWithUser(req *http.Request, username, role string) *http.Request {
	req.Header.Set("X-User", username)
	req.Header.Set("X-Role", role)
	return req
}

func TestWebAuthnRegisterBegin(t *testing.T) {
	h, _ := setupTestWebAuthnHandler(t)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/webauthn/register/begin", nil)
	req.Host = "localhost:8080"
	req = reqWithUser(req, "alice", models.RoleAdmin)
	rec := httptest.NewRecorder()

	h.WebAuthnRegisterBegin(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var res struct {
		PublicKey json.RawMessage `json:"publicKey"`
		SessionID string          `json:"session_id"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.SessionID == "" || len(res.PublicKey) == 0 {
		t.Fatalf("invalid register begin response: %+v", res)
	}
}

func TestWebAuthnLoginBegin(t *testing.T) {
	h, us := setupTestWebAuthnHandler(t)

	// 1. Discoverable login begin (no username)
	req1 := httptest.NewRequest(http.MethodPost, "/api/auth/webauthn/login/begin", bytes.NewReader([]byte("{}")))
	req1.Host = "localhost:8080"
	rec1 := httptest.NewRecorder()
	h.WebAuthnLoginBegin(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec1.Code, rec1.Body.String())
	}

	var res1 struct {
		PublicKey json.RawMessage `json:"publicKey"`
		SessionID string          `json:"session_id"`
	}
	if err := json.NewDecoder(rec1.Body).Decode(&res1); err != nil {
		t.Fatal(err)
	}
	if res1.SessionID == "" {
		t.Fatal("expected session_id in login begin")
	}

	// 2. Targeted login begin (alice with credential)
	_ = us.AddWebAuthnCredential("alice", models.WebAuthnCredential{
		ID:        "alice-passkey-1",
		Name:      "Alice Key",
		CreatedAt: time.Now().UTC(),
	})
	body, _ := json.Marshal(map[string]string{"username": "alice"})
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/webauthn/login/begin", bytes.NewReader(body))
	req2.Host = "localhost:8080"
	rec2 := httptest.NewRecorder()
	h.WebAuthnLoginBegin(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestWebAuthnCredentialsCRUD(t *testing.T) {
	h, us := setupTestWebAuthnHandler(t)

	// Inject a mock credential directly into Alice
	now := time.Now().UTC()
	err := us.AddWebAuthnCredential("alice", models.WebAuthnCredential{
		ID:        "test-cred-id-123",
		Name:      "Alice MacBook",
		CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 1. List credentials
	reqList := httptest.NewRequest(http.MethodGet, "/api/auth/webauthn/credentials", nil)
	reqList = reqWithUser(reqList, "alice", models.RoleAdmin)
	recList := httptest.NewRecorder()
	h.WebAuthnListCredentials(recList, reqList)

	if recList.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recList.Code, recList.Body.String())
	}

	var listRes struct {
		Credentials []models.WebAuthnCredential `json:"credentials"`
	}
	if err := json.NewDecoder(recList.Body).Decode(&listRes); err != nil {
		t.Fatal(err)
	}
	if len(listRes.Credentials) != 1 || listRes.Credentials[0].Name != "Alice MacBook" {
		t.Fatalf("unexpected credentials: %+v", listRes.Credentials)
	}

	// 2. Update credential name
	bodyUpd, _ := json.Marshal(map[string]string{"name": "Alice YubiKey 5C"})
	reqUpd := httptest.NewRequest(http.MethodPut, "/api/auth/webauthn/credentials/test-cred-id-123", bytes.NewReader(bodyUpd))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-cred-id-123")
	reqUpd = reqUpd.WithContext(context.WithValue(reqUpd.Context(), chi.RouteCtxKey, rctx))
	reqUpd = reqWithUser(reqUpd, "alice", models.RoleAdmin)
	recUpd := httptest.NewRecorder()

	h.WebAuthnUpdateCredential(recUpd, reqUpd)
	if recUpd.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recUpd.Code, recUpd.Body.String())
	}

	// 3. Delete credential
	reqDel := httptest.NewRequest(http.MethodDelete, "/api/auth/webauthn/credentials/test-cred-id-123", nil)
	reqDel = reqDel.WithContext(context.WithValue(reqDel.Context(), chi.RouteCtxKey, rctx))
	reqDel = reqWithUser(reqDel, "alice", models.RoleAdmin)
	recDel := httptest.NewRecorder()

	h.WebAuthnDeleteCredential(recDel, reqDel)
	if recDel.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recDel.Code, recDel.Body.String())
	}

	// Verify it was deleted
	creds, _ := us.GetWebAuthnCredentials("alice")
	if len(creds) != 0 {
		t.Fatalf("expected 0 credentials after deletion, got %d", len(creds))
	}
}
