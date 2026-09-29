// Package incus is the container backend (v2.2.0) — migrated from LXD
// to Incus (the community fork, github.com/lxc/incus). The Incus Go
// client keeps the LXD REST API, so it manages BOTH Incus and legacy
// LXD daemons over the local unix socket. Socket auto-detection probes
// Incus paths first (/var/lib/incus/unix.socket, /run/incus/*) and falls
// back to the LXD snap/apt paths for existing installs.
//
// Implementation is GRADUAL and fail-safe:
//   - Fase 1: read path (ListVMs) so containers appear alongside VMs.
//   - Fase 2: lifecycle (start/stop/forceoff/reboot/freeze) + interactive
//     serial console (exec bash over websockets) + cloud-init mapping.
//   - Fase 3: creation from image remotes (zero ISOs, cloud-init injected
//     as user.user-data/user.network-config), tags/metadata in custom
//     config keys (user.webkvm.tags / user.webkvm.desc) so the v1.3 RBAC
//     and backup-by-tag policies keep working, and streaming backups via
//     the native /1.0/instances/<name>/export endpoint (io.Copy, no
//     double buffering, no temp download).
//
// Unimplemented operations return compute.ErrNotImplemented, which the
// handlers surface as HTTP 501 Not Implemented.
package incus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"webkvm/internal/config"

	"webkvm/internal/backupstore"
	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/events"
	"webkvm/internal/models"
)

// DefaultSocketPath returns the container daemon unix socket, probing
// the Incus paths first (v2.2.0) and falling back to legacy LXD paths
// (snap, then apt) so existing LXD installs keep working unchanged.
func DefaultSocketPath() string {
	for _, p := range []string{
		"/var/lib/incus/unix.socket",
		"/run/incus/unix.socket",
		"/run/incus/incus.socket",
		"/var/snap/lxd/common/lxd/unix.socket",
		"/var/lib/lxd/unix.socket",
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return "/var/lib/incus/unix.socket"
}

// IncusBackend is the compute.Backend adapter for the LXD daemon.
type IncusBackend struct {
	client incus.InstanceServer
	// socketPath is the unix socket the client dials. Kept so the
	// backup export can stream /1.0/instances/<name>/export over its
	// own unix-socket HTTP request (the official client only decodes
	// JSON bodies, never binary streams).
	socketPath string
}

// IncusBackendOption customizes the backend at construction time.
type IncusBackendOption func(*IncusBackend)

// NewIncusBackend connects to the LXD daemon over a unix socket. An empty
// path uses DefaultSocketPath(). Returns an error (including
// compute.ErrNotImplemented if LXD is unreachable) when the daemon
// cannot be reached, so the caller can degrade to KVM-only.
func NewIncusBackend(socketPath string, opts ...IncusBackendOption) (*IncusBackend, error) {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	client, err := incus.ConnectIncusUnix(socketPath, &incus.ConnectionArgs{})
	if err != nil {
		return nil, err
	}
	b := &IncusBackend{client: client, socketPath: socketPath}
	for _, o := range opts {
		o(b)
	}
	return b, nil
}

// EnsureIncusPool creates the dedicated Incus container storage pool
// (webkvm-incus) if it doesn't exist. This pool lives at
// /opt/webkvm/pools/webkvm-incus and is a sibling of webkvm-disks
// (KVM) and ISOS (ISO images). Uses dir driver.
func (b *IncusBackend) EnsureIncusPool(ctx context.Context, cfg *config.Config) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	poolName := config.IncusPoolName
	poolPath := cfg.IncusPoolPath()

	// Check if pool already exists
	_, _, err := b.client.GetStoragePool(poolName)
	if err == nil {
		return nil // already exists
	}

	// Create the directory if it doesn't exist
	if err := os.MkdirAll(poolPath, 0755); err != nil {
		return fmt.Errorf("create pool dir %q: %w", poolPath, err)
	}

	// Create the storage pool in Incus
	post := api.StoragePoolsPost{
		Name:   poolName,
		Driver: "dir",
		StoragePoolPut: api.StoragePoolPut{
			Config: map[string]string{
				"source": poolPath,
			},
		},
	}
	if err := b.client.CreateStoragePool(post); err != nil {
		return fmt.Errorf("create incus storage pool %q: %w", poolName, err)
	}

	return nil
}

// bridgeForNetwork resolves the container's network to the Linux bridge its
// NIC attaches to. WebKVM v2.4 uses ONE model: real OS-level Linux bridges
// (vmbr0, vmbr1, … — Proxmox-style). An empty name lands on the host's main
// physical bridge; a named network must itself be a real host bridge.
// libvirt virtual/NAT networks ("default", "webkvm-bridge", virbr0/lxdbr0)
// are never used.
func (b *IncusBackend) bridgeForNetwork(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		// No network selected: land on the host's PHYSICAL shared bridge
		// (vmbr0/br0). If there is none, fail loudly — WebKVM requires
		// real Layer-2, never an intermediate NAT/virtual bridge.
		if mb := incusMainBridge(); mb != "" {
			return mb, nil
		}
		return "", compute.ErrNoPhysicalBridge
	}
	// A named network must BE a real Linux bridge on the host (vmbrX).
	// This rejects virbr0/lxdbr0 and any virtual/NAT network name.
	if !isPhysicalBridge(name) {
		return "", fmt.Errorf("%w: %q is not a physical Linux bridge on the host (WebKVM only uses vmbrX bridges; virtual/NAT networks like virbr0/lxdbr0 are not used)", compute.ErrNoPhysicalBridge, name)
	}
	return name, nil
}

// incusMainBridge returns the host's primary PHYSICAL Linux bridge for
// containers (vmbr0, br0, then the first physical bridge found). Virtual
// bridges (virbr0/lxdbr0/docker) are NEVER candidates — containers must
// share the real LAN. Empty when the host has no physical bridge.
func incusMainBridge() string {
	for _, preferred := range []string{"vmbr0", "br0"} {
		if isPhysicalBridge(preferred) {
			return preferred
		}
	}
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if isPhysicalBridge(e.Name()) {
			return e.Name()
		}
	}
	return ""
}

// isPhysicalBridge reports whether name is a Linux bridge on the host that
// is NOT a virtual/NAT bridge (virbr*, lxdbr*, lxcbr*, docker*, br-*) — a
// real, shared L2 bridge wired to a physical NIC (vmbr0/br0, …).
func isPhysicalBridge(name string) bool {
	if !isLinuxBridge(name) {
		return false
	}
	for _, p := range []string{"virbr", "lxdbr", "lxcbr", "incusbr", "docker", "br-"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	return true
}

// isLinuxBridge reports whether name is a real Linux bridge on the host.
// Containers attach to these with nictype=bridged parent=<bridge>.
func isLinuxBridge(name string) bool {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") || strings.ContainsAny(name, " \t\n\r") {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name + "/bridge") // lgtm[go/path-injection] - name validated above
	return err == nil
}

// ServerInfo returns the LXD daemon version for the status page.
func (b *IncusBackend) ServerInfo() (string, error) {
	s, _, err := b.client.GetServer()
	if err != nil {
		return "", err
	}
	return s.Environment.ServerVersion, nil
}

// Close closes the underlying client connections.
func (b *IncusBackend) Close() {
	b.client.Disconnect()
}

// NewMetricsCollector builds the per-container metrics collector
// (v1.4 Fase 4.1) bound to this backend's daemon connection.
func (b *IncusBackend) NewMetricsCollector(hub *events.Hub) *MetricsCollector {
	return NewMetricsCollector(clientAdapter{client: b.client}, hub)
}

// --- Instance lifecycle ---

// ListVMs lists every LXD instance (containers + VMs) and maps them to
// the neutral domain model. This is the Fase 1 MVP: it makes containers
// appear in the unified VM list.
func (b *IncusBackend) ListDomains() ([]models.VM, error) {
	instances, err := b.client.GetInstances(api.InstanceTypeAny)
	if err != nil {
		return nil, err
	}
	out := make([]models.VM, 0, len(instances))
	for i := range instances {
		vm := instanceToVM(&instances[i])
		// N+1: surface the container's LAN IP (Incus GetInstance does not
		// include it). Acceptable for the homelab context.
		if st, _, err := b.client.GetInstanceState(instances[i].Name); err == nil {
			vm.IP = instanceIP(st)
			vm.IPs = instanceIPs(st)
			vm.UptimeSec = instanceUptimeSec(st)
			macIPs := instanceIfaceIPs(st)
			for j := range vm.Networks {
				vm.Networks[j].IPs = macIPs[strings.ToLower(vm.Networks[j].MAC)]
			}
		}
		out = append(out, vm)
	}
	return out, nil
}

func (b *IncusBackend) GetDomain(id string) (models.VM, error) {
	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return models.VM{}, err
	}
	vm := instanceToVM(inst)
	// Surface the container's IP from the live instance state (eth0 IPv4).
	if st, _, err := b.client.GetInstanceState(id); err == nil {
		vm.IP = instanceIP(st)
		vm.IPs = instanceIPs(st)
		vm.UptimeSec = instanceUptimeSec(st)
		macIPs := instanceIfaceIPs(st)
		for j := range vm.Networks {
			vm.Networks[j].IPs = macIPs[strings.ToLower(vm.Networks[j].MAC)]
		}
	}
	return vm, nil
}

// instanceUptimeSec derives a running container's uptime from Incus's own
// process-start timestamp — previously never set for containers at all
// (only KVM VMs computed one, from libvirt), so a running container's
// Overview tab always showed "—" for uptime regardless of how long it had
// actually been up.
func instanceUptimeSec(st *api.InstanceState) int64 {
	if st == nil || st.StartedAt.IsZero() {
		return 0
	}
	if up := time.Since(st.StartedAt); up > 0 {
		return int64(up.Seconds())
	}
	return 0
}

// instanceIfaceIPs maps each NIC's own MAC (lowercased) to its own IPv4
// addresses — st.Network is already keyed by interface name with a Hwaddr
// per entry, but that per-interface association was previously discarded:
// every interface exposed the SAME instanceIPs() flat list regardless of
// which NIC it actually belonged to, so a container with more than one NIC
// showed the identical combined IP list on every row in the UI.
func instanceIfaceIPs(st *api.InstanceState) map[string][]string {
	out := map[string][]string{}
	if st == nil {
		return out
	}
	for _, net := range st.Network {
		mac := strings.ToLower(net.Hwaddr)
		if mac == "" {
			continue
		}
		for _, a := range net.Addresses {
			if a.Family == "inet" && a.Address != "" {
				out[mac] = append(out[mac], a.Address)
			}
		}
	}
	return out
}

// instanceIPs extracts every IPv4 (eth0 first, then any interface except lo)
// from an Incus instance state, so containers with several NICs expose all
// their IPs instead of a single one. The first entry is the primary (eth0).
func instanceIPs(st *api.InstanceState) []string {
	if st == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
	// eth0 (and other ethernet NICs) first, in a stable order.
	names := make([]string, 0, len(st.Network))
	for name := range st.Network {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name == "lo" {
			continue
		}
		for _, a := range st.Network[name].Addresses {
			if a.Family == "inet" && a.Address != "" && !seen[a.Address] {
				seen[a.Address] = true
				out = append(out, a.Address)
			}
		}
	}
	return out
}

// instanceIP returns the primary IPv4 (eth0's first, else the first address
// on any non-loopback interface). Kept for the single-IP model field.
func instanceIP(st *api.InstanceState) string {
	if ips := instanceIPs(st); len(ips) > 0 {
		return ips[0]
	}
	return ""
}

func (b *IncusBackend) DomainExists(name string) (bool, error) {
	_, _, err := b.client.GetInstance(name)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (b *IncusBackend) DeleteDomain(id string) error {
	// A running container cannot be deleted; stop it first (the delete
	// handler expects delete to always succeed, matching KVM). The Incus
	// v6 client's DeleteInstance takes no force flag.
	if inst, _, err := b.client.GetInstance(id); err == nil && inst.Status == "Running" {
		if err := b.ForceOffDomain(id); err != nil {
			return err
		}
	}
	op, err := b.client.DeleteInstance(id)
	if err != nil {
		return mapLXErr(err)
	}
	return waitOperation(op)
}
func (b *IncusBackend) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	return models.VM{}, compute.ErrNotImplemented
}
func (b *IncusBackend) StartDomain(id string) error {
	return b.setState(id, "start", 30, false)
}
func (b *IncusBackend) ShutdownDomain(id string) error {
	// Graceful stop: LXD sends the guest a shutdown signal and waits up to
	// the timeout before giving up (Force=false).
	return b.setState(id, "stop", 60, false)
}
func (b *IncusBackend) ForceOffDomain(id string) error {
	// Kill: immediate forced stop (no graceful shutdown grace period).
	return b.setState(id, "stop", 0, true)
}
func (b *IncusBackend) RebootDomain(id string) error {
	return b.setState(id, "restart", 60, false)
}
func (b *IncusBackend) SuspendDomain(id string) error { return b.setState(id, "freeze", 30, false) }
func (b *IncusBackend) ResumeDomain(id string) error  { return b.setState(id, "unfreeze", 30, false) }
func (b *IncusBackend) SetDomainAutostart(id string, enabled bool) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) GetDomainAutostart(id string) (bool, error) {
	return false, compute.ErrNotImplemented
}
func (b *IncusBackend) SetBootDevice(id string, device string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) GetBootDevice(id string) (string, error) { return "", compute.ErrNotImplemented }
func (b *IncusBackend) ValidateDomainDisks(id string) error     { return compute.ErrNotImplemented }

