package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"syscall"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/appliances"
	"webkvm/internal/auth"
	"webkvm/internal/backupstore"
	"webkvm/internal/config"
	"webkvm/internal/configstore"
	"webkvm/internal/events"
	"webkvm/internal/firewall"
	"webkvm/internal/libvirt"
	"webkvm/internal/nodes"
	"webkvm/internal/notify"
	"webkvm/internal/tokens"
	"webkvm/internal/user"
	"webkvm/internal/vmsched"
)

type Handler struct {
	lv            *libvirt.Connector
	auth          *auth.Manager
	loginLimiter  *auth.LoginRateLimiter
	userStore     *user.Store
	cfg           *config.Config
	hub           *events.Hub
	gs            *groupsStore
	appStore      *appliances.Store
	metrics       *libvirt.MetricsCollector
	hostMetrics   *libvirt.HostMetricsCollector
	audit         *audit.Logger
	settings      *configstore.Store
	tokens        *tokens.Store
	nodes         *nodes.Registry
	backupStore   *backupstore.Store
	backupRunner  *backupstore.Runner
	notifier      *notify.Notifier
	fwStore       *firewall.Store
	fwMgr         *firewall.Manager
	vmSchedStore  *vmsched.Store
	vmScheduler   *vmsched.Scheduler
	StartedAt     time.Time
}

// Health reports backend liveness and the status of its dependencies.
// Returns 200 if everything is ok, 503 if libvirt is unreachable or
// the data dir is critically full. The data dir's free space is
// always reported (in bytes) so an orchestrator can graph it.
//
// Version and build_time come from the build-time ldflags (set in
// main.go) and propagated into cfg by config.Load. They let a
// post-deploy health probe confirm the *new* binary is the one
// answering, which is what the Makefile install-systemd target
// uses as the rollback trigger.
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	health := map[string]interface{}{
		"status":     "ok",
		"libvirt":    "ok",
		"uptime":     int64(time.Since(h.StartedAt).Seconds()),
		"data_dir":   h.cfg.DataDir,
		"version":    h.cfg.Version,
		"build_time": h.cfg.BuildTime,
	}

	// libvirt connectivity. h.lv can be nil in unit tests that
	// exercise Health in isolation; in production it is always
	// set by NewRouter.
	if h.lv == nil {
		health["status"] = "degraded"
		health["libvirt"] = "down"
	} else {
		conn := h.lv.Get()
		if conn == nil {
			health["status"] = "degraded"
			health["libvirt"] = "down"
		} else if _, err := conn.GetVersion(); err != nil {
			health["status"] = "degraded"
			health["libvirt"] = "down"
			health["libvirt_error"] = err.Error()
		}
	}

	// data dir free space
	var stat syscall.Statfs_t
	if err := syscall.Statfs(h.cfg.DataDir, &stat); err == nil {
		free := int64(stat.Bavail) * int64(stat.Bsize)
		total := int64(stat.Blocks) * int64(stat.Bsize)
		health["disk_free"] = free
		health["disk_total"] = total
		if total > 0 && free < total/20 {
			health["status"] = "degraded"
			health["disk_warning"] = "less than 5% free"
		}
	}

	status := http.StatusOK
	if health["status"] == "degraded" {
		status = http.StatusServiceUnavailable
	}
	jsonResp(w, status, health)
}

func jsonResp(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	jsonResp(w, status, map[string]string{"error": msg})
}

func decodeBody(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// auditFor is a small convenience that pulls the (user, role, ip) tuple
// from the request and returns an audit.Entry pre-populated with them.
// The action/resource/detail are set by the caller. Safe to call when
// no auth headers are set (e.g. for unauthenticated endpoints) — the
// returned entry simply has empty User/Role.
func auditFor(r *http.Request, action, resource string, detail map[string]interface{}) audit.Entry {
	u, role, ip := audit.FromRequest(r)
	return audit.Entry{User: u, Role: role, IP: ip, Action: action, Resource: resource, Detail: detail}
}

// logError logs a non-fatal background failure with slog (never
// exposes secrets; used for best-effort re-apply paths).
func (h *Handler) logError(msg string, err error, resource string) {
	slog.Warn(msg, "err", err, "resource", resource)
}
