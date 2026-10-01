package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestBatchCloneVM_Validation(t *testing.T) {
	h := &Handler{}

	// Missing base_name
	body := strings.NewReader(`{"base_name":"","count":5}`)
	r := httptest.NewRequest(http.MethodPost, "/api/vms/test-vm/batch-clone", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "test-vm")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	w := httptest.NewRecorder()
	h.BatchCloneVM(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for empty base_name, got %d", w.Code)
	}

	// Invalid count > 50
	body = strings.NewReader(`{"base_name":"node","count":100}`)
	r = httptest.NewRequest(http.MethodPost, "/api/vms/test-vm/batch-clone", body)
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	w = httptest.NewRecorder()
	h.BatchCloneVM(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for count > 50, got %d", w.Code)
	}
}
