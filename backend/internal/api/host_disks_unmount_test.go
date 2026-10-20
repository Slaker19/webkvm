package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
)

func TestUnmountHostDisk_Validation(t *testing.T) {
	aud, err := audit.New(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{audit: aud}

	// 1. Missing both path and mount_point -> 400
	{
		body := bytes.NewBufferString(`{}`)
		req := httptest.NewRequest(http.MethodPost, "/api/host/disks/unmount", body)
		rec := httptest.NewRecorder()
		h.UnmountHostDisk(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for empty body, got %d", rec.Code)
		}
	}

	// 2. Refuses system mount point -> 403
	{
		body := bytes.NewBufferString(`{"mount_point": "/"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/host/disks/unmount", body)
		rec := httptest.NewRecorder()
		h.UnmountHostDisk(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for system root unmount, got %d", rec.Code)
		}
	}

	{
		body := bytes.NewBufferString(`{"mount_point": "/boot"}`)
		req := httptest.NewRequest(http.MethodPost, "/api/host/disks/unmount", body)
		rec := httptest.NewRecorder()
		h.UnmountHostDisk(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for /boot unmount, got %d", rec.Code)
		}
	}
}

func TestDestroyHostZPool_Validation(t *testing.T) {
	aud, err := audit.New(filepath.Join(t.TempDir(), "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{audit: aud}

	// Invalid pool name -> 400
	rCtx := chi.NewRouteContext()
	rCtx.URLParams.Add("name", "bad pool name with spaces")
	req := httptest.NewRequest(http.MethodDelete, "/api/host/zpools/bad%20name", nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rCtx))
	rec := httptest.NewRecorder()
	h.DestroyHostZPool(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid pool name, got %d", rec.Code)
	}
}
