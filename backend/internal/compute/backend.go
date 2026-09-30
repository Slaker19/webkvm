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
	"strings"

	"webkvm/internal/backupstore"
	"webkvm/internal/models"
)

// Error sentinels raised from the hypervisor packages so handlers can
// classify errors without knowing which backend produced them.
var (
	// ErrDomainNotRunning is returned when an operation (e.g. serial
	// console) requires the instance to be running.
	ErrDomainNotRunning = errors.New("the VM must be running to use the console")
	// ErrNoSerialDevice is returned when the instance has no serial or
	// console device. It is terminal: retrying cannot make one appear,
	// so console callers must report it instead of waiting out a grace
	// period.
	ErrNoSerialDevice = errors.New("this VM has no serial console device; add one to its hardware to use the text console")
	// ErrDomainNotPaused is returned when a resume targets a VM that is
	// not paused (or a suspend a VM that is already paused).
	ErrDomainNotPaused = errors.New("the VM is not paused")
	// ErrDomainAlreadyRunning is returned when a start targets a VM that
	// is already running.
	ErrDomainAlreadyRunning = errors.New("the VM is already running")
	// ErrDomainMustBeStoppedToRename is returned when a rename targets a
	// VM that is still running (libvirt only renames inactive domains).
	ErrDomainMustBeStoppedToRename = errors.New("the VM must be stopped to rename it")
	// ErrDomainMustBeStoppedToClone is returned when a clone targets a
	// VM that is still running (its disk can't be safely copied while
	// QEMU holds the write lock).
	ErrDomainMustBeStoppedToClone = errors.New("the VM must be stopped to clone it (its disk cannot be safely copied while running)")
	// ErrMemorySnapshotRequiresRunning is returned when a memory
	// snapshot is requested on a powered-off instance.
	ErrMemorySnapshotRequiresRunning = errors.New("a memory snapshot requires the VM to be running")
	// ErrDomainMustBeStoppedToMove is returned when a storage move
	// targets a running VM. libvirt can only relocate a disk under a
	// live domain through a block-copy job, which WebKVM does not
	// drive; copying the file underneath a running QEMU would corrupt
	// it, so the operation refuses instead of risking the data.
	ErrDomainMustBeStoppedToMove = errors.New("the VM must be stopped to move its storage (its disk cannot be safely copied while running)")
	// ErrSamePool is returned when a move's source and destination are
	// the same pool. Treated as an error rather than a no-op so the
	// caller learns the request was meaningless instead of seeing a
	// "success" that changed nothing.
	ErrSamePool = errors.New("the source and destination pools are the same")
	// ErrCrossBackendMove is returned when a move would cross the
	// libvirt/Incus boundary. Those pools hold different kinds of
	// object; going between them is a conversion (export + import),
	// not a move.
	ErrCrossBackendMove = errors.New("cannot move between a VM pool and a container pool; export and re-import instead")
	// ErrVolumeInUse is returned when a move targets a volume still
	// attached to a defined instance.
	ErrVolumeInUse = errors.New("the volume is attached to an instance; detach it or stop the instance first")
	// ErrVolumeHasDependents is returned when a move targets an image
	// other disks are layered on as linked clones. The dependency is
	// recorded inside each clone's own qcow2 header, so it is invisible
	// to the attachment check: the disk attached to the VM is the
	// overlay, never the image underneath. Moving the image away leaves
	// every clone unopenable.
	ErrVolumeHasDependents = errors.New("the volume is the backing file of one or more linked clones, which would stop working")
	// ErrBackingCheckUnavailable is returned when the linked-clone
	// check could not run at all. The operation is refused rather than
	// allowed: the check exists to prevent silent destruction of
	// clones, so not knowing the answer has to block, not permit.
	ErrBackingCheckUnavailable = errors.New("cannot determine whether this image is the backing file of other disks; refusing to risk breaking them")
	// ErrInsufficientSpace is returned when the destination pool cannot
	// fit the data. Checked up front: discovering it halfway through
	// leaves a truncated file and a full disk.
	ErrInsufficientSpace = errors.New("not enough free space in the destination pool")
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
	// ErrNetworkNotFound is returned when the named bridge does not
	// exist. Handlers map it to HTTP 404, not 500.
	ErrNetworkNotFound = errors.New("bridge not found")
	// ErrLeaseNotFound is returned by ReleaseNetworkLease when the given
	// (ip, mac) pair isn't in the bridge's current lease table — dhcp_release
	// itself can't tell a real release apart from a no-op (it just fires a
	// UDP packet and exits 0 either way), so the libvirt layer checks the
	// leasefile first. Handlers map it to HTTP 404.
	ErrLeaseNotFound = errors.New("lease not found")
	// ErrVolumeNotFound is returned when a storage volume (or ISO) lookup
	// names a volume that does not exist. Handlers map it to HTTP 404,
	// not 500.
	ErrVolumeNotFound = errors.New("storage volume not found")
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

// MoveVolumeOpts tunes a volume move between pools.
type MoveVolumeOpts struct {
	// Kind is "disk" or "iso". The two live in different places and
	// have different in-use semantics, so the caller states which it
	// means rather than having the backend guess from the filename.
	Kind string
	// NewName renames the volume at the destination. Empty keeps the
	// current name.
	NewName string
	// KeepSource copies instead of moving, leaving the original in
	// place. The source is only unlinked after the destination is
	// fully written and verified.
	KeepSource bool
	OnProgress func(pct float64, stage string)
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

// GuestFilesystem is one mounted filesystem as reported from INSIDE
// the guest by the QEMU guest agent. The disk sizes WebKVM shows
// elsewhere are host-side qcow2 allocation; these are what the guest
// OS itself sees, which is the only way to answer "is /var full?".
type GuestFilesystem struct {
	Name       string `json:"name"`
	Mountpoint string `json:"mountpoint"`
	Type       string `json:"type"`
	TotalBytes int64  `json:"total_bytes"`
	UsedBytes  int64  `json:"used_bytes"`
	UsedPct    int    `json:"used_pct"`
}

// GuestNetworkInterface is one NIC as seen from inside the guest,
// with every address bound to it. The host only knows about DHCP
// leases, so statically-configured guests are invisible without this.
type GuestNetworkInterface struct {
	Name string   `json:"name"`
	MAC  string   `json:"mac,omitempty"`
	IPv4 []string `json:"ipv4,omitempty"`
	IPv6 []string `json:"ipv6,omitempty"`
}

// GuestOSInfo identifies the operating system running in the guest.
type GuestOSInfo struct {
	ID            string `json:"id,omitempty"`
	Name          string `json:"name,omitempty"`
	PrettyName    string `json:"pretty_name,omitempty"`
	Version       string `json:"version,omitempty"`
	VersionID     string `json:"version_id,omitempty"`
	KernelRelease string `json:"kernel_release,omitempty"`
	KernelVersion string `json:"kernel_version,omitempty"`
	Machine       string `json:"machine,omitempty"`
}

// GuestUser represents an active user session inside the guest.
type GuestUser struct {
	User      string  `json:"user"`
	LoginTime float64 `json:"login_time,omitempty"`
	Domain    string  `json:"domain,omitempty"`
}

// GuestTimezone represents timezone information from the guest.
type GuestTimezone struct {
	Zone   string `json:"zone,omitempty"`
	Offset int    `json:"offset,omitempty"`
}

// GuestTrimmedPath represents one filesystem path trimmed during an fstrim operation.
type GuestTrimmedPath struct {
	Path    string `json:"path"`
	Trimmed int64  `json:"trimmed"`
	Minimum int64  `json:"minimum,omitempty"`
	Error   string `json:"error,omitempty"`
}

// GuestFSTrimResult summarizes the outcome of guest-fstrim across mounted filesystems.
type GuestFSTrimResult struct {
	Paths []GuestTrimmedPath `json:"paths"`
}

// GuestInfo bundles the guest-agent telemetry for one instance.
// Available=false with a populated Error means the agent isn't
// installed or isn't answering — an expected state, not a failure.
type GuestInfo struct {
	Available   bool                    `json:"available"`
	Error       string                  `json:"error,omitempty"`
	OS          *GuestOSInfo            `json:"os,omitempty"`
	Filesystems []GuestFilesystem       `json:"filesystems,omitempty"`
	Interfaces  []GuestNetworkInterface `json:"interfaces,omitempty"`
	Hostname    string                  `json:"hostname,omitempty"`
	Users       []GuestUser             `json:"users,omitempty"`
	Timezone    *GuestTimezone          `json:"timezone,omitempty"`
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
	GetDomainLog(id string, lines int) (string, error)
	// ListIncusProfiles returns the profile names available on the Incus
	// backend (empty for KVM-only hosts).
	ListIncusProfiles() ([]string, error)
	// ListIncusImages returns available official and locally cached Incus images.
	ListIncusImages() ([]models.IncusImageItem, error)
	// PullIncusImage downloads and caches ref (e.g. "images:alpine/3.20")
	// in the local Incus image store WITHOUT creating any instance — it
	// requires no network/bridge at all, unlike instantiating a throwaway
	// container just to warm the cache.
	PullIncusImage(ref string) error
	// DeleteIncusImage deletes a cached image from the local Incus image store.
	DeleteIncusImage(fingerprint string) error
	// GetIncusImagesVolume reports the storage volume Incus caches its
	// downloaded images in, as "pool/volume". Empty means the default:
	// /var/lib/incus/images on the system disk, which is where the
	// cache silently grows on an install whose disks are elsewhere.
	GetIncusImagesVolume() (string, error)
	// SetIncusImagesVolume points the image cache at a custom volume in
	// pool, creating the volume if it does not exist. An empty pool
	// restores the default location.
	SetIncusImagesVolume(pool, volume string) error

	// --- Disks / devices / USB ---
	AttachDisk(id string, req models.AttachDiskRequest) error
	DetachDisk(id, target string) error
	ChangeDiskBus(id, target, newBus string) error
	UpdateDiskSource(id, target, source string) error
	ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error)
	// RunCloudInitReprovision best-effort re-runs cloud-init inside a
	// running Linux guest via the QEMU guest agent, after a fresh
	// NoCloud seed ISO has been attached/swapped in. Fire-and-forget;
	// backends without a guest-exec channel (or for guests that don't
	// run cloud-init) return ErrNotImplemented.
	RunCloudInitReprovision(id string) error
	AttachNetworkIface(id string, req models.AttachNetRequest) error
	DetachNetworkIface(id, mac string) error
	UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error
	AttachUSBDevice(id, vendorID, productID string) error
	DetachUSBDevice(id, vendorID, productID string) error
	ListHostUSBDevices() ([]models.USBDevice, error)
	AttachPCIDevice(id string, addresses []string) error
	DetachPCIDevice(id, address string) error
	ListHostPCIDevices() ([]models.PCIIOMMUGroup, error)
	GetHostPCIPreflight() (models.PCIPreflightInfo, error)
	AttachSharedFolder(id, hostPath, tag string, readOnly bool) error
	DetachSharedFolder(id, tag string) error

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
	FindZVolAttachments(zvolName string) ([]models.VolumeAttachment, error)
	// BackingDependents lists the images layered on top of this volume
	// as linked clones. Being unattached does not make a volume unused:
	// a clone records its backing file inside its own qcow2 header, so
	// FindVolumeAttachments cannot see the dependency — the disk the VM
	// has attached is the overlay, never the image underneath it.
	// Deleting or moving that image leaves every clone unopenable.
	BackingDependents(poolName, volName string) ([]string, error)
	// BackingDependentsOfPath answers the same question for a file that
	// is not a pool volume, such as an image cached outside any pool.
	BackingDependentsOfPath(path string) ([]string, error)
	GetISOs(poolName string) ([]models.ISOScanResult, error)
	RenameISO(oldName, newName, poolName string) error
	DeleteISO(name, poolName string) error
	// MoveDomainStorage relocates an instance's storage to another pool
	// of the SAME backend, reporting coarse progress through onProgress
	// (which may be nil).
	//
	// Cross-backend moves are deliberately absent: a libvirt VM and an
	// Incus container are not the same kind of object, and "moving" one
	// into the other's pool would be a conversion, not a move.
	MoveDomainStorage(id, destPool string, onProgress func(pct float64, stage string)) error
	// MoveVolume relocates a single volume (a VM disk image or an ISO)
	// between two pools of the same backend.
	MoveVolume(srcPool, volName, destPool string, opts MoveVolumeOpts) error
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
	GetGuestInfo(id string) (GuestInfo, error)
	FSTrim(id string) (GuestFSTrimResult, error)

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
	PoolPurposeDisk      = "disk"
	PoolPurposeISO       = "iso"
	PoolPurposeContainer = "container"
	// Backup/template pools are libvirt-side pools rooted at the
	// disk's /backups and /plantillas folders. They are neutral:
	// excluded from the KVM/LXC/ISO selectors, listed in the grid.
	PoolPurposeBackup   = "backup"
	PoolPurposeTemplate = "template"
)

// HasPurpose reports whether a pool's declared purpose carries the given
// nature. Membership, not equality: nothing writes multi-purpose pools
// anymore, but installations created before the per-purpose model still
// have registry rows like "iso,disk" or "disk,iso,container". Comparing
// those with == made them fail BOTH requireISOPool and requireDiskPool,
// so a pool the UI happily offered answered 400 to every upload. "lxc"
// is accepted as a legacy spelling of "container", matching
// frontend/src/lib/purpose.js.
func HasPurpose(purpose, nature string) bool {
	if nature == "lxc" {
		nature = PoolPurposeContainer
	}
	if purpose == "" {
		// An undeclared pool is a disk pool, same default as the UI.
		return nature == PoolPurposeDisk
	}
	for _, p := range strings.Split(purpose, ",") {
		p = strings.TrimSpace(p)
		if p == "lxc" {
			p = PoolPurposeContainer
		}
		if p == nature {
			return true
		}
	}
	return false
}

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
