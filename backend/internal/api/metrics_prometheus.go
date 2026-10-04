package api

import (
	"net/http"
	"strings"

	"webkvm/internal/auth"
	"webkvm/internal/metrics"
	"webkvm/internal/models"
)

// isScrapeAuthorized checks whether a /metrics scrape request is allowed.
// It allows access if:
// 1. metrics.allow_unauthenticated is enabled in settings, OR
// 2. A valid Bearer token (JWT or API Token) is supplied, OR
// 3. A valid ?token= query parameter is supplied, OR
// 4. A valid webkvm_session cookie is present.
func (h *Handler) isScrapeAuthorized(r *http.Request) bool {
	if h.settings != nil {
		if h.settings.GetBool("metrics.allow_unauthenticated") {
			return true
		}
	}

	tokenStr := ""
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			tokenStr = parts[1]
		}
	}
	if tokenStr == "" {
		tokenStr = r.URL.Query().Get("token")
	}
	if tokenStr == "" {
		tokenStr = auth.SessionToken(r)
	}
	if tokenStr == "" {
		return false
	}

	if h.auth != nil {
		if claims, err := h.auth.ValidateToken(tokenStr); err == nil && claims != nil {
			return true
		}
	}
	if h.tokens != nil {
		if tok, err := h.tokens.Validate(tokenStr); err == nil && tok != nil {
			return true
		}
	}

	return false
}

// PrometheusMetrics handles GET /metrics and GET /api/metrics/prometheus.
// It outputs standard OpenMetrics / Prometheus plain text metrics.
func (h *Handler) PrometheusMetrics(w http.ResponseWriter, r *http.Request) {
	if h.settings != nil {
		if !h.settings.GetBool("metrics.prometheus_enabled") {
			http.Error(w, "Prometheus metrics endpoint disabled in settings", http.StatusNotFound)
			return
		}
	}

	if !h.isScrapeAuthorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="WebKVM Prometheus Metrics"`)
		http.Error(w, "unauthorized: provide an API token via Bearer header or ?token= parameter", http.StatusUnauthorized)
		return
	}

	data := metrics.PrometheusData{
		Version: h.cfg.Version,
	}

	// 1. Host metrics
	if h.hostMetrics != nil {
		series := h.hostMetrics.Series()
		if len(series.Points) > 0 {
			last := series.Points[len(series.Points)-1]
			data.Host = &metrics.HostTelemetry{
				CPUUsage:  last.CPUUsage,
				UsedRAM:   last.UsedRAM,
				TotalRAM:  last.TotalRAM,
				UsedDisk:  last.UsedDisk,
				TotalDisk: last.TotalDisk,
				NetRx:     last.NetRx,
				NetTx:     last.NetTx,
				Uptime:    readHostUptimeSec(),
			}
		}
	}
	if data.Host == nil {
		data.Host = &metrics.HostTelemetry{
			Uptime: readHostUptimeSec(),
		}
	}

	// 2. VMs and containers
	if h.compute != nil {
		vms, err := h.compute.ListDomains()
		if err == nil {
			data.VMs = vms
			if h.metrics != nil {
				data.VMMetrics = make(map[string]models.VMMetrics, len(vms))
				for _, vm := range vms {
					if m, err := h.metrics.Get(vm.ID); err == nil {
						data.VMMetrics[vm.ID] = m
					}
				}
			}
		}
	}

	// 3. Storage pools
	if h.compute != nil {
		pools, err := h.compute.ListStoragePools()
		if err == nil {
			data.Pools = pools
		}
	}

	output := metrics.RenderPrometheus(data)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(output)
}

// GrafanaDashboard returns a pre-configured Grafana dashboard JSON file for WebKVM.
func (h *Handler) GrafanaDashboard(w http.ResponseWriter, r *http.Request) {
	data := metrics.GenerateGrafanaDashboard()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=\"webkvm-grafana-dashboard.json\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// AlertRules returns Prometheus / Alertmanager alerting rules YAML for WebKVM.
func (h *Handler) AlertRules(w http.ResponseWriter, r *http.Request) {
	data := metrics.GenerateAlertRules()
	w.Header().Set("Content-Type", "application/x-yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"webkvm-alert-rules.yml\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
