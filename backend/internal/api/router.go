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
	"webkvm/internal/helperscripts"
	"webkvm/internal/libvirt"
	"webkvm/internal/metrics"
	"webkvm/internal/models"
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
	compute compute.Backend,
	authMgr *auth.Manager,
	globalRateLimiter *auth.GlobalRateLimiter,
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
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		AllowCredentials: allowCreds,
		MaxAge:           300,
	}))
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
		// Paths the user is allowed to hit even with must_change=true or a
		// revoked session epoch.
		// /api/auth/* — so the frontend can call /me to detect the flag
		// and /login or /refresh to establish a fresh, current-epoch token.
		// /api/users/me/password — the actual recovery path.
		// /api/health — used by load balancers; never requires auth.
		"/api/auth/",
		"/api/users/me/password",
		"/api/health",
		"/api/system/cert",
	))

	snippetsStore, _ := cloudinit.NewSnippetStore(cfg.DataDir)

	h := &Handler{
		lv:            lv,
		compute:       compute,
		auth:          authMgr,
		loginLimiter:  loginLimiter,
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
		backupStore:   backupStore,
		backupRunner:  backupRunner,
		notifier:      notifier,
		fwStore:       fwStore,
		fwMgr:         fwMgr,
		vmSchedStore:  vmSchedStore,
		vmScheduler:   vmScheduler,
		snippets:      snippetsStore,
		metricHist:    metricHist,
		alerter:       alerter,
		incusMetrics:  incusMetrics,
		StartedAt:     time.Now(),
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
	r.Get("/api/health", h.Health)
	r.Get("/api/alerts/active", h.ListActiveAlerts)
	r.Get("/api/tags", h.ListAllTags)
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
		r.Post("/login/2fa", h.Login2FA)
		// Logout/refresh/me/2fa are authenticated; the auth middleware
		// already enforces the JWT.
		r.Post("/logout", h.Logout)
		r.Post("/refresh", h.Refresh)
		r.Get("/me", h.Me)
		r.Post("/2fa/setup", h.Setup2FA)
		r.Post("/2fa/enable", h.Enable2FA)
		r.Post("/2fa/disable", h.Disable2FA)
	})

	// Async jobs (VM clone / snapshot / downloads). Read-only job
	// lookup; the jobs are created by the respective endpoints.
	r.Route("/api/jobs", func(r chi.Router) {
		r.Use(auth.RequireAtLeast("viewer"))
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
			r.Get("/", h.GetVM)
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireAtLeast("operator"))
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
				r.Post("/disks/{dev}/bus", h.ChangeDiskBus)

				r.Post("/networks", h.CreateNetIface)
				r.Patch("/networks/{mac}", h.UpdateNetIface)
				r.Delete("/networks/{mac}", h.DeleteNetIface)

				r.Put("/meta", h.UpdateVMMeta)
				r.Post("/cover", h.UploadCover)
				r.Delete("/cover", h.DeleteCover)

				r.Get("/cloudinit", h.GetCloudInitStatus)
				r.Post("/cloudinit", h.ReapplyCloudInit)
				r.Get("/logs", h.GetVMLogs)

				r.Post("/clone", h.CloneVM)
				// Relocating an instance's storage to another pool.
				// Same permission level as a clone: both copy the whole
				// disk, neither can reach outside the caller's pools.
				r.Post("/move-storage", h.MoveVMStorage)
				r.Post("/boot", h.SetBootDevice)
				r.Post("/autostart", h.SetAutostart)

				r.Post("/snapshots", h.CreateSnapshot)
				r.Delete("/snapshots/{sid}", h.DeleteSnapshot)
				r.Post("/snapshots/{sid}/revert", h.RevertSnapshot)

				// Per-VM backups. The admin /api/backup/* surface stays
				// admin-only because it exposes fleet-wide destinations
				// and their credentials; these three operate on this one
				// VM, with the run scope pinned server-side.
				r.Group(func(r chi.Router) {
					r.Use(h.requireCapability("backups"))
					r.Get("/backup/targets", h.ListVMBackupTargets)
					r.Post("/backup", h.BackupVMNow)
					r.Get("/backup/jobs", h.ListVMBackupJobs)
				})

				r.Put("/firewall", h.SetVMFirewall)
				r.Post("/make-template", h.MakeVMTemplate)
				r.Post("/unset-template", h.UnsetVMTemplate)

				r.Get("/schedule", h.GetVMSchedule)
				r.Put("/schedule", h.SetVMSchedule)
				r.With(h.requireCapability("control_power")).Post("/power/{action}", h.PowerVMNow)
				r.Post("/reset-password", h.ResetVMPassword)

				// Exporting streams every byte of the VM's disks. That is
				// a data-exfiltration path rather than a console one, so
				// it is gated on its own capability instead of riding on
				// 'console'.
				r.With(h.requireCapability("export")).Get("/export", h.ExportVM)
			})

			// Console access sits OUTSIDE the operator role gate on
			// purpose. A viewer is read-only, but console is the one
			// capability that can be granted to them explicitly — the
			// support technician who must see a screen without being
			// able to change anything. Nesting this group inside
			// RequireAtLeast("operator") made that grant unreachable:
			// the role gate answered 403 before requireCapability ever
			// ran, so "viewer + console" was silently impossible.
			//
			// Ownership is still enforced: requireVMOwnership is applied
			// by the parent route, and requireCapability("console")
			// denies anyone whose console flag is off, operator included.
			r.Group(func(r chi.Router) {
				r.Use(h.requireCapability("console"))
				r.Get("/graphics", h.GetGraphics)
				r.Get("/vnc", h.VNCProxy)
				r.Get("/serial", h.SerialProxy)
				r.Post("/console-ticket", h.VMConsoleTicket)
				r.Post("/vnc-ticket", h.VNCTicket)
				r.Get("/rdp", h.DownloadRDP)
				r.Get("/spice", h.DownloadSPICE)
				r.Post("/clipboard", h.SetClipboard)
			})

			// USB device passthrough: admin only.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(modelsRoleAdmin()))
				r.Post("/usb", h.AttachUSBDevice)
				r.Delete("/usb/{vendorId}/{productId}", h.DetachUSBDevice)
			})

			// PCI device passthrough: admin only, VM must be shut off
			// (enforced in the libvirt layer).
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(modelsRoleAdmin()))
				r.Post("/pci", h.AttachPCIDevice)
				r.Delete("/pci/{address}", h.DetachPCIDevice)
			})

			// 9p shared folders: admin only, VM must be shut off.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(modelsRoleAdmin()))
				r.Post("/shared-folders", h.AttachSharedFolder)
				r.Delete("/shared-folders/{tag}", h.DetachSharedFolder)
			})

			// Read-only metadata for everyone authenticated (viewer
			// dashboards etc.) — no console access, no raw disk export.
			r.Get("/firewall", h.GetVMFirewall)
			r.Get("/disks", h.ListDisks)
			r.Get("/disks/{dev}/probe", h.ProbeDisk)
			r.Get("/networks", h.ListNetIfaces)
			r.Get("/vlan-support", h.CheckVLANSupport)
			r.Get("/meta", h.GetVMMeta)
			r.Get("/metrics", h.GetVMMetrics)
			r.Get("/metrics/history", h.GetVMMetricsHistory)
			r.Get("/guest-info", h.GetGuestInfo)
			r.Get("/alerts", h.GetVMAlerterRules)
			// V13-C-04: alert rules for a VM are admin-editable.
			r.Group(func(r chi.Router) {
				r.Use(auth.RequireRole(modelsRoleAdmin()))
				r.Put("/alerts", h.SetVMAlerterRules)
			})
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

	// Community helper scripts (third-party installers from the
	// community-scripts project). Listing the cached catalog is open to
	// any authenticated user, but everything that reaches the network or
	// reveals the code to be executed is admin-only: a refresh makes the
	// server issue hundreds of outbound requests, and the provision
	// endpoint returns third-party shell that will run as root.
	r.Route("/api/helper-scripts", func(r chi.Router) {
		r.Get("/", h.ListHelperScripts)
		r.Group(func(r chi.Router) {
			r.Use(auth.RequireRole(modelsRoleAdmin()))
			r.Post("/refresh", h.RefreshHelperScripts)
			r.Get("/{slug}/provision", h.GetHelperScriptProvision)
		})
	})

	r.Route("/api/images", func(r chi.Router) {
		r.Get("/containers", h.ListContainerImages)
		r.Get("/cloud-base", h.ListBaseCloudImages)
		r.Get("/isos", h.ListISOs)
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
		r.Get("/pools", h.ListPools)
		r.Get("/pools/breakdown", h.GetStorageBreakdown)
		r.Get("/volumes", h.ListVolumes)
		r.Get("/isos", h.ListISOs)
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
		r.Get("/", h.ListNetworks)
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
		r.Get("/interfaces", h.ListHostInterfaces)
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
			r.Post("/capabilities/refresh", h.RefreshCapabilities)
			r.Get("/zvols", h.ListZVols)
			r.Post("/disks/wipe", h.WipeHostDisk)
			r.Post("/disks/initialize-directory", h.InitHostDiskDirectory)
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
		r.Get("/{id}", h.GetNode)
		r.Group(func(r chi.Router) {
			r.Post("/", h.CreateNode)
			r.Put("/{id}", h.UpdateNode)
			r.Delete("/{id}", h.DeleteNode)
		})
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