// CreateDomain creates a container from an official LXD image remote
// (v1.4 Fase 3) — zero ISOs, templates only. The image reference is
// req.Image ("ubuntu:24.04", "images:alpine/3.20", or a full
// <server-url>:<alias>). Cloud-init is injected straight into the native
// user.user-data / user.network-config config keys the Fase 2 mapper
// already produces.
func (b *IncusBackend) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	// Argument validation first: it needs no daemon, and reporting
	// "unsupported backend" for a request that is malformed regardless
	// would hide the real problem.
	if strings.TrimSpace(req.Image) == "" {
		return models.VM{}, errors.New("an LXD image reference is required to create a container (e.g. ubuntu:24.04)")
	}
	if b.client == nil {
		return models.VM{}, compute.ErrNotImplemented
	}
	// A local image identified by fingerprint is already in the store,
	// so there is no remote to resolve and no alias to look up. Incus
	// takes it verbatim through InstanceSource.Fingerprint.
	fingerprint := ""
	var server, alias string
	if isImageFingerprint(req.Image) {
		fingerprint = strings.ToLower(strings.TrimSpace(req.Image))
		// A well-formed fingerprint that names no stored image makes
		// Incus answer "Image not provided for instance creation" — a
		// message about a field we DID send, which sends the operator
		// looking in the wrong place entirely. Say what is actually
		// wrong instead.
		if _, _, err := b.client.GetImage(fingerprint); err != nil {
			return models.VM{}, fmt.Errorf("no local image with fingerprint %q", req.Image)
		}
	} else {
		var err error
		server, alias, err = parseImageRef(req.Image)
		if err != nil {
			return models.VM{}, err
		}
	}
	// A container that is asked to run cloud-init needs an image that
	// actually ships it. The linuxcontainers "default" variant does NOT:
	// it has no cloud-init binary at all, so user.user-data is stored on
	// the instance and then silently ignored. The container boots fine,
	// gets an IP and looks healthy, while the provisioning script never
	// runs and the app is simply absent — a failure with no error
	// anywhere, which is the expensive kind.
	// Only a remote alias has a "/cloud" sibling to switch to. A local
	// fingerprint names one specific image and nothing else: swapping it
	// for a different one is not ours to do.
	if needsCloudInit(req.CloudInit) && fingerprint == "" {
		alias = cloudVariantAlias(server, alias)
	}
	// v1.4 Fase 4.1: the operator picks the network from the SAME
	// selector as KVM; resolve the libvirt network name to the Linux
	// bridge the container NIC lands on.
	bridge, err := b.bridgeForNetwork(req.Network)
	if err != nil {
		return models.VM{}, err
	}
	config := map[string]string{
		"boot.autostart":   "true",
		"security.nesting": "true",
	}
	if req.VCPUs > 0 {
		config["limits.cpu"] = strconv.Itoa(req.VCPUs)
	}
	if req.RAMMB > 0 {
		config["limits.memory"] = formatMemoryMB(req.RAMMB)
	}
	if req.Autostart != nil {
		config["boot.autostart"] = strconv.FormatBool(*req.Autostart)
	}
	if req.Privileged != nil {
		config["security.privileged"] = strconv.FormatBool(*req.Privileged)
	}
	if req.Nesting != nil {
		config["security.nesting"] = strconv.FormatBool(*req.Nesting)
	}
	profiles := req.Profiles
	if len(profiles) == 0 {
		profiles = []string{"default"}
	}
	if err := validateProfiles(profiles); err != nil {
		return models.VM{}, err
	}
	devices := map[string]map[string]string{}
	if req.DiskGB > 0 {
		// The operator-chosen Incus pool (storage_pool) wins. When the
		// caller does not name one, the pool is resolved from the
		// profiles the instance is actually created with, and only then
		// from the WebKVM pool. Hardcoding "default" here was a real
		// bug: no such pool exists on a standard Incus install, and
		// because this root device OVERRIDES the profile's own root,
		// it replaced a perfectly good pool with a missing one and the
		// create failed with "Storage pool not found".
		pool := strings.TrimSpace(req.StoragePool)
		if pool == "" {
			pool = b.rootPoolForProfiles(profiles)
		}
		if pool == "" {
			// No profile supplies a root disk, so a size cannot be
			// applied to one. Leaving the device out entirely is the
			// honest outcome: the instance still gets its root from
			// the profile chain (or fails with the daemon's own clear
			// error), instead of us inventing a pool name that is only
			// right by luck.
			slog.Warn("incus: no storage pool resolved for root disk; leaving profile root untouched",
				"instance", req.Name, "profiles", profiles, "disk_gb", req.DiskGB)
		} else {
			devices["root"] = map[string]string{
				"type": "disk",
				"path": "/",
				"pool": pool,
				"size": fmt.Sprintf("%dGB", req.DiskGB),
			}
		}
	}
	// NIC on the chosen bridge. The device MUST be named strictly "eth0"
	// (map key AND name property): LXD overrides a same-name profile device
	// (the "default" profile's NIC), so the container gets exactly ONE
	// interface on the physical bridge — never an extra eth0/eth1 duplicate
	// from the profile's lxdbr0 NIC.
	devices["eth0"] = map[string]string{
		"type":    "nic",
		"nictype": "bridged",
		"parent":  bridge,
		"name":    "eth0",
	}
	// A bare "gpu" device with no vendor/product filter exposes every
	// host render node to the container. Unlike a VM's PCI passthrough
	// this does not detach anything from the host: the card stays in use
	// by the host and by any other container asking for it, which is
	// exactly what hardware transcoding (Jellyfin, Plex, Frigate) needs.
	if req.GPU != nil && *req.GPU {
		devices["gpu0"] = map[string]string{"type": "gpu"}
	}
	if req.CloudInit != nil {
		ci := cloudinit.Config{
			User:            req.CloudInit.User,
			Password:        req.CloudInit.Password,
			SSHKey:          req.CloudInit.SSHKey,
			Hostname:        req.CloudInit.Hostname,
			ProvisionScript: req.CloudInit.ProvisionScript,
			CustomUserData:  req.CloudInit.CustomUserData,
			SnippetID:       req.CloudInit.SnippetID,
			// Containers have no QEMU guest agent (v1.4 Fase 4.1).
			SkipGuestAgent: true,
		}
		if err := ci.Validate(); err != nil {
			return models.VM{}, err
		}
		cloudKeys, _ := lxdCloudInitConfig(ci, bridge)
		for k, v := range cloudKeys {
			config[k] = v
		}
	}
	// Always derive the netplan from the actual NIC devices (currently
	// just eth0), whether or not the operator filled in cloud-init.
	// Without this, a container created with the cloud-init fields left
	// blank never gets a user.network-config key at all; a later NIC
	// attach then has nothing to regenerate (see the same guard removed
	// in AttachNetworkIface/DetachNetworkIface below), so a second NIC
	// never gets DHCPed in the guest and looks like it never got an IP.
	config["user.network-config"] = lxdNetworkConfig(devices)
	// A fingerprint source carries NO server and NO protocol: naming a
	// remote alongside it would send Incus looking for the image there
	// instead of using the copy it already has.
	source := api.InstanceSource{Type: "image"}
	if fingerprint != "" {
		source.Fingerprint = fingerprint
	} else {
		source.Protocol = "simplestreams"
		source.Server = server
		source.Alias = alias
	}
	post := api.InstancesPost{
		Name:   req.Name,
		Type:   api.InstanceTypeContainer,
		Source: source,
		InstancePut: api.InstancePut{
			Config:   config,
			Devices:  devices,
			Profiles: profiles,
		},
	}
	op, err := b.client.CreateInstance(post)
	if err != nil {
		return models.VM{}, err
	}
	if err := waitOperation(op); err != nil {
		return models.VM{}, err
	}
	return b.GetDomain(req.Name)
}

// UpdateDomain updates a container's writable properties: the name
// (rename), CPU/RAM limits. KVM-only fields (video, firmware, chipset,
// secure boot, TPM, network model) are not applicable to containers and
// return compute.ErrNotImplemented instead of silently ignoring them.
func (b *IncusBackend) UpdateDomain(id string, req models.UpdateVMRequest) (models.VM, error) {
	for name, v := range map[string]bool{
		"CPUMode": req.CPUMode != nil, "VideoModel": req.VideoModel != nil,
		"OSType": req.OSType != nil, "OSVersion": req.OSVersion != nil,
		"Chipset": req.Chipset != nil, "SecureBoot": req.SecureBoot != nil,
		"TPMEnabled": req.TPMEnabled != nil, "Firmware": req.Firmware != nil,
		"NetworkModel": req.NetworkModel != nil, "Network": req.Network != nil,
		"BootOrder":       req.BootOrder != nil,
		"TPMVersion":      req.TPMVersion != nil,
		"WatchdogEnabled": req.WatchdogEnabled != nil,
	} {
		if v {
			return models.VM{}, fmt.Errorf("field %s is not applicable to a container: %w", name, compute.ErrNotImplemented)
		}
	}

	if req.Name != nil && *req.Name != id {
		op, err := b.client.RenameInstance(id, api.InstancePost{Name: *req.Name})
		if err != nil {
			return models.VM{}, err
		}
		if err := waitOperation(op); err != nil {
			return models.VM{}, err
		}
		id = *req.Name
	}
	// CPU/RAM limits require a full config update (read-modify-write).
	if req.VCPUs != nil || req.RAMMB != nil {
		inst, etag, err := b.client.GetInstance(id)
		if err != nil {
			return models.VM{}, err
		}
		cfg := inst.Config
		if req.VCPUs != nil {
			if *req.VCPUs > 0 {
				cfg["limits.cpu"] = strconv.Itoa(*req.VCPUs)
			} else {
				delete(cfg, "limits.cpu")
			}
		}
		if req.RAMMB != nil {
			if *req.RAMMB > 0 {
				cfg["limits.memory"] = formatMemoryMB(*req.RAMMB)
			} else {
				delete(cfg, "limits.memory")
			}
		}
		op, err := b.client.UpdateInstance(id, api.InstancePut{
			Config:      cfg,
			Devices:     inst.Devices,
			Profiles:    inst.Profiles,
			Description: inst.Description,
		}, etag)
		if err != nil {
			return models.VM{}, err
		}
		if err := waitOperation(op); err != nil {
			return models.VM{}, err
		}
	}
	// Advanced container options (privileged, nesting, autostart,
	// profiles) also require a full read-modify-write config update.
	if req.Privileged != nil || req.Nesting != nil || req.Autostart != nil || req.Profiles != nil {
		inst, etag, err := b.client.GetInstance(id)
		if err != nil {
			return models.VM{}, err
		}
		cfg := inst.Config
		if req.Privileged != nil {
			// security.privileged cannot be toggled while the
			// container is running (Incus/LXD restriction): abort with
			// an explicit error asking to stop the container first.
			if inst.StatusCode == api.Running {
				return models.VM{}, fmt.Errorf("cannot change security.privileged while the container is running — stop it first")
			}
			cfg["security.privileged"] = strconv.FormatBool(*req.Privileged)
		}
		if req.Nesting != nil {
			cfg["security.nesting"] = strconv.FormatBool(*req.Nesting)
		}
		if req.Autostart != nil {
			cfg["boot.autostart"] = strconv.FormatBool(*req.Autostart)
		}
		profiles := inst.Profiles
		if req.Profiles != nil {
			if err := validateProfiles(req.Profiles); err != nil {
				return models.VM{}, err
			}
			profiles = req.Profiles
		}
		op, err := b.client.UpdateInstance(id, api.InstancePut{
			Config:      cfg,
			Devices:     inst.Devices,
			Profiles:    profiles,
			Description: inst.Description,
		}, etag)
		if err != nil {
			return models.VM{}, err
		}
		if err := waitOperation(op); err != nil {
			return models.VM{}, err
		}
	}
	return b.GetDomain(id)
}

// setState drives the LXD instance state machine (start/stop/restart/
// freeze/unfreeze) and waits for the operation to complete.
func (b *IncusBackend) setState(id, action string, timeout int, force bool) error {
	op, err := b.client.UpdateInstanceState(id, api.InstanceStatePut{
		Action:  action,
		Timeout: timeout,
		Force:   force,
	}, "")
	if err != nil {
		return mapLXErr(err)
	}
	return waitOperation(op)
}

// waitOperation polls an LXD operation until it reaches a terminal state.
// Deliberately avoids Operation.Wait(), which subscribes to the /1.0/events
// websocket — polling Refresh()/Get() is equally correct against a real
// daemon and keeps the adapter testable against a minimal server.
func waitOperation(op incus.Operation) error {
	return waitOperationTimeout(op, 90*time.Second)
}

// waitOperationLong is like waitOperation but uses a longer timeout suitable
// for heavy I/O operations (backup creation, export streaming, import) where
// 90 seconds is routinely insufficient for containers larger than ~2 GB.
func waitOperationLong(op incus.Operation) error {
	return waitOperationTimeout(op, 15*time.Minute)
}

func waitOperationTimeout(op incus.Operation, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		cur := op.Get()
		switch cur.Status {
		case "Success":
			if cur.Err != "" {
				return errors.New(cur.Err)
			}
			return nil
		case "Failure", "Cancelled":
			if cur.Err != "" {
				return errors.New(cur.Err)
			}
			return errors.New("LXD operation failed")
		}
		if time.Now().After(deadline) {
			return errors.New("LXD operation timed out")
		}
		if err := op.Refresh(); err != nil {
			return err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (b *IncusBackend) GetDomainLog(id string, lines int) (string, error) {
	if id == "" || strings.Contains(id, "/") || strings.Contains(id, "\\") || strings.Contains(id, "..") {
		return "", fmt.Errorf("invalid container id %q", id)
	}
	logPath := filepath.Join("/var/log/incus", id, "lxc.log")
	if rel, err := filepath.Rel("/var/log/incus", logPath); err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return "", fmt.Errorf("invalid log path")
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Sprintf("(No container log file found at %s)", logPath), nil
		}
		return "", fmt.Errorf("read container log: %w", err)
	}
	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	allLines := strings.Split(string(data), "\n")
	if len(allLines) > lines {
		allLines = allLines[len(allLines)-lines:]
	}
	return strings.Join(allLines, "\n"), nil
}

// --- Disks / devices / USB ---

func (b *IncusBackend) AttachDisk(id string, req models.AttachDiskRequest) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) DetachDisk(id, target string) error { return compute.ErrNotImplemented }
func (b *IncusBackend) ChangeDiskBus(id, target, newBus string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) UpdateDiskSource(id, target, source string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) RunCloudInitReprovision(id string) error {
	// Incus containers get their cloud-init user-data natively
	// (config.user.user-data) applied on start, not via a swappable
	// NoCloud ISO — there is no "reprovision" concept here.
	return compute.ErrNotImplemented
}
func (b *IncusBackend) ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error) {
	// Containers have a single root device; only that can be resized
	// (lxc config device set <name> root size=X), applied live via a
	// read-modify-write instance update.
	if target != "" && target != "root" {
		return 0, fmt.Errorf("only the root device can be resized on a container: %w", compute.ErrNotImplemented)
	}
	if newSizeGB <= 0 {
		return 0, errors.New("size must be positive")
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return 0, err
	}
	root, ok := inst.Devices["root"]
	if !ok {
		return 0, errors.New("container has no root device")
	}
	root["size"] = fmt.Sprintf("%dGB", newSizeGB)
	inst.Devices["root"] = root
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return 0, err
	}
	if err := waitOperation(op); err != nil {
		return 0, err
	}
	return newSizeGB, nil
}

