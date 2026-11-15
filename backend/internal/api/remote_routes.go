package api

import (
	"net/http"

	"webkvm/internal/auth"
	"webkvm/internal/libvirt"

	"github.com/go-chi/chi/v5"
)

// routeMode selects which flavour of the per-VM route tree is mounted.
type routeMode int

const (
	// modeLocal mounts the handlers as-is under /api/vms/{id}.
	modeLocal routeMode = iota
	// modeRemote mounts the same tree for a remote libvirt node: every route
	// carries a class (A, B or L) and anything not served remotely answers 501.
	modeRemote
)

// mountVMRoutes registers every per-VM route (relative to /api/vms/{id}) on r.
// It does NOT apply the ownership gate nor the node dispatch: callers mount
// those before it.
func (h *Handler) mountVMRoutes(r chi.Router, mode routeMode) {
	if mode == modeRemote {
		r = &classRouter{Router: r, h: h}
	}
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
		r.Post("/migrate", h.MigrateVM)
		r.Post("/move", h.MoveVM)
		r.Put("/managed", h.SetWorkloadManaged)

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
		r.Post("/batch-clone", h.BatchCloneVM)
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
		r.Post("/guest/fstrim", h.VMGuestFSTrim)
		r.Post("/guest/exec", h.VMGuestExec)
		r.Post("/guest/freeze", h.VMGuestFSFreeze)
		r.Get("/guest/freeze-status", h.VMGuestFSFreezeStatus)
		r.Post("/guest/sync-time", h.VMGuestSyncTime)

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
		r.Get("/spice-ws", h.SPICEProxy)
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
}

// vmRouteClasses is the class of EVERY route mounted by mountVMRoutes,
// keyed by "METHOD pattern" (pattern relative to /api/vms/{id}).
//
//	A  served unchanged on a remote libvirt node
//	B  served remotely with an adaptation inside the handler
//	L  local only: 501 remote_unsupported (hint open_node_ui)
//
// A route that is mounted but absent here is answered 501 on a remote node
// and fails TestEveryVMRouteHasARemoteClass: when in doubt, choose L, never
// silently act on the local host.
var vmRouteClasses = map[string]routeClass{
	"GET /":                        classA,
	"PATCH /":                      classB, // no local path validation
	"DELETE /":                     classB, // volumes deleted through libvirt
	"POST /start":                  classA,
	"POST /shutdown":               classA,
	"POST /forceoff":               classA,
	"POST /reboot":                 classA,
	"POST /suspend":                classA,
	"POST /resume":                 classA,
	"POST /migrate":                classB, // client-orchestrated (Pool.MigrateDomain)
	"POST /move":                   classL, // Incus cluster members only
	"PUT /managed":                 classA, // local state keyed by (node,hv,id)
	"POST /disks":                  classB, // pools of the node; no local media/zvol
	"PUT /disks/{dev}":             classL, // media source swap resolves paths locally
	"DELETE /disks/{dev}":          classA,
	"POST /disks/{dev}/resize":     classB, // stopped resize through the pool volume
	"POST /disks/{dev}/bus":        classA,
	"POST /networks":               classB, // bridge validated on the node
	"PATCH /networks/{mac}":        classB,
	"DELETE /networks/{mac}":       classA,
	"PUT /meta":                    classA,
	"POST /cover":                  classL, // covers live in the local data dir
	"DELETE /cover":                classL,
	"GET /cloudinit":               classL, // seed ISOs live in the local data dir
	"POST /cloudinit":              classL,
	"GET /logs":                    classL, // qemu logs are read from the local FS
	"POST /clone":                  classL,
	"POST /batch-clone":            classL,
	"POST /move-storage":           classL,
	"POST /boot":                   classA,
	"POST /autostart":              classA,
	"POST /snapshots":              classA,
	"DELETE /snapshots/{sid}":      classA,
	"POST /snapshots/{sid}/revert": classA,
	"GET /backup/targets":          classL,
	"POST /backup":                 classL,
	"GET /backup/jobs":             classL,
	"PUT /firewall":                classL,
	"POST /make-template":          classA, // verified: libvirt metadata only
	"POST /unset-template":         classA,
	"GET /schedule":                classL,
	"PUT /schedule":                classL,
	"POST /power/{action}":         classA,
	"POST /reset-password":         classA,
	"POST /guest/fstrim":           classA,
	"POST /guest/exec":             classL, // shells out to the LOCAL virsh binary
	"POST /guest/freeze":           classA,
	"GET /guest/freeze-status":     classA,
	"POST /guest/sync-time":        classL, // same: local virsh qemu-agent-command
	"GET /export":                  classL,
	// Console family: VNC/SPICE through an SSH direct-tcpip forward to the
	// node, serial through the node's libvirt stream, files with the node host.
	"GET /graphics":                      classB,
	"GET /vnc":                           classB,
	"GET /spice-ws":                      classB,
	"GET /serial":                        classB,
	"POST /console-ticket":               classB, // ticket bound to (node, vm)
	"POST /vnc-ticket":                   classB,
	"GET /rdp":                           classB, // node host, not this one
	"GET /spice":                         classB,
	"POST /clipboard":                    classL, // guest-agent exec through the local virsh
	"POST /usb":                          classA,
	"DELETE /usb/{vendorId}/{productId}": classA,
	"POST /pci":                          classL, // safety checks read the local /sys
	"DELETE /pci/{address}":              classL,
	"POST /shared-folders":               classB, // path checked against the node's pools
	"DELETE /shared-folders/{tag}":       classA,
	"GET /firewall":                      classL,
	"GET /disks":                         classA,
	"GET /disks/{dev}/probe":             classL,
	"GET /networks":                      classA,
	"GET /vlan-support":                  classL,
	"GET /meta":                          classA,
	"GET /metrics":                       classL,
	"GET /metrics/history":               classL,
	"GET /guest-info":                    classL, // guest-agent queries through the local virsh
	"GET /alerts":                        classL,
	"PUT /alerts":                        classL,
	"GET /boot":                          classA,
	"GET /autostart":                     classA,
	"GET /snapshots":                     classA,
}

