package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestScrubHostZPool_Validation(t *testing.T) {
	h := &Handler{}

	// Invalid pool name
	r := httptest.NewRequest(http.MethodPost, "/api/host/zpools/invalid;name/scrub", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("name", "invalid;name")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.ScrubHostZPool(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", w.Code)
	}
}

func TestSyncHostRAID_Validation(t *testing.T) {
	h := &Handler{}

	// Invalid action
	body := strings.NewReader(`{"action":"destroy"}`)
	r := httptest.NewRequest(http.MethodPost, "/api/host/raid/md0/sync", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("device", "md0")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.SyncHostRAID(w, r)

	if w.Code != http.StatusInternalServerError && w.Code != http.StatusBadRequest {
		t.Fatalf("expected error response, got %d", w.Code)
	}
}

func TestProbeHostDiskSMART_Validation(t *testing.T) {
	h := &Handler{}

	// Missing path query parameter
	r := httptest.NewRequest(http.MethodGet, "/api/host/disks/smart", nil)
	w := httptest.NewRecorder()
	h.ProbeHostDiskSMART(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", w.Code)
	}

	// Malformed disk path
	r2 := httptest.NewRequest(http.MethodGet, "/api/host/disks/smart?path=/etc/passwd", nil)
	w2 := httptest.NewRecorder()
	h.ProbeHostDiskSMART(w2, r2)

	if w2.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request, got %d", w2.Code)
	}
}