func (b *IncusBackend) AttachNetworkIface(id string, req models.AttachNetRequest) error {
	bridge, err := b.bridgeForNetwork(req.Network)
	if err != nil {
		return err
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	nicName := nextNicName(inst.Devices)
	inst.Devices[nicName] = map[string]string{
		"type":    "nic",
		"nictype": "bridged",
		"parent":  bridge,
		"name":    nicName,
	}
	// The guest only configures the NICs declared in its netplan
	// (user.network-config). Regenerate it unconditionally — including
	// when the container never had cloud-init network config to begin
	// with (created without filling in the cloud-init fields) — so the
	// newly attached NIC gets DHCP on the next cloud-init/netplan run.
	// Previously this only ran when the key was already non-empty,
	// which meant a container created without cloud-init could never
	// get its second+ NIC configured: the interface showed up in the
	// UI but never received an IP.
	inst.Config["user.network-config"] = lxdNetworkConfig(inst.Devices)
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	if err := waitOperation(op); err != nil {
		return err
	}
	// Regenerating user.network-config (above) only helps guests that
	// actually run cloud-init — plenty of stock container images
	// (confirmed live: Debian's own container images) have no cloud-init
	// at all and instead rely on a systemd-networkd unit that Incus/the
	// image seeds ONLY for the primary NIC at creation time, or ship no
	// per-interface config at all. For those, a hot-attached NIC would
	// otherwise sit up at the link layer with no IP forever. Best-effort,
	// never fails the attach itself.
	b.afterAttachConfigureGuestNetwork(id, nicName)
	return nil
}

// afterAttachConfigureGuestNetwork nudges a RUNNING container's guest OS
// into actually using a newly attached NIC, covering the case
// user.network-config regeneration cannot reach (see AttachNetworkIface):
// a guest with no cloud-init, or one that only re-applies cloud-init on
// reboot. It (1) requests a DHCP lease on the interface immediately via
// whichever DHCP client the guest has (isc-dhcp-client's dhclient or
// busybox's udhcpc cover the large majority of Debian/Ubuntu/Alpine
// container images), so the IP appears right away without a reboot, and
// (2) if the guest is recognizably using systemd-networkd (it already has
// at least one unit under /etc/systemd/network/, e.g. the one seeding
// eth0), adds a matching unit for the new interface so the IP also
// survives a restart. Every step is best-effort: a stopped container, a
// guest with neither DHCP client, or a push/exec failure are all silently
// skipped rather than surfaced — the NIC attach itself already succeeded,
// and the operator can always finish configuring the guest by hand.
func (b *IncusBackend) afterAttachConfigureGuestNetwork(id, nicName string) {
	state, _, err := b.client.GetInstanceState(id)
	if err != nil || state.Status != "Running" {
		return
	}
	dhcpScript := fmt.Sprintf(
		`ip link set %s up 2>/dev/null; `+
			`if command -v dhclient >/dev/null 2>&1; then dhclient -1 %s; `+
			`elif command -v udhcpc >/dev/null 2>&1; then udhcpc -i %s -n -q -t 3; fi`,
		nicName, nicName, nicName,
	)
	if _, err := b.runInGuest(id, []string{"sh", "-c", dhcpScript}); err != nil {
		slog.Debug("incus_attach_guest_dhcp_kick_failed", "instance", id, "nic", nicName, "err", err)
	}
	if b.guestUsesNetworkd(id) {
		unit := fmt.Sprintf("[Match]\nName=%s\n\n[Network]\nDHCP=true\n", nicName)
		err := b.client.CreateInstanceFile(id, "/etc/systemd/network/"+nicName+".network", incus.InstanceFileArgs{
			Content: strings.NewReader(unit),
			Mode:    0644,
			Type:    "file",
		})
		if err != nil {
			slog.Debug("incus_attach_networkd_unit_push_failed", "instance", id, "nic", nicName, "err", err)
			return
		}
		if _, err := b.runInGuest(id, []string{"networkctl", "reload"}); err != nil {
			slog.Debug("incus_attach_networkd_reload_failed", "instance", id, "nic", nicName, "err", err)
		}
	}
}

// runInGuest runs a non-interactive command inside a running instance,
// waits for it to finish, and returns its exit code. Note that a
// non-zero exit code is NOT a Go error here — Incus reports the exec
// OPERATION itself as "Success" as soon as the process exits, regardless
// of what it exited with (verified against a real instance: `exec
// ["false"]` still completes as status Success, with the actual exit
// code tucked away in the operation's `metadata.return` field) — so
// callers that care about success/failure of the command itself must
// check the returned exit code, not just the error.
func (b *IncusBackend) runInGuest(id string, cmd []string) (exitCode int, err error) {
	op, err := b.client.ExecInstance(id, api.InstanceExecPost{Command: cmd}, nil)
	if err != nil {
		return -1, err
	}
	if err := waitOperation(op); err != nil {
		return -1, err
	}
	if rc, ok := op.Get().Metadata["return"].(float64); ok {
		return int(rc), nil
	}
	return -1, nil
}

// guestUsesNetworkd reports whether the guest already manages at least one
// interface via systemd-networkd, by checking for any existing per-interface
// unit under /etc/systemd/network/. A guest with none is left alone — it's
// either using cloud-init/netplan (already handled) or some other network
// stack (NetworkManager, ifupdown, busybox init) this codebase does not
// try to guess at.
func (b *IncusBackend) guestUsesNetworkd(id string) bool {
	rc, err := b.runInGuest(id, []string{"sh", "-c", "ls /etc/systemd/network/*.network >/dev/null 2>&1"})
	return err == nil && rc == 0
}

func (b *IncusBackend) DetachNetworkIface(id, mac string) error {
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	devName := nicDeviceByMAC(inst, mac)
	if devName == "" {
		return fmt.Errorf("no network interface with MAC %s", mac)
	}
	delete(inst.Devices, devName)
	// Drop the removed NIC from the guest's netplan config too, same
	// unconditional regeneration as AttachNetworkIface above.
	inst.Config["user.network-config"] = lxdNetworkConfig(inst.Devices)
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	if err := waitOperation(op); err != nil {
		return err
	}
	// Best-effort symmetric cleanup of the per-interface systemd-networkd
	// unit afterAttachConfigureGuestNetwork may have pushed for this NIC —
	// otherwise it lingers referencing a device that no longer exists.
	// Never fails the detach itself.
	if state, _, err := b.client.GetInstanceState(id); err == nil && state.Status == "Running" {
		_, _ = b.runInGuest(id, []string{"rm", "-f", "/etc/systemd/network/" + devName + ".network"})
	}
	return nil
}

func (b *IncusBackend) UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error {
	if req.VLANTag != nil && *req.VLANTag != 0 {
		return fmt.Errorf("VLAN tags are not applicable to a container NIC: %w", compute.ErrNotImplemented)
	}
	if req.MAC != nil && *req.MAC != oldMAC {
		return fmt.Errorf("changing a container NIC MAC is not supported: %w", compute.ErrNotImplemented)
	}
	if req.Network == nil {
		return nil
	}
	bridge, err := b.bridgeForNetwork(*req.Network)
	if err != nil {
		return err
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	devName := nicDeviceByMAC(inst, oldMAC)
	if devName == "" {
		return fmt.Errorf("no network interface with MAC %s", oldMAC)
	}
	inst.Devices[devName]["parent"] = bridge
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

// nextNicName returns the first free ethN device name for a container.
func nextNicName(devices map[string]map[string]string) string {
	n := 0
	for name := range devices {
		if len(name) > 3 && name[:3] == "eth" {
			if v, err := strconv.Atoi(name[3:]); err == nil && v >= n {
				n = v + 1
			}
		}
	}
	return fmt.Sprintf("eth%d", n)
}

// nicDeviceByMAC returns the NIC device name whose volatile MAC equals
// mac (LXD stores NIC MACs as volatile.<name>.hwaddr config keys).
func nicDeviceByMAC(inst *api.Instance, mac string) string {
	for name, dev := range inst.Devices {
		if dev["type"] != "nic" {
			continue
		}
		if inst.Config["volatile."+name+".hwaddr"] == mac {
			return name
		}
	}
	return ""
}
func (b *IncusBackend) AttachUSBDevice(id, vendorID, productID string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) DetachUSBDevice(id, vendorID, productID string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) ListHostUSBDevices() ([]models.USBDevice, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) AttachPCIDevice(id string, addresses []string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) DetachPCIDevice(id, address string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) ListHostPCIDevices() ([]models.PCIIOMMUGroup, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) GetHostPCIPreflight() (models.PCIPreflightInfo, error) {
	return models.PCIPreflightInfo{}, compute.ErrNotImplemented
}
func (b *IncusBackend) AttachSharedFolder(id, hostPath, tag string, readOnly bool) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) DetachSharedFolder(id, tag string) error {
	return compute.ErrNotImplemented
}

// --- Snapshots ---

func (b *IncusBackend) ListSnapshots(domainID string) ([]models.Snapshot, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) CreateSnapshot(domainID string, req models.CreateSnapshotRequest) (models.Snapshot, error) {
	return models.Snapshot{}, compute.ErrNotImplemented
}
func (b *IncusBackend) DeleteSnapshot(domainID, snapID string) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *IncusBackend) RevertSnapshot(domainID, snapID string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error) {
	return nil, compute.ErrNotImplemented
}

// --- Storage / pools / volumes / ISO ---

func (b *IncusBackend) ListStoragePools() ([]models.StoragePool, error) {
	if b.client == nil {
		return nil, compute.ErrNotImplemented
	}
	pools, err := b.client.GetStoragePools()
	if err != nil {
		return nil, err
	}
	result := make([]models.StoragePool, 0, len(pools))
	for _, p := range pools {
		sp := models.StoragePool{
			Name:      p.Name,
			Type:      p.Driver,
			Purpose:   "container",
			State:     strings.ToLower(p.Status),
			Autostart: true,
		}
		if sp.State == "" {
			sp.State = "active"
		}
		if src, ok := p.Config["source"]; ok && src != "" {
			sp.Path = src
		} else {
			sp.Path = "/var/lib/incus/storage-pools/" + p.Name
		}
		if res, err := b.client.GetStoragePoolResources(p.Name); err == nil && res != nil {
			sp.Capacity = int64(res.Space.Total)
			sp.Allocated = int64(res.Space.Used)
			sp.Available = int64(res.Space.Total - res.Space.Used)
		}
		result = append(result, sp)
	}
	return result, nil
}
func (b *IncusBackend) CreateStoragePool(ctx context.Context, req models.CreatePoolRequest) (models.StoragePool, error) {
	if b.client == nil {
		return models.StoragePool{}, compute.ErrNotImplemented
	}
	driver := strings.TrimPrefix(strings.TrimSpace(req.Type), "incus-")
	if driver == "" {
		driver = "dir"
	}

	configMap := make(map[string]string)
	if req.Path != "" {
		if strings.Contains(req.Path, "..") {
			return models.StoragePool{}, fmt.Errorf("invalid pool path %q: traversal not allowed", req.Path)
		}
		if driver == "dir" {
			if err := os.MkdirAll(req.Path, 0755); err != nil {
				return models.StoragePool{}, fmt.Errorf("create pool directory %q: %w", req.Path, err)
			}
		}
		configMap["source"] = req.Path
	}

	post := api.StoragePoolsPost{
		Name:   req.Name,
		Driver: driver,
		StoragePoolPut: api.StoragePoolPut{
			Config: configMap,
		},
	}

	if err := b.client.CreateStoragePool(post); err != nil {
		return models.StoragePool{}, err
	}

	pool, _, err := b.client.GetStoragePool(req.Name)
	if err != nil {
		return models.StoragePool{
			Name:    req.Name,
			Type:    driver,
			Purpose: compute.PoolPurposeContainer,
			State:   "active",
			Path:    req.Path,
		}, nil
	}

	sp := models.StoragePool{
		Name:      pool.Name,
		Type:      pool.Driver,
		Purpose:   compute.PoolPurposeContainer,
		State:     strings.ToLower(pool.Status),
		Autostart: true,
	}
	if sp.State == "" {
		sp.State = "active"
	}
	if src, ok := pool.Config["source"]; ok && src != "" {
		sp.Path = src
	} else {
		sp.Path = "/var/lib/incus/storage-pools/" + pool.Name
	}
	if res, err := b.client.GetStoragePoolResources(pool.Name); err == nil && res != nil {
		sp.Capacity = int64(res.Space.Total)
		sp.Allocated = int64(res.Space.Used)
		sp.Available = int64(res.Space.Total - res.Space.Used)
	}
	return sp, nil
}
func (b *IncusBackend) UpdateStoragePool(ctx context.Context, name string, req models.UpdatePoolRequest) (models.StoragePool, error) {
	return models.StoragePool{}, compute.ErrNotImplemented
}
func (b *IncusBackend) DeletePool(name string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	return b.client.DeleteStoragePool(name)
}
func (b *IncusBackend) RefreshPool(name string) error { return compute.ErrNotImplemented }
func (b *IncusBackend) GetPoolPath(name string) (string, error) {
	if b.client == nil {
		return "", compute.ErrNotImplemented
	}
	pool, _, err := b.client.GetStoragePool(name)
	if err != nil {
		return "", err
	}
	// Return the source path from pool config, or the default path
	if src, ok := pool.Config["source"]; ok && src != "" {
		return src, nil
	}
	return "/var/lib/incus/storage-pools/" + name, nil
}
func (b *IncusBackend) DiskPoolName() string { return "" }
func (b *IncusBackend) ISOPoolName() string  { return "" }
func (b *IncusBackend) ListStorageVolumes(poolName string) ([]models.StorageVolume, error) {
	if b.client == nil {
		return nil, compute.ErrNotImplemented
	}
	// Try GetStoragePoolVolumesFull first
	volsFull, err := b.client.GetStoragePoolVolumesFull(poolName)
	if err == nil {
		result := make([]models.StorageVolume, 0, len(volsFull))
		for _, v := range volsFull {
			format := v.ContentType
			if format == "" {
				format = v.Type
			}
			path := "/var/lib/incus/storage-pools/" + poolName + "/" + v.Type + "s/" + v.Name
			var capBytes, allocBytes int64
			if v.State != nil && v.State.Usage != nil {
				capBytes = v.State.Usage.Total
				allocBytes = int64(v.State.Usage.Used)
			}
			result = append(result, models.StorageVolume{
				Name:      v.Name,
				Pool:      poolName,
				Path:      path,
				Format:    format,
				Capacity:  capBytes,
				Allocated: allocBytes,
			})
		}
		return result, nil
	}

	// Fallback to GetStoragePoolVolumes
	vols, err := b.client.GetStoragePoolVolumes(poolName)
	if err != nil {
		return nil, err
	}
	result := make([]models.StorageVolume, 0, len(vols))
	for _, v := range vols {
		format := v.ContentType
		if format == "" {
			format = v.Type
		}
		path := "/var/lib/incus/storage-pools/" + poolName + "/" + v.Type + "s/" + v.Name
		var capBytes, allocBytes int64
		if st, err := b.client.GetStoragePoolVolumeState(poolName, v.Type, v.Name); err == nil && st != nil && st.Usage != nil {
			capBytes = st.Usage.Total
			allocBytes = int64(st.Usage.Used)
		}
		result = append(result, models.StorageVolume{
			Name:      v.Name,
			Pool:      poolName,
			Path:      path,
			Format:    format,
			Capacity:  capBytes,
			Allocated: allocBytes,
		})
	}
	return result, nil
}
func (b *IncusBackend) GetStorageVolume(poolName, volName string) (models.StorageVolume, error) {
	return models.StorageVolume{}, compute.ErrNotImplemented
}
func (b *IncusBackend) CreateStorageVolume(req models.CreateVolumeRequest) (models.StorageVolume, error) {
	return models.StorageVolume{}, compute.ErrNotImplemented
}
func (b *IncusBackend) ResizeStorageVolume(poolName, volName string, newSizeGB int64) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) DeleteStorageVolume(poolName, volName string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	if err := b.client.DeleteStoragePoolVolume(poolName, "custom", volName); err == nil {
		return nil
	}
	return b.client.DeleteStoragePoolVolume(poolName, "container", volName)
}
func (b *IncusBackend) DeleteIncusImage(fingerprint string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	op, err := b.client.DeleteImage(fingerprint)
	if err != nil {
		return err
	}
	return op.Wait()
}

// GetIncusImagesVolume reports where Incus caches downloaded images.
//
// An empty answer means the daemon default, /var/lib/incus/images,
// which sits on the system disk: on a host whose storage was
// deliberately put elsewhere, that is a cache growing on the one disk
// the operator was trying to keep free, and nothing in the UI said so.
func (b *IncusBackend) GetIncusImagesVolume() (string, error) {
	if b.client == nil {
		return "", compute.ErrNotImplemented
	}
	s, _, err := b.client.GetServer()
	if err != nil {
		return "", err
	}
	if s.Config == nil {
		return "", nil
	}
	return s.Config["storage.images_volume"], nil
}

// SetIncusImagesVolume repoints the image cache at a custom volume.
//
// Incus moves the existing cache itself when the key changes, so this
// does not copy anything — but it validates hard first, because the
// daemon rejects the key outright if the pool or volume is wrong and
// the error it returns is not one an operator can act on.
//
// An empty pool clears the key and restores the default location.
func (b *IncusBackend) SetIncusImagesVolume(pool, volume string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	s, etag, err := b.client.GetServer()
	if err != nil {
		return err
	}
	cfg := make(map[string]string, len(s.Config)+1)
	for k, v := range s.Config {
		cfg[k] = v
	}

	if pool == "" {
		delete(cfg, "storage.images_volume")
	} else {
		if volume == "" {
			volume = "images"
		}
		// The pool has to exist before the volume can, and a missing
		// pool is the mistake worth naming: the daemon's own error for
		// it does not say which pool it could not find.
		if _, _, err := b.client.GetStoragePool(pool); err != nil {
			return fmt.Errorf("storage pool %q not found in Incus: %w", pool, err)
		}
		// Create the volume only when it is not already there, so
		// re-pointing at a volume that already holds the cache is not
		// an error.
		if _, _, err := b.client.GetStoragePoolVolume(pool, "custom", volume); err != nil {
			if cerr := b.client.CreateStoragePoolVolume(pool, api.StorageVolumesPost{
				Name: volume,
				Type: "custom",
				StorageVolumePut: api.StorageVolumePut{
					Description: "WebKVM: Incus image cache",
				},
			}); cerr != nil {
				return fmt.Errorf("create volume %q in pool %q: %w", volume, pool, cerr)
			}
		}
		cfg["storage.images_volume"] = pool + "/" + volume
	}

	return b.client.UpdateServer(api.ServerPut{Config: cfg}, etag)
}

func (b *IncusBackend) VolumeExists(poolName, volName string) (bool, error) {
	return false, compute.ErrNotImplemented
}
func (b *IncusBackend) FindVolumeAttachments(poolName, volName string) ([]models.VolumeAttachment, error) {
	return nil, compute.ErrNotImplemented
}

// BackingDependents has no meaning here: Incus volumes are managed by
// the daemon (zfs/btrfs/lvm/dir), not raw qcow2 files an operator can
// layer overlays on, and the daemon refuses to delete a volume that
// still has dependants itself. No dependants, no error, so callers do
// not have to special-case the container backend.
func (b *IncusBackend) BackingDependents(poolName, volName string) ([]string, error) {
	return nil, nil
}

func (b *IncusBackend) BackingDependentsOfPath(path string) ([]string, error) {
	return nil, nil
}
func (b *IncusBackend) GetISOs(poolName string) ([]models.ISOScanResult, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) RenameISO(oldName, newName, poolName string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) DeleteISO(name, poolName string) error { return compute.ErrNotImplemented }