// routeClassFor returns the remote class of a registered route.
func routeClassFor(method, pattern string) (routeClass, bool) {
	c, ok := vmRouteClasses[method+" "+pattern]
	return c, ok
}

// classRouter wraps a chi.Router so that, in modeRemote, every route
// registered through it is swapped for its class behaviour: L and
// unclassified routes answer 501, A and B keep their handler.
type classRouter struct {
	chi.Router
	h *Handler
}

func (c *classRouter) wrap(method, pattern string, hf http.Handler) http.Handler {
	class, ok := routeClassFor(method, pattern)
	if !ok || class == classL {
		return c.h.remoteUnsupportedHandler()
	}
	return c.h.remoteDomainGuard(hf)
}

func (c *classRouter) Get(p string, hf http.HandlerFunc) {
	c.Router.Method(http.MethodGet, p, c.wrap(http.MethodGet, p, hf))
}
func (c *classRouter) Post(p string, hf http.HandlerFunc) {
	c.Router.Method(http.MethodPost, p, c.wrap(http.MethodPost, p, hf))
}
func (c *classRouter) Put(p string, hf http.HandlerFunc) {
	c.Router.Method(http.MethodPut, p, c.wrap(http.MethodPut, p, hf))
}
func (c *classRouter) Patch(p string, hf http.HandlerFunc) {
	c.Router.Method(http.MethodPatch, p, c.wrap(http.MethodPatch, p, hf))
}
func (c *classRouter) Delete(p string, hf http.HandlerFunc) {
	c.Router.Method(http.MethodDelete, p, c.wrap(http.MethodDelete, p, hf))
}
func (c *classRouter) Group(fn func(chi.Router)) chi.Router {
	return c.Router.Group(func(sub chi.Router) { fn(&classRouter{Router: sub, h: c.h}) })
}
func (c *classRouter) With(mw ...func(http.Handler) http.Handler) chi.Router {
	return &classRouter{Router: c.Router.With(mw...), h: c.h}
}
func (c *classRouter) Route(p string, fn func(chi.Router)) chi.Router {
	return c.Router.Route(p, func(sub chi.Router) { fn(&classRouter{Router: sub, h: c.h}) })
}

// Methods that would bypass the classification are refused outright: the
// route tree must only use the typed helpers above.
func (c *classRouter) Handle(string, http.Handler) { panic("classRouter: Handle is not classified") }
func (c *classRouter) HandleFunc(string, http.HandlerFunc) {
	panic("classRouter: HandleFunc is not classified")
}
func (c *classRouter) Mount(string, http.Handler) { panic("classRouter: Mount is not classified") }
func (c *classRouter) Method(string, string, http.Handler) {
	panic("classRouter: Method is not classified")
}
func (c *classRouter) MethodFunc(string, string, http.HandlerFunc) {
	panic("classRouter: MethodFunc is not classified")
}

// remoteDomainExists is the typed existence probe of the remote guard (a
// variable so tests can stub it).
var remoteDomainExists = func(lv *libvirt.Connector, id string) (bool, error) {
	if lv == nil {
		return true, nil
	}
	return lv.HasDomain(id)
}

// remoteDomainGuard centrally maps "this VM does not exist on that remote
// node" to 404 domain_not_found for every class A/B route, instead of each
// handler turning libvirt's VIR_ERR_NO_DOMAIN into a 500. The check is typed
// (libvirt error code, see Connector.HasDomain); any other probe failure lets
// the request through so the handler reports the real error.
func (h *Handler) remoteDomainGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id != "" && h.lv != nil {
			if ok, err := remoteDomainExists(h.lv, id); err == nil && !ok {
				name, nid := h.remoteNodeLabel()
				jsonResp(w, http.StatusNotFound, map[string]string{
					"error": "instance not found", "code": "domain_not_found", "node": name, "node_id": nid,
				})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
