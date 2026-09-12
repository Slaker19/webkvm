// Package compute is the neutral ComputeBackend abstraction (v1.4, Fase 0).
//
// Handlers talk ONLY to this interface — never to the hypervisor packages.
// KVMBackend is the KVM adapter (the only active backend today); an LXD
// adapter will join it in Fase 1. Types that used to leak out of
// internal/libvirt into api/* (ExportBackupOptions, OVAOptions, ImportOpts,
// the serial console session, …) are raised here and translated by the
// adapter. Zero behaviour change: KVM is the only backend, and the adapter
// is a thin delegation shim.
package compute

import (
	"context"
	"errors"
	"io"

	"webkvm/internal/backupstore"
	"webkvm/internal/models"
)

// Error sentinels raised from the hypervisor packages so handlers can
// classify errors without knowing which backend produced them.
var (
	// ErrDomainNotRunning is returned when an operation (e.g. serial
	// console) requires the instance to be running.
	ErrDomainNotRunning = errors.New("the VM must be running to use the console")
	// ErrDomainNotPaused is returned when a resume targets a VM that is
	// not paused (or a suspend a VM that is already paused).
	ErrDomainNotPaused = errors.New("the VM is not paused")
	// ErrDomainAlreadyRunning is returned when a start targets a VM that
	// is already running.
	ErrDomainAlreadyRunning = errors.New("the VM is already running")
	// ErrDomainMustBeStoppedToRename is returned when a rename targets a
	// VM that is still running (libvirt only renames inactive domains).
	ErrDomainMustBeStoppedToRename = errors.New("the VM must be stopped to rename it")
	// ErrMemorySnapshotRequiresRunning is returned when a memory
	// snapshot is requested on a powered-off instance.
	ErrMemorySnapshotRequiresRunning = errors.New("a memory snapshot requires the VM to be running")
	// ErrNotImplemented is returned by backends for operations the
	// hypervisor does not support (Fase 1: most LXD methods). Handlers
	// map it to HTTP 501 Not Implemented.
	ErrNotImplemented = errors.New("this operation is not supported by the current hypervisor backend")
	// ErrNoPhysicalBridge is the fatal error surfaced when the host has no
	// physical Linux bridge (vmbr0/br0 attached to a physical NIC). WebKVM
	// requires shared Layer-2 (Proxmox-style): KVM and Incus must land on
	// the real LAN, never on an intermediate NAT/virtual bridge.
	ErrNoPhysicalBridge = errors.New("no physical bridge found on host; please configure a Linux bridge (vmbr0 or br0) attached to your physical NIC")
	// ErrNetworkInUse is returned when a bridge still has a live
	// VM/container interface attached — the caller must detach it (or
	// stop/delete the instance) before the bridge itself can be deleted.
	// Handlers map it to HTTP 409, not 500: this is an expected, caller-
	// actionable guard, not an unexpected server failure.
	ErrNetworkInUse = errors.New("network is in use")
	// ErrLeaseNotFound is returned by ReleaseNetworkLease when the given
	// (ip, mac) pair isn't in the bridge's current lease table — dhcp_release
	// itself can't tell a real release apart from a no-op (it just fires a
	// UDP packet and exits 0 either way), so the libvirt layer checks the
	// leasefile first. Handlers map it to HTTP 404.
	ErrLeaseNotFound = errors.New("lease not found")
)

// ExportBackupOptions controls a backup export stream.
type ExportBackupOptions struct {
	Compress    string // "gzip" (legacy) or "zstd" (default for new exports)
	ZstdLevel   int    // 1..22, default 19
	RepackDisks bool   // if true, run qemu-img convert -c on each disk first
}

// ImportOpts controls a domain restore from a backup archive.
type ImportOpts struct {
	Network    string // rewrites every <source network=…>; empty keeps the archive's
	VCPUs      int    // overrides <vcpu>; 0 keeps the archive's
	RAMMB      int    // overrides <memory>; 0 keeps it
	Autostart  *bool  // nil keeps the hypervisor default
	SourceSize int64  // compressed archive size (progress denominator)
	OnProgress func(pct int, stage string, vars map[string]any)
}

// OVATarget selects the OVA export flavor.
type OVATarget string

