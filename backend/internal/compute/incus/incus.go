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
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/gorilla/websocket"
	"github.com/klauspost/compress/zstd"

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
	if name == "" || strings.ContainsAny(name, "/ \t\n\r") {
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
			vm.IP = instanceIP(st); vm.IPs = instanceIPs(st)
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
		vm.IP = instanceIP(st); vm.IPs = instanceIPs(st)
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
func (b *IncusBackend) SetBootDevice(id string, device string) error { return compute.ErrNotImplemented }
func (b *IncusBackend) GetBootDevice(id string) (string, error)      { return "", compute.ErrNotImplemented }
func (b *IncusBackend) ValidateDomainDisks(id string) error          { return compute.ErrNotImplemented }

// CreateDomain creates a container from an official LXD image remote
// (v1.4 Fase 3) — zero ISOs, templates only. The image reference is
// req.Image ("ubuntu:24.04", "images:alpine/3.20", or a full
// <server-url>:<alias>). Cloud-init is injected straight into the native
// user.user-data / user.network-config config keys the Fase 2 mapper
// already produces.
func (b *IncusBackend) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	if strings.TrimSpace(req.Image) == "" {
		return models.VM{}, errors.New("an LXD image reference is required to create a container (e.g. ubuntu:24.04)")
	}
	server, alias, err := parseImageRef(req.Image)
	if err != nil {
		return models.VM{}, err
	}
	// v1.4 Fase 4.1: the operator picks the network from the SAME
	// selector as KVM; resolve the libvirt network name to the Linux
	// bridge the container NIC lands on.
	bridge, err := b.bridgeForNetwork(req.Network)
	if err != nil {
		return models.VM{}, err
	}
	config := map[string]string{
		"boot.autostart": "true",
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
		devices["root"] = map[string]string{
			"type": "disk",
			"path": "/",
			"pool": "default",
			"size": fmt.Sprintf("%dGB", req.DiskGB),
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
	if req.CloudInit != nil {
		ci := cloudinit.Config{
			User:            req.CloudInit.User,
			Password:        req.CloudInit.Password,
			SSHKey:          req.CloudInit.SSHKey,
			Hostname:        req.CloudInit.Hostname,
			ProvisionScript: req.CloudInit.ProvisionScript,
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
	post := api.InstancesPost{
		Name: req.Name,
		Type: api.InstanceTypeContainer,
		Source: api.InstanceSource{
			Type:     "image",
			Protocol: "simplestreams",
			Server:   server,
			Alias:    alias,
		},
		InstancePut: api.InstancePut{
			Config:  config,
			Devices: devices,
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
	const timeout = 90 * time.Second
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
func (b *IncusBackend) RevertSnapshot(domainID, snapID string) error { return compute.ErrNotImplemented }
func (b *IncusBackend) ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error) {
	return nil, compute.ErrNotImplemented
}

// --- Storage / pools / volumes / ISO ---

func (b *IncusBackend) ListStoragePools() ([]models.StoragePool, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) CreateStoragePool(ctx context.Context, req models.CreatePoolRequest) (models.StoragePool, error) {
	return models.StoragePool{}, compute.ErrNotImplemented
}
func (b *IncusBackend) UpdateStoragePool(ctx context.Context, name string, req models.UpdatePoolRequest) (models.StoragePool, error) {
	return models.StoragePool{}, compute.ErrNotImplemented
}
func (b *IncusBackend) DeletePool(name string) error            { return compute.ErrNotImplemented }
func (b *IncusBackend) RefreshPool(name string) error           { return compute.ErrNotImplemented }
func (b *IncusBackend) GetPoolPath(name string) (string, error) { return "", compute.ErrNotImplemented }
func (b *IncusBackend) DiskPoolName() string                    { return "" }
func (b *IncusBackend) ISOPoolName() string                     { return "" }
func (b *IncusBackend) ListStorageVolumes(poolName string) ([]models.StorageVolume, error) {
	return nil, compute.ErrNotImplemented
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
	return compute.ErrNotImplemented
}
func (b *IncusBackend) VolumeExists(poolName, volName string) (bool, error) {
	return false, compute.ErrNotImplemented
}
func (b *IncusBackend) FindVolumeAttachments(poolName, volName string) ([]models.VolumeAttachment, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) GetISOs(poolName string) ([]models.ISOScanResult, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) RenameISO(oldName, newName, poolName string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) DeleteISO(name, poolName string) error { return compute.ErrNotImplemented }
func (b *IncusBackend) DeleteVMDiskFiles(vmName string, exact ...string) (deleted []string, skipped []string, err error) {
	return nil, nil, compute.ErrNotImplemented
}
func (b *IncusBackend) RefreshCIFSSecretIfNeeded(ctx context.Context, poolName string) (*compute.SecretRef, error) {
	return nil, compute.ErrNotImplemented
}

// --- Networking ---

func (b *IncusBackend) ListNetworks() ([]models.Network, error) { return nil, compute.ErrNotImplemented }
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
		stdinW.Close()
		stdoutR.Close()
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
		CiUser: meta.CiUser, AppInfo: meta.AppInfo, UpdatedAt: meta.UpdatedAt,
	}
	if d.Alias == "" && d.Notes == "" && d.Cover == "" && len(d.Groups) == 0 &&
		d.OwnerID == "" && !d.Template && d.CiUser == "" && d.AppInfo == "" && d.UpdatedAt == 0 {
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
	if err := waitOperation(op); err != nil {
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
	return "", "", nil, compute.ErrNotImplemented
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

// parseMemoryMB parses an LXD memory limit ("4GB", "512MiB", "1GiB")
// into MB. Returns 0 when unparseable.
func parseMemoryMB(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	mult := int64(1)
	num := v
	upper := strings.ToUpper(v)
	switch {
	case strings.HasSuffix(upper, "GIB"):
		mult, num = 1024, strings.TrimSuffix(v, "GiB")
	case strings.HasSuffix(upper, "GB"):
		mult, num = 1000, strings.TrimSuffix(v, "GB")
	case strings.HasSuffix(upper, "MIB"):
		mult, num = 1, strings.TrimSuffix(v, "MiB")
	case strings.HasSuffix(upper, "MB"):
		mult, num = 1, strings.TrimSuffix(v, "MB")
	case strings.HasSuffix(upper, "KIB"):
		mult, num = 0, strings.TrimSuffix(v, "KiB")
	case strings.HasSuffix(upper, "KB"):
		mult, num = 0, strings.TrimSuffix(v, "KB")
	}
	if mult == 0 { // sub-MB value
		return 1
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil {
		return 0
	}
	return int64(n * float64(mult))
}

// parseDiskGB parses an LXD root device size ("10GB", "2GiB") into a
// whole GB count. GiB is converted up to GB (1024 vs 1000); returns 0
// when unparseable.
func parseDiskGB(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	mult := float64(1)
	num := v
	upper := strings.ToUpper(v)
	switch {
	case strings.HasSuffix(upper, "GIB"):
		mult, num = 1.073741824, strings.TrimSuffix(v, "GiB")
	case strings.HasSuffix(upper, "GB"):
		mult, num = 1, strings.TrimSuffix(v, "GB")
	case strings.HasSuffix(upper, "TB"):
		mult, num = 1000, strings.TrimSuffix(v, "TB")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil {
		return 0
	}
	return int64(n * mult)
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

// parseImageRef splits an LXD image reference ("ubuntu:24.04",
// "https://images.linuxcontainers.org:alpine/3.20") into the
// simplestreams server URL and the alias. Bare names without a remote
// are rejected (ambiguous).
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

// parseDiskSizeGB parses an LXD root disk size ("10GB", "20GiB") into
// whole GB; 0 when unparseable.
func parseDiskSizeGB(v string) int64 {
	v = strings.TrimSpace(v)
	upper := strings.ToUpper(v)
	var num string
	switch {
	case strings.HasSuffix(upper, "GIB"):
		num = strings.TrimSuffix(v, "GiB")
	case strings.HasSuffix(upper, "GB"):
		num = strings.TrimSuffix(v, "GB")
	default:
		return 0
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(num), 64)
	if err != nil || n <= 0 {
		return 0
	}
	return int64(n)
}

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
	if cfg.User == "" && cfg.ProvisionScript == "" && cfg.Hostname == "" {
		return nil, false // nothing to provision
	}
	userData := cloudinit.BuildUserData(cfg)
	if userData != "" {
		out["user.user-data"] = userData
	}
	if networkBridge != "" {
		out["user.network-config"] = "network:\n  version: 2\n  ethernets:\n    eth0:\n      dhcp4: true\n"
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
		if dev["type"] == "nic" && len(name) > 3 && name[:3] == "eth" {
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
		sb.WriteString("    " + n + ":\n      dhcp4: true\n")
	}
	return sb.String()
}