// MoveDomainStorage relocates a container's root disk to another Incus
// pool using the daemon's native cross-pool move.
//
// Incus copies the filesystem into the destination pool and switches the
// instance over atomically, so there is no window where the container
// references a half-written rootfs. A running container is NOT a problem
// here — unlike a libvirt VM, whose disk file cannot be copied while
// QEMU holds it — but Incus does require it to be stopped for a local
// pool move, so the caller is told plainly when it is not.
func (b *IncusBackend) MoveDomainStorage(id, destPool string, onProgress func(pct float64, stage string)) error {
	if b.client == nil {
		return errors.New("incus client not initialized")
	}
	report := func(pct float64, stage string) {
		if onProgress != nil {
			onProgress(pct, stage)
		}
	}

	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return fmt.Errorf("get instance %q: %w", id, err)
	}
	if inst.StatusCode == api.Running {
		return compute.ErrDomainMustBeStoppedToMove
	}

	// Refusing a same-pool move is not pedantry: Incus would happily
	// perform a full copy of the rootfs onto itself.
	if cur := b.instanceRootPool(inst); cur != "" && cur == destPool {
		return compute.ErrSamePool
	}
	if _, _, err := b.client.GetStoragePool(destPool); err != nil {
		return fmt.Errorf("destination pool %q: %w", destPool, err)
	}

	report(5, "move_preparing")
	// Migration MUST be true even though nothing leaves this host:
	// the SDK reads a false here as "this is a rename" and refuses the
	// call outright. Name repeats the current one because a local
	// pool move keeps the instance's identity; only its storage moves.
	// InstanceOnly stays false so snapshots travel with the instance
	// instead of being silently dropped.
	op, err := b.client.MigrateInstance(id, api.InstancePost{
		Name:         id,
		Pool:         destPool,
		Migration:    true,
		InstanceOnly: false,
	})
	if err != nil {
		return fmt.Errorf("move instance to pool %q: %w", destPool, err)
	}

	// The daemon reports real byte progress; surfacing it keeps a
	// multi-GB move from looking hung. Progress is mapped into the
	// 10-95 band left free by the surrounding stages, so the bar never
	// jumps back to 0 or claims completion before op.Wait returns.
	_, _ = op.AddHandler(func(o api.Operation) {
		txt, _ := o.Metadata["move_progress"].(string)
		if pct, ok := parseTransferPercent(txt); ok {
			report(10+pct*0.85, "move_copying")
			return
		}
		report(-1, "move_copying")
	})
	report(10, "move_copying")
	if err := op.Wait(); err != nil {
		return fmt.Errorf("move instance to pool %q: %w", destPool, err)
	}
	report(100, "move_done")
	return nil
}

// parseTransferPercent extracts a 0-100 figure from Incus's progress
// metadata, which is a human string rather than a number — the shapes
// seen in practice are "45% (123.45MB/s)" and "1.20GB (45.00MB/s)".
// Only the first carries a percentage; the second reports bytes with no
// total, so there is nothing to compute a fraction from and the caller
// falls back to "stage changed, percentage unknown".
func parseTransferPercent(s string) (float64, bool) {
	i := strings.IndexByte(s, '%')
	if i <= 0 {
		return 0, false
	}
	pct, err := strconv.ParseFloat(strings.TrimSpace(s[:i]), 64)
	if err != nil || pct < 0 || pct > 100 {
		return 0, false
	}
	return pct, true
}

// instanceRootPool returns the pool backing an instance's root disk,
// looking at the expanded devices so a pool inherited from a profile is
// seen too.
func (b *IncusBackend) instanceRootPool(inst *api.Instance) string {
	if inst == nil {
		return ""
	}
	for _, devs := range []map[string]map[string]string{inst.ExpandedDevices, inst.Devices} {
		for name, dev := range devs {
			if name == "root" && dev["type"] == "disk" {
				if p := strings.TrimSpace(dev["pool"]); p != "" {
					return p
				}
			}
		}
	}
	return ""
}

// MoveVolume relocates a custom storage volume between Incus pools.
//
// Container root disks are not custom volumes — those move with the
// instance, via MoveDomainStorage.
func (b *IncusBackend) MoveVolume(srcPool, volName, destPool string, opts compute.MoveVolumeOpts) error {
	if b.client == nil {
		return errors.New("incus client not initialized")
	}
	if srcPool == destPool {
		return compute.ErrSamePool
	}
	// Incus has no ISO pool concept; only libvirt serves those.
	if opts.Kind == "iso" {
		return compute.ErrNotImplemented
	}

	src, _, err := b.client.GetStoragePoolVolume(srcPool, "custom", volName)
	if err != nil {
		return fmt.Errorf("get volume %q in pool %q: %w", volName, srcPool, err)
	}
	newName := opts.NewName
	if newName == "" {
		newName = volName
	}
	if opts.OnProgress != nil {
		opts.OnProgress(10, "move_copying")
	}

	// Both calls return a RemoteOperation (they can cross servers, even
	// though here source and destination are the same daemon), which is
	// a different interface from the local Operation waitOperation
	// takes.
	copyArgs := incus.StoragePoolVolumeCopyArgs{Name: newName, VolumeOnly: false}
	verb := "move"
	var op incus.RemoteOperation
	if opts.KeepSource {
		verb = "copy"
		op, err = b.client.CopyStoragePoolVolume(destPool, b.client, srcPool, *src, &copyArgs)
	} else {
		op, err = b.client.MoveStoragePoolVolume(destPool, b.client, srcPool, *src,
			&incus.StoragePoolVolumeMoveArgs{StoragePoolVolumeCopyArgs: copyArgs})
	}
	if err != nil {
		return fmt.Errorf("%s volume to %q: %w", verb, destPool, err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("%s volume to %q: %w", verb, destPool, err)
	}
	if opts.OnProgress != nil {
		opts.OnProgress(100, "move_done")
	}
	return nil
}
func (b *IncusBackend) DeleteVMDiskFiles(vmName string, exact ...string) (deleted []string, skipped []string, err error) {
	return nil, nil, compute.ErrNotImplemented
}
func (b *IncusBackend) RefreshCIFSSecretIfNeeded(ctx context.Context, poolName string) (*compute.SecretRef, error) {
	return nil, compute.ErrNotImplemented
}

// --- Networking ---

func (b *IncusBackend) ListNetworks() ([]models.Network, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) CreateNetwork(req models.CreateNetworkRequest) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *IncusBackend) UpdateNetwork(name string, req models.UpdateNetworkRequest) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *IncusBackend) DeleteNetwork(id string) error { return compute.ErrNotImplemented }
func (b *IncusBackend) StartNetwork(name string) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *IncusBackend) StopNetwork(name string) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *IncusBackend) CheckVLANSupport(networkName string) (models.VlanSupport, error) {
	return models.VlanSupport{}, compute.ErrNotImplemented
}
func (b *IncusBackend) NetworkLeases(name string) ([]models.DHCPLease, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) ReleaseNetworkLease(br, ip, mac string) error {
	return compute.ErrNotImplemented
}

// --- Console / cloud-init / metadata ---

// lxdConsoleStream adapts an LXD interactive exec (PTY over websockets)
// to the neutral ConsoleStream the serial proxy already uses. Bytes flow
// straight from/to the container PTY through the client's internal
// websocket bridge — the frontend terminal never notices the hypervisor.
type lxdConsoleStream struct {
	op     incus.Operation
	stdin  *io.PipeWriter
	stdout *io.PipeReader
	done   chan bool
	ctrlMu sync.Mutex
	ctrl   *websocket.Conn // window-resize/signal channel
	closed atomic.Bool
}

func (s *lxdConsoleStream) Recv(buf []byte) (int, error) { return s.stdout.Read(buf) }
func (s *lxdConsoleStream) Send(b []byte) (int, error) {
	// The KVM serial proxy also forwards resize JSON; only LXD execs are
	// real PTYs, so translate resize frames here without the proxy knowing.
	var rs struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if json.Unmarshal(b, &rs) == nil && rs.Cols > 0 && rs.Rows > 0 {
		_ = s.resize(rs.Cols, rs.Rows)
		return len(b), nil
	}
	return s.stdin.Write(b)
}
func (s *lxdConsoleStream) resize(cols, rows int) error {
	ctrl := s.controlConn()
	if ctrl == nil {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"command": "window-resize", "width": cols, "height": rows})
	return ctrl.WriteMessage(websocket.TextMessage, payload)
}
func (s *lxdConsoleStream) controlConn() *websocket.Conn {
	s.ctrlMu.Lock()
	defer s.ctrlMu.Unlock()
	return s.ctrl
}
func (s *lxdConsoleStream) setControl(conn *websocket.Conn) {
	s.ctrlMu.Lock()
	s.ctrl = conn
	s.ctrlMu.Unlock()
}
func (s *lxdConsoleStream) Finish() error {
	// Signal SIGHUP on the control channel so the PTY shell exits.
	if c := s.controlConn(); c != nil {
		payload, _ := json.Marshal(map[string]any{"command": "signal", "signal": 1})
		_ = c.WriteMessage(websocket.TextMessage, payload)
	}
	_ = s.stdin.Close()
	return nil
}
func (s *lxdConsoleStream) Free() {
	s.closed.Store(true)
	_ = s.stdin.Close()
	_ = s.stdout.Close()
	// Wait for the exec bridge to finish (bounded).
	select {
	case <-s.done:
	case <-time.After(3 * time.Second):
	}
}

// OpenSerialConsole opens an interactive shell (bash) inside the LXD
// instance and returns a ConsoleStream bridged to it. A stopped or
// missing instance returns compute.ErrDomainNotRunning so the serial
// proxy retries until the container is running.
func (b *IncusBackend) OpenSerialConsole(id string) (compute.ConsoleStream, error) {
	// Pre-check the instance is running: exec on a stopped container
	// would fail asynchronously with no websockets to attach to.
	state, _, err := b.client.GetInstanceState(id)
	if err != nil {
		return nil, mapLXErr(err)
	}
	if state.Status != "Running" {
		return nil, compute.ErrDomainNotRunning
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	done := make(chan bool)
	stream := &lxdConsoleStream{stdin: stdinW, stdout: stdoutR, done: done}

	exec := api.InstanceExecPost{
		Command:     []string{"bash"},
		Interactive: true,
		WaitForWS:   true,
		Environment: map[string]string{"TERM": "xterm-256color"},
		Width:       120,
		Height:      30,
	}
	op, err := b.client.ExecInstance(id, exec, &incus.InstanceExecArgs{
		Stdin:    stdinR,
		Stdout:   stdoutW,
		Control:  stream.setControl,
		DataDone: done,
	})
	if err != nil {
		_ = stdinW.Close()
		_ = stdoutR.Close()
		return nil, mapLXErr(err)
	}
	stream.op = op
	return stream, nil
}
func (b *IncusBackend) SetUserPassword(id, user, password string) error {
	return compute.ErrNotImplemented
}

// WebKVM app metadata for LXD instances is stored in two custom config
// keys (v1.4 Fase 3): user.webkvm.tags (comma-joined) and
// user.webkvm.desc (JSON of the remaining VMMeta fields). Keys persist
// with the instance and keep the v1.3 RBAC / backup-by-tag policies
// working for containers with zero changes on the API side.
const (
	metaTagsKey = "user.webkvm.tags"
	metaDescKey = "user.webkvm.desc"
)

type lxdWebKVMMeta struct {
	Alias     string   `json:"alias,omitempty"`
	Notes     string   `json:"notes,omitempty"`
	Cover     string   `json:"cover,omitempty"`
	Groups    []string `json:"groups,omitempty"`
	OwnerID   string   `json:"owner_id,omitempty"`
	Template  bool     `json:"template"`
	Shared    bool     `json:"shared,omitempty"`
	CiUser    string   `json:"ci_user,omitempty"`
	AppInfo   string   `json:"app_info,omitempty"`
	UpdatedAt int64    `json:"updated_at,omitempty"`
}

func (b *IncusBackend) GetVMMeta(uuid string) (models.VMMeta, error) {
	inst, _, err := b.client.GetInstance(uuid)
	if err != nil {
		return models.VMMeta{}, err
	}
	return lxdMetaToModel(inst.Config), nil
}

// lxdMetaToModel decodes the two webkvm config keys back into VMMeta.
// Missing keys yield the zero value (no error), matching KVM.
func lxdMetaToModel(config map[string]string) models.VMMeta {
	meta := models.VMMeta{}
	if raw, ok := config[metaTagsKey]; ok && raw != "" {
		meta.Tags = strings.Split(raw, ",")
	}
	if raw, ok := config[metaDescKey]; ok && raw != "" {
		var d lxdWebKVMMeta
		if err := json.Unmarshal([]byte(raw), &d); err == nil {
			meta.Alias, meta.Notes, meta.Cover = d.Alias, d.Notes, d.Cover
			meta.Groups, meta.OwnerID = d.Groups, d.OwnerID
			meta.Template, meta.CiUser = d.Template, d.CiUser
			meta.Shared = d.Shared
			meta.AppInfo, meta.UpdatedAt = d.AppInfo, d.UpdatedAt
		}
	}
	return meta
}

// applyMetaToConfig encodes VMMeta into the two config keys.
func applyMetaToConfig(config map[string]string, meta models.VMMeta) {
	if len(meta.Tags) == 0 {
		delete(config, metaTagsKey)
	} else {
		config[metaTagsKey] = strings.Join(meta.Tags, ",")
	}
	d := lxdWebKVMMeta{
		Alias: meta.Alias, Notes: meta.Notes, Cover: meta.Cover,
		Groups: meta.Groups, OwnerID: meta.OwnerID, Template: meta.Template,
		Shared: meta.Shared, CiUser: meta.CiUser, AppInfo: meta.AppInfo, UpdatedAt: meta.UpdatedAt,
	}
	if d.Alias == "" && d.Notes == "" && d.Cover == "" && len(d.Groups) == 0 &&
		d.OwnerID == "" && !d.Template && !d.Shared && d.CiUser == "" && d.AppInfo == "" && d.UpdatedAt == 0 {
		delete(config, metaDescKey)
		return
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return
	}
	config[metaDescKey] = string(raw)
}

func (b *IncusBackend) SetVMMeta(uuid string, meta models.VMMeta) error {
	return b.setMetaConfig(uuid, func(cfg map[string]string) { applyMetaToConfig(cfg, meta) })
}

func (b *IncusBackend) UpdateVMMeta(uuid string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	inst, etag, err := b.client.GetInstance(uuid)
	if err != nil {
		return models.VMMeta{}, err
	}
	current := lxdMetaToModel(inst.Config)
	if upd.Alias != nil {
		current.Alias = *upd.Alias
	}
	if upd.Notes != nil {
		current.Notes = *upd.Notes
	}
	if upd.Cover != nil {
		current.Cover = *upd.Cover
	}
	if upd.Groups != nil {
		if *upd.Groups == nil {
			current.Groups = nil
		} else {
			current.Groups = *upd.Groups
		}
	}
	if upd.Tags != nil {
		if *upd.Tags == nil {
			current.Tags = nil
		} else {
			current.Tags = *upd.Tags
		}
	}
	if upd.OwnerID != nil {
		current.OwnerID = *upd.OwnerID
	}
	if upd.Template != nil {
		current.Template = *upd.Template
	}
	if upd.Shared != nil {
		current.Shared = *upd.Shared
	}
	if upd.CiUser != nil {
		current.CiUser = *upd.CiUser
	}
	if upd.AppInfo != nil {
		current.AppInfo = *upd.AppInfo
	}
	current.UpdatedAt = time.Now().Unix()
	if err := b.setMetaConfig(uuid, func(cfg map[string]string) { applyMetaToConfig(cfg, current) }, etag); err != nil {
		return models.VMMeta{}, err
	}
	return current, nil
}

// setMetaConfig is the shared read-modify-write helper: it applies fn
// to a copy of the instance config and pushes the result via
// UpdateInstance, preserving devices/profiles/description. An ETag from
// a previous read avoids clobbering a concurrent edit.
func (b *IncusBackend) setMetaConfig(uuid string, fn func(map[string]string), etag ...string) error {
	inst, curETag, err := b.client.GetInstance(uuid)
	if err != nil {
		return err
	}
	cfg := inst.Config
	if cfg == nil {
		cfg = map[string]string{}
	}
	fn(cfg)
	if len(etag) > 0 {
		curETag = etag[0]
	}
	op, err := b.client.UpdateInstance(uuid, api.InstancePut{
		Config:      cfg,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, curETag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) GetVNCInfo(id string) (compute.GraphicsInfo, error) {
	return compute.GraphicsInfo{}, compute.ErrNotImplemented
}
func (b *IncusBackend) GetDomainIP(id string) string { return "" }
func (b *IncusBackend) GetDomainXML(id string) (string, error) {
	return "", compute.ErrNotImplemented
}
func (b *IncusBackend) GuestGetClipboard(id string) (string, error) {
	return "", compute.ErrNotImplemented
}
func (b *IncusBackend) GuestSetClipboard(id, text string) error { return compute.ErrNotImplemented }

// GetGuestInfo has no Incus equivalent: the QEMU guest agent is a
// KVM concept, and for containers the host already sees the real
// filesystem and network state directly.
func (b *IncusBackend) GetGuestInfo(id string) (compute.GuestInfo, error) {
	return compute.GuestInfo{}, compute.ErrNotImplemented
}

// --- Backup / export / OVA / import ---

// ExportDomain streams the container's native LXD backup export straight
// into w via io.Copy (v1.4 Fase 3). Modern LXD (6.x) dropped the
// standalone /1.0/instances/<name>/export endpoint — `lxc export` now
// creates a temporary instance backup, streams its export and deletes it
// again. This mirrors that exact flow:
//
//  1. CreateInstanceBackup → wait for the storage snapshot to land.
//  2. Raw GET /1.0/instances/<name>/backups/<backup>/export, copied
//     chunk-by-chunk into w — no whole-archive buffering in RAM, no
//     temp download on our side.
//  3. DeleteInstanceBackup (deferred) so no backup is left behind.
//
// When the caller asked for zstd, the stream is re-encoded with a
// streaming zstd compressor (bounded window, still no full buffering)
// so the declared Content-Type stays truthful.
func (b *IncusBackend) ExportDomain(ctx context.Context, id string, opts compute.ExportBackupOptions, w io.Writer) (backupstore.ProducerResult, error) {
	backupName := fmt.Sprintf("webkvm-export-%d", time.Now().UnixNano())
	op, err := b.client.CreateInstanceBackup(id, api.InstanceBackupsPost{Name: backupName})
	if err != nil {
		return backupstore.ProducerResult{}, err
	}
	if err := waitOperationLong(op); err != nil {
		// Clean up the (possibly half-created) backup so it does not
		// accumulate on the Incus side after repeated failures.
		if dop, derr := b.client.DeleteInstanceBackup(id, backupName); derr == nil {
			_ = waitOperation(dop)
		}
		return backupstore.ProducerResult{}, err
	}
	defer func() {
		if dop, derr := b.client.DeleteInstanceBackup(id, backupName); derr == nil {
			_ = waitOperation(dop)
		}
	}()

	body, err := b.exportStream(ctx, id, backupName)
	if err != nil {
		return backupstore.ProducerResult{}, err
	}
	defer body.Close()

	var out io.Writer = w
	var zw io.Closer
	if opts.Compress == "zstd" {
		level := opts.ZstdLevel
		if level < 1 || level > 22 {
			level = 19
		}
		enc, zerr := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(level)))
		if zerr != nil {
			return backupstore.ProducerResult{}, zerr
		}
		zw = enc
		out = enc
	}
	n, err := io.Copy(out, body)
	if err != nil {
		return backupstore.ProducerResult{}, err
	}
	if zw != nil {
		if cerr := zw.Close(); cerr != nil {
			return backupstore.ProducerResult{}, cerr
		}
	}
	return backupstore.ProducerResult{DisksIncluded: 1, TotalBytes: n}, nil
}

// exportStream opens the raw binary backup-export endpoint. The official
// client only hands out io.WriteSeeker downloaders; streaming into an
// arbitrary io.Writer needs a minimal unix-socket HTTP round-trip (same
// socket + X-LXD-authenticated handshake as the client, which is how the
// local daemon authorizes the root peer).
func (b *IncusBackend) exportStream(ctx context.Context, id, backupName string) (io.ReadCloser, error) {
	if b.socketPath == "" {
		return nil, errors.New("lxd socket path unknown; cannot stream instance export")
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", b.socketPath)
		},
		DisableKeepAlives: true,
	}
	hc := &http.Client{Transport: transport}
	u := "http://unix.socket/1.0/instances/" + url.PathEscape(id) + "/backups/" + url.PathEscape(backupName) + "/export"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-LXD-authenticated", "true")
	req.Header.Set("User-Agent", "webkvm")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("LXD export failed: %s: %s", resp.Status, strings.TrimSpace(string(detail)))
	}
	return resp.Body, nil
}

