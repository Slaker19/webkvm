package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/backupstore"
)

func TestPruneBackupTarget(t *testing.T) {
	tmpDir := t.TempDir()
	store, err := backupstore.New(tmpDir)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	aud, err := audit.New(filepath.Join(tmpDir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{backupStore: store, audit: aud}

	// 1. Target not found -> 404
	{
		rCtx := chi.NewRouteContext()
		rCtx.URLParams.Add("id", "nonexistent")
		req := httptest.NewRequest(http.MethodPost, "/api/backup/targets/nonexistent/prune", nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rCtx))
		rec := httptest.NewRecorder()
		h.PruneBackupTarget(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", rec.Code)
		}
	}

	// 2. Target without retention policy -> 400
	targetPath := filepath.Join(tmpDir, "backups1")
	t1, err := store.CreateTargetOpts("t1", targetPath, backupstore.TargetLocal, "all", nil, backupstore.TargetOptions{})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	{
		rCtx := chi.NewRouteContext()
		rCtx.URLParams.Add("id", t1.ID)
		req := httptest.NewRequest(http.MethodPost, "/api/backup/targets/"+t1.ID+"/prune", nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rCtx))
		rec := httptest.NewRecorder()
		h.PruneBackupTarget(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for disabled retention, got %d", rec.Code)
		}
	}

	// 3. Target with retention policy enabled -> 200
	t2Path := filepath.Join(tmpDir, "backups2")
	t2, err := store.CreateTargetOpts("t2", t2Path, backupstore.TargetLocal, "all", nil, backupstore.TargetOptions{
		Retention: backupstore.RetentionPolicy{KeepLast: 3},
	})
	if err != nil {
		t.Fatalf("create target t2: %v", err)
	}
	{
		rCtx := chi.NewRouteContext()
		rCtx.URLParams.Add("id", t2.ID)
		req := httptest.NewRequest(http.MethodPost, "/api/backup/targets/"+t2.ID+"/prune", nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rCtx))
		rec := httptest.NewRecorder()
		h.PruneBackupTarget(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("expected 200 for enabled retention prune, got %d (body: %s)", rec.Code, rec.Body.String())
		}
	}
}
