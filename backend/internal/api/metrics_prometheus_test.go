package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/auth"
	"webkvm/internal/config"
	"webkvm/internal/configstore"
	"webkvm/internal/models"
	"webkvm/internal/tokens"
)

func TestPrometheusMetrics_AuthAndContent(t *testing.T) {
	tempDir := t.TempDir()

	authMgr := auth.NewManager("test-secret-key-32-chars-long!!", nil)
	t.Cleanup(func() { authMgr.Close() })

	tokStore, err := tokens.New(tempDir)
	if err != nil {
		t.Fatalf("tokens.New: %v", err)
	}

	cfgStore, err := configstore.New(tempDir, configstore.DefaultSchema())
	if err != nil {
		t.Fatalf("configstore.New: %v", err)
	}

	cfg := &config.Config{
		Version: "0.1.5",
	}

	h := &Handler{
		cfg:      cfg,
		auth:     authMgr,
		tokens:   tokStore,
		settings: cfgStore,
	}

	// 1. By default, unauthenticated scrape must be rejected with 401
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	h.PrometheusMetrics(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated /metrics got status %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("missing WWW-Authenticate header: %v", rec.Header())
	}

	// 2. Scrape with API token via Bearer header
	tok, plainTok, err := tokStore.Create("prom-agent", "admin", models.RoleAdmin, nil, 0, 0)
	if err != nil {
		t.Fatalf("tokStore.Create: %v", err)
	}
	_ = tok

	recBearer := httptest.NewRecorder()
	reqBearer := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	reqBearer.Header.Set("Authorization", "Bearer "+plainTok)
	h.PrometheusMetrics(recBearer, reqBearer)

	if recBearer.Code != http.StatusOK {
		t.Fatalf("authenticated /metrics got status %d, want 200: %s", recBearer.Code, recBearer.Body.String())
	}
	if ct := recBearer.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("expected text/plain Content-Type, got %q", ct)
	}
	body := recBearer.Body.String()
	if !strings.Contains(body, `webkvm_info{version="0.1.5"} 1`) {
		t.Errorf("missing webkvm_info in Prometheus output: %s", body)
	}
	if !strings.Contains(body, `webkvm_up 1`) {
		t.Errorf("missing webkvm_up in Prometheus output: %s", body)
	}

	// 3. Scrape with token in query string ?token=...
	recQuery := httptest.NewRecorder()
	reqQuery := httptest.NewRequest(http.MethodGet, "/api/metrics/prometheus?token="+plainTok, nil)
	h.PrometheusMetrics(recQuery, reqQuery)

	if recQuery.Code != http.StatusOK {
		t.Errorf("query param /api/metrics/prometheus got status %d, want 200", recQuery.Code)
	}

	// 4. Enable metrics.allow_unauthenticated in settings
	if _, _, err := cfgStore.SetMany(configstore.Set{"metrics.allow_unauthenticated": true}); err != nil {
		t.Fatalf("Set metrics.allow_unauthenticated: %v", err)
	}

	recUnauthAllowed := httptest.NewRecorder()
	reqUnauthAllowed := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	h.PrometheusMetrics(recUnauthAllowed, reqUnauthAllowed)

	if recUnauthAllowed.Code != http.StatusOK {
		t.Errorf("unauthenticated allowed got status %d, want 200", recUnauthAllowed.Code)
	}

	// 5. Disable metrics.prometheus_enabled
	if _, _, err := cfgStore.SetMany(configstore.Set{"metrics.prometheus_enabled": false}); err != nil {
		t.Fatalf("Set metrics.prometheus_enabled: %v", err)
	}

	recDisabled := httptest.NewRecorder()
	reqDisabled := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	h.PrometheusMetrics(recDisabled, reqDisabled)

	if recDisabled.Code != http.StatusNotFound {
		t.Errorf("disabled metrics got status %d, want 404", recDisabled.Code)
	}
}
