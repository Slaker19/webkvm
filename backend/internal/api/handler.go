package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"syscall"
	"time"

	"webkvm/internal/appliances"
	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/backupstore"
	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/compute/incus"
	"webkvm/internal/config"
	"webkvm/internal/configstore"
	"webkvm/internal/events"
	"webkvm/internal/firewall"
	"webkvm/internal/hainstall"
	"webkvm/internal/helperscripts"
	"webkvm/internal/hostsched"
	"webkvm/internal/libvirt"
	"webkvm/internal/metrics"
	"webkvm/internal/netguard"
	"webkvm/internal/nodes"
	"webkvm/internal/notify"
	"webkvm/internal/tokens"
	"webkvm/internal/user"
	"webkvm/internal/vmsched"
)

type Handler struct {
	lv *libvirt.Connector
	// compute is the generic ComputeBackend (v1.4 Fase 0): every instance
	// operation goes through this seam. h.lv is kept ONLY for
	// hypervisor-infrastructure concerns (raw connection, connectivity
	// checks, host/system libvirt queries).
	compute      compute.Backend
	auth         *auth.Manager
	loginLimiter *auth.LoginRateLimiter
	jail         *netguard.Jail
	userStore    *user.Store
	cfg          *config.Config
	hub          *events.Hub
	gs           *groupsStore
	appStore     *appliances.Store
	// helperScripts caches the imported community-scripts catalog.
	helperScripts *helperscripts.Store
	metrics       *libvirt.MetricsCollector
	hostMetrics   *libvirt.HostMetricsCollector
	audit         *audit.Logger
	settings      *configstore.Store
	tokens        *tokens.Store
	nodes         *nodes.Registry
	// sshKeys holds the managed cluster SSH key and pinned host keys;
	// libvirtPool the long-lived connections to remote libvirt nodes.
	sshKeys       *nodes.SSHKeyStore
	libvirtPool   *libvirt.Pool
	sshTests      *clusterSSHTests
	backupStore   *backupstore.Store
	backupRunner  *backupstore.Runner
	notifier      *notify.Notifier
	fwStore       *firewall.Store
	fwMgr         *firewall.Manager
	clusterFW     *firewall.ClusterManager
	haInstall     *hainstall.Manager
	haCluster     *haClusterState
	vmSchedStore  *vmsched.Store
	vmScheduler   *vmsched.Scheduler
	hostSched     *hostsched.Store
	hostScheduler *hostsched.Scheduler
	snippets      *cloudinit.SnippetStore
	// V13-C-03/04: metric history + alert engine.
	metricHist *metrics.TimeSeriesStore
	alerter    *metrics.AlertEngine
	// incusMetrics collects CPU/RAM/Net for LXD containers (v1.4 Fase 4.1).
	// GET /vms/{id}/metrics routes to it when the instance is a container.
	incusMetrics *incus.MetricsCollector
	webauthn     *auth.WebAuthnManager
	StartedAt    time.Time

	// Per-name serialization of appliance deployments. Two
	// concurrent deploys must never race: the second one waits, then
	// verifies the target is still free. The map is trimmed when the last
	// holder releases, so it cannot grow without bound.
	deploy *deployState

	// remote is non-nil on the per-node clones built by forBackend: every
	// instance operation of such a handler targets that libvirt node.
	remote *remoteTarget
	// remoteRouters caches the per-node route trees (shared by all clones).
	remoteRouters *remoteRouterCache
	// workloads persists per-(node,hv,id) workload state (managed flag).
	workloads *workloadStore
}

// deployState serializes appliance deployments per VM name. It lives behind
// a pointer so Handler clones share it and never copy the mutex.
type deployState struct {
	mu    sync.Mutex
	locks map[string]*deployLockEntry
}

// sharedInitMu guards the lazy creation of the pointer-held shared state of
// handlers built without NewRouter (tests).
var sharedInitMu sync.Mutex

func (h *Handler) deployState() *deployState {
	sharedInitMu.Lock()
	defer sharedInitMu.Unlock()
	if h.deploy == nil {
		h.deploy = &deployState{}
	}
	return h.deploy
}

func (h *Handler) haState() *haClusterState {
	sharedInitMu.Lock()
	defer sharedInitMu.Unlock()
	if h.haCluster == nil {
		h.haCluster = &haClusterState{}
	}
	return h.haCluster
}

func (h *Handler) sshTestState() *clusterSSHTests {
	sharedInitMu.Lock()
	defer sharedInitMu.Unlock()
	if h.sshTests == nil {
		h.sshTests = &clusterSSHTests{store: h.sshKeys}
		if h.sshKeys != nil {
			h.sshTests.m = h.sshKeys.LoadLastTests()
		}
	}
	return h.sshTests
}

// deployLockEntry is an acquired-or-waited deploy slot for one VM name.
// holders counts acquirers that registered (holding or queued); when it
// drops to zero the entry is removable.
type deployLockEntry struct {
	mu      sync.Mutex
	holders int
}

// acquireDeployLock blocks until it holds the deploy lock for vmName. The
// returned func releases it and must be deferred by the caller.
func (h *Handler) acquireDeployLock(vmName string) func() {
	ds := h.deployState()
	ds.mu.Lock()
	if ds.locks == nil {
		ds.locks = make(map[string]*deployLockEntry)
	}
	e := ds.locks[vmName]
	if e == nil {
		e = &deployLockEntry{}
		ds.locks[vmName] = e
	}
	e.holders++
	mu := &e.mu
	name := vmName
	ds.mu.Unlock()

	mu.Lock()
	return func() {
		ds.mu.Lock()
		if e := ds.locks[name]; e != nil {
			e.holders--
			if e.holders <= 0 {
				delete(ds.locks, name)
			}
		}
		ds.mu.Unlock()
		mu.Unlock()
	}
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

func jsonErrCode(w http.ResponseWriter, status int, code, msg string) {
	jsonResp(w, status, map[string]string{"error": msg, "code": code})
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
	// Requests routed to another node (remote KVM, Incus cluster member) tag
	// every audit entry with where they acted.
	if d, ok := dispatchFrom(r); ok {
		merged := make(map[string]interface{}, len(detail)+3)
		for k, v := range detail {
			merged[k] = v
		}
		merged["node"] = d.Node
		if d.NodeID != "" {
			merged["node_id"] = d.NodeID
		}
		merged["hv"] = d.HV
		detail = merged
	}
	return audit.Entry{User: u, Role: role, IP: ip, Action: action, Resource: resource, Detail: detail}
}

// logError logs a non-fatal background failure with slog (never
// exposes secrets; used for best-effort re-apply paths).
func (h *Handler) logError(msg string, err error, resource string) {
	slog.Warn(msg, "err", err, "resource", resource)
}
