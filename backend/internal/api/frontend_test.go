package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestFrontendHandler_MissingAssetIs404 guards against a real bug: a
// request for a hashed /assets/*.js file that no longer exists (e.g. the
// browser holds an older bundle across a backend update, or a stale tab
// left open across a redeploy) used to silently fall through to the SPA
// "unknown path" branch and get served index.html with a 200 — the
// browser then fails to parse HTML as a JS module with no visible error
// at all. assets/ is always a specific, content-hashed build output, so
// a miss there must be a real 404.
func TestFrontendHandler_MissingAssetIs404(t *testing.T) {
	h := frontendHandler()

	req := httptest.NewRequest(http.MethodGet, "/assets/definitely-not-a-real-chunk-12345.js", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset: got status %d, want 404", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<!doctype html") || strings.Contains(rec.Body.String(), "<!DOCTYPE html") {
		t.Fatalf("missing asset served index.html instead of a 404 body: %q", rec.Body.String())
	}
}

// TestFrontendHandler_UnknownRouteFallsBackToIndex covers the legitimate
// SPA-fallback case this must not break: any non-/assets/ path (a
// client-side route like /vms/abc-123 hit on a hard refresh) still
// serves index.html so Svelte's own router can take over.
func TestFrontendHandler_UnknownRouteFallsBackToIndex(t *testing.T) {
	h := frontendHandler()

	req := httptest.NewRequest(http.MethodGet, "/vms/some-vm-id-that-does-not-exist", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unknown SPA route: got status %d, want 200 (index.html fallback)", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("unknown SPA route: got Content-Type %q, want text/html", ct)
	}
}
