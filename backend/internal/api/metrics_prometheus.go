package api

import (
	"net/http"
	"strings"

	"webkvm/internal/auth"
	"webkvm/internal/metrics"
	"webkvm/internal/models"
)

// scrapeAuth is the outcome of authorizing a /metrics scrape.
type scrapeAuth int

const (
	scrapeOK scrapeAuth = iota
	scrapeUnauthorized
	scrapeForbidden
)

// authorizeScrape decides whether a /metrics scrape request is allowed. The
// route is public to the generic auth middleware (so Prometheus can reach it
// with its own credentials), which means the checks the middleware and
// SessionEnforcer do for every other route are repeated here:
//
//  1. metrics.allow_unauthenticated is enabled, OR
//  2. a session JWT (issuer "webkvm", Bearer header or session cookie), or
//     an API token (Bearer header, or ?token= for scrapers that can't set
//     headers; JWTs are never accepted in the URL), whose account exists, is
//     active, and whose session epoch is current.
//
// The series cover every VM, so only admins get them: any other valid
// identity is answered 403.
func (h *Handler) authorizeScrape(r *http.Request) scrapeAuth {
	if h.settings != nil && h.settings.GetBool("metrics.allow_unauthenticated") {
		return scrapeOK
	}

	bearer := ""
	if authHeader := r.Header.Get("Authorization"); authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			bearer = parts[1]
		}
	}
	queryTok := r.URL.Query().Get("token")
	cookieTok := auth.SessionToken(r)

	var username string
	var epoch int
	ok := false

	// Session JWT (header or cookie). Only "webkvm"-issuer session tokens
	// count: an MFA-pending token ("webkvm_mfa") must never open a session.
	for _, cand := range []string{bearer, cookieTok} {
		if cand == "" || h.auth == nil {
			continue
		}
		if claims, err := h.auth.ValidateToken(cand); err == nil && claims != nil && claims.Issuer == "webkvm" {
			username, epoch, ok = claims.Username, claims.SessionEpoch, true
			break
		}
	}
	// API token (header, or query for scrapers).
	if !ok && h.tokens != nil && h.auth != nil && h.auth.AllowAPITokens() {
		for _, cand := range []string{bearer, queryTok} {
			if cand == "" {
				continue
			}
			if tok, err := h.tokens.Validate(cand); err == nil && tok != nil {
				username, epoch, ok = tok.Username, tok.SessionEpoch, true
				break
			}
		}
	}
	if !ok {
		return scrapeUnauthorized
	}

	// Live account state, same as SessionEnforcer: fail closed.
	if h.userStore == nil {
		return scrapeUnauthorized
	}
	must, curEpoch, active, err := h.userStore.SessionStatus(username)
	if err != nil || !active || epoch != curEpoch || must {
		return scrapeUnauthorized
	}
	u, err := h.userStore.Get(username)
	if err != nil {
		return scrapeUnauthorized
	}
	if u.Role != models.RoleAdmin {
		return scrapeForbidden
	}
	return scrapeOK
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

	switch h.authorizeScrape(r) {
	case scrapeUnauthorized:
		w.Header().Set("WWW-Authenticate", `Bearer realm="WebKVM Prometheus Metrics"`)
		http.Error(w, "unauthorized: provide an admin API token via Bearer header", http.StatusUnauthorized)
		return
	case scrapeForbidden:
		http.Error(w, "forbidden: metrics are restricted to administrators", http.StatusForbidden)
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