func (b *IncusBackend) ExportDomainOVA(ctx context.Context, id string, opts compute.OVAOptions, w io.Writer) error {
	return compute.ErrNotImplemented
}

// EstimateExportSize returns the configured root disk size as a
// pre-export progress estimate. The true archive size is not knowable
// without exporting, so this is the documented upper bound; 0 = unknown.
func (b *IncusBackend) EstimateExportSize(ctx context.Context, id string, compress bool) (int64, error) {
	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return 0, err
	}
	if root, ok := inst.Devices["root"]; ok {
		if gb := parseDiskSizeGB(root["size"]); gb > 0 {
			return gb * int64(1<<30), nil
		}
	}
	return 0, nil
}
func (b *IncusBackend) EstimateOVASize(ctx context.Context, id string, target compute.OVATarget) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *IncusBackend) ImportDomain(tarPath, newName, poolName string, opts compute.ImportOpts) (string, string, []string, error) {
	if b.client == nil {
		return "", "", nil, errors.New("incus client not initialized")
	}
	f, err := os.Open(tarPath)
	if err != nil {
		return "", "", nil, fmt.Errorf("open backup file: %w", err)
	}
	defer f.Close()

	if opts.OnProgress != nil {
		opts.OnProgress(10, "restore_copying", map[string]any{"file": filepath.Base(tarPath)})
	}

	args := incus.InstanceBackupArgs{
		BackupFile: f,
		Name:       newName,
		PoolName:   poolName,
	}
	// If the pool is a KVM pool (webkvm-disks) or doesn't exist in Incus,
	// let Incus use its default pool.
	if poolName == "" || poolName == "webkvm-disks" {
		args.PoolName = ""
	}

	op, err := b.client.CreateInstanceFromBackup(args)
	if err != nil {
		return "", "", nil, fmt.Errorf("create instance from backup: %w", err)
	}
	if err := waitOperationLong(op); err != nil {
		return "", "", nil, fmt.Errorf("import backup: %w", err)
	}

	resolvedName := newName
	if resolvedName != "" {
		// Clean up volatile MAC and last_state so Incus generates a fresh MAC and doesn't conflict with existing containers
		inst, curETag, gerr := b.client.GetInstance(resolvedName)
		if gerr == nil {
			changed := false
			for k := range inst.Config {
				if strings.HasPrefix(k, "volatile.eth") || strings.HasPrefix(k, "volatile.last_state") {
					delete(inst.Config, k)
					changed = true
				}
			}
			if opts.Autostart != nil {
				if *opts.Autostart {
					inst.Config["boot.autostart"] = "true"
				} else {
					inst.Config["boot.autostart"] = "false"
				}
				changed = true
			}
			// opts.Network: rewire eth0 to the requested bridge/managed network.
			if opts.Network != "" {
				if inst.Devices == nil {
					inst.Devices = map[string]map[string]string{}
				}
				// Preserve existing eth0 device settings (limits, etc.) but
				// switch the parent network.
				eth0 := inst.Devices["eth0"]
				if eth0 == nil {
					eth0 = map[string]string{"type": "nic", "nictype": "bridged"}
				}
				eth0["parent"] = opts.Network
				eth0["network"] = opts.Network
				inst.Devices["eth0"] = eth0
				changed = true
			}
			if changed {
				put := inst.Writable()
				// Explicitly include Devices in the PUT to ensure network device is persisted
				put.Devices = inst.Devices
				if uop, uerr := b.client.UpdateInstance(resolvedName, put, curETag); uerr == nil {
					_ = waitOperation(uop)
				} else {
					slog.Warn("import: failed to update instance network", "name", resolvedName, "err", uerr)
				}
			}
		}
	}

	if opts.OnProgress != nil {
		opts.OnProgress(100, "done", nil)
	}

	return resolvedName, resolvedName, nil, nil
}
func (b *IncusBackend) ImportOVA(ovaPath, newName, poolName string) (string, string, error) {
	return "", "", compute.ErrNotImplemented
}

// Capabilities reports what LXD supports (Fase 3: listing, lifecycle,
// serial console, create/update, metadata and streaming backups).
func (b *IncusBackend) Capabilities() compute.Capabilities {
	return compute.Capabilities{
		SupportsSerialConsole: true,
	}
}

// --- helpers ---

// instanceToVM maps an LXD instance to the neutral domain model. Pure
// and unit-tested (the fake-server integration test feeds real payloads).
// rootPoolForProfiles resolves the storage pool that the instance's own
// profiles already put its root disk on, so a create that only sets a
// disk SIZE does not have to invent a pool name.
//
// Order matters: profiles are applied in sequence and the last one to
// define a device wins, which is why the list is walked in reverse. An
// unreadable profile is skipped rather than fatal — the caller treats an
// empty result as "leave the profile's root alone", which is strictly
// safer than guessing.
//
// Falls back to the WebKVM-managed pool only if it really exists, so a
// host that never provisioned it is not handed a name that is wrong in a
// different way.
func (b *IncusBackend) rootPoolForProfiles(profiles []string) string {
	if b.client == nil {
		return ""
	}
	for i := len(profiles) - 1; i >= 0; i-- {
		prof, _, err := b.client.GetProfile(profiles[i])
		if err != nil || prof == nil {
			continue
		}
		for name, dev := range prof.Devices {
			if name != "root" || dev["type"] != "disk" || dev["path"] != "/" {
				continue
			}
			if pool := strings.TrimSpace(dev["pool"]); pool != "" {
				return pool
			}
		}
	}
	if _, _, err := b.client.GetStoragePool(config.IncusPoolName); err == nil {
		return config.IncusPoolName
	}
	return ""
}

// validateProfiles rejects empty or malformed Incus profile names before
// they reach the daemon.
func validateProfiles(profiles []string) error {
	for _, p := range profiles {
		if p == "" {
			return fmt.Errorf("incus profile names must not be empty")
		}
		if strings.TrimSpace(p) != p || strings.ContainsAny(p, "\n\r\t ") {
			return fmt.Errorf("incus profile %q contains invalid characters", p)
		}
	}
	return nil
}

// ListIncusProfiles returns the profile names available on the Incus
// backend (empty for KVM-only hosts, where the API returns []).
func (b *IncusBackend) ListIncusProfiles() ([]string, error) {
	return b.client.GetProfileNames()
}

