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
	req.RemoteAddr = "127.0.0.1:4242"
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
	reqRes.RemoteAddr = "127.0.0.1:4242"
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

	// 3. The secret in the query string is no longer accepted -> 401
	reqAuthQuery := httptest.NewRequest(http.MethodPost, "/api/alerts/webhook?secret=secret-token-123", bytes.NewReader(body))
	recAuthQuery := httptest.NewRecorder()
	h.AlertmanagerWebhook(recAuthQuery, reqAuthQuery)
	if recAuthQuery.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for ?secret=, got %d", recAuthQuery.Code)
	}

	// 4. X-Webhook-Secret header -> 200; wrong value -> 401
	reqHdr := httptest.NewRequest(http.MethodPost, "/api/alerts/webhook", bytes.NewReader(body))
	reqHdr.Header.Set("X-Webhook-Secret", "secret-token-123")
	recHdr := httptest.NewRecorder()
	h.AlertmanagerWebhook(recHdr, reqHdr)
	if recHdr.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with X-Webhook-Secret, got %d", recHdr.Code)
	}
	reqBad := httptest.NewRequest(http.MethodPost, "/api/alerts/webhook", bytes.NewReader(body))
	reqBad.Header.Set("Authorization", "Bearer secret-token-124")
	recBad := httptest.NewRecorder()
	h.AlertmanagerWebhook(recBad, reqBad)
	if recBad.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for wrong secret, got %d", recBad.Code)
	}
}

// With no secret configured, only a direct loopback peer may post.
func TestAlertmanagerWebhookNoSecretIsLoopbackOnly(t *testing.T) {
	dir := t.TempDir()
	alerter := metrics.NewAlertEngine(dir, nil, events.NewHub())
	settings, err := configstore.New(dir, configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{alerter: alerter, settings: settings}
	body := []byte(`{"status":"firing","alerts":[]}`)

	cases := []struct {
		name   string
		remote string
		xff    string
		want   int
	}{
		{"loopback v4", "127.0.0.1:5000", "", http.StatusOK},
		{"loopback v6", "[::1]:5000", "", http.StatusOK},
		{"lan peer", "192.168.1.50:5000", "", http.StatusForbidden},
		{"loopback proxy forwarding a remote", "127.0.0.1:5000", "203.0.113.9", http.StatusForbidden},
		{"loopback with spoofed xff", "127.0.0.1:5000", "127.0.0.1", http.StatusForbidden},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/alerts/webhook", bytes.NewReader(body))
		req.RemoteAddr = c.remote
		if c.xff != "" {
			req.Header.Set("X-Forwarded-For", c.xff)
		}
		rec := httptest.NewRecorder()
		h.AlertmanagerWebhook(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d want %d", c.name, rec.Code, c.want)
		}
	}
}
