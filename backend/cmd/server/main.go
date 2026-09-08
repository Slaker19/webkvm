package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"webkvm/internal/api"
	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/backupstore"
	"webkvm/internal/config"
	"webkvm/internal/configstore"
	"webkvm/internal/compute"
	"webkvm/internal/compute/incus"
	"webkvm/internal/events"
	"webkvm/internal/firewall"
	"webkvm/internal/libvirt"
	"webkvm/internal/logging"
	metrics2 "webkvm/internal/metrics"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
	"webkvm/internal/notify"
	"webkvm/internal/tokens"
	"webkvm/internal/user"
	"webkvm/internal/vmsched"
)

// Set by -ldflags at build time. Defaults are used for `go run`.
var (
	Version   = "dev"
	BuildTime = "unknown"
)

// mustContainerVersion returns the container daemon version ("" on error)
func mustContainerVersion(b *incus.IncusBackend) string {
	v, err := b.ServerInfo()
	if err != nil {
		return ""
	}
	return v
}

func main() {
	// version / --version: print the build-time -ldflags version (the
	// binary is distributed pre-built, not compiled by the installer —
	// see install.sh) and exit immediately, before any config/libvirt
	// setup. update.sh relies on this to report what it's upgrading
	// from/to; without it, "webkvm version" would fall through to a
	// full second server instance that just fails to bind the port
	// already held by the running one.
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version" || os.Args[1] == "-v") {
		version, buildTime := Version, BuildTime
		if version == "dev" {
			if v := os.Getenv("WEBKVM_VERSION"); v != "" {
				version = v
			}
		}
		if buildTime == "unknown" {
			if v := os.Getenv("WEBKVM_BUILD_TIME"); v != "" {
				buildTime = v
			}
		}
		fmt.Printf("webkvm %s (built %s)\n", version, buildTime)
		return
	}

	// --fix-perms: one-shot CLI helper that chmod 0644 every disk
	// file in every active storage pool so a non-root backend can
	// read them. Requires root (the binary is invoked via sudo).
	// Exits 0 on success, 1 if any file couldn't be changed.
	if len(os.Args) > 1 && os.Args[1] == "--fix-perms" {
		if os.Geteuid() != 0 {
			fmt.Fprintln(os.Stderr, "webkvm --fix-perms must be run as root (try: sudo webkvm --fix-perms)")
			os.Exit(1)
		}
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			os.Exit(1)
		}
		lv := libvirt.NewConnector("qemu:///system", cfg)
		if err := lv.Open(); err != nil {
			fmt.Fprintln(os.Stderr, "connect to libvirt:", err)
			os.Exit(1)
		}
		defer lv.Close()
		if err := lv.FixDiskPermissions(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	// Set up structured logging early so even pre-config errors
	// (e.g. invalid .env) land in the right format. Defaults: json+info.
	logFormat := os.Getenv("WEBKVM_LOG_FORMAT")
	if logFormat == "" {
		logFormat = "json"
	}
	logLevel := os.Getenv("WEBKVM_LOG_LEVEL")
	if logLevel == "" {
		logLevel = "info"
	}
	logging.Init(logFormat, logLevel)
	logger := slog.Default().With("component", "main")
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		logger.Error("config_load_failed", "err", err)
		os.Exit(1)
	}
	// Build-time -ldflags Version takes precedence; only fall back to the
	// env var if the binary was built without one.
	if Version != "dev" {
		cfg.Version = Version
	}
	if BuildTime != "unknown" {
		cfg.BuildTime = BuildTime
	}
	// If WEBKVM_LOG_FILE is set (typically via a systemd drop-in),
	// re-init logging so the same records are also written to that
	// file. This is the path /api/system/logs reads back. This has
	// to happen here and not before config.Load because config.Load()
	// reads .env as a fallback.
	if cfg.LogFile != "" {
		logging.InitWithFile(logFormat, logLevel, cfg.LogFile)
		logger = slog.Default().With(
			"component", "main",
			"version", cfg.Version,
			"build_time", cfg.BuildTime,
		)
		slog.SetDefault(logger)
		logger.Info("log_file_enabled", "path", cfg.LogFile)
	} else {
		// No file tee: enrich the default with version/build_time
		// the same way we would inside the if branch above.
		logger = slog.Default().With(
			"version", cfg.Version,
			"build_time", cfg.BuildTime,
		)
		slog.SetDefault(logger)
	}

	// Shutdown context for background goroutines (libvirt reconnection,
	// event loop, backup runner, metrics). Created early so it can be
	// wired into the libvirt retry loop before the event loop is set up.
	eventCtx, cancelEvents := context.WithCancel(context.Background())
	defer cancelEvents()

	// --- Settings store: MUST be initialized before anything that
	// reads from it. Phase 1.7-bis wired 12 of the 30+ schema fields
	// to live code paths, so the order below matters.
	settingsStore, err := configstore.New(cfg.DataDir, configstore.DefaultSchema())
	if err != nil {
		logger.Error("configstore_init_failed", "err", err)
		os.Exit(1)
	}
	logger.Info("configstore_loaded", "path", settingsStore.Path(), "pending_restart", settingsStore.PendingRestart())

	// Honor the persisted server.bind_addr / server.port from the
	// store. Env-var wins on first boot (we never overwrite a value
	// the operator explicitly set in the systemd unit file), but on
	// a subsequent restart the operator's UI change sticks — which
	// is the whole point of Settings being more than
	// a vanity screen.
	bindAddr := cfg.BindAddr
	if v := settingsStore.GetString("server.bind_addr"); v != "" {
		bindAddr = v
	}
	port := cfg.Port
	if v := settingsStore.GetInt("server.port"); v > 0 {
		port = v
	}
	if bindAddr != cfg.BindAddr || port != cfg.Port {
		logger.Info("settings_overrode_addr",
			"bind_addr_from", cfg.BindAddr, "bind_addr_to", bindAddr,
			"port_from", cfg.Port, "port_to", port)
	}
	cfg.BindAddr = bindAddr
	cfg.Port = port
	if v := settingsStore.GetString("server.public_host"); v != "" {
		cfg.PublicHost = v
	}

	// TLS: serve HTTPS directly with a self-signed cert and/or automatic
	// Let's Encrypt. Wired from the same settings store as bind_addr/port
	// so the installer (or the Settings page) can persist it and the
	// backend picks it up on the next restart.
	tlsConfig, tlsMode := configureTLS(settingsStore, cfg.DataDir, logger)

	// Also re-apply the log level from the store so the operator's
	// Settings page choice takes precedence over the env-var. The
	// env-var is just a first-boot default.
	if v := settingsStore.GetString("logging.level"); v != "" {
		logging.SetLevel(v)
	}
	if v := settingsStore.GetString("logging.format"); v != "" && cfg.LogFile == "" {
		logging.SetFormat(v)
	}

	lv := libvirt.NewConnector(cfg.LibvirtURI, cfg)

	if err := lv.Open(); err != nil {
		logger.Warn("libvirt_connect_failed", "uri", cfg.LibvirtURI, "err", err)
		logger.Warn("running_in_offline_mode", "note", "VM operations will fail until libvirt is available")
		// Retry in background with exponential backoff so the backend
		// recovers automatically when libvirtd becomes available.
		go retryLibvirtConnect(eventCtx, logger, lv, cfg)
	} else {
		logger.Info("libvirt_connected", "uri", cfg.LibvirtURI)
		defer lv.Close()
		lv.EnsureDefaults()
		// Sweep stale import leftovers (orphan .tmp uploads and OVA
		// work dirs) from previous runs. The normal import path
		// cleans up after itself via defer, but a crash or OOM kill
		// leaves files behind. Anything older than 1 hour is safe
		// to remove because a successful import finishes in
		// minutes and any retry would create a new temp file.
		stats, err := lv.CleanupStaleImports(1 * time.Hour)
		if err != nil {
			logger.Warn("stale_import_cleanup_failed", "err", err)
		} else if stats.TmpFiles > 0 || stats.OvaDirs > 0 {
			logger.Info("janitor_cleanup_done",
				"tmp_files", stats.TmpFiles,
				"ova_dirs", stats.OvaDirs,
				"bytes_freed", stats.BytesFree,
				"mb_freed", stats.BytesFree/1024/1024)
		}

		// CIFS secret mapping: hydrate the in-memory map from disk
		// and warn about any secret we know about that's no longer
		// in libvirt (e.g. after a libvirtd reinstall). Neither
		// step is fatal — operators can recover via the API.
		if err := libvirt.LoadCIFSSecrets(lv); err != nil {
			logger.Warn("cifs_secrets_load_failed", "err", err.Error())
		}
		if err := libvirt.VerifyCIFSSecretsConsistency(context.Background(), lv); err != nil {
			logger.Warn("cifs_secrets_inconsistent", "err", err.Error())
		}
	}

	// If libvirt connected, run CIFS consistency check again with the
	// cancellable context (the first check above uses a throwaway ctx).
	if lv.IsConnected() {
		if err := libvirt.VerifyCIFSSecretsConsistency(eventCtx, lv); err != nil {
			logger.Warn("cifs_secrets_inconsistent", "err", err.Error())
		}
	}

	// Auth manager with the settings store wired in. The store is
	// consulted on every GenerateToken (TTL) and every Middleware
	// invocation (allow_api_tokens), so a Settings page change takes
	// effect on the next request — no restart.
	authMgr := auth.NewManagerWithPath(cfg.JWTSecret, settingsStore, cfg.RevokedFile())
	authMgr.SetSecureCookies(cfg.SecureCookies)

	// V13-SEC-02: global per-IP rate limiter (token bucket + sweeper).
	// settingsStore implements auth.RateLimitSettings, so enabled/rps/
	// burst/trusted_cidrs reload live from the Settings page.
	globalRateLimiter := auth.NewGlobalRateLimiter(settingsStore)
	defer globalRateLimiter.Close()
	loginLimiter := auth.NewLoginRateLimiterWithSettings(settingsStore)

	// API tokens: long-lived Bearer tokens for scripting. The store
	// is consulted by the auth middleware as a fallback after JWT
	// validation, so session cookies and API tokens share the same
	// Authorization header.
	tokensStore, err := tokens.New(cfg.DataDir)
	if err != nil {
		logger.Error("tokens_store_init_failed", "err", err)
		os.Exit(1)
	}
	authMgr.SetTokenValidator(func(plain string) (string, string, error) {
		t, err := tokensStore.Validate(plain)
		if err != nil {
			return "", "", err
		}
		return t.Username, t.Role, nil
	})
	// Sweep expired tokens every hour. Best-effort.
	go func() {
		t := time.NewTicker(1 * time.Hour)
		defer t.Stop()
		for range t.C {
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Error("tokens_purge_panicked", "recover", r)
					}
				}()
				if n, _ := tokensStore.PurgeExpired(); n > 0 {
					logger.Info("tokens_purged", "count", n)
				}
			}()
		}
	}()

	userStore, err := user.NewStore(cfg.DataDir)
	if err != nil {
		logger.Error("user_store_init_failed", "err", err)
		os.Exit(1)
	}

	auditLogger, err := audit.New(cfg.AuditLogFile())
	if err != nil {
		logger.Error("audit_log_init_failed", "err", err)
		os.Exit(1)
	}
	// Durability contract (V12-OPS-05): flush + fsync + close the audit
	// log on shutdown so the last acknowledged entries are never lost to
	// a buffered write.
	defer auditLogger.Close()

	// Job sweeper (V12-OPS-06): purge finished ISO/appliance jobs older
	// than 24h on a 5-minute ticker. queued/running jobs are never
	// removed, regardless of age.
	api.StartJobSweeper(eventCtx, 5*time.Minute, 24*time.Hour, logger.Info)
	logger.Info("job_sweeper_started", "interval", "5m", "ttl", "24h")

	// Nodes registry: every libvirt host the backend knows about.
	// The local node is auto-created from cfg.LibvirtURI; remote
	// nodes are added via /api/nodes (admin only).
	nodesReg, err := nodes.New(cfg.DataDir, cfg.LibvirtURI)
	if err != nil {
		logger.Error("nodes_registry_init_failed", "err", err)
		os.Exit(1)
	}
	logger.Info("nodes_loaded", "path", nodesReg.Path(), "count", len(nodesReg.List()))

	// Event hub for SSE broadcasts (VM state changes)
	hub := events.NewHub()

	// Start the libvirt event loop (with polling fallback) so the SSE
	// channel receives VM state changes in realtime.
	stopEventLoop := lv.StartEventLoop(eventCtx, hub, 4*time.Second)
	defer stopEventLoop()

	// Backup v2: multi-target / schedule / retention system.
	// The runner is wired with the same dataDir the rest of the
	// backend uses, so a backup of {DataDir} covers every file
	// the running server depends on (users.json, audit.log,
	// backup/, nodes.json, api-tokens.json, jwt.key, etc.).
	//
	// computeBackend is declared here (assigned once the KVM connector
	// and optional LXD backend are built below) so the runner's VMSource
	// / XML / snapshot / export closures see the FINAL combined backend:
	// containers from LXD are backed up via their native export stream.
	var computeBackend compute.Backend
	backupStore, err := backupstore.New(cfg.DataDir)
	if err != nil {
		logger.Error("backupstore_init_failed", "err", err)
		os.Exit(1)
	}
	backupRunner := backupstore.NewRunnerWithConfig(
		backupStore,
		cfg.DataDir,
		func() backupstore.BackupConfig {
			return backupstore.BackupConfig{
				MaxFileSizeMB: settingsStore.GetInt("backup.max_file_size_mb"),
				VerifyOnWrite: settingsStore.GetBool("backup.verify_on_write"),
			}
		},
		// VMSource lets the runner honour per-target VMFilter
		// (all / include / exclude) and per-target VMIDs at
		// backup time. A failure here aborts the run rather
		// than silently writing an empty archive. The combined
		// backend merges KVM VMs and LXD containers (v1.4).
		func() ([]models.VM, error) { return computeBackend.ListDomains() },
		// VMXMLSource returns the libvirt <domain> XML for
		// each in-scope VM, used to populate domain.xml in
		// the per-VM archive. Without this the per-VM tars
		// would be missing the XML and a restore would be
		// incomplete. main.go is the only production caller;
		// tests can pass nil.
		func(id string) (string, error) { return computeBackend.GetDomainXML(id) },
		// VMSnapshotSource returns snapshot metadata and
		// overlay volumes for each in-scope VM, used to
		// populate the snapshots/ entries in the per-VM
		// archive. Optional; nil skips snapshot data.
		func(id string) ([]backupstore.SnapshotBackup, error) {
			return computeBackend.ExportSnapshots(id)
		},
		// VMExportSource streams the native LXD export for
		// containers straight into the per-VM archive
		// (io.Copy from /1.0/instances/<name>/export — no
		// double buffering). KVM VMs never take this path.
		func(ctx context.Context, vm models.VM, w io.Writer) (int64, error) {
			if vm.Hypervisor != "incus" {
				return 0, fmt.Errorf("instance %q is not an LXD container", vm.ID)
			}
			res, xerr := computeBackend.ExportDomain(ctx, vm.ID, compute.ExportBackupOptions{Compress: "gzip"}, w)
			return res.TotalBytes, xerr
		},
		logger,
	)
	go backupRunner.Start(eventCtx)
	logger.Info("backupstore_loaded", "targets", len(backupStore.ListTargets()), "schedules", len(backupStore.ListSchedules()))
	// V13-BCK-03: retention janitor — a dedicated 6h goroutine, decoupled
	// from the cron ticker that fires backup jobs. A failing target is
	// logged and skipped; it never aborts the cycle.
	backupstore.StartRetentionJanitor(eventCtx, 6*time.Hour, backupStore, logger)
	logger.Info("retention_janitor_started", "interval", "6h")

	// Metrics collector: 5s sampling, in-memory ring buffer per VM.
	metrics := libvirt.NewMetricsCollector(lv, hub)
	go metrics.Run(eventCtx)

	// V13-C-03/04: metric history (bucketed time series -> JSONL files)
	// and the alert state machine. The collector feeds both via its sink;
	// the history store flushes completed buckets to disk every minute.
	metricHist := metrics2.NewTimeSeriesStore(cfg.DataDir)
	if mErr := metricHist.Load(); mErr != nil {
		logger.Warn("metrics_history_load_failed", "err", mErr)
	}
	go metricHist.Run(eventCtx)

	// Host metrics collector: 5s sampling, in-memory ring buffer.
	hostMetrics := libvirt.NewHostMetricsCollector(hub)
	go hostMetrics.Run(eventCtx)

	// Notification/alert subsystem. The notifier stores its secrets
	// in {dataDir}/notify-secrets.json (0600), kept separate from the
	// config backup. Defaults: alerts disabled until the operator
	// enables a channel.
	notifier, nerr := notify.New(cfg.DataDir, notify.Config{
		Enabled:          false,
		CheckIntervalSec: 60,
	}, logger)
	if nerr != nil {
		logger.Warn("notify_init_failed", "err", nerr)
	} else {
		// Alert engine: evaluates VM downtime, low disk and backup
		// failures on an interval. Sources wired to live runtime
		// state; a nil notifier or failed init just disables alerts.
		sources := notify.Sources{
			VMState: func() (map[string]bool, error) {
				out := map[string]bool{}
				if lv == nil {
					return out, nil
				}
				vms, err := lv.ListDomains()
				if err != nil {
					return nil, err
				}
				for _, vm := range vms {
					out[vm.Name] = vm.State == models.VMStateRunning
				}
				return out, nil
			},
			DiskFreePercent: func() (int, error) {
				var st syscall.Statfs_t
				if err := syscall.Statfs(cfg.DataDir, &st); err != nil {
					return 0, err
				}
				total := st.Blocks * uint64(st.Bsize)
				if total == 0 {
					return 100, nil
				}
				free := st.Bavail * uint64(st.Bsize)
				return int(free * 100 / total), nil
			},
			LastBackupResult: func() map[string]string {
				out := map[string]string{}
				for _, j := range backupStore.ListJobs(5) {
					if _, ok := out[j.TargetID]; ok {
						continue
					}
					out[j.TargetID] = j.Status
				}
				return out
			},
		}
		engine := notify.NewEngine(notifier, sources, time.Duration(60)*time.Second, time.Hour, logger)
		go engine.Run(eventCtx)
		logger.Info("notify_ready")
	}

	// V13-C-04: metric alert state machine (PENDING -> FIRING with
	// cooldown). Fed by the collector sink, evaluates every sample.
	alerter := metrics2.NewAlertEngine(cfg.DataDir, notifier, hub)
	if aErr := alerter.Load(); aErr != nil {
		logger.Warn("alerts_load_failed", "err", aErr)
	}
	metrics.SetSink(func(vmID string, at time.Time, m models.VMMetrics) {
		metricHist.Record(vmID, at, m)
		alerter.Evaluate(vmID, at, m)
	})

	// Firewall subsystem: per-VM rules + port forwards via nftables.
	// Rules are rebuilt and applied at startup so they survive service
	// restarts. Failures are logged but never fatal — the backend still
	// serves; the operator sees the error via the UI.
	fwStore := firewall.NewStore(cfg.DataDir)
	if ferr := fwStore.Load(); ferr != nil {
		logger.Warn("firewall_load_failed", "err", ferr)
	}
	var ipResolver firewall.IPResolver
	if lv != nil {
		ipResolver = lv.GetDomainIP
	}
	fwMgr := firewall.NewManager(fwStore, ipResolver, cfg.Port, logger)
	// V13-C-01: host firewall store + Safe Apply. The confirmed host
	// ruleset is loaded and applied at startup so it survives restarts
	// and never drifts.
	fwHostStore := firewall.NewHostStore(cfg.DataDir)
	if ferr := fwHostStore.Load(); ferr != nil {
		logger.Warn("firewall_host_load_failed", "err", ferr)
	}
	fwMgr.SetHostStore(fwHostStore)
	if _, ferr := fwMgr.Apply(); ferr != nil {
		logger.Warn("firewall_apply_failed", "err", ferr)
	} else {
		logger.Info("firewall_ready")
	}

	// VM power scheduler: automatic start/stop via cron. Schedules
	// are re-registered at startup.
	vmSchedStore := vmsched.NewStore(cfg.DataDir)
	if serr := vmSchedStore.Load(); serr != nil {
		logger.Warn("vmsched_load_failed", "err", serr)
	}
	vmScheduler := vmsched.NewScheduler(vmSchedStore, func(vmID, action string) error {
		if lv == nil {
			return fmt.Errorf("libvirt not connected")
		}
		switch action {
		case "start":
			return lv.StartDomain(vmID)
		case "stop":
			return lv.ShutdownDomain(vmID)
		}
		return fmt.Errorf("unknown action %q", action)
	}, logger)
	vmScheduler.Start()
	logger.Info("vmsched_ready")

	// V1.4-Fase 0: the ComputeBackend seam. KVM is the only backend; the
	// adapter wraps the existing connector so api handlers never touch
	// libvirt types. Bind the managed-bridge/network predicates too.
	computeBackend = compute.NewKVMBackend(lv)
	compute.BindHelpers(libvirt.IsManagedBridge, libvirt.IsManagedNetwork)

	// V1.4-Fase 1: optional LXD container backend. Fail-safe: when
	// disabled or when the daemon socket is unreachable, the backend
	// degrades to KVM-only with zero regression.
	// v2.4: the backend uses ONE network model — real OS-level Linux
	// bridges (vmbr0, vmbr1, …). No libvirt network-name resolver is
	// needed: the network IS the bridge. A per-container metrics collector
	// shares the KVM sink.
	var incusMetrics *incus.MetricsCollector
	if cfg.IncusEnabled {
		incusSocket := cfg.IncusSocket
		if incusBackend, lerr := incus.NewIncusBackend(incusSocket); lerr != nil {
			logger.Warn("incus_disabled", "err", lerr, "socket", incusSocket)
		} else {
			computeBackend = compute.NewCombined(computeBackend, incusBackend)
			logger.Info("incus_connected", "socket", incusSocket, "server_version", mustContainerVersion(incusBackend))
			// Container metrics feed the same history store + alert
			// engine as KVM, so charts and alerts just work for LXC.
			incusMetrics = incusBackend.NewMetricsCollector(hub)
			go incusMetrics.Run(eventCtx)
			incusMetrics.SetSink(func(vmID string, at time.Time, m models.VMMetrics) {
				metricHist.Record(vmID, at, m)
				alerter.Evaluate(vmID, at, m)
			})
		}
	}

	router := api.NewRouter(cfg, lv, computeBackend, authMgr, globalRateLimiter, loginLimiter, userStore, hub, metrics, hostMetrics, auditLogger, settingsStore, tokensStore, nodesReg, backupStore, backupRunner, notifier, fwStore, fwMgr, vmSchedStore, vmScheduler, metricHist, alerter, incusMetrics)

	srv := &http.Server{
		Addr:    net.JoinHostPort(cfg.BindAddr, fmt.Sprintf("%d", cfg.Port)),
		Handler: router,
		// ReadHeaderTimeout caps how long the client may take to send
		// the request headers (slowloris protection). 30s is
		// generous for browsers and proxies on a LAN.
		ReadHeaderTimeout: 30 * time.Second,
		// WriteTimeout is the upper bound for the full request →
		// response cycle. We allow 30 minutes so a 5 GB import
		// (upload + extract + libvirt define) can finish without
		// the server preemptively closing the connection. There is
		// no ReadTimeout set, so the client can take as long as it
		// needs for the request body (the actual upload).
		WriteTimeout: 30 * time.Minute,
		// IdleTimeout kills keep-alive sockets that go silent for
		// too long; protects against leaked goroutines on dropped
		// clients.
		IdleTimeout: 120 * time.Second,
	}
	if tlsConfig != nil {
		srv.TLSConfig = tlsConfig
	}

	go func() {
		logger.Info("server_starting",
			"addr", srv.Addr,
			"data_dir", cfg.DataDir,
			"public_host", cfg.PublicHost,
			"tls_mode", string(tlsMode),
		)
		var serveErr error
		if tlsConfig != nil {
			// With TLSConfig.GetCertificate or Certificates set, the
			// empty file args are fine: the certificate is resolved
			// from the config, not from disk paths.
			serveErr = srv.ListenAndServeTLS("", "")
		} else {
			serveErr = srv.ListenAndServe()
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Error("server_failed", "err", serveErr)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	// SIGHUP is intentionally ignored. The service unit uses it for
	// `systemctl reload`, and the config is loaded once at startup
	// (not re-read), so there is nothing to do. Without this, Go's
	// default behavior would exit on SIGHUP, which combined with
	// Restart=always would make every reload silently restart the
	// process.
	signal.Ignore(syscall.SIGHUP)
	<-quit

	logger.Info("shutting_down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Release the auth Manager's background GC goroutine (token blacklist).
	authMgr.Close()

	if err := srv.Shutdown(ctx); err != nil {
		logger.Error("forced_shutdown", "err", err)
		os.Exit(1)
	}
	logger.Info("server_stopped")
}

// retryLibvirtConnect attempts to establish the libvirt connection in the
// background with exponential backoff (10s → 20s → 40s → 60s cap). Once
// connected it runs EnsureDefaults, stale-import cleanup, and CIFS secret
// loading — the same initialisation the main path performs when the initial
// lv.Open() succeeds. Stops when ctx is cancelled (server shutdown).
func retryLibvirtConnect(ctx context.Context, logger *slog.Logger, lv *libvirt.Connector, cfg *config.Config) {
	backoff := 10 * time.Second
	maxBackoff := 60 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
			if err := lv.Open(); err != nil {
				logger.Warn("libvirt_retry_failed", "backoff", backoff.String(), "err", err)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				continue
			}
			logger.Info("libvirt_reconnected", "uri", cfg.LibvirtURI)
			lv.EnsureDefaults()

			stats, err := lv.CleanupStaleImports(1 * time.Hour)
			if err != nil {
				logger.Warn("stale_import_cleanup_failed", "err", err)
			} else if stats.TmpFiles > 0 || stats.OvaDirs > 0 {
				logger.Info("janitor_cleanup_done",
					"tmp_files", stats.TmpFiles,
					"ova_dirs", stats.OvaDirs,
					"bytes_freed", stats.BytesFree,
					"mb_freed", stats.BytesFree/1024/1024)
			}
			if err := libvirt.LoadCIFSSecrets(lv); err != nil {
				logger.Warn("cifs_secrets_load_failed", "err", err.Error())
			}
			if err := libvirt.VerifyCIFSSecretsConsistency(ctx, lv); err != nil {
				logger.Warn("cifs_secrets_inconsistent", "err", err.Error())
			}
			return
		}
	}
}