const (
	OVATargetVMware  OVATarget = "vmware"  // VirtualBox, VMware Workstation/ESXi
	OVATargetLibvirt OVATarget = "libvirt" // Proxmox, libvirt, GNOME Boxes, this app
)

// OVACompress is the compression applied to the surrounding OVA tar.
type OVACompress string

const (
	OVACompressZstd OVACompress = "zstd"
)

// OVAOptions controls an OVA export.
type OVAOptions struct {
	Target    OVATarget
	Compress  OVACompress
	ZstdLevel int
}

// GraphicsInfo describes a VM's display/VNC endpoint.
type GraphicsInfo struct {
	Type      string `json:"type"`
	Port      int    `json:"port"`
	WebSocket int    `json:"websocket,omitempty"`
	Host      string `json:"host"`
}

// SecretRef is a CIFS secret managed by the backend.
type SecretRef struct {
	PoolName   string `json:"pool_name"`
	SecretUUID string `json:"secret_uuid"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
}

// ConsoleStream is the neutral serial-console session. The adapter wraps
// the hypervisor's stream so handlers never touch libvirt types.
type ConsoleStream interface {
	Recv(buf []byte) (int, error)
	Send(b []byte) (int, error)
	Finish() error
	Free()
}

// Capabilities reports which features the active backend supports. A
// backend that does not implement a feature returns 501 from its
// handlers (Fase 1: LXD turns off OVA/VNC/USB/serial/cdrom/NoCloudISO/
// qemu-guest-agent).
type Capabilities struct {
	SupportsOVA             bool
	SupportsSnapshots       bool
	SupportsVNC             bool
	SupportsSerialConsole   bool
	SupportsUSB             bool
	SupportsNoCloudISO      bool
	SupportsQemuGuestAgent  bool
	SupportsNftPortForwards bool
}

// Backend is the full ComputeBackend seam (Fase 0). Every method is an
// instance/storage/network/backup OPERATION — the metric collectors and
// event loop are hypervisor infrastructure and deliberately NOT here
// (handlers keep a raw *libvirt.Connector for those).
type Backend interface {
	// --- Instance lifecycle ---
	ListDomains() ([]models.VM, error)
	GetDomain(id string) (models.VM, error)
	DomainExists(name string) (bool, error)
	CreateDomain(req models.CreateVMRequest) (models.VM, error)
	UpdateDomain(id string, req models.UpdateVMRequest) (models.VM, error)
	DeleteDomain(id string) error
	CloneDomain(id string, req models.CloneVMRequest) (models.VM, error)
	StartDomain(id string) error
	ShutdownDomain(id string) error
	ForceOffDomain(id string) error
	RebootDomain(id string) error
	SuspendDomain(id string) error
	ResumeDomain(id string) error
	SetDomainAutostart(id string, enabled bool) error
	GetDomainAutostart(id string) (bool, error)
	SetBootDevice(id string, device string) error
	GetBootDevice(id string) (string, error)
	ValidateDomainDisks(id string) error
	// ListIncusProfiles returns the profile names available on the Incus
	// backend (empty for KVM-only hosts).
	ListIncusProfiles() ([]string, error)

	// --- Disks / devices / USB ---
	AttachDisk(id string, req models.AttachDiskRequest) error
	DetachDisk(id, target string) error
	ChangeDiskBus(id, target, newBus string) error
	UpdateDiskSource(id, target, source string) error
	ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error)
	AttachNetworkIface(id string, req models.AttachNetRequest) error
	DetachNetworkIface(id, mac string) error
	UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error
	AttachUSBDevice(id, vendorID, productID string) error
	DetachUSBDevice(id, vendorID, productID string) error
	ListHostUSBDevices() ([]models.USBDevice, error)

	// --- Snapshots ---
	ListSnapshots(domainID string) ([]models.Snapshot, error)
	CreateSnapshot(domainID string, req models.CreateSnapshotRequest) (models.Snapshot, error)
	DeleteSnapshot(domainID, snapID string) (int64, error)
	RevertSnapshot(domainID, snapID string) error
	ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error)

	// --- Storage / pools / volumes / ISO ---
	ListStoragePools() ([]models.StoragePool, error)
	CreateStoragePool(ctx context.Context, req models.CreatePoolRequest) (models.StoragePool, error)
	UpdateStoragePool(ctx context.Context, name string, req models.UpdatePoolRequest) (models.StoragePool, error)
	DeletePool(name string) error
	RefreshPool(name string) error
	GetPoolPath(name string) (string, error)
	DiskPoolName() string
	ISOPoolName() string
	ListStorageVolumes(poolName string) ([]models.StorageVolume, error)
	GetStorageVolume(poolName, volName string) (models.StorageVolume, error)
	CreateStorageVolume(req models.CreateVolumeRequest) (models.StorageVolume, error)
	ResizeStorageVolume(poolName, volName string, newSizeGB int64) error
	DeleteStorageVolume(poolName, volName string) error
	VolumeExists(poolName, volName string) (bool, error)
	FindVolumeAttachments(poolName, volName string) ([]models.VolumeAttachment, error)
	GetISOs(poolName string) ([]models.ISOScanResult, error)
	RenameISO(oldName, newName, poolName string) error
	DeleteISO(name, poolName string) error
	DeleteVMDiskFiles(vmName string, exact ...string) (deleted []string, skipped []string, err error)
	RefreshCIFSSecretIfNeeded(ctx context.Context, poolName string) (*SecretRef, error)

	// --- Networking ---
	ListNetworks() ([]models.Network, error)
	CreateNetwork(req models.CreateNetworkRequest) (models.Network, error)
	UpdateNetwork(name string, req models.UpdateNetworkRequest) (models.Network, error)
	DeleteNetwork(id string) error
	StartNetwork(name string) (models.Network, error)
	StopNetwork(name string) (models.Network, error)
	CheckVLANSupport(networkName string) (models.VlanSupport, error)
	NetworkLeases(name string) ([]models.DHCPLease, error)
	ReleaseNetworkLease(br, ip, mac string) error

	// --- Console / cloud-init / metadata ---
	OpenSerialConsole(id string) (ConsoleStream, error)
	SetUserPassword(id, user, password string) error
	GetVMMeta(uuid string) (models.VMMeta, error)
	SetVMMeta(uuid string, meta models.VMMeta) error
	UpdateVMMeta(uuid string, upd models.VMMetaUpdate) (models.VMMeta, error)
	GetVNCInfo(id string) (GraphicsInfo, error)
	GetDomainIP(id string) string
	GetDomainXML(id string) (string, error)
	GuestGetClipboard(id string) (string, error)
	GuestSetClipboard(id, text string) error

	// --- Backup / export / OVA / import ---
	ExportDomain(ctx context.Context, id string, opts ExportBackupOptions, w io.Writer) (backupstore.ProducerResult, error)
	ExportDomainOVA(ctx context.Context, id string, opts OVAOptions, w io.Writer) error
	EstimateExportSize(ctx context.Context, id string, compress bool) (int64, error)
	EstimateOVASize(ctx context.Context, id string, target OVATarget) (int64, error)
	ImportDomain(tarPath, newName, poolName string, opts ImportOpts) (string, string, []string, error)
	ImportOVA(ovaPath, newName, poolName string) (string, string, error)
}

// BackendHelpers are hypervisor-agnostic predicates used by handlers
// (managed bridge/network naming, pool purposes). Kept here so api/*
// stops importing internal/libvirt for constants.
const (
	PoolPurposeDisk = "disk"
	PoolPurposeISO  = "iso"
)

// IsManagedBridge reports whether a bridge name is WebKVM-managed.
// IsManagedNetwork reports whether a network id is WebKVM-managed.
// The KVM adapter binds the real predicates at startup (BindHelpers);
// until then (tests) they safely return false.
func IsManagedBridge(name string) bool {
	return isManagedBridge != nil && isManagedBridge(name)
}

func IsManagedNetwork(id string) bool {
	return isManagedNetwork != nil && isManagedNetwork(id)
}

var (
	isManagedBridge  func(string) bool
	isManagedNetwork func(string) bool
)

// BindHelpers wires the managed-name predicates (called at startup by
// the KVM adapter).
func BindHelpers(isBridge, isNetwork func(string) bool) {
	isManagedBridge = isBridge
	isManagedNetwork = isNetwork
}
