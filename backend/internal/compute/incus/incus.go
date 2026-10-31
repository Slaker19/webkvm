// Package incus is the container backend (v2.2.0) — migrated from LXD
// to Incus (the community fork, github.com/lxc/incus).
package incus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/config"
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
	// own unix-socket HTTP request.
	socketPath string
	// allowedProfiles returns the profile names a non-admin may attach
	// (setting incus.allowed_profiles). nil means only "default".
	allowedProfiles func() []string
}

// IncusBackendOption customizes the backend at construction time.
type IncusBackendOption func(*IncusBackend)

// WithAllowedProfiles wires the source of incus.allowed_profiles.
func WithAllowedProfiles(f func() []string) IncusBackendOption {
	return func(b *IncusBackend) { b.allowedProfiles = f }
}

func (b *IncusBackend) authorizeSpec(scope models.MediaScope, privileged, nesting *bool, profiles []string) error {
	if scope.Admin {
		return nil
	}
	if privileged != nil && *privileged {
		return fmt.Errorf("%w: privileged containers require an administrator", compute.ErrForbidden)
	}
	if nesting != nil && *nesting && !scope.CanNesting {
		return fmt.Errorf("%w: nesting requires the nesting permission", compute.ErrForbidden)
	}
	allowed := []string{"default"}
	if b.allowedProfiles != nil {
		if l := b.allowedProfiles(); len(l) > 0 {
			allowed = l
		}
	}
	for _, p := range profiles {
		ok := false
		for _, a := range allowed {
			if a == p {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("%w: profile %q is not allowed for this user", compute.ErrForbidden, p)
		}
	}
	return nil
}

// NewIncusBackend connects to the LXD daemon over a unix socket.
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
// (webkvm-incus) if it doesn't exist.
func (b *IncusBackend) EnsureIncusPool(ctx context.Context, cfg *config.Config) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	poolName := config.IncusPoolName
	poolPath := cfg.IncusPoolPath()

	_, _, err := b.client.GetStoragePool(poolName)
	if err == nil {
		return nil
	}

	if err := os.MkdirAll(poolPath, 0755); err != nil {
		return fmt.Errorf("create pool dir %q: %w", poolPath, err)
	}

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

func (b *IncusBackend) bridgeForNetwork(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if mb := incusMainBridge(); mb != "" {
			return mb, nil
		}
		return "", compute.ErrNoPhysicalBridge
	}
	if !isPhysicalBridge(name) {
		return "", fmt.Errorf("%w: %q is not a physical Linux bridge on the host", compute.ErrNoPhysicalBridge, name)
	}
	return name, nil
}

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

func isLinuxBridge(name string) bool {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "\\") || strings.Contains(name, "..") || strings.ContainsAny(name, " \t\n\r") {
		return false
	}
	_, err := os.Stat("/sys/class/net/" + name + "/bridge")
	return err == nil
}

func (b *IncusBackend) ServerInfo() (string, error) {
	s, _, err := b.client.GetServer()
	if err != nil {
		return "", err
	}
	return s.Environment.ServerVersion, nil
}

func (b *IncusBackend) Close() {
	b.client.Disconnect()
}

