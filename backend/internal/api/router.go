package api

import (
	"net/http"
	"strings"
	"time"
	"webkvm/internal/appliances"
	"webkvm/internal/audit"
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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

func NewRouter(
	cfg *config.Config,
	lv *libvirt.Connector,
	authMgr *auth.Manager,
	loginLimiter *auth.LoginRateLimiter,
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
) *chi.Mux {
	r := chi.NewRouter()

	r.Use(middleware.Recoverer)
	r.Use(requestLogger)
	origins := strings.FieldsFunc(cfg.CORSOrigin, func(r rune) bool { return r == ',' || r == ' ' })
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
	SetAllowedOrigins(origins)
	r.Use(authMgr.Middleware)
	r.Use(auth.MustChangeEnforcer(
		us.MustChangePassword,
		// Paths the user is allowed to hit even with must_change=true.
		// /api/auth/* — so the frontend can call /me to detect the flag
		// and /refresh to rotate jti after a password change.
		// /api/users/me/password — the actual recovery path.
		// /api/health — used by load balancers; never requires auth.
		"/api/auth/",
		"/api/users/me/password",
		"/api/health",
		"/api/system/cert",
	))

	h := &Handler{
		lv:           lv,
		auth:         authMgr,
		loginLimiter: loginLimiter,
		userStore:    us,
		cfg:          cfg,
		hub:          hub,
		gs:           newGroupsStore(cfg.GroupsFile()),
		appStore:     appliances.NewStore(cfg.AppliancesFile()),
		metrics:      metrics,
		hostMetrics:  hostMetrics,
		audit:        auditLogger,
		settings:     settings,
		tokens:       tokensStore,
		nodes:        nodesReg,
		backupStore:  backupStore,
		backupRunner: backupRunner,
		notifier:     notifier,
		fwStore:      fwStore,
		fwMgr:        fwMgr,
		vmSchedStore: vmSchedStore,
		vmScheduler:  vmScheduler,
		StartedAt:    time.Now(),
	}

	r.Get("/console/{id}", h.ConsolePage)
	r.Mount("/static", staticRouter())

	// Cover images are served like static assets so they can be used
	// directly in <img src="..."> without an Authorization header.
	// UUID-in-URL acts as the access control (consistent with the VNC
	// console pattern in the original design brief).
	r.Group(func(r chi.Router) {
		r.Get("/api/covers/{path}", h.ServeCover)
	})
	r.Get("/api/health", h.Health)
	r.Get("/api/events", h.EventsSSE)
	r.Post("/api/events/ticket", h.EventsTicket)

	// Frontend SPA: serve embedded Svelte build on /. Acts as catch-all
	// for anything that isn't /api/*, /console/* or /static/*, so the
	// Svelte router can handle deep links after a page refresh.
	fh := frontendHandler()
	r.Handle("/*", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p := req.URL.Path
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/console") || strings.HasPrefix(p, "/serial") || strings.HasPrefix(p, "/host-terminal") || strings.HasPrefix(p, "/static/") {
			http.NotFound(w, req)
			return
		}
		fh.ServeHTTP(w, req)
	}))

	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/login", h.Login)
		// Logout/refresh/me are authenticated; the auth middleware
		// already enforces the JWT.
		r.Post("/logout", h.Logout)
		r.Post("/refresh", h.Refresh)
		r.Get("/me", h.Me)
	})

	// User management: read-only for any authenticated user, mutating
	// actions restricted to admins.
	r.Route("/api/users", func(r chi.Router) {
		r.Get("/", h.ListUsers)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Post("/", h.CreateUser)
			r.Put("/{username}", h.UpdateUser)
			r.Delete("/{username}", h.DeleteUser)
		})
		// Self-service: any authenticated user can change their own
		// password (and only their own — handler enforces username).
		r.Put("/me/password", h.ChangeMyPassword)
	})

	r.Route("/api/vms", func(r chi.Router) {
		r.Get("/", h.ListVMs)
		// Cross-fleet snapshot view (every VM's snapshots in one call,
		// vs. the per-VM /vms/{id}/snapshots below). Same visibility as
		// ListVMs — chi resolves this static segment before the /{id}
		// wildcard route, so it can't be shadowed by a VM literally
		// named "snapshots".
		r.Get("/snapshots", h.ListAllSnapshots)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/", h.CreateVM)
		})
		r.Route("/{id}", func(r chi.Router) {
			r.Get("/", h.GetVM)
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireAtLeast("operator"))
				// Ownership gate: a caller must be admin or the VM's
				// recorded owner. Without this, role alone (operator)
				// let anyone act on, console into, or export any VM,
				// not just their own.
				r.Use(h.requireVMOwnership)
				r.Patch("/", h.UpdateVM)
				r.Delete("/", h.DeleteVM)
				r.Post("/start", h.StartVM)
				r.Post("/shutdown", h.ShutdownVM)
				r.Post("/forceoff", h.ForceOffVM)
				r.Post("/reboot", h.RebootVM)
				r.Post("/suspend", h.SuspendVM)
				r.Post("/resume", h.ResumeVM)

				r.Post("/disks", h.CreateDisk)
				r.Put("/disks/{dev}", h.UpdateDisk)
				r.Delete("/disks/{dev}", h.DeleteDisk)
				r.Post("/disks/{dev}/resize", h.ResizeDomainDisk)

				r.Post("/networks", h.CreateNetIface)
				r.Patch("/networks/{mac}", h.UpdateNetIface)
				r.Delete("/networks/{mac}", h.DeleteNetIface)

				r.Put("/meta", h.UpdateVMMeta)
				r.Post("/cover", h.UploadCover)
				r.Delete("/cover", h.DeleteCover)

				r.Post("/clone", h.CloneVM)
				r.Post("/boot", h.SetBootDevice)
				r.Post("/autostart", h.SetAutostart)

				r.Post("/snapshots", h.CreateSnapshot)
				r.Delete("/snapshots/{sid}", h.DeleteSnapshot)
				r.Post("/snapshots/{sid}/revert", h.RevertSnapshot)

				r.Put("/firewall", h.SetVMFirewall)
				r.Post("/make-template", h.MakeVMTemplate)
				r.Post("/unset-template", h.UnsetVMTemplate)

				r.Get("/schedule", h.GetVMSchedule)
				r.Put("/schedule", h.SetVMSchedule)
				r.Post("/power/{action}", h.PowerVMNow)
				r.Post("/reset-password", h.ResetVMPassword)

				// These grant interactive console control or full VM
				// data export, not mere metadata visibility, so they
				// need the same owner-or-admin gate as the routes
				// above rather than being open to any authenticated
				// (including viewer-role) user.
				r.Get("/graphics", h.GetGraphics)
				r.Get("/vnc", h.VNCProxy)
				r.Get("/serial", h.SerialProxy)
				r.Post("/console-ticket", h.VMConsoleTicket)
				r.Post("/vnc-ticket", h.VNCTicket)
				r.Get("/rdp", h.DownloadRDP)
				r.Get("/spice", h.DownloadSPICE)
				r.Get("/export", h.ExportVM)
				r.Post("/clipboard", h.SetClipboard)
			})

			// USB device passthrough: admin only.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(modelsRoleAdmin()))
				r.Post("/usb", h.AttachUSBDevice)
				r.Delete("/usb/{vendorId}/{productId}", h.DetachUSBDevice)
			})

			// Read-only metadata for everyone authenticated (viewer
			// dashboards etc.) — no console access, no raw disk export.
			r.Get("/firewall", h.GetVMFirewall)
			r.Get("/disks", h.ListDisks)
			r.Get("/networks", h.ListNetIfaces)
			r.Get("/vlan-support", h.CheckVLANSupport)
			r.Get("/meta", h.GetVMMeta)
			r.Get("/metrics", h.GetVMMetrics)
			r.Get("/boot", h.GetBootDevice)
			r.Get("/autostart", h.GetAutostart)
			r.Get("/snapshots", h.ListSnapshots)
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

	r.Route("/api/storage", func(r chi.Router) {
		r.Get("/pools", h.ListPools)
		r.Get("/volumes", h.ListVolumes)
		r.Get("/isos", h.ListISOs)
		r.Get("/jobs/{id}", h.GetDownloadJob)

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/pools", h.CreatePool)
			r.Put("/pools/{name}", h.UpdatePool)
			r.Post("/volumes", h.CreateVolume)
			r.Patch("/volumes/{pool}/{name}", h.ResizeVolume)
			r.Post("/upload-iso", h.UploadISO)
			r.Post("/upload-iso/raw", h.UploadISOByCURL)
			r.Post("/upload-disk", h.UploadDisk)
			r.Post("/download-iso", h.DownloadISO)
		})

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Delete("/pools/{name}", h.DeletePool)
			r.Delete("/volumes/{pool}/{name}", h.DeleteVolume)
			r.Delete("/isos/{pool}/{name}", h.DeleteISO)
			r.Patch("/isos/{pool}/{name}", h.RenameISO)
		})
	})

	r.Route("/api/networks", func(r chi.Router) {
		r.Get("/", h.ListNetworks)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAtLeast("operator"))
			r.Post("/", h.CreateNetwork)
			r.Put("/{id}", h.UpdateNetwork)
			r.Post("/{id}/start", h.StartNetwork)
			r.Post("/{id}/stop", h.StopNetwork)
		})
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Delete("/{id}", h.DeleteNetwork)
		})
	})

	r.Route("/api/host", func(r chi.Router) {
		r.Get("/", h.GetHostInfo)
		r.Get("/stats", h.GetHostStats)
		r.Get("/metrics", h.GetHostMetrics)
		r.Get("/interfaces", h.ListHostInterfaces)
		r.Get("/bridges", h.ListHostBridges)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Post("/bridges", h.CreateHostBridge)
			r.Delete("/bridges/{name}", h.DeleteHostBridge)
			r.Post("/bridges/{name}/vlan_aware", h.SetHostBridgeVLanAware)
			r.Get("/usb-devices", h.ListHostUSBDevices)
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
		r.Get("/status", h.SystemStatus)
		r.Get("/logs", h.SystemLogs)
		r.Get("/cert", h.SystemCert)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
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
	})

	// Settings (config store). Schema and current values are
	// read-only for any authenticated user; mutations are admin-only.
	r.Route("/api/settings", func(r chi.Router) {
		r.Get("/schema", h.GetSettingsSchema)
		r.Get("/", h.GetSettings)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Put("/", h.SetSettings)
			r.Post("/reset", h.ResetSettings)
			r.Post("/apply-live", h.ApplyLiveSettings)
		})
	})

	// Notifications / alerts. Config reads are for any authenticated
	// user (never expose secrets); mutations, tests and history are
	// admin-only.
	r.Route("/api/notify", func(r chi.Router) {
		r.Get("/config", h.GetNotifyConfig)
		r.Get("/events", h.ListNotifyEvents)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Put("/config", h.UpdateNotifyConfig)
			r.Post("/test", h.TestNotify)
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
	r.Route("/api/nodes", func(r chi.Router) {
		r.Get("/", h.ListNodes)
		r.Get("/{id}", h.GetNode)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Post("/", h.CreateNode)
			r.Put("/{id}", h.UpdateNode)
			r.Delete("/{id}", h.DeleteNode)
		})
	})

	// Backup v2: targets, schedules, jobs, files.
	r.Route("/api/backup/targets", func(r chi.Router) {
		r.Get("/", h.ListBackupTargets)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
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
		})
	})
	r.Route("/api/backup/schedules", func(r chi.Router) {
		r.Get("/", h.ListBackupSchedules)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Post("/", h.CreateBackupSchedule)
			r.Put("/{id}", h.UpdateBackupSchedule)
			r.Delete("/{id}", h.DeleteBackupSchedule)
		})
	})
	r.Get("/api/backup/jobs", h.ListBackupJobs)

	return r
}

// modelsRoleAdmin is a small helper to avoid importing models in the
// router file. Returns the admin role string used by the auth package.
func modelsRoleAdmin() string { return "admin" }