// defaultIncusCatalog returns the curated official Incus/LXC container image templates.
func defaultIncusCatalog() []models.IncusImageItem {
	return []models.IncusImageItem{
		// Populares / Servidor
		{
			Ref:         "images:ubuntu/resolute",
			Label:       "Ubuntu 26.04 (Resolute)",
			Category:    "popular",
			Distro:      "ubuntu",
			Description: "Ubuntu 26.04 Resolute LTS (desarrollo / compilación más reciente).",
			Arch:        "x86_64 / arm64",
			Badge:       "26.04",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			// NOTE: intentionally "images:ubuntu/24.04", NOT the legacy
			// "ubuntu:24.04" (cloud-images.ubuntu.com) remote. That
			// stream only ships old-style "lxd.tar.xz" image metadata,
			// which the vendored Incus client (v7.4.0) never recognizes
			// as a valid container image — every pull against it fails
			// with "Alias ... doesn't exist" regardless of the alias
			// requested. images.linuxcontainers.org mirrors the same
			// Ubuntu releases in the modern format and actually works.
			Ref:         "images:ubuntu/24.04",
			Label:       "Ubuntu 24.04 LTS (Noble)",
			Category:    "popular",
			Distro:      "ubuntu",
			Description: "LTS más reciente de Ubuntu con soporte oficial hasta 2029.",
			Arch:        "x86_64 / arm64",
			Badge:       "LTS",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:ubuntu/22.04",
			Label:       "Ubuntu 22.04 LTS (Jammy)",
			Category:    "popular",
			Distro:      "ubuntu",
			Description: "Versión LTS clásica y ampliamente compatible para producción.",
			Arch:        "x86_64 / arm64",
			Badge:       "LTS",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:debian/forky",
			Label:       "Debian 14 (Forky)",
			Category:    "dev",
			Distro:      "debian",
			Description: "Siguiente generación de Debian 14 con paquetería de última hornada.",
			Arch:        "x86_64 / arm64",
			Badge:       "Debian 14",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:debian/13",
			Label:       "Debian 13 (Trixie)",
			Category:    "popular",
			Distro:      "debian",
			Description: "Siguiente generación de Debian con paquetería moderna.",
			Arch:        "x86_64 / arm64",
			Badge:       "Testing",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:debian/12",
			Label:       "Debian 12 (Bookworm)",
			Category:    "popular",
			Distro:      "debian",
			Description: "Debian estable, ideal para servidores ligeros y bases de datos.",
			Arch:        "x86_64 / arm64",
			Badge:       "Estable",
			RecVCPUs:    2,
			RecRAMMB:    1024,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:debian/11",
			Label:       "Debian 11 (Bullseye)",
			Category:    "popular",
			Distro:      "debian",
			Description: "Debian oldstable optimizada para servicios heredados.",
			Arch:        "x86_64 / arm64",
			Badge:       "Oldstable",
			RecVCPUs:    1,
			RecRAMMB:    1024,
			RecDiskGB:   10,
		},
		{
			Ref:         "images:debian/sid",
			Label:       "Debian Sid (Unstable)",
			Category:    "dev",
			Distro:      "debian",
			Description: "Rama inestable de Debian para desarrolladores y bleeding-edge.",
			Arch:        "x86_64 / arm64",
			Badge:       "Unstable",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		// Ligeras / Minimalistas
		{
			Ref:         "images:alpine/3.24",
			Label:       "Alpine Linux 3.24",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Alpine Linux versión más reciente.",
			Arch:        "x86_64 / arm64",
			Badge:       "3.24",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/3.23",
			Label:       "Alpine Linux 3.23",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Alpine Linux versión reciente estable.",
			Arch:        "x86_64 / arm64",
			Badge:       "3.23",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/3.22",
			Label:       "Alpine Linux 3.22",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Alpine Linux versión estable.",
			Arch:        "x86_64 / arm64",
			Badge:       "3.22",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/3.21",
			Label:       "Alpine Linux 3.21",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Ultraligera basada en musl y BusyBox (~5MB). Máximo rendimiento y bajo consumo.",
			Arch:        "x86_64 / arm64",
			Badge:       "5 MB",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/3.20",
			Label:       "Alpine Linux 3.20",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Versión estable consolidada de Alpine Linux.",
			Arch:        "x86_64 / arm64",
			Badge:       "5 MB",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:alpine/edge",
			Label:       "Alpine Linux Edge",
			Category:    "minimal",
			Distro:      "alpine",
			Description: "Rama rolling de vanguardia para pruebas de paquetes y bleeding-edge.",
			Arch:        "x86_64 / arm64",
			Badge:       "Edge",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   5,
		},
		{
			Ref:         "images:voidlinux",
			Label:       "Void Linux (glibc)",
			Category:    "minimal",
			Distro:      "void",
			Description: "Distribución independiente ligera con gestor XBPS y runit.",
			Arch:        "x86_64",
			Badge:       "Lightweight",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   10,
		},
		{
			Ref:         "images:voidlinux/musl",
			Label:       "Void Linux (musl)",
			Category:    "minimal",
			Distro:      "void",
			Description: "Variante ultra compacta de Void Linux compilada contra musl libc.",
			Arch:        "x86_64",
			Badge:       "musl",
			RecVCPUs:    1,
			RecRAMMB:    512,
			RecDiskGB:   10,
		},
		{
			Ref:         "images:openwrt/25.12",
			Label:       "OpenWrt 25.12",
			Category:    "minimal",
			Distro:      "openwrt",
			Description: "Sistema operativo embebido ultraligero para redes y servicios.",
			Arch:        "x86_64 / arm64",
			Badge:       "Router",
			RecVCPUs:    1,
			RecRAMMB:    256,
			RecDiskGB:   2,
		},
		{
			Ref:         "images:openwrt/23.05",
			Label:       "OpenWrt 23.05",
			Category:    "minimal",
			Distro:      "openwrt",
			Description: "Sistema operativo embebido ultraligero ideal para enrutamiento y micro-servicios.",
			Arch:        "x86_64 / arm64",
			Badge:       "Router",
			RecVCPUs:    1,
			RecRAMMB:    256,
			RecDiskGB:   2,
		},
		{
			Ref:         "images:devuan/daedalus",
			Label:       "Devuan 5 (Daedalus)",
			Category:    "minimal",
			Distro:      "devuan",
			Description: "Bifurcación de Debian sin systemd (usa SysVinit / OpenRC / runit).",
			Arch:        "x86_64 / arm64",
			Badge:       "No-systemd",
			RecVCPUs:    1,
			RecRAMMB:    1024,
			RecDiskGB:   10,
		},

		// Enterprise / RHEL
		{
			Ref:         "images:rockylinux/10",
			Label:       "Rocky Linux 10",
			Category:    "enterprise",
			Distro:      "rocky",
			Description: "Siguiente generación compatible con Red Hat Enterprise Linux 10.",
			Arch:        "x86_64 / arm64",
			Badge:       "RHEL 10",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:rockylinux/9",
			Label:       "Rocky Linux 9 (images:)",
			Category:    "enterprise",
			Distro:      "rocky",
			Description: "100% compatible binario con Red Hat Enterprise Linux 9 desde remote images:.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:rockylinux/8",
			Label:       "Rocky Linux 8",
			Category:    "enterprise",
			Distro:      "rocky",
			Description: "Versión empresarial con compatibilidad RHEL 8.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:almalinux/9",
			Label:       "AlmaLinux 9 (images:)",
			Category:    "enterprise",
			Distro:      "almalinux",
			Description: "Enterprise Linux comunitaria compatible 1:1 con RHEL 9.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:almalinux/8",
			Label:       "AlmaLinux 8",
			Category:    "enterprise",
			Distro:      "almalinux",
			Description: "Enterprise Linux comunitaria compatible 1:1 con RHEL 8.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:centos/10-Stream",
			Label:       "CentOS Stream 10",
			Category:    "enterprise",
			Distro:      "centos",
			Description: "Rama upstream más reciente de desarrollo para RHEL 10.",
			Arch:        "x86_64 / arm64",
			Badge:       "Stream 10",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:centos/9-Stream",
			Label:       "CentOS Stream 9",
			Category:    "enterprise",
			Distro:      "centos",
			Description: "Rama de desarrollo upstream de Red Hat Enterprise Linux 9.",
			Arch:        "x86_64 / arm64",
			Badge:       "Upstream",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:oracle/10",
			Label:       "Oracle Linux 10",
			Category:    "enterprise",
			Distro:      "oracle",
			Description: "Siguiente generación Enterprise optimizada para bases de datos.",
			Arch:        "x86_64 / arm64",
			Badge:       "Oracle 10",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:oracle/9",
			Label:       "Oracle Linux 9",
			Category:    "enterprise",
			Distro:      "oracle",
			Description: "Distribución Enterprise optimizada para bases de datos y cargas pesadas.",
			Arch:        "x86_64 / arm64",
			Badge:       "Enterprise",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:amazonlinux/2023",
			Label:       "Amazon Linux 2023",
			Category:    "enterprise",
			Distro:      "amazon",
			Description: "Distribución orientada a la nube de AWS optimizada para contenedores.",
			Arch:        "x86_64 / arm64",
			Badge:       "Cloud",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:opensuse/16.0",
			Label:       "openSUSE 16.0",
			Category:    "enterprise",
			Distro:      "opensuse",
			Description: "Siguiente generación de openSUSE Leap.",
			Arch:        "x86_64 / arm64",
			Badge:       "Leap 16",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:opensuse/15.6",
			Label:       "openSUSE Leap 15.6",
			Category:    "enterprise",
			Distro:      "opensuse",
			Description: "Distribución estable orientada a servidores empresariales con YaST.",
			Arch:        "x86_64 / arm64",
			Badge:       "Leap",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},

		// Desarrollo / Rolling / Seguridad
		{
			Ref:         "images:archlinux",
			Label:       "Arch Linux",
			Category:    "dev",
			Distro:      "arch",
			Description: "Distribución Rolling Release siempre al día con pacman.",
			Arch:        "x86_64",
			Badge:       "Rolling",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   15,
		},
		{
			Ref:         "images:fedora/44",
			Label:       "Fedora 44 (Rawhide/Next)",
			Category:    "dev",
			Distro:      "fedora",
			Description: "Siguiente generación de Fedora con las tecnologías más punteras.",
			Arch:        "x86_64 / arm64",
			Badge:       "Fedora 44",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:fedora/43",
			Label:       "Fedora 43",
			Category:    "dev",
			Distro:      "fedora",
			Description: "Versión de vanguardia de Fedora con paquetería actualizada.",
			Arch:        "x86_64 / arm64",
			Badge:       "Fedora 43",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:fedora/41",
			Label:       "Fedora 41",
			Category:    "dev",
			Distro:      "fedora",
			Description: "Última versión estable de Fedora con las últimas novedades de GNU/Linux.",
			Arch:        "x86_64 / arm64",
			Badge:       "Latest",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:fedora/40",
			Label:       "Fedora 40",
			Category:    "dev",
			Distro:      "fedora",
			Description: "Versión probada y estable de Fedora para desarrollo.",
			Arch:        "x86_64 / arm64",
			Badge:       "Stable",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:kali",
			Label:       "Kali Linux",
			Category:    "dev",
			Distro:      "kali",
			Description: "Distribución especializada en auditoría de seguridad y pentesting.",
			Arch:        "x86_64 / arm64",
			Badge:       "SecTools",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:nixos/26.05",
			Label:       "NixOS 26.05",
			Category:    "dev",
			Distro:      "nixos",
			Description: "Distribución declarativa y reproducible basada en el gestor de paquetes Nix.",
			Arch:        "x86_64 / arm64",
			Badge:       "Declarative",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:opensuse/tumbleweed",
			Label:       "openSUSE Tumbleweed",
			Category:    "dev",
			Distro:      "opensuse",
			Description: "Rolling release con pruebas automáticas openQA de openSUSE.",
			Arch:        "x86_64 / arm64",
			Badge:       "Rolling",
			RecVCPUs:    2,
			RecRAMMB:    2048,
			RecDiskGB:   20,
		},
		{
			Ref:         "images:gentoo",
			Label:       "Gentoo Linux",
			Category:    "dev",
			Distro:      "gentoo",
			Description: "Distribución source-based meta altamente optimizable con Portage.",
			Arch:        "x86_64 / arm64",
			Badge:       "Source",
			RecVCPUs:    4,
			RecRAMMB:    4096,
			RecDiskGB:   30,
		},
	}
}

// simplestreamsIndex represents the root structure of Simplestreams index.json
type simplestreamsIndex struct {
	Index map[string]struct {
		Products []string `json:"products"`
	} `json:"index"`
}

type simplestreamsCache struct {
	sync.RWMutex
	lastFetched time.Time
	items       []models.IncusImageItem
}

var ssCache simplestreamsCache

// fetchDynamicSimplestreamsImages queries https://images.linuxcontainers.org/streams/v1/index.json
// dynamically with in-memory caching (6h TTL) and a fast 3-second timeout.
// If offline or if an error occurs, it falls back seamlessly to defaultIncusCatalog().
func fetchDynamicSimplestreamsImages() []models.IncusImageItem {
	ssCache.RLock()
	if time.Since(ssCache.lastFetched) < 6*time.Hour && len(ssCache.items) > 0 {
		cached := make([]models.IncusImageItem, len(ssCache.items))
		copy(cached, ssCache.items)
		ssCache.RUnlock()
		return cached
	}
	ssCache.RUnlock()

	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://images.linuxcontainers.org/streams/v1/index.json", nil)
	if err != nil {
		return defaultIncusCatalog()
	}
	req.Header.Set("User-Agent", "WebKVM/2.4")

	resp, err := client.Do(req)
	if err != nil {
		return defaultIncusCatalog()
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return defaultIncusCatalog()
	}

	var idx simplestreamsIndex
	if err := json.NewDecoder(resp.Body).Decode(&idx); err != nil {
		return defaultIncusCatalog()
	}

	imgNode, ok := idx.Index["images"]
	if !ok || len(imgNode.Products) == 0 {
		return defaultIncusCatalog()
	}

	items := parseSimplestreamsProducts(imgNode.Products)
	if len(items) == 0 {
		return defaultIncusCatalog()
	}

	ssCache.Lock()
	ssCache.lastFetched = time.Now()
	ssCache.items = items
	ssCache.Unlock()

	res := make([]models.IncusImageItem, len(items))
	copy(res, items)
	return res
}

func parseSimplestreamsProducts(products []string) []models.IncusImageItem {
	targetArch := "amd64"
	if runtime.GOARCH == "arm64" {
		targetArch = "arm64"
	}

	type key struct {
		distro  string
		release string
	}
	// best tracks, per (distro, release), both the winning variant AND
	// the release string's ORIGINAL casing from the product line. Ref
	// generation below must reuse that exact casing — simplestreams
	// aliases are case-sensitive (e.g. "centos/10-Stream", capital S),
	// so lower-casing it when building "images:%s/%s" produced a Ref
	// that resolves on nothing, failing every pull for that image with
	// "Alias ... doesn't exist" even though the image is real.
	type best struct {
		variant     string
		origDistro  string
		origRelease string
	}

	variantScore := func(v string) int {
		switch v {
		case "default", "default+ufs":
			return 100
		case "cloud", "cloud+ufs":
			return 90
		case "tinycloud":
			return 80
		case "openrc", "systemd":
			return 70
		default:
			return 10
		}
	}

	bestVariants := make(map[key]best)
	for _, p := range products {
		parts := strings.Split(p, ":")
		if len(parts) < 4 {
			continue
		}
		distro, release, arch, variant := parts[0], parts[1], parts[2], parts[3]
		if arch != targetArch {
			continue
		}
		k := key{distro: strings.ToLower(distro), release: strings.ToLower(release)}
		curr, exists := bestVariants[k]
		if !exists || variantScore(variant) > variantScore(curr.variant) {
			bestVariants[k] = best{variant: variant, origDistro: distro, origRelease: release}
		}
	}

	archLabel := "x86_64 / arm64"
	var out []models.IncusImageItem

	// NOTE: deliberately NOT prepending hardcoded "ubuntu:24.04"/
	// "ubuntu:22.04" entries here (the legacy cloud-images.ubuntu.com
	// remote). That stream's metadata format isn't recognized by the
	// vendored Incus client, so every pull against it fails with
	// "Alias ... doesn't exist" no matter which alias is requested.
	// "images:ubuntu/noble" and "images:ubuntu/jammy" below — sourced
	// from the SAME live index this function already parses — cover the
	// exact same releases and actually work.

	// Sort keys for deterministic output
	keys := make([]key, 0, len(bestVariants))
	for k := range bestVariants {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].distro != keys[j].distro {
			return keys[i].distro < keys[j].distro
		}
		return keys[i].release > keys[j].release
	})

	for _, k := range keys {
		// d/r stay lower-cased for the switch matching below; the Ref
		// sent to Incus must use the ORIGINAL casing the image server
		// advertised (see the "best" struct comment above).
		d := k.distro
		r := k.release
		orig := bestVariants[k]

		item := models.IncusImageItem{
			Ref:       fmt.Sprintf("images:%s/%s", orig.origDistro, orig.origRelease),
			Arch:      archLabel,
			RecVCPUs:  2,
			RecRAMMB:  2048,
			RecDiskGB: 20,
		}

		switch d {
		case "ubuntu":
			item.Category = "popular"
			item.Distro = "ubuntu"
			switch r {
			case "resolute":
				item.Label = "Ubuntu 26.04 (Resolute)"
				item.Badge = "26.04"
				item.Description = "Ubuntu 26.04 Resolute LTS (desarrollo / compilación más reciente)."
			case "noble":
				item.Label = "Ubuntu 24.04 LTS (Noble)"
				item.Badge = "LTS"
				item.Description = "LTS más reciente de Ubuntu con soporte oficial hasta 2029."
			case "jammy":
				item.Label = "Ubuntu 22.04 LTS (Jammy)"
				item.Badge = "LTS"
				item.Description = "Versión LTS clásica y ampliamente compatible para producción."
			case "oracular":
				item.Label = "Ubuntu 24.10 (Oracular)"
				item.Badge = "Interim"
				item.Category = "dev"
				item.Description = "Versión de ciclo corto con los paquetes y kernels más recientes."
			default:
				item.Label = fmt.Sprintf("Ubuntu %s", titleCase(r))
				item.Badge = "Ubuntu"
				item.Description = fmt.Sprintf("Plantilla Ubuntu %s desde images:.", r)
			}

		case "debian":
			item.Distro = "debian"
			item.RecRAMMB = 1024
			item.RecDiskGB = 15
			switch r {
			case "forky":
				item.Label = "Debian 14 (Forky)"
				item.Category = "dev"
				item.Badge = "Debian 14"
				item.Description = "Siguiente generación de Debian 14 con paquetería de última hornada."
			case "trixie":
				item.Label = "Debian 13 (Trixie)"
				item.Category = "popular"
				item.Badge = "Testing"
				item.Description = "Siguiente generación de Debian con paquetería moderna."
			case "bookworm":
				item.Label = "Debian 12 (Bookworm)"
				item.Category = "popular"
				item.Badge = "Estable"
				item.Description = "Debian estable, ideal para servidores ligeros y bases de datos."
			case "bullseye":
				item.Label = "Debian 11 (Bullseye)"
				item.Category = "popular"
				item.Badge = "Oldstable"
				item.Description = "Debian oldstable optimizada para servicios heredados."
			case "sid":
				item.Label = "Debian Sid (Unstable)"
				item.Category = "dev"
				item.Badge = "Unstable"
				item.Description = "Rama inestable de Debian para desarrolladores y bleeding-edge."
			default:
				item.Label = fmt.Sprintf("Debian %s", titleCase(r))
				item.Category = "popular"
				item.Badge = "Debian"
				item.Description = fmt.Sprintf("Plantilla Debian %s.", r)
			}

		case "alpine":
			item.Distro = "alpine"
			item.Category = "minimal"
			item.Label = fmt.Sprintf("Alpine Linux %s", r)
			item.Badge = r
			if r == "edge" {
				item.Badge = "Rolling"
			}
			item.Description = fmt.Sprintf("Alpine Linux distribución ultraligera y segura basada en musl y BusyBox (v%s).", r)
			item.RecVCPUs = 1
			item.RecRAMMB = 512
			item.RecDiskGB = 5

		case "fedora":
			item.Distro = "fedora"
			item.Label = fmt.Sprintf("Fedora %s", r)
			item.Description = fmt.Sprintf("Distribución Fedora %s orientada a desarrollo y servidores modernos.", r)
			switch r {
			case "44":
				item.Category = "dev"
				item.Badge = "Fedora 44"
				item.Label = "Fedora 44 (Rawhide/Next)"
			case "43":
				item.Category = "dev"
				item.Badge = "Fedora 43"
			case "41":
				item.Category = "popular"
				item.Badge = "Latest"
			case "40":
				item.Category = "popular"
				item.Badge = "Stable"
			default:
				item.Category = "dev"
				item.Badge = r
			}

		case "rockylinux":
			item.Distro = "rocky"
			item.Category = "enterprise"
			item.Label = fmt.Sprintf("Rocky Linux %s", r)
			item.Badge = fmt.Sprintf("RHEL %s", r)
			item.Description = fmt.Sprintf("100%% compatible binario con Red Hat Enterprise Linux %s.", r)

		case "almalinux":
			item.Distro = "almalinux"
			item.Category = "enterprise"
			item.Label = fmt.Sprintf("AlmaLinux %s", r)
			item.Badge = fmt.Sprintf("Enterprise %s", r)
			item.Description = fmt.Sprintf("Distribución empresarial con soporte comunitario y compatibilidad binaria RHEL %s.", r)

		case "archlinux":
			item.Distro = "arch"
			item.Category = "dev"
			item.Ref = "images:archlinux"
			item.Label = "Arch Linux"
			item.Badge = "Rolling"
			item.Description = "Distribución Rolling Release siempre al día con pacman."
			item.RecDiskGB = 15

		case "kali":
			item.Distro = "kali"
			item.Category = "dev"
			item.Ref = "images:kali"
			item.Label = "Kali Linux"
			item.Badge = "SecTools"
			item.Description = "Distribución especializada en auditoría de seguridad y pentesting."

		case "nixos":
			item.Distro = "nixos"
			item.Category = "dev"
			item.Label = fmt.Sprintf("NixOS %s", r)
			item.Badge = "Declarative"
			item.Description = "Distribución declarativa y reproducible basada en el gestor de paquetes Nix."

		case "opensuse":
			item.Distro = "opensuse"
			if r == "tumbleweed" {
				item.Category = "dev"
				item.Label = "openSUSE Tumbleweed"
				item.Badge = "Rolling"
				item.Description = "Rolling release con pruebas automáticas openQA de openSUSE."
			} else {
				item.Category = "enterprise"
				item.Label = fmt.Sprintf("openSUSE Leap %s", r)
				item.Badge = "Leap"
				item.Description = "Distribución estable orientada a servidores empresariales con YaST."
			}

		case "openwrt":
			item.Distro = "openwrt"
			item.Category = "minimal"
			item.Label = fmt.Sprintf("OpenWrt %s", r)
			item.Badge = "Router"
			item.Description = "Sistema operativo embebido ultraligero ideal para enrutamiento y micro-servicios."
			item.RecVCPUs = 1
			item.RecRAMMB = 256
			item.RecDiskGB = 2

		case "voidlinux":
			item.Distro = "voidlinux"
			item.Category = "minimal"
			item.Ref = "images:voidlinux"
			item.Label = "Void Linux"
			item.Badge = "runit"
			item.Description = "Distribución independiente minimalista con gestor de paquetes xbps e init runit."
			item.RecVCPUs = 1
			item.RecRAMMB = 512
			item.RecDiskGB = 10

		case "busybox":
			item.Distro = "busybox"
			item.Category = "minimal"
			item.Label = fmt.Sprintf("BusyBox %s", r)
			item.Badge = "Tiny"
			item.Description = "Entorno mínimo autocontenido con utilidades estándar UNIX en un único binario."
			item.RecVCPUs = 1
			item.RecRAMMB = 128
			item.RecDiskGB = 1

		case "gentoo":
			item.Distro = "gentoo"
			item.Category = "dev"
			item.Ref = "images:gentoo"
			item.Label = "Gentoo Linux"
			item.Badge = "Source"
			item.Description = "Distribución source-based meta altamente optimizable con Portage."
			item.RecVCPUs = 4
			item.RecRAMMB = 4096
			item.RecDiskGB = 30

		case "oracle":
			item.Distro = "oracle"
			item.Category = "enterprise"
			item.Label = fmt.Sprintf("Oracle Linux %s", r)
			item.Badge = "UEK"
			item.Description = fmt.Sprintf("Oracle Linux %s con Unbreakable Enterprise Kernel.", r)

		case "centos":
			// r is the lower-cased release key (e.g. "10-stream"); strip
			// the redundant "-stream" suffix so the label doesn't read
			// "CentOS Stream 10-stream".
			ver := strings.TrimSuffix(r, "-stream")
			item.Distro = "centos"
			item.Category = "enterprise"
			item.Label = fmt.Sprintf("CentOS Stream %s", ver)
			item.Badge = "Stream"
			item.Description = fmt.Sprintf("Rama previa y de desarrollo continuo para RHEL %s.", ver)

		case "devuan":
			item.Distro = "devuan"
			item.Category = "minimal"
			item.Label = fmt.Sprintf("Devuan %s", titleCase(r))
			item.Badge = "No-systemd"
			item.Description = "Bifurcación de Debian sin systemd (SysVinit / OpenRC)."
			item.RecVCPUs = 1
			item.RecRAMMB = 1024
			item.RecDiskGB = 10

		case "mint":
			item.Distro = "mint"
			item.Category = "popular"
			item.Label = fmt.Sprintf("Linux Mint %s", titleCase(r))
			item.Badge = "Mint"
			item.Description = fmt.Sprintf("Distribución de escritorio/servidor Linux Mint %s.", titleCase(r))

		case "freebsd":
			item.Distro = "freebsd"
			item.Category = "community"
			item.Label = fmt.Sprintf("FreeBSD %s", r)
			item.Badge = "FreeBSD"
			item.Description = fmt.Sprintf("Sistema operativo Unix avanzado FreeBSD %s.", r)

		case "netbsd":
			item.Distro = "netbsd"
			item.Category = "community"
			item.Label = fmt.Sprintf("NetBSD %s", r)
			item.Badge = "NetBSD"
			item.Description = fmt.Sprintf("Sistema operativo tipo Unix altamente portable NetBSD %s.", r)

		default:
			item.Distro = d
			item.Category = "community"
			item.Label = fmt.Sprintf("%s %s", titleCase(d), r)
			item.Badge = "Community"
			item.Description = fmt.Sprintf("Plantilla %s %s desde el catálogo oficial images:.", titleCase(d), r)
		}

		out = append(out, item)
	}

	return out
}

