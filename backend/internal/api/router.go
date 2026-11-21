package api

import (
	"net/http"
	"slices"
	"strings"
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
	"webkvm/internal/models"
	"webkvm/internal/netguard"
	"webkvm/internal/nodes"
	"webkvm/internal/notify"
	"webkvm/internal/sysutil"
	"webkvm/internal/tokens"
	"webkvm/internal/user"
	"webkvm/internal/vmsched"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

func NewRouter(
	cfg *config.Config,
	lv *libvirt.Connector,
	compute compute.Backend,
	authMgr *auth.Manager,
	globalRateLimiter *auth.GlobalRateLimiter,
	loginLimiter *auth.LoginRateLimiter,
	jail *netguard.Jail,
	us *user.Store,
	hub *events.Hub,
	metrics *libvirt.MetricsCollector,
	hostMetrics *libvirt.HostMetricsCollector,
	auditLogger *audit.Logger,
	settings *configstore.Store,
	tokensStore *tokens.Store,
	nodesReg *nodes.Registry,
	backupStore *backupstore.Store,
	backupRunner *backupstore.Runner,
	notifier *notify.Notifier,
	fwStore *firewall.Store,
	fwMgr *firewall.Manager,
	vmSchedStore *vmsched.Store,
	vmScheduler *vmsched.Scheduler,
	hostSchedStore *hostsched.Store,
	hostScheduler *hostsched.Scheduler,
	metricHist *metrics.TimeSeriesStore,
	alerter *metrics.AlertEngine,
	incusMetrics *incus.MetricsCollector,
) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.Recoverer)
	// Mounted before everything else so that error responses produced by
	// the middleware below (rate limit, auth, body limit) carry the
	// headers too — a 401 page is still a page a browser renders.
	r.Use(securityHeaders)
	r.Use(requestLogger)
	r.Use(limitRequestBody)
	origins := strings.FieldsFunc(cfg.CORSOrigin, func(r rune) bool { return r == ',' || r == ' ' })
	// V13-SEC-01: cookie sessions need AllowCredentials when a real
	// cross-origin deployment is configured (explicit origins). A
	// wildcard "*" cannot carry credentials, so it stays as-is
	// (same-origin installs don't need CORS at all).
	allowCreds := len(origins) > 0 && !slices.Contains(origins, "*")
	// No CORS_ORIGIN configured (the default) means same-origin only: the
	// bundled SPA is served by this backend, so no CORS headers are needed.
	// (rs/cors treats an empty origin list as "*", hence the guard.)
	if len(origins) > 0 {
		r.Use(cors.Handler(cors.Options{
			AllowedOrigins:   origins,
			AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
			AllowCredentials: allowCreds,
			MaxAge:           300,
		}))
	}
	SetAllowedOrigins(origins)
	r.Use(authMgr.Middleware)
	// V13-SEC-02: global per-IP rate limit. Mounted AFTER the JWT
	// middleware so the Bearer exemption can only match already-validated
	// API-token requests (a forged header is 401'd before we ever see it).
	if globalRateLimiter != nil {
		r.Use(globalRateLimiter.Middleware)
	}
	r.Use(auth.SessionEnforcer(
		us.SessionStatus,
		// Paths the user is allowed to hit with must_change=true. The
		// epoch/active checks still apply to them (a revoked token gets 401).
		// /api/auth/me|logout|refresh — so the frontend can detect the flag
		// and establish a fresh, current-epoch token. Registering a passkey
		// or enabling 2FA is NOT allowed while a password change is pending.
		// /api/users/me/password — the actual recovery path.
		// /api/health, cert, webhook — public paths (no identity attached).
		"/api/auth/me",
		"/api/auth/logout",
		"/api/auth/refresh",
		"/api/users/me/password",
		"/api/health",
		"/api/system/cert",
		"/api/alerts/webhook",
	))

	snippetsStore, _ := cloudinit.NewSnippetStore(cfg.DataDir)

	var clusterFW *firewall.ClusterManager
	if fwStore != nil {
		if manager, err := firewall.NewClusterManager(fwStore.ClusterProfilePath(), nil); err == nil {
			clusterFW = manager
		}
	}

	h := &Handler{
		lv:            lv,
		compute:       compute,
		auth:          authMgr,
		loginLimiter:  loginLimiter,
		jail:          jail,
		userStore:     us,
		cfg:           cfg,
		hub:           hub,
		gs:            newGroupsStore(cfg.GroupsFile()),
		appStore:      appliances.NewStore(cfg.AppliancesFile()),
		helperScripts: helperscripts.NewStore(cfg.HelperScriptsFile()),
		metrics:       metrics,
		hostMetrics:   hostMetrics,
		audit:         auditLogger,
		settings:      settings,
		tokens:        tokensStore,
		nodes:         nodesReg,
		sshKeys:       nodes.SharedSSHKeyStore(cfg.DataDir),
		libvirtPool:   libvirt.DefaultPool(),
		backupStore:   backupStore,
		backupRunner:  backupRunner,
		notifier:      notifier,
		fwStore:       fwStore,
		fwMgr:         fwMgr,
		clusterFW:     clusterFW,
		haInstall:     hainstall.New(cfg.DataDir, sysutil.DefaultRunner),
		vmSchedStore:  vmSchedStore,
		vmScheduler:   vmScheduler,
		hostSched:     hostSchedStore,
		hostScheduler: hostScheduler,
		snippets:      snippetsStore,
		metricHist:    metricHist,
		alerter:       alerter,
		incusMetrics:  incusMetrics,
		webauthn:      auth.NewWebAuthnManager(cfg),
		workloads:     newWorkloadStore(cfg.DataDir),
		StartedAt:     time.Now(),
	}

	// Wire the scheduled-upgrade callback now that the Handler exists.
	if hostScheduler != nil {
		hostScheduler.SetRun(h.runScheduledUpgrade)
	}

	h.EnsureMediaDirs()

	r.Get("/console/{id}", h.ConsolePage)
	r.Mount("/static", staticRouter())

	// Cover images are served like static assets so they can be used
	// directly in <img src="..."> without an Authorization header.
	// UUID-in-URL acts as the access control (consistent with the VNC
	// console pattern in the original design brief).
	r.Group(func(r chi.Router) {
		r.Get("/api/covers/{path}", h.ServeCover)
		r.Get("/api/media/{id}/raw", h.GetMediaRaw)
		// Branding has to be readable before login: the logo and the
		// favicon are part of the login screen itself. It exposes only
		// two media references, which are already public via the route
		// above, so there is nothing here to protect.
		r.Get("/api/branding", h.GetBranding)
	})
	h.mountIncusCluster(r)
	r.Get("/api/health", h.Health)
	r.Get("/metrics", h.PrometheusMetrics)
	r.Get("/api/metrics/prometheus", h.PrometheusMetrics)
	r.Get("/api/metrics/grafana-dashboard", h.GrafanaDashboard)
	r.Get("/api/metrics/alert-rules", h.AlertRules)
	r.Post("/api/alerts/webhook", h.AlertmanagerWebhook)
	r.Get("/api/alerts/active", h.ListActiveAlerts)
	r.Get("/api/alerts/incidents", h.ListIncidents)
	r.With(auth.RequireRole(modelsRoleAdmin())).Delete("/api/alerts/incidents/resolved", h.ClearResolvedIncidents)
	r.Get("/api/tags", h.ListAllTags)
	r.Get("/api/events", h.EventsSSE)
	r.Post("/api/events/ticket", h.EventsTicket)

	// Frontend SPA: serve embedded Svelte build on /. Acts as catch-all
	// for anything that isn't /api/*, /console/* or /static/*, so the
	// Svelte router can handle deep links after a page refresh.
	fh := frontendHandler()
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/console") || strings.HasPrefix(p, "/serial") || strings.HasPrefix(p, "/host-terminal") || strings.HasPrefix(p, "/static/") || p == "/metrics" {
			http.NotFound(w, req)
			return
		}
		fh.ServeHTTP(w, req)
	}))

	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/login", h.Login)
		r.Get("/oidc/status", h.GetOIDCStatus)
		r.Get("/oidc/login", h.OIDCLogin)
		r.Get("/oidc/callback", h.OIDCCallback)
		r.Post("/login/2fa", h.Login2FA)
		r.Post("/webauthn/login/begin", h.WebAuthnLoginBegin)
		r.Post("/webauthn/login/finish", h.WebAuthnLoginFinish)
		// Logout/refresh/me/2fa/webauthn are authenticated; the auth middleware
		// already enforces the JWT.
		r.Post("/logout", h.Logout)
		r.Post("/refresh", h.Refresh)
		r.Get("/me", h.Me)
		r.Post("/2fa/setup", h.Setup2FA)
		r.Post("/2fa/enable", h.Enable2FA)
		r.Post("/2fa/disable", h.Disable2FA)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("viewer"))
			r.Post("/webauthn/register/begin", h.WebAuthnRegisterBegin)
			r.Post("/webauthn/register/finish", h.WebAuthnRegisterFinish)
			r.Get("/webauthn/credentials", h.WebAuthnListCredentials)
			r.Put("/webauthn/credentials/{id}", h.WebAuthnUpdateCredential)
			r.Delete("/webauthn/credentials/{id}", h.WebAuthnDeleteCredential)
		})
	})

	// Async jobs (VM clone / snapshot / downloads).
	r.Route("/api/jobs", func(r chi.Router) {
		r.Use(auth.RequireAtLeast("viewer"))
		r.Get("/", h.ListJobs)
		r.Get("/stats", h.GetJobStats)
		r.Get("/history", h.GetJobHistory)
		r.Get("/{id}/log", h.GetJobLog)
		r.Get("/{id}", h.GetDownloadJob)
	})

	// User management: restricted to admins, except self-service password change.
	r.Route("/api/users", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Get("/", h.ListUsers)
			r.Post("/", h.CreateUser)
			r.Put("/{username}", h.UpdateUser)
			r.Delete("/{username}", h.DeleteUser)
			r.Post("/{username}/revoke-sessions", h.RevokeUserSessions)
			r.Get("/{username}/usage", h.GetUserQuotaUsage)
		})
		// Self-service: any authenticated user can change their own
		// password (and only their own — handler enforces username).
		r.Put("/me/password", h.ChangeMyPassword)
		// Self-service: a user may see their own quota consumption.
		// Registered before the admin-only /{username}/usage above would
		// match, since chi resolves the static "me" segment first.
		r.Get("/me/usage", h.GetMyQuotaUsage)
	})

	r.Route("/api/vms", func(r chi.Router) {
		r.Get("/", h.ListVMs)
		// Cross-fleet snapshot view (every VM's snapshots in one call,
		// vs. the per-VM /vms/{id}/snapshots below). Same visibility as
		// ListVMs — chi resolves this static segment before the /{id}
		// wildcard route, so it can't be shadowed by a VM literally
		// named "snapshots".
		r.Get("/snapshots", h.ListAllSnapshots)
		r.Get("/incus-profiles", h.ListIncusProfiles)
		r.Get("/incus-images", h.ListIncusImages)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/", h.CreateVM)
		})
		r.Route("/{id}", func(r chi.Router) {
			// ACL / Ownership gate: a caller must be admin, recorded owner,
			// or have matching AllowedGroups / AllowedTags.
			r.Use(h.requireVMOwnership)
			r.Use(h.nodeDispatch)
			h.mountVMRoutes(r, modeLocal)
		})
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/import", h.ImportVM)
			r.Post("/import-ova", h.ImportOVA)
		})
	})

	// Admin-only host terminal.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAtLeast("admin"))
		r.Get("/api/host/terminal", h.HostTerminal)
		r.Post("/api/host/terminal-ticket", h.HostTerminalTicket)
	})

	// VM templates.
	r.Route("/api/templates", func(r chi.Router) {
		r.Get("/", h.ListTemplates)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/{id}/instantiate", h.InstantiateTemplate)
		})
	})

	// Community appliances (curated official disk images). Listing is
	// open to any authenticated user; creating/editing/deleting is
	// admin-only. Deploying requires at least operator.
	r.Route("/api/appliances", func(r chi.Router) {
		r.Get("/", h.ListAppliances)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/{id}/deploy", h.DeployAppliance)
			// Operators may inspect what a deploy will run; only admins
			// can change it (script travels in the admin PUT/POST body).
			r.Get("/{id}/provision", h.GetApplianceProvision)
		})
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Post("/", h.CreateAppliance)
			r.Put("/{id}", h.UpdateAppliance)
			r.Delete("/{id}", h.DeleteAppliance)
		})
	})

	// Community helper scripts (third-party installers from the
	// community-scripts project). Strictly admin-only in full: third-party shell
	// that runs as root.
	r.Route("/api/helper-scripts", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/", h.ListHelperScripts)
		r.Post("/refresh", h.RefreshHelperScripts)
		r.Get("/{slug}/provision", h.GetHelperScriptProvision)
	})

	r.Route("/api/images", func(r chi.Router) {
		r.Get("/containers", h.ListContainerImages)
		r.Get("/cloud-base", h.ListBaseCloudImages)
		r.Get("/isos", h.withNodeSelector(func(x *Handler) http.HandlerFunc { return x.ListISOs }))
		// Where Incus caches container images. Readable by anyone who
		// can see the images; changing it is a daemon-wide setting and
		// stays admin-only below.
		r.Get("/incus-volume", h.GetIncusImagesVolume)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/containers/pull", h.PullContainerImage)
			r.Post("/cloud-base/pull", h.PullBaseCloudImage)
			r.Post("/isos/download", h.DownloadISO)
		})

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Delete("/containers/{fingerprint}", h.DeleteContainerImage)
			r.Delete("/cloud-base/{id}", h.DeleteBaseCloudImage)
			r.Delete("/isos/{pool}/{name}", h.DeleteISO)
			r.Put("/incus-volume", h.SetIncusImagesVolume)
		})
	})

	r.Route("/api/media", func(r chi.Router) {
		r.Get("/", h.ListMedia)
		r.Get("/{id}/raw", h.GetMediaRaw)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/upload", h.UploadMedia)
			r.Delete("/{id}", h.DeleteMedia)
			r.Post("/apply-usage", h.ApplyMediaUsage)
		})
	})

	r.Route("/api/storage", func(r chi.Router) {
		r.Get("/pools", h.withNodeSelector(func(x *Handler) http.HandlerFunc { return x.ListPools }))
		r.Get("/pools/breakdown", h.GetStorageBreakdown)
		r.Get("/volumes", h.withNodeSelector(func(x *Handler) http.HandlerFunc { return x.ListVolumes }))
		r.Get("/isos", h.withNodeSelector(func(x *Handler) http.HandlerFunc { return x.ListISOs }))
		r.Get("/jobs/{id}", h.GetDownloadJob)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/volumes", h.CreateVolume)
			r.Patch("/volumes/{pool}/{name}", h.ResizeVolume)
			r.Post("/upload-iso", h.UploadISO)
			r.Post("/upload-iso/raw", h.UploadISOByCURL)
			r.Post("/upload-disk", h.UploadDisk)
			r.Post("/download-iso", h.DownloadISO)
			r.Post("/probe-disk", h.ProbeStorageDisk)
			// Moving a volume or ISO between pools stays at operator
			// level: it is bounded by the caller's pool ACL and never
			// defines or reconfigures a pool.
			r.Post("/volumes/{pool}/{name}/move", h.MoveVolume)
		})

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			// Creating/updating a storage pool is a host-level
			// mutation (pool paths map to host directories; CIFS creds in
			// UpdatePool), so it must be admin-only — operators keep full
			// read access (GET /pools) and volume/ISO uploads, but cannot
			// define or reconfigure pools.
			r.Post("/pools", h.CreatePool)
			r.Put("/pools/{name}", h.UpdatePool)
			r.Delete("/pools/{name}", h.DeletePool)
			r.Post("/browse-remote", h.BrowseRemote)
			// Local folder picker for the pool path. Admin-only for the
			// same reason as pool creation itself: it reads directory
			// names off the server's own filesystem.
			r.Post("/browse-local", h.BrowseLocal)
			r.Delete("/volumes/{pool}/{name}", h.DeleteVolume)
			r.Delete("/isos/{pool}/{name}", h.DeleteISO)
			r.Patch("/isos/{pool}/{name}", h.RenameISO)
		})
	})

	r.Route("/api/networks", func(r chi.Router) {
		r.Get("/", h.withNodeSelector(func(x *Handler) http.HandlerFunc { return x.ListNetworks }))
		r.Get("/{id}/leases", h.ListNetworkLeases)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/{id}/start", h.StartNetwork)
			r.Post("/{id}/stop", h.StopNetwork)
			r.Delete("/{id}/leases/{mac}", h.ReleaseNetworkLease)
		})
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			// Creating, configuring or deleting a network bridge is a host-level
			// mutation, so it is admin-only, mirroring /api/pools.
			r.Post("/", h.CreateNetwork)
			r.Put("/{id}", h.UpdateNetwork)
			r.Delete("/{id}", h.DeleteNetwork)
		})
	})

	r.Route("/api/host", func(r chi.Router) {
		r.Get("/", h.GetHostInfo)
		r.Get("/stats", h.GetHostStats)
		r.Get("/metrics", h.GetHostMetrics)
		r.Get("/interfaces", h.listNodeLocalOnly(h.ListHostInterfaces))
		r.Get("/disks", h.ListHostDisks)
		r.Get("/disks/filesystems", h.ListFilesystems)
		// Broken systemd automount units shadowing a directory. Read-only
		// diagnostics, so it sits with the other disk queries.
		r.Get("/disks/orphan-mounts", h.ListOrphanMounts)
		// Host capabilities (supported video/network/audio models, CPU
		// modes) read-only for authenticated users: the VM create/edit
		// pickers need it and it exposes no host secrets.
		r.Get("/capabilities", h.GetCapabilities)
		// Read-only device enumeration for the VM-create passthrough
		// pickers (operator+): lsusb/lspci-equivalent data, no mutation.
		// Anything host-mutating stays admin-only.
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Get("/usb-devices", h.ListHostUSBDevices)
			r.Get("/pci-devices", h.ListHostPCIDevices)
			r.Get("/pci-preflight", h.GetHostPCIPreflight)
		})
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Get("/zvols", h.ListHostZVols)
			r.Post("/zvols", h.CreateHostZVol)
			r.Get("/zpools", h.ListHostZPools)
			r.Post("/zpools", h.CreateHostZPool)
			r.Delete("/zpools/{name}", h.DestroyHostZPool)
			r.Get("/raid", h.ListHostRAID)
			r.Post("/raid", h.CreateHostRAID)
			r.Post("/raid/{device}/sync", h.SyncHostRAID)
			r.Delete("/raid/{device}", h.StopHostRAID)
			r.Post("/disks/wipe", h.WipeHostDisk)
			r.Post("/disks/unmount", h.UnmountHostDisk)
			r.Post("/disks/initialize-directory", h.InitHostDiskDirectory)
			r.Get("/disks/smart", h.ProbeHostDiskSMART)
			r.Post("/disks/selftest", h.RunHostDiskSelfTest)
			r.Post("/zpools/{name}/scrub", h.ScrubHostZPool)
			r.Post("/interfaces/{name}/configure", h.ConfigureHostInterface)
			r.Post("/bonds", h.CreateHostBond)
			r.Delete("/bonds/{name}", h.DeleteHostBond)
			r.Post("/pci-devices/{address}/bind-vfio", h.BindHostPCIDeviceVFIO)
			r.Post("/pci-devices/{address}/unbind-vfio", h.UnbindHostPCIDeviceVFIO)
			r.Get("/updates", h.GetHostUpdates)
			r.Post("/updates/refresh", h.RefreshHostUpdates)
			r.Post("/updates/upgrade", h.UpgradeHostUpdates)
			r.Get("/updates/schedule", h.GetHostUpdateSchedule)
			r.Put("/updates/schedule", h.SetHostUpdateSchedule)
			r.Post("/reboot", h.RebootHost)
		})
	})

	r.Route("/api/groups", func(r chi.Router) {
		r.Get("/", h.ListGroups)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Post("/", h.CreateGroup)
			r.Put("/{name}", h.UpdateGroup)
			r.Delete("/{name}", h.DeleteGroup)
		})
	})

	r.Route("/api/system", func(r chi.Router) {
		r.Get("/cert", h.SystemCert)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Get("/status", h.SystemStatus)
			r.Get("/logs", h.SystemLogs)
			r.Post("/restart", h.SystemRestart)
			r.Post("/apply-restart", h.ApplyRestartSettings)
			r.Post("/update", h.SystemUpdate)
			r.Post("/backup", h.SystemBackup)
			r.Get("/backups", h.SystemListBackups)
		})
	})

	// Audit log (read-only). Admin-only — entries carry every user's
	// actions, IPs, and action detail across the whole install.
	r.Route("/api/audit", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/", h.ListAudit)
		r.Get("/export", h.ExportAudit)
	})

	// Settings (config store). Restricted to admins.
	r.Route("/api/settings", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/schema", h.GetSettingsSchema)
		r.Get("/", h.GetSettings)
		r.Put("/", h.SetSettings)
		r.Post("/reset", h.ResetSettings)
		r.Post("/apply-live", h.ApplyLiveSettings)
		r.Get("/jail", h.ListJailedIPs)
		r.Post("/jail/ban", h.ManualBanIP)
		r.Post("/jail/unban", h.UnbanJailedIP)
		r.Get("/jail/whitelist", h.GetJailWhitelist)
		r.Post("/jail/whitelist", h.AddJailWhitelist)
		r.Delete("/jail/whitelist", h.RemoveJailWhitelist)
		r.Get("/jail/config", h.GetJailConfig)
		r.Put("/jail/config", h.UpdateJailConfig)
		r.Post("/jail/custom", h.AddCustomJail)
		r.Delete("/jail/custom/{id}", h.DeleteCustomJail)
	})

	// Notifications / alerts. Config reads are for any authenticated
	// user (never expose secrets); mutations, tests and history are
	// admin-only.
	// Admin-only in full: the config carries SMTP recipients, webhook
	// URLs and Telegram chat IDs, and the event log carries the alert
	// history of the whole fleet. Both used to be readable by any
	// authenticated user, viewers included, while the only UI that
	// renders them (NotificationsTab, inside Settings) is admin-only.
	r.Route("/api/notify", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/config", h.GetNotifyConfig)
		r.Get("/events", h.ListNotifyEvents)
		r.Group(func(r chi.Router) {
			r.Put("/config", h.UpdateNotifyConfig)
			r.Post("/test", h.TestNotify)
		})
	})

	// Browser push subscriptions are per authenticated user. The VAPID
	// public key is public by design; subscription mutations only touch
	// the caller's own endpoints.
	r.Route("/api/push", func(r chi.Router) {
		r.Get("/vapid-key", h.GetWebPushKey)
		r.Post("/subscribe", h.SubscribeWebPush)
		r.Delete("/subscribe", h.UnsubscribeWebPush)
		r.Post("/test", h.TestWebPush)
	})

	// Cloud-Init Studio & Snippets.
	r.Route("/api/cloudinit", func(r chi.Router) {
		r.Use(auth.RequireAtLeast("viewer"))
		r.Get("/snippets", h.ListCloudInitSnippets)
		r.Get("/snippets/{id}", h.GetCloudInitSnippet)
		r.Post("/preview", h.PreviewCloudInit)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/snippets", h.CreateCloudInitSnippet)
			r.Put("/snippets/{id}", h.UpdateCloudInitSnippet)
			r.Delete("/snippets/{id}", h.DeleteCloudInitSnippet)
		})
	})

	// API tokens (long-lived, for scripting). Owner-only mutations;
	// admins can list/delete any.
	r.Route("/api/tokens", func(r chi.Router) {
		r.Get("/", h.ListTokens)
		r.Post("/", h.CreateToken)
		r.Delete("/{id}", h.DeleteToken)
		r.Post("/{id}/revoke", h.RevokeToken)
	})

	// Nodes (libvirt hosts the backend can talk to). The local
	// node is auto-created; remote nodes are added by admins.
	// Admin-only in full: a node record is a hypervisor connection
	// descriptor (libvirt URI, host, credentials hints). The Nodes
	// page is admin-only in the sidebar already.
	r.Route("/api/nodes", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/", h.ListNodes)
		r.Get("/summary", h.GetClusterSummary)
		r.Post("/ping-all", h.PingAllNodes)
		r.Post("/test", h.TestNodeConnection)
		r.Get("/install-script", h.GetNodeDependencyInstaller)
		r.Post("/install", h.InstallNodeDependencies)
		r.Get("/{id}/incus-inventory", h.GetNodeIncusInventory)
		r.Get("/{id}", h.GetNode)
		r.Post("/{id}/ping", h.PingNode)
		r.Post("/{id}/drain", h.DrainNode)
		r.Post("/{id}/undrain", h.UndrainNode)
		r.Get("/{id}/drain-plan", h.GetNodeDrainPlan)
		r.Post("/{id}/evacuate", h.EvacuateNode)
		r.Get("/{id}/evacuate", h.GetNodeEvacuation)
		r.Post("/{id}/evacuate/cancel", h.CancelNodeEvacuation)
		r.Group(func(r chi.Router) {
			r.Post("/", h.CreateNode)
			r.Put("/{id}", h.UpdateNode)
			r.Delete("/{id}", h.DeleteNode)
		})
	})
	h.mountClusterSSH(r)

	// Cluster/HA is an optional module: when modules.cluster_ha is off these
	// routes answer 404 module_not_installed and never touch pcs/pacemaker.
	// The module installer itself is always reachable: it is how an operator
	// turns the optional feature on in the first place.
	r.Route("/api/modules/ha", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/", h.GetHAModule)
		r.Get("/compat", h.GetHACompat)
		r.Post("/preflight", h.PreflightHAModule)
		r.Post("/install", h.InstallHAModule)
		r.Post("/verify", h.VerifyHAModule)
		r.Post("/enable", h.EnableHAModule)
		r.Post("/disable", h.DisableHAModule)
		r.Post("/uninstall", h.UninstallHAModule)
		// Cluster formation: needs the packages, not the cluster_ha flag.
		r.Route("/cluster", func(r chi.Router) {
			r.Use(h.requireHAPackages)
			r.Post("/plan", h.PlanHACluster)
			r.Post("/setup", h.SetupHACluster)
			r.Get("/status", h.GetHAClusterStatus)
		})
	})

	r.Route("/api/ha", func(r chi.Router) {
		r.Use(h.requireClusterHA)
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/readiness", h.GetHAReadiness)
		r.Post("/fence-pve/verify", h.VerifyFencePVE)
		r.Get("/vm-migration", h.GetHAVMMigration)
		r.Post("/fencing/test", h.TestHAFencing)
		r.Get("/fencing/evidence", h.GetHAFencingEvidence)
		r.Post("/nodes/{name}/standby", h.StandbyHANode)
		r.Post("/nodes/{name}/unstandby", h.UnstandbyHANode)
		h.mountHAWorkloads(r)
	})

	r.Route("/api/cluster", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/members", h.GetIncusClusterMembers)
	})

	r.Route("/api/incus", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/healing", h.GetIncusHealing)
		r.Put("/healing", h.UpdateIncusHealing)
	})

	// Backup v2: targets, schedules, jobs, files.
	// Admin-only in full. The target list leaks absolute destination
	// paths, remote hosts, usernames and ports of every backup
	// location; the schedule list leaks which VMs are backed up and
	// when. The Backup page is already admin-only in the sidebar, so
	// no non-admin UI regresses.
	r.Route("/api/backup/targets", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/", h.ListBackupTargets)
		r.Group(func(r chi.Router) {
			r.Post("/", h.CreateBackupTarget)
			r.Post("/test", h.TestBackupTarget)
			r.Put("/{id}", h.UpdateBackupTarget)
			r.Delete("/{id}", h.DeleteBackupTarget)
			r.Post("/{id}/run", h.BackupNow)
			r.Get("/{id}/files", h.ListBackupsOnTarget)
			r.Delete("/{id}/files/{filename}", h.DeleteBackupFile)
			r.Delete("/{id}/config", h.DeleteBackupConfig)
			r.Delete("/{id}/runs/{suffix}", h.DeleteBackupRun)
			r.Post("/{id}/restore", h.RestoreBackup)
			r.Post("/{id}/restore-as-vm", h.RestoreAsVM)
			r.Get("/{id}/verify", h.VerifyBackup)
			r.Post("/{id}/reconcile", h.ReconcileBackupTarget)
			r.Post("/{id}/prune", h.PruneBackupTarget)
			r.Get("/{id}/chains", h.ListBackupChains)
			r.Delete("/{id}/chains/{vmid}", h.DeleteBackupChain)
		})
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(models.RoleAdmin))
			r.Get("/checkpoints/{vmid}", h.ListCheckpoints)
		})
	})
	r.Route("/api/backup/schedules", func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/", h.ListBackupSchedules)
		r.Group(func(r chi.Router) {
			r.Post("/", h.CreateBackupSchedule)
			r.Put("/{id}", h.UpdateBackupSchedule)
			r.Delete("/{id}", h.DeleteBackupSchedule)
		})
	})
	// Was registered at the top level, outside both /api/backup/*
	// groups above, so it inherited no role gate at all: any viewer
	// could read the last 50 jobs of the whole fleet, including VM
	// names, destination paths and error messages with remote hosts.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/api/backup/jobs", h.ListBackupJobs)
	})
	r.Route("/api/firewall/cluster", func(r chi.Router) {
		r.Use(h.requireClusterHA)
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/", h.GetClusterFirewall)
		r.Post("/preview", h.PreviewClusterFirewall)
		r.Put("/", h.SaveClusterFirewall)
		r.Post("/apply", h.ApplyClusterFirewall)
		r.Post("/confirm", h.ConfirmClusterFirewall)
		r.Post("/rollback", h.RollbackClusterFirewall)
	})

	r.Route("/api/firewall/host", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Get("/", h.GetHostFirewall)
			r.Post("/preview", h.PreviewHostFirewall)
			r.Post("/apply", h.ApplyHostFirewall)
			r.Post("/confirm", h.ConfirmHostFirewall)
			r.Post("/rollback", h.RollbackHostFirewall)
			r.Get("/export", h.ExportHostFirewall)
			r.Post("/import", h.ImportHostFirewall)
		})
	})

	return r
}

// modelsRoleAdmin is a small helper to avoid importing models in the
// router file. Returns the admin role string used by the auth package.
func modelsRoleAdmin() string { return "admin" }
