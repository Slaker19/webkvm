package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"webkvm/internal/notify"
)

func TestNotifyAPI_ConfigAndTest(t *testing.T) {
	tempDir := t.TempDir()
	n, err := notify.New(tempDir, notify.Config{}, nil)
	if err != nil {
		t.Fatalf("notify.New: %v", err)
	}

	h := &Handler{
		notifier: n,
	}

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer mockServer.Close()

	// 1. Get initial status
	recGet := httptest.NewRecorder()
	reqGet := httptest.NewRequest(http.MethodGet, "/api/notify/config", nil)
	h.GetNotifyConfig(recGet, reqGet)

	if recGet.Code != http.StatusOK {
		t.Fatalf("GetNotifyConfig status %d, want 200", recGet.Code)
	}

	var st notify.Status
	if err := json.Unmarshal(recGet.Body.Bytes(), &st); err != nil {
		t.Fatalf("Unmarshal notify status: %v", err)
	}
	if st.HasNtfyToken || st.HasGotifyToken {
		t.Errorf("expected no tokens initially: %+v", st)
	}

	// 2. Test alert without channels must fail
	recTestFail := httptest.NewRecorder()
	reqTestFail := httptest.NewRequest(http.MethodPost, "/api/notify/test", nil)
	h.TestNotify(recTestFail, reqTestFail)
	if recTestFail.Code != http.StatusBadRequest {
		t.Errorf("TestNotify with no channel got %d, want 400", recTestFail.Code)
	}

	// 3. Update config with Ntfy and Gotify pointing to mockServer
	updateBody := map[string]any{
		"config": notify.Config{
			Enabled:         true,
			NtfyEnabled:     true,
			NtfyTopic:       "alerts",
			NtfyServerURL:   mockServer.URL,
			GotifyEnabled:   true,
			GotifyServerURL: mockServer.URL,
		},
		"ntfy_token":   "ntfy-secret-123",
		"gotify_token": "gotify-app-456",
	}
	rawUpdate, _ := json.Marshal(updateBody)
	recUpdate := httptest.NewRecorder()
	reqUpdate := httptest.NewRequest(http.MethodPut, "/api/notify/config", bytes.NewReader(rawUpdate))
	h.UpdateNotifyConfig(recUpdate, reqUpdate)

	if recUpdate.Code != http.StatusOK {
		t.Fatalf("UpdateNotifyConfig status %d, want 200: %s", recUpdate.Code, recUpdate.Body.String())
	}

	var updatedSt notify.Status
	if err := json.Unmarshal(recUpdate.Body.Bytes(), &updatedSt); err != nil {
		t.Fatalf("Unmarshal updated notify status: %v", err)
	}
	if !updatedSt.HasNtfyToken || !updatedSt.HasGotifyToken {
		t.Errorf("expected tokens to be set: %+v", updatedSt)
	}
	if !updatedSt.Config.NtfyEnabled || !updatedSt.Config.GotifyEnabled {
		t.Errorf("expected ntfy and gotify enabled in config: %+v", updatedSt.Config)
	}

	// 4. Test alert with channels enabled must succeed
	recTestSuccess := httptest.NewRecorder()
	reqTestSuccess := httptest.NewRequest(http.MethodPost, "/api/notify/test", nil)
	h.TestNotify(recTestSuccess, reqTestSuccess)
	if recTestSuccess.Code != http.StatusOK {
		t.Fatalf("TestNotify with ntfy got %d, want 200: %s", recTestSuccess.Code, recTestSuccess.Body.String())
	}

	// Wait for goroutines to complete request
	time.Sleep(50 * time.Millisecond)

	// 5. Test history contains test notification
	recEvents := httptest.NewRecorder()
	reqEvents := httptest.NewRequest(http.MethodGet, "/api/notify/events", nil)
	h.ListNotifyEvents(recEvents, reqEvents)
	if recEvents.Code != http.StatusOK {
		t.Fatalf("ListNotifyEvents got %d, want 200", recEvents.Code)
	}
	if !strings.Contains(recEvents.Body.String(), "WebKVM test notification") {
		t.Errorf("ListNotifyEvents body missing test notification: %s", recEvents.Body.String())
	}
}