// titleCaser replaces strings.Title (deprecated, SA1019) with the
// officially recommended x/text implementation for image labels.
var titleCaser = cases.Title(language.English)

func titleCase(s string) string { return titleCaser.String(s) }

// formatLocalImageLabel derives a clean, human-readable name from local image metadata.
func formatLocalImageLabel(img api.Image, shortFP string) string {
	osName := strings.ToLower(img.Properties["os"])
	release := strings.ToLower(img.Properties["release"])

	switch osName {
	case "ubuntu":
		switch release {
		case "resolute":
			return "Ubuntu 26.04 LTS (Resolute)"
		case "oracular":
			return "Ubuntu 24.10 (Oracular)"
		case "noble":
			return "Ubuntu 24.04 LTS (Noble)"
		case "jammy":
			return "Ubuntu 22.04 LTS (Jammy)"
		case "focal":
			return "Ubuntu 20.04 LTS (Focal)"
		default:
			if release != "" {
				return "Ubuntu " + titleCase(release)
			}
		}
	case "fedora":
		if release != "" {
			return "Fedora " + release
		}
	case "debian":
		switch release {
		case "forky":
			return "Debian 14 (Forky)"
		case "trixie":
			return "Debian 13 (Trixie)"
		case "bookworm":
			return "Debian 12 (Bookworm)"
		case "bullseye":
			return "Debian 11 (Bullseye)"
		default:
			if release != "" {
				return "Debian " + titleCase(release)
			}
		}
	case "alpine":
		if release != "" {
			return "Alpine Linux " + release
		}
	}

	if len(img.Aliases) > 0 && img.Aliases[0].Name != "" {
		return img.Aliases[0].Name
	}
	if osName != "" {
		if release != "" {
			return fmt.Sprintf("%s %s (%s)", titleCase(osName), release, shortFP)
		}
		return fmt.Sprintf("%s (%s)", titleCase(osName), shortFP)
	}
	return "Local Image (" + shortFP + ")"
}

// resolveRemoteAlias looks up alias on remote, falling back to a
// case-insensitive scan of every alias the server advertises when the
// exact name doesn't match. Upstream image servers are not consistent
// about casing (e.g. "centos/10-Stream" with a capital S), and any
// component that builds a ref programmatically — our own dynamic catalog
// included — can get that one detail wrong; this keeps a single typo in
// casing from turning into a hard "doesn't exist" failure.
func resolveRemoteAlias(remote incus.ImageServer, alias string) (*api.ImageAliasesEntry, error) {
	if entry, _, err := remote.GetImageAlias(alias); err == nil {
		return entry, nil
	}
	names, err := remote.GetImageAliasNames()
	if err != nil {
		return nil, fmt.Errorf("alias %q doesn't exist", alias)
	}
	lower := strings.ToLower(alias)
	for _, name := range names {
		if strings.ToLower(name) != lower {
			continue
		}
		entry, _, err := remote.GetImageAlias(name)
		if err != nil {
			return nil, err
		}
		return entry, nil
	}
	return nil, fmt.Errorf("alias %q doesn't exist", alias)
}

// needsCloudInit reports whether this create actually depends on
// cloud-init running inside the guest. Hostname alone does not: Incus
// sets that itself. Anything else in the seed — users, passwords, SSH
// keys, a provisioning script, custom user-data — is delivered ONLY by
// cloud-init, so an image without it produces a container that silently
// lacks everything the operator asked for.
func needsCloudInit(ci *models.CloudInitRequest) bool {
	if ci == nil {
		return false
	}
	return ci.User != "" || ci.Password != "" || ci.SSHKey != "" ||
		ci.ProvisionScript != "" || ci.CustomUserData != "" || ci.SnippetID != ""
}

// cloudVariantAlias upgrades an image alias to its "/cloud" variant, the
// linuxcontainers build that ships cloud-init preinstalled.
//
// The upgrade is verified against the image server before it is applied,
// and the original alias is returned untouched when the variant does not
// exist (openSUSE and Fedora publish no /cloud container at the time of
// writing) or the server cannot be reached. Failing to find a better
// image is not a reason to fail the create: the caller keeps the exact
// image it asked for, which is no worse than before this check existed.
func cloudVariantAlias(server, alias string) string {
	if alias == "" || strings.Contains(alias, "/cloud") {
		return alias
	}
	candidate := alias + "/cloud"
	remote, err := incus.ConnectSimpleStreams(server, &incus.ConnectionArgs{})
	if err != nil {
		slog.Warn("incus: cannot reach image server to select a cloud-init image",
			"server", server, "alias", alias, "error", err)
		return alias
	}
	if _, err := resolveRemoteAlias(remote, candidate); err != nil {
		slog.Warn("incus: no cloud-init variant for this image; cloud-init will not run",
			"alias", alias, "tried", candidate)
		return alias
	}
	return candidate
}

// PullIncusImage downloads ref (e.g. "images:alpine/3.20") straight from
// its simplestreams image server into the local Incus image store. This
// talks only to the (remote, read-only) ImageServer and the local daemon's
// image API — no instance is ever created, so unlike CreateDomain it needs
// no network/bridge at all and works on hosts with zero configured
// networks.
func (b *IncusBackend) PullIncusImage(ref string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	// A fingerprint names an image that is ALREADY in the local store —
	// that is the only reason we know the fingerprint at all. There is
	// nothing to download, so this is a no-op rather than an error.
	if isImageFingerprint(ref) {
		if _, _, err := b.client.GetImage(strings.ToLower(strings.TrimSpace(ref))); err == nil {
			return nil
		}
		return fmt.Errorf("no local image with fingerprint %q", ref)
	}
	server, alias, err := parseImageRef(ref)
	if err != nil {
		return err
	}
	// Idempotent: if the alias is already cached locally (a previous pull,
	// or the image warmed via some other path), there is nothing to do.
	// Without this check, re-pulling an already-cached image fails with
	// "Alias already exists" even though the desired end state (the
	// image sitting in the local store) is already true.
	if _, _, err := b.client.GetImageAlias(alias); err == nil {
		return nil
	}
	remote, err := incus.ConnectSimpleStreams(server, &incus.ConnectionArgs{})
	if err != nil {
		return fmt.Errorf("connect to image server %s: %w", server, err)
	}
	aliasEntry, err := resolveRemoteAlias(remote, alias)
	if err != nil {
		return fmt.Errorf("resolve image alias %q on %s: %w", alias, server, err)
	}
	image, _, err := remote.GetImage(aliasEntry.Target)
	if err != nil {
		return fmt.Errorf("fetch image metadata: %w", err)
	}
	// The fingerprint (not just the alias) may already be cached under a
	// different alias — CopyAliases below would then also collide.
	if _, _, err := b.client.GetImage(image.Fingerprint); err == nil {
		return b.client.CreateImageAlias(api.ImageAliasesPost{
			ImageAliasesEntry: api.ImageAliasesEntry{
				Name:                 alias,
				ImageAliasesEntryPut: api.ImageAliasesEntryPut{Target: image.Fingerprint},
			},
		})
	}
	// CopyAliases pulls every alias the source image server already
	// advertises for this fingerprint (e.g. "24.04", "noble", "n" for a
	// single Ubuntu image) — the requested alias is always among them.
	// Passing it AGAIN via the Aliases field made the client submit the
	// same alias twice in one CreateImage call, which the daemon rejects
	// with "Alias already exists" on every single pull, clean or not.
	op, err := b.client.CopyImage(remote, *image, &incus.ImageCopyArgs{
		Type:        aliasEntry.Type,
		CopyAliases: true,
		AutoUpdate:  true,
	})
	if err != nil {
		return fmt.Errorf("start image copy: %w", err)
	}
	return op.Wait()
}

// ListIncusImages returns available official and locally cached Incus images.
func (b *IncusBackend) ListIncusImages() ([]models.IncusImageItem, error) {
	catalog := fetchDynamicSimplestreamsImages()
	if b.client == nil {
		return catalog, nil
	}

	// Query cached images from Incus daemon
	localImages, err := b.client.GetImages()
	if err != nil {
		// Return catalog even if querying local daemon fails
		return catalog, nil
	}

	return mergeLocalImages(catalog, localImages), nil
}

// mergeLocalImages folds the host's cached Incus images into the remote
// catalog: entries whose ref matches a local alias are marked as cached,
// and any local image with no catalog counterpart is appended as its own
// entry.
//
// It is deliberately a pure function. Every interesting failure in here
// needs a specific set of local images AND a specific catalog, which a
// live Incus daemon will not reliably provide on demand — see
// merge_local_images_test.go for the shapes that used to produce a list
// whose entries could not be told apart.
func mergeLocalImages(catalog []models.IncusImageItem, localImages []api.Image) []models.IncusImageItem {
	// Index local images by alias, release, and fingerprint
	localByAlias := make(map[string]api.Image)
	localByFingerprint := make(map[string]api.Image)
	for _, img := range localImages {
		fp := img.Fingerprint
		if len(fp) > 12 {
			localByFingerprint[fp[:12]] = img
		}
		localByFingerprint[fp] = img
		for _, a := range img.Aliases {
			localByAlias[strings.ToLower(a.Name)] = img
		}
		if rel := strings.ToLower(img.Properties["release"]); rel != "" {
			osName := strings.ToLower(img.Properties["os"])
			localByAlias[osName+"/"+rel] = img
			localByAlias["images:"+osName+"/"+rel] = img
		}
	}

	// Match and mark local images in catalog
	matchedFPs := make(map[string]bool)
	for i := range catalog {
		ref := catalog[i].Ref
		server, alias, err := parseImageRef(ref)
		if err == nil {
			aliasLower := strings.ToLower(alias)
			if img, ok := localByAlias[aliasLower]; ok {
				catalog[i].IsLocal = true
				catalog[i].Fingerprint = img.Fingerprint[:12]
				catalog[i].Size = img.Size
				catalog[i].Badge = "Local"
				matchedFPs[img.Fingerprint] = true
				continue
			}
			refLower := strings.ToLower(ref)
			if img, ok := localByAlias[refLower]; ok {
				catalog[i].IsLocal = true
				catalog[i].Fingerprint = img.Fingerprint[:12]
				catalog[i].Size = img.Size
				catalog[i].Badge = "Local"
				matchedFPs[img.Fingerprint] = true
				continue
			}
		}
		_ = server
	}

	// Add any unmatched local images as custom local entries.
	//
	// Two local images can share an (os, release) pair — two builds of
	// the same release, pulled at different times — and need not carry
	// aliases at all. localByAlias keeps only the last of them, so the
	// others land here. The ref derived from (os, release) is then
	// IDENTICAL to the catalog entry that was matched to their sibling,
	// and the API would return two different images claiming the same
	// ref. That is not cosmetic: the UI keys its list on ref, so Svelte
	// throws each_key_duplicate and aborts the whole render — the Images
	// page comes up showing zero cached images, zero ISOs and zero base
	// disks even though every one of those API calls returned data.
	//
	// seenRefs keeps the list's identity unique by construction, so the
	// invariant does not depend on how many collisions the host happens
	// to have today.
	seenRefs := make(map[string]bool, len(catalog))
	for i := range catalog {
		if catalog[i].Ref != "" {
			seenRefs[strings.ToLower(catalog[i].Ref)] = true
		}
	}
	for _, img := range localImages {
		if matchedFPs[img.Fingerprint] {
			continue
		}
		primaryAlias := ""
		if len(img.Aliases) > 0 {
			primaryAlias = img.Aliases[0].Name
		}
		shortFP := img.Fingerprint
		if len(shortFP) > 12 {
			shortFP = shortFP[:12]
		}
		label := formatLocalImageLabel(img, shortFP)
		desc := img.Properties["description"]
		if desc == "" {
			desc = fmt.Sprintf("Imagen local (%s) creada en %s.", shortFP, img.CreatedAt.Format("2006-01-02"))
		}
		distro := strings.ToLower(img.Properties["os"])
		if distro == "" {
			distro = "linux"
		}
		ref := shortFP
		if primaryAlias != "" {
			ref = primaryAlias
		} else if img.Properties["os"] != "" && img.Properties["release"] != "" {
			ref = fmt.Sprintf("images:%s/%s", strings.ToLower(img.Properties["os"]), strings.ToLower(img.Properties["release"]))
		}
		// A ref already taken by a catalog entry would make this image
		// indistinguishable from it. Fall back to the fingerprint, which
		// is what an image with no os/release already gets and is
		// guaranteed unique.
		if seenRefs[strings.ToLower(ref)] {
			ref = shortFP
		}
		seenRefs[strings.ToLower(ref)] = true
		catalog = append([]models.IncusImageItem{
			{
				Ref:         ref,
				Label:       label,
				Category:    "local",
				Distro:      distro,
				Description: desc,
				Arch:        img.Architecture,
				Badge:       "Local",
				IsLocal:     true,
				Fingerprint: shortFP,
				Size:        img.Size,
				RecVCPUs:    2,
				RecRAMMB:    2048,
				RecDiskGB:   15,
			},
		}, catalog...)
	}

	return catalog
}

