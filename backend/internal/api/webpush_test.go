package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/webpush"
)

func TestWebPushHandlersDisabled(t *testing.T) {
	SetWebPushManager(nil)
	defer SetWebPushManager(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/push/vapid-key", nil)
	(&Handler{}).GetWebPushKey(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
}

func TestWebPushSubscribeAndKey(t *testing.T) {
	m, err := webpush.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	SetWebPushManager(m)
	defer SetWebPushManager(nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/push/vapid-key", nil)
	(&Handler{}).GetWebPushKey(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), m.PublicKey()) {
		t.Fatalf("vapid key: %d %s", rec.Code, rec.Body.String())
	}

	body := `{"endpoint":"https://push.example.invalid/abc","keys":{"p256dh":"p","auth":"a"},"locale":"es"}`
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/push/subscribe", strings.NewReader(body))
	req.Header.Set("X-User", "admin")
	(&Handler{}).SubscribeWebPush(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body.String())
	}
}
