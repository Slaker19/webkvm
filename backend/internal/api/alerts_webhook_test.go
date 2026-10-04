package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"webkvm/internal/configstore"
	"webkvm/internal/events"
	"webkvm/internal/metrics"
)

func TestAlertmanagerWebhookAndIncidents(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub()
	alerter := metrics.NewAlertEngine(dir, nil, hub)

	h := &Handler{
		alerter: alerter,
	}

	payload := metrics.AlertmanagerPayload{
		Status: "firing",
		Alerts: []metrics.AlertmanagerAlert{
			{
				Status: "firing",
				Labels: map[string]string{
					"alertname": "KvmHostDiskAlmostFull",
					"severity":  "critical",
					"instance":  "hypervisor-1",
				},
				Annotations: map[string]string{
					"summary":     "Root disk usage > 90%",
					"description": "Only 8GB free on /",
				},
				StartsAt: time.Now(),
			},
		},
	}

	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/alerts/webhook", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	h.AlertmanagerWebhook(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webhook returned %d: %s", rec.Code, rec.Body.String())
	}

	// Fetch incidents
	reqGet := httptest.NewRequest(http.MethodGet, "/api/alerts/incidents", nil)
	recGet := httptest.NewRecorder()
	h.ListIncidents(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("ListIncidents returned %d: %s", recGet.Code, recGet.Body.String())
	}

	var res struct {
		Incidents []metrics.Incident `json:"incidents"`
	}
	if err := json.NewDecoder(recGet.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if len(res.Incidents) != 1 || res.Incidents[0].Alertname != "KvmHostDiskAlmostFull" {
		t.Fatalf("unexpected incidents: %+v", res.Incidents)
	}

	// Resolve
	payload.Alerts[0].Status = "resolved"
	bodyRes, _ := json.Marshal(payload)
	reqRes := httptest.NewRequest(http.MethodPost, "/api/alerts/webhook", bytes.NewReader(bodyRes))
	recRes := httptest.NewRecorder()
	h.AlertmanagerWebhook(recRes, reqRes)

	// Clear resolved
	reqDel := httptest.NewRequest(http.MethodDelete, "/api/alerts/incidents/resolved", nil)
	recDel := httptest.NewRecorder()
	h.ClearResolvedIncidents(recDel, reqDel)
	if recDel.Code != http.StatusOK {
		t.Fatalf("ClearResolvedIncidents returned %d", recDel.Code)
	}

	reqGet2 := httptest.NewRequest(http.MethodGet, "/api/alerts/incidents", nil)
	recGet2 := httptest.NewRecorder()
	h.ListIncidents(recGet2, reqGet2)
	_ = json.NewDecoder(recGet2.Body).Decode(&res)
	if len(res.Incidents) != 0 {
		t.Fatalf("expected 0 incidents after clear, got %d", len(res.Incidents))
	}
}

func TestAlertmanagerWebhookSecretAuth(t *testing.T) {
	dir := t.TempDir()
	hub := events.NewHub()
	alerter := metrics.NewAlertEngine(dir, nil, hub)
	settings, err := configstore.New(dir, configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := settings.SetMany(configstore.Set{"alerts.webhook_secret": "secret-token-123"}); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		alerter:  alerter,
		settings: settings,
	}

	body := []byte(`{"status":"firing","alerts":[]}`)

	// 1. Missing secret -> 401
	reqUnauth := httptest.NewRequest(http.MethodPost, "/api/alerts/webhook", bytes.NewReader(body))
	recUnauth := httptest.NewRecorder()
	h.AlertmanagerWebhook(recUnauth, reqUnauth)
	if recUnauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d", recUnauth.Code)
	}

	// 2. Valid secret in Authorization Bearer header -> 200
	reqAuthBearer := httptest.NewRequest(http.MethodPost, "/api/alerts/webhook", bytes.NewReader(body))
	reqAuthBearer.Header.Set("Authorization", "Bearer secret-token-123")
	recAuthBearer := httptest.NewRecorder()
	h.AlertmanagerWebhook(recAuthBearer, reqAuthBearer)
	if recAuthBearer.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with Bearer, got %d", recAuthBearer.Code)
	}

	// 3. Valid secret in query param ?secret=... -> 200
	reqAuthQuery := httptest.NewRequest(http.MethodPost, "/api/alerts/webhook?secret=secret-token-123", bytes.NewReader(body))
	recAuthQuery := httptest.NewRecorder()
	h.AlertmanagerWebhook(recAuthQuery, reqAuthQuery)
	if recAuthQuery.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with query param, got %d", recAuthQuery.Code)
	}
}