func instanceToVM(i *api.Instance) models.VM {
	vm := models.VM{
		ID:         i.Name,
		Name:       i.Name,
		Type:       "container",
		Hypervisor: "incus",
		State:      lxdState(i.Status),
		VCPUs:      parseIntConfig(i.Config["limits.cpu"]),
		RAMMB:      parseMemoryMB(i.Config["limits.memory"]),
		Autostart:  i.Config["boot.autostart"] == "true",
		// security.privileged defaults to false (unprivileged) when the
		// key is absent — mirror that so the UI toggle reflects reality.
		Privileged: i.Config["security.privileged"] == "true",
		Nesting:    i.Config["security.nesting"] == "true",
		Profiles:   append([]string(nil), i.Profiles...),
	}
	if i.Type == string(api.InstanceTypeVM) {
		vm.Type = "vm"
	}
	// v1.4 Fase 4.1: surface the root device as a disk and each NIC as
	// a network interface so the KVM-equivalent Disks/Net tabs work on
	// containers (root resize + interface attach/detach/update).
	devNames := make([]string, 0, len(i.Devices))
	for name := range i.Devices {
		devNames = append(devNames, name)
	}
	sort.Strings(devNames)
	for _, name := range devNames {
		dev := i.Devices[name]
		switch dev["type"] {
		case "disk":
			if name == "root" {
				d := models.DiskInfo{
					Device: "disk", Bus: "incus", Target: "root", Name: "root",
					Pool: dev["pool"], Type: "block",
				}
				if sz := parseDiskGB(dev["size"]); sz > 0 {
					d.SizeGB = sz
					vm.DiskGB = sz
				}
				vm.Disks = append(vm.Disks, d)
			}
		case "nic":
			parent := dev["parent"]
			vm.Networks = append(vm.Networks, models.NetIface{
				MAC:     i.Config["volatile."+name+".hwaddr"],
				Network: parent,
				Model:   "incus",
				Type:    "incus",
				Source:  parent,
			})
		}
	}
	// v1.4 Fase 3: surface the webkvm metadata (tags for RBAC and
	// backup-by-tag, alias for the UI) straight from the config keys so
	// the unified list and the tag filters work for containers too.
	if t := i.Config[metaTagsKey]; t != "" {
		vm.Tags = strings.Split(t, ",")
	}
	if d := i.Config[metaDescKey]; d != "" {
		var m lxdWebKVMMeta
		if json.Unmarshal([]byte(d), &m) == nil {
			vm.Alias = m.Alias
			vm.Cover = m.Cover
			vm.Groups = m.Groups
			vm.OwnerID = m.OwnerID
			vm.Template = m.Template
			vm.Shared = m.Shared
			if m.CiUser != "" {
				vm.ProvisionMethod = "cloud-init"
			}
		}
	}
	// Native cloud-init (user.user-data present at creation) drives the
	// provisioning chip on the VM card (PLAN-LXD 5.2).
	if i.Config["user.user-data"] != "" {
		vm.ProvisionMethod = "cloud-init"
	}
	return vm
}

// lxdState maps the LXD status string to the neutral VMState.
func lxdState(status string) models.VMState {
	switch strings.ToLower(status) {
	case "running":
		return models.VMStateRunning
	case "stopped":
		return models.VMStateShutoff
	case "frozen":
		return models.VMStatePaused
	case "error":
		return models.VMStateCrashed
	default:
		return models.VMStateUnknown
	}
}

// parseIntConfig parses an integer config value ("2", "4"), returning 0
// for ranges/percentages we cannot map to a plain count.
func parseIntConfig(v string) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if !strings.ContainsAny(v, "-%") {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// parseSizeBytes parses an Incus size string into bytes.
//
// It replaces three near-duplicate parsers (parseMemoryMB, parseDiskGB,
// parseDiskSizeGB) that each matched the suffix against an UPPER-CASED
// copy but then trimmed it off the ORIGINAL with a fixed-case literal.
// For any input whose case did not match exactly — "4gib", "4GIB",
// "512mib", "10gb", all of which Incus itself accepts — the branch was
// selected, TrimSuffix removed nothing, ParseFloat("4gib") failed and
// the caller silently got 0: a container shown with 0 MB RAM and 0 GB
// disk, and counted as consuming nothing against its owner's quota.
//
// The three also disagreed with each other: parseDiskGB converted GiB
// to GB while parseDiskSizeGB treated them as equal, "2TiB" fell
// through to 2 GB (off by 2048x), and a bare byte count like
// "10737418240" — valid Incus input — was read by parseDiskGB as ten
// thousand million GB.
//
// Units follow the Incus reference (docs "Units for storage, memory and
// network limits"): the plain spellings kB/MB/GB/TB/PB are DECIMAL
// (1000^n) and only the iB spellings KiB/MiB/GiB/TiB/PiB are binary
// (1024^n). A bare number is a byte count.
func parseSizeBytes(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	upper := strings.ToUpper(v)
	// Longest suffixes first: "GIB" must win over "B", and "BYTES"
	// must be tested before "B".
	for _, u := range []struct {
		suffix string
		mult   float64
	}{
		{"BYTES", 1},
		{"PIB", 1 << 50}, {"TIB", 1 << 40}, {"GIB", 1 << 30}, {"MIB", 1 << 20}, {"KIB", 1 << 10},
		{"PB", 1e15}, {"TB", 1e12}, {"GB", 1e9}, {"MB", 1e6}, {"KB", 1e3},
		{"B", 1},
	} {
		if !strings.HasSuffix(upper, u.suffix) {
			continue
		}
		// Trim from the upper-cased copy so the length is right
		// regardless of how the caller spelled the unit.
		num := strings.TrimSpace(upper[:len(upper)-len(u.suffix)])
		n, err := strconv.ParseFloat(num, 64)
		if err != nil || n < 0 {
			return 0
		}
		return int64(n * u.mult)
	}
	// No unit at all: Incus treats this as a byte count.
	n, err := strconv.ParseFloat(upper, 64)
	if err != nil || n < 0 {
		return 0
	}
	return int64(n)
}

// parseMemoryMB parses an Incus memory limit ("4GB", "512MiB", "1GiB")
// into MB. Returns 0 when unparseable. A non-zero limit below 1 MB
// rounds up to 1 so it is never mistaken for "unlimited".
func parseMemoryMB(v string) int64 {
	b := parseSizeBytes(v)
	if b <= 0 {
		return 0
	}
	// Round-trip stability with the writer decides the divisor here:
	// formatMemoryMB emits "%dMiB", so a limit this backend wrote as
	// 2048MiB must read back as 2048. Dividing by 1e6 would turn it
	// into 2147 and the value would drift on every edit/save cycle.
	mb := b / (1 << 20)
	if mb == 0 {
		return 1
	}
	return mb
}

// parseDiskGB parses an Incus root device size ("10GB", "2GiB") into a
// whole GB count; 0 when unparseable. Sizes below 1 GB round up to 1 so
// a real disk never reports as 0 (which quota accounting reads as
// "consumes nothing").
func parseDiskGB(v string) int64 {
	b := parseSizeBytes(v)
	if b <= 0 {
		return 0
	}
	// Decimal GB, matching how the size was written: a root device of
	// "10GB" must read back as 10, not 9.
	gb := b / 1e9
	if gb == 0 {
		return 1
	}
	return gb
}

// isNotFound reports whether an LXD error is a "not found" (instance
// absent), so DomainExists can distinguish it from real failures.
func isNotFound(err error) bool {
	var statusErr api.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status() == httpNotFound
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

// httpNotFound mirrors net/http.StatusNotFound to avoid importing net/http.
const httpNotFound = 404

// imageRemotes maps the well-known official LXD remote names to their
// simplestreams image servers (v1.4 Fase 3). Any full <server-url>:<alias>
// reference is also accepted; unknown names are rejected up front.
var imageRemotes = map[string]string{
	"ubuntu":       "https://cloud-images.ubuntu.com/releases",
	"ubuntu-daily": "https://cloud-images.ubuntu.com/daily",
	"images":       "https://images.linuxcontainers.org",
	"almalinux":    "https://repo.almalinux.org/almalinux",
	"rockylinux":   "https://dl.rockylinux.org/pub/rocky",
}

// imageFingerprintRE matches a bare image fingerprint: the hex digest
// Incus identifies an image by, either in full (64 chars) or in the
// 12-char short form the UI and `incus image list` display.
var imageFingerprintRE = regexp.MustCompile(`^[0-9a-f]{12}$|^[0-9a-f]{64}$`)

// isImageFingerprint reports whether ref names a LOCAL image by its
// fingerprint rather than by <remote>:<alias>.
//
// A locally cached image need not have any alias at all: every image
// pulled by an instance create is stored by fingerprint alone, and
// ListIncusImages surfaces exactly those as catalog entries whose ref
// IS the short fingerprint. Feeding that ref back in is therefore a
// normal round trip through our own API, not an odd input — and it used
// to fail, because parseImageRef only understood <remote>:<alias> and
// rejected anything without a colon. The operator picked a cached image
// from the visual catalog and got "Image must be <remote>:<alias>".
func isImageFingerprint(ref string) bool {
	return imageFingerprintRE.MatchString(strings.ToLower(strings.TrimSpace(ref)))
}

// parseImageRef splits an LXD image reference ("ubuntu:24.04",
// "https://images.linuxcontainers.org:alpine/3.20") into the
// simplestreams server URL and the alias. Bare names without a remote
// are rejected (ambiguous) — except a local image fingerprint, which
// callers must detect with isImageFingerprint BEFORE calling this: a
// fingerprint names an image already in the local store, so there is no
// remote server to resolve it against.
func parseImageRef(ref string) (server, alias string, err error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		// Full server URL: the alias is the segment after the LAST colon.
		i := strings.LastIndex(ref, ":")
		if i <= 0 || i == len(ref)-1 {
			return "", "", fmt.Errorf("invalid LXD image reference %q (want <server-url>:<alias>)", ref)
		}
		return strings.TrimSuffix(ref[:i], "/"), ref[i+1:], nil
	}
	i := strings.Index(ref, ":")
	if i <= 0 || i == len(ref)-1 {
		return "", "", fmt.Errorf("invalid LXD image reference %q (want <remote>:<alias>, e.g. ubuntu:24.04)", ref)
	}
	remote, alias := ref[:i], ref[i+1:]
	if strings.Contains(remote, "/") || strings.HasPrefix(remote, "http") {
		// Full server URL without scheme is unusual; treat as URL anyway.
		return strings.TrimSuffix(remote, "/"), alias, nil
	}
	if remote == "ubuntu" && (alias == "26.04" || alias == "resolute") {
		return imageRemotes["images"], "ubuntu/" + alias, nil
	}
	server, ok := imageRemotes[remote]
	if !ok {
		known := make([]string, 0, len(imageRemotes))
		for k := range imageRemotes {
			known = append(known, k)
		}
		sort.Strings(known)
		return "", "", fmt.Errorf("unknown LXD image remote %q (known: %s)", remote, strings.Join(known, ", "))
	}
	return server, alias, nil
}

// formatMemoryMB renders a RAM limit in MiB as LXD expects it.
func formatMemoryMB(mb int64) string { return fmt.Sprintf("%dMiB", mb) }

// parseDiskSizeGB parses an Incus root disk size into whole GB. Kept as
// a distinct name for its call site; it used to disagree with
// parseDiskGB on the very same input (GiB converted vs not, TB and bare
// byte counts unsupported), so both now share one parser.
func parseDiskSizeGB(v string) int64 { return parseDiskGB(v) }

// mapLXErr translates LXD daemon errors to the neutral compute sentinels
// where a meaningful mapping exists; anything else passes through.
func mapLXErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "not running") ||
		strings.Contains(strings.ToLower(err.Error()), "instance is not running") {
		return compute.ErrDomainNotRunning
	}
	if isNotFound(err) {
		return err
	}
	return err
}

// lxdCloudInitConfig builds the native LXD instance config keys from a
// cloud-init request (v1.4 Fase 2). Unlike KVM (NoCloud seed ISO), LXD
// accepts cloud-init directly as instance config: user.user-data carries
// the #cloud-config document and user.network-config the network YAML.
// networkBridge is the LXD managed bridge to attach by default (empty =
// no network-config block).
func lxdCloudInitConfig(cfg cloudinit.Config, networkBridge string) (map[string]string, bool) {
	out := map[string]string{}
	if cfg.User == "" && cfg.ProvisionScript == "" && cfg.Hostname == "" && cfg.CustomUserData == "" {
		return nil, false // nothing to provision
	}
	userData := cloudinit.BuildUserData(cfg)
	if userData != "" {
		out["user.user-data"] = userData
	}
	if networkBridge != "" {
		out["user.network-config"] = "network:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n      dhcp6: true\n      optional: true\n    eth1:\n      dhcp4: true\n      dhcp6: true\n      optional: true\n    all-en:\n      match:\n        name: 'en*'\n      dhcp4: true\n      dhcp6: true\n      optional: true\n"
	}
	return out, true
}

// lxdNetworkConfig renders a netplan (version 2) user.network-config that
// declares every NIC device (eth0, eth1, …) with dhcp4: true, so cloud-init
// configures ALL of them in the guest. Attaching/detaching a NIC without
// regenerating this leaves the new interface unconfigured (no DHCP client),
// which looked like a broken/duplicate NIC.
func lxdNetworkConfig(devices map[string]map[string]string) string {
	names := make([]string, 0, len(devices))
	for name, dev := range devices {
		if dev["type"] == "nic" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return "network:\n  version: 2\n"
	}
	sort.Strings(names)
	var sb strings.Builder
	sb.WriteString("network:\n  version: 2\n  ethernets:\n")
	for _, n := range names {
		sb.WriteString("    " + n + ":\n      dhcp4: true\n      dhcp6: true\n      optional: true\n")
	}
	return sb.String()
}