func (b *IncusBackend) NewMetricsCollector(hub *events.Hub) *MetricsCollector {
	return NewMetricsCollector(clientAdapter{client: b.client}, hub)
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

// --- helpers ---

func timeNow() time.Time {
	return time.Now()
}

func logWarnNoPool(name string, profiles []string, diskGB int64) {
	slog.Warn("incus: no storage pool resolved for root disk; leaving profile root untouched",
		"instance", name, "profiles", profiles, "disk_gb", diskGB)
}

func waitOperation(op incus.Operation) error {
	return waitOperationTimeout(op, 90*time.Second)
}

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

func instanceIPs(st *api.InstanceState) []string {
	if st == nil {
		return nil
	}
	seen := map[string]bool{}
	out := []string{}
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

func instanceIP(st *api.InstanceState) string {
	if ips := instanceIPs(st); len(ips) > 0 {
		return ips[0]
	}
	return ""
}

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

func instanceUptimeSec(st *api.InstanceState) int64 {
	if st == nil || st.StartedAt.IsZero() {
		return 0
	}
	if up := time.Since(st.StartedAt); up > 0 {
		return int64(up.Seconds())
	}
	return 0
}

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
		Privileged: i.Config["security.privileged"] == "true",
		Nesting:    i.Config["security.nesting"] == "true",
		Profiles:   append([]string(nil), i.Profiles...),
	}
	if i.Type == string(api.InstanceTypeVM) {
		vm.Type = "vm"
	}
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
			} else if strings.HasPrefix(name, "shared-") {
				vm.SharedFolders = append(vm.SharedFolders, models.SharedFolder{
					Tag:      strings.TrimPrefix(name, "shared-"),
					HostPath: dev["source"],
					ReadOnly: dev["readonly"] == "true",
				})
			} else {
				d := models.DiskInfo{
					Device: "disk", Bus: "incus", Target: name, Name: name,
					Source: dev["source"], Pool: dev["pool"], Type: "disk",
				}
				if sz := parseDiskGB(dev["size"]); sz > 0 {
					d.SizeGB = sz
				}
				vm.Disks = append(vm.Disks, d)
			}
		case "usb":
			vID := dev["vendorid"]
			pID := dev["productid"]
			if !strings.HasPrefix(vID, "0x") && vID != "" {
				vID = "0x" + vID
			}
			if !strings.HasPrefix(pID, "0x") && pID != "" {
				pID = "0x" + pID
			}
			vm.USBDevices = append(vm.USBDevices, models.USBDevice{
				VendorID:  vID,
				ProductID: pID,
				Name:      fmt.Sprintf("USB Device %s:%s", dev["vendorid"], dev["productid"]),
			})
		case "nic":
			parent := dev["parent"]
			netIface := models.NetIface{
				MAC:     i.Config["volatile."+name+".hwaddr"],
				Network: parent,
				Model:   "incus",
				Type:    "incus",
				Source:  parent,
			}
			if vlanStr := dev["vlan"]; vlanStr != "" {
				if tag, err := strconv.Atoi(vlanStr); err == nil && tag > 0 {
					netIface.VLANTag = &tag
				}
			}
			vm.Networks = append(vm.Networks, netIface)
		case "gpu":
			vm.GPU = true
		case "pci":
			if addr := dev["address"]; addr != "" {
				vm.PCIDevices = append(vm.PCIDevices, models.PCIDevice{
					Address: addr,
					Name:    "PCI " + addr,
				})
			}
		}
	}
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
	if i.Config["user.user-data"] != "" {
		vm.ProvisionMethod = "cloud-init"
	}
	return vm
}

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

func parseSizeBytes(v string) int64 {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	upper := strings.ToUpper(v)
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
		num := strings.TrimSpace(upper[:len(upper)-len(u.suffix)])
		n, err := strconv.ParseFloat(num, 64)
		if err != nil || n < 0 {
			return 0
		}
		return int64(n * u.mult)
	}
	n, err := strconv.ParseFloat(upper, 64)
	if err != nil || n < 0 {
		return 0
	}
	return int64(n)
}

func parseMemoryMB(v string) int64 {
	b := parseSizeBytes(v)
	if b <= 0 {
		return 0
	}
	mb := b / (1 << 20)
	if mb == 0 {
		return 1
	}
	return mb
}

func parseDiskGB(v string) int64 {
	b := parseSizeBytes(v)
	if b <= 0 {
		return 0
	}
	gb := b / 1e9
	if gb == 0 {
		return 1
	}
	return gb
}

func isNotFound(err error) bool {
	var statusErr api.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Status() == httpNotFound
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found")
}

const httpNotFound = 404

var imageRemotes = map[string]string{
	"ubuntu":       "https://cloud-images.ubuntu.com/releases",
	"ubuntu-daily": "https://cloud-images.ubuntu.com/daily",
	"images":       "https://images.linuxcontainers.org",
	"almalinux":    "https://repo.almalinux.org/almalinux",
	"rockylinux":   "https://dl.rockylinux.org/pub/rocky",
}

var imageFingerprintRE = regexp.MustCompile(`^[0-9a-f]{12}$|^[0-9a-f]{64}$`)

func isImageFingerprint(ref string) bool {
	return imageFingerprintRE.MatchString(strings.ToLower(strings.TrimSpace(ref)))
}

func parseImageRef(ref string) (server, alias string, err error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
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

func formatMemoryMB(mb int64) string { return fmt.Sprintf("%dMiB", mb) }

func parseDiskSizeGB(v string) int64 { return parseDiskGB(v) }

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

func lxdCloudInitConfig(cfg cloudinit.Config, networkBridge string) (map[string]string, bool) {
	out := map[string]string{}
	if cfg.User == "" && cfg.ProvisionScript == "" && cfg.Hostname == "" && cfg.CustomUserData == "" {
		return nil, false
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
