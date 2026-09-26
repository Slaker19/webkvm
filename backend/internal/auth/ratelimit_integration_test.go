package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"webkvm/internal/auth"
	"webkvm/internal/configstore"
)

// TestGlobalRateLimiter_RealStoreWiring reproduces the production wiring
// (real configstore.Store -> NewGlobalRateLimiter) to prove the live
// settings are honored for new buckets.
func TestGlobalRateLimiter_RealStoreWiring(t *testing.T) {
	dir := t.TempDir()
	s, err := configstore.New(dir, configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	_, failed, err := s.SetMany(configstore.Set{
		"server.rate_limit_enabled": true,
		"server.rate_limit_rps":     100,
		"server.rate_limit_burst":   1,
	})
	if err != nil || len(failed) > 0 {
		t.Fatalf("SetMany: %v %v", err, failed)
	}

	l := auth.NewGlobalRateLimiter(s)
	t.Cleanup(l.Close)

	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	rr := httptest.NewRecorder()
	l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if got := rr.Header().Get("X-RateLimit-Limit"); got != "1" {
		t.Fatalf("X-RateLimit-Limit = %q, want 1 (live store burst must apply)", got)
	}
}
