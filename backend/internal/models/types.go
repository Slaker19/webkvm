package models

import "time"

type VMState string

const (
	VMStateRunning VMState = "running"
	VMStateShutoff VMState = "shutoff"
	VMStatePaused  VMState = "paused"
	VMStateCrashed VMState = "crashed"
	VMStateUnknown VMState = "unknown"
)

type VM struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type is the instance kind: "vm" (KVM or LXD virtual machine) or
	// "container" (LXC). Added in v1.4 Fase 1 so the UI can render
	// containers alongside VMs (unified model).
	Type string `json:"type"`
	// Hypervisor is the backend that owns this instance: "kvm" or "incus".
	Hypervisor string  `json:"hypervisor"`
	State      VMState `json:"state"`
	VCPUs      int     `json:"vcpus"`
	RAMMB      int64   `json:"ram_mb"`
	DiskGB     int64   `json:"disk_gb"`
	OSIcon     string  `json:"os_icon,omitempty"`
	UptimeSec  int64   `json:"uptime_sec,omitempty"`
	CPUUsage   float64 `json:"cpu_usage,omitempty"`
	RAMUsedMB  int64   `json:"ram_used_mb,omitempty"`
	OSType     string  `json:"os_type,omitempty"`
	OSVersion  string  `json:"os_version,omitempty"`
	Chipset    string  `json:"chipset,omitempty"`
	SecureBoot bool    `json:"secure_boot"`
	TPMEnabled bool    `json:"tpm_enabled"`
	// TPMVersion is "1.2" or "2.0", read back from the domain's actual
	// <tpm><backend version='...'/> when TPMEnabled. Empty when TPM is
	// off.
	TPMVersion string `json:"tpm_version,omitempty"`
	// WatchdogEnabled reflects whether a <watchdog> device is present.
	WatchdogEnabled bool `json:"watchdog_enabled"`
	// MinRAMMB is the guaranteed minimum memory allocation (memory ballooning floor).
	MinRAMMB int64 `json:"min_ram_mb,omitempty"`
	// IOThreads specifies dedicated IOThread count for disk/SCSI IO processing.
	IOThreads int `json:"iothreads,omitempty"`
	// Autostart mirrors libvirtd's autostart flag: when true,
	// libvirtd starts the domain automatically on host boot.
	// Surfaced here so the UI doesn't need a second round-trip
	// to GET /vms/{id}/autostart after fetching the VM.
	Autostart  bool     `json:"autostart"`
	Firmware   string   `json:"firmware,omitempty"`
	CPUMode    string   `json:"cpu_mode,omitempty"`
	CPUModel   string   `json:"cpu_model,omitempty"`
	CPUFlags   []string `json:"cpu_flags,omitempty"`
	CPUUnits   int      `json:"cpu_units,omitempty"`
	KVMHidden  bool     `json:"kvm_hidden,omitempty"`
	VideoModel string   `json:"video_model,omitempty"`
	AudioModel string   `json:"audio_model,omitempty"`
	SerialPort bool     `json:"serial_port"`
	GraphicsType string `json:"graphics_type,omitempty"` // "vnc", "spice", "both"
	BootOrder  string   `json:"boot_order,omitempty"`
	Privileged bool     `json:"privileged"`
	Nesting    bool     `json:"nesting"`
	Profiles   []string `json:"profiles,omitempty"`
	IP         string   `json:"ip,omitempty"`
	// IPs lists every IPv4 the instance holds across its NICs (containers
	// with several interfaces expose one per NIC). IP is the primary.
	IPs     []string `json:"ips,omitempty"`
	Alias   string   `json:"alias,omitempty"`
	Cover   string   `json:"cover,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	OwnerID string   `json:"owner_id,omitempty"`
	// Template mirrors the metadata flag so the VM list can badge
	// templates without a per-VM GetVMMeta round-trip.
	Template bool `json:"template,omitempty"`
	// Shared mirrors the metadata flag: a template every user may list
	// and instantiate regardless of owner/group/tag ACLs.
	Shared bool `json:"shared,omitempty"`
	// ProvisionMethod describes how the guest is provisioned at first
	// boot (PLAN-LXD 5.2): "cloud-init" (native LXD user-data or NoCloud
	// ISO), "script", "seed-iso" or "" (none). Orthogonal to the
	// hypervisor; drives the footer chip on the VM card.
	ProvisionMethod string         `json:"provision_method,omitempty"`
	Disks           []DiskInfo     `json:"disks,omitempty"`
	Networks        []NetIface     `json:"networks,omitempty"`
	USBDevices      []USBDevice    `json:"usb_devices,omitempty"`
	PCIDevices      []PCIDevice    `json:"pci_devices,omitempty"`
	SharedFolders   []SharedFolder `json:"shared_folders,omitempty"`
}

type CreateVMRequest struct {
	Name string `json:"name"`
	// Type is the instance kind to create: "vm" (default, KVM) or
	// "container" (LXD). Empty keeps the historical default (vm).
	Type string `json:"type,omitempty"`
	// Image is the LXD image reference for container creation, e.g.
	// "ubuntu:24.04" or "images:alpine/3.20" (<remote>:<alias>, where
	// remote is one of the official LXD remotes). When set, the request
	// is routed to the LXD backend — zero ISOs involved.
	Image        string `json:"image,omitempty"`
	VCPUs        int    `json:"vcpus"`
	RAMMB        int64  `json:"ram_mb"`
	DiskGB       int64  `json:"disk_gb"`
	ISO          string `json:"iso,omitempty"`
	Network      string `json:"network,omitempty"`
	StoragePool  string `json:"storage_pool,omitempty"`
	OSVariant    string `json:"os_variant,omitempty"`
	CPUMode      string `json:"cpu_mode,omitempty"`
	CPUModel     string `json:"cpu_model,omitempty"`
	VideoModel   string `json:"video_model,omitempty"`
	AudioModel   string `json:"audio_model,omitempty"`
	NetworkModel string `json:"network_model,omitempty"`
	DiskBus      string `json:"disk_bus,omitempty"`
	OSType       string `json:"os_type,omitempty"`
	OSVersion    string `json:"os_version,omitempty"`
	Chipset      string `json:"chipset,omitempty"`
	SecureBoot   *bool  `json:"secure_boot,omitempty"`
	TPMEnabled   *bool  `json:"tpm_enabled,omitempty"`
	// TPMVersion is "1.2" or "2.0" (validated in CreateVM). Empty keeps
	// the historical default (2.0/tpm-crb) when TPMEnabled is true.
	TPMVersion string `json:"tpm_version,omitempty"`
	// WatchdogEnabled adds an i6300esb watchdog device (action='reset')
	// — works on both q35 and i440fx, no chipset restriction.
	WatchdogEnabled *bool `json:"watchdog_enabled,omitempty"`
	// MinRAMMB sets the minimum memory floor in MB for memory ballooning.
	MinRAMMB int64 `json:"min_ram_mb,omitempty"`
	// IOThreads sets dedicated IOThread workers for storage/controller IO.
	IOThreads        int    `json:"iothreads,omitempty"`
	Firmware         string `json:"firmware,omitempty"`
	DiskFormat       string `json:"disk_format,omitempty"`
	VirtIOISO        string `json:"virtio_iso,omitempty"`
	ExistingDiskPool string `json:"existing_disk_pool,omitempty"`
	ExistingDiskName string `json:"existing_disk_name,omitempty"`
	// DiskCacheIO applies the recommended cache='none' io='native'
	// driver attributes to the main disk.
	DiskCacheIO *bool `json:"disk_cache_io,omitempty"`
	// DiskDiscard enables discard='unmap' (TRIM passthrough) on the
	// main disk, reclaiming space on thin-provisioned qcow2 volumes.
	DiskDiscard *bool `json:"disk_discard,omitempty"`
	// CPUSockets/CPUCores/CPUThreads describe an explicit CPU
	// topology. All three must be set together, and their product
	// must equal VCPUs, or CreateVM rejects the request.
	CPUSockets *int `json:"cpu_sockets,omitempty"`
	CPUCores   *int `json:"cpu_cores,omitempty"`
	CPUThreads *int `json:"cpu_threads,omitempty"`
	// CPUFlags allows requiring (+aes, +avx2, +topoext) or disabling (-hypervisor) specific CPU features.
	CPUFlags []string `json:"cpu_flags,omitempty"`
	// CPUUnits sets cgroups shares/weight (default 1024, range 100-500000).
	CPUUnits *int `json:"cpu_units,omitempty"`
	// KVMHidden hides KVM hypervisor signature to bypass GPU driver code 43 and anti-cheat detections.
	KVMHidden *bool `json:"kvm_hidden,omitempty"`
	// SerialPort adds an emulated PTY serial console (<serial> and <console>).
	// Default is true (enabled).
	SerialPort *bool `json:"serial_port,omitempty"`
	// GraphicsType is "vnc", "spice", "both", or "none" (default: "both").
	GraphicsType string `json:"graphics_type,omitempty"`
	// BootOrder is the primary boot device for KVM domains:
	// "disk", "cdrom" or "network". Empty keeps the default (disk).
	BootOrder string `json:"boot_order,omitempty"`
	// Autostart starts the instance when the host daemon boots
	// (KVM: libvirtd autostart flag; Incus: boot.autostart).
	// nil keeps the backend default (true).
	Autostart *bool `json:"autostart,omitempty"`
	// Privileged toggles security.privileged on Incus containers
	// (false = unprivileged, the safe default).
	Privileged *bool `json:"privileged,omitempty"`
	// Nesting enables security.nesting (run Docker/other containers
	// inside the container).
	Nesting *bool `json:"nesting,omitempty"`
	// Profiles are the Incus profiles applied to a container.
	// Empty uses the "default" profile.
	Profiles []string `json:"profiles,omitempty"`
	// GPU adds an Incus "gpu" device, giving the container access to the
	// host's render nodes (/dev/dri) for hardware transcoding.
	//
	// This is SHARED access, not the exclusive PCI passthrough a VM
	// gets: the host and any other container keep using the same card.
	// That difference is why it is offered as an ordinary container
	// option rather than an admin-only, host-disrupting operation.
	GPU *bool `json:"gpu,omitempty"`
	// VLANTag optionally assigns a VLAN tag (1-4094) to the primary network interface.
	VLANTag *int `json:"vlan_tag,omitempty"`
	// CloudInit optionally provisions the VM with a NoCloud seed
	// (user + SSH key + hostname) on first boot.
	CloudInit *CloudInitRequest `json:"cloud_init,omitempty"`
}

type UpdateVMRequest struct {
	Name         *string  `json:"name,omitempty"`
	VCPUs        *int     `json:"vcpus,omitempty"`
	RAMMB        *int64   `json:"ram_mb,omitempty"`
	DiskGB       *int64   `json:"disk_gb,omitempty"`
	CPUMode      *string  `json:"cpu_mode,omitempty"`
	CPUModel     *string  `json:"cpu_model,omitempty"`
	CPUFlags     []string `json:"cpu_flags,omitempty"`
	CPUUnits     *int     `json:"cpu_units,omitempty"`
	KVMHidden    *bool    `json:"kvm_hidden,omitempty"`
	VideoModel   *string  `json:"video_model,omitempty"`
	AudioModel   *string  `json:"audio_model,omitempty"`
	Network      *string  `json:"network,omitempty"`
	NetworkModel *string  `json:"network_model,omitempty"`
	OSType       *string  `json:"os_type,omitempty"`
	OSVersion    *string  `json:"os_version,omitempty"`
	Chipset      *string  `json:"chipset,omitempty"`
	SecureBoot   *bool    `json:"secure_boot,omitempty"`
	TPMEnabled   *bool    `json:"tpm_enabled,omitempty"`
	// TPMVersion is "1.2" or "2.0" (validated in UpdateVM). nil leaves
	// the current version unchanged; only meaningful together with
	// TPMEnabled (or when TPM is already on).
	TPMVersion *string `json:"tpm_version,omitempty"`
	// WatchdogEnabled adds/removes an i6300esb watchdog device.
	WatchdogEnabled *bool    `json:"watchdog_enabled,omitempty"`
	MinRAMMB        *int64   `json:"min_ram_mb,omitempty"`
	IOThreads       *int     `json:"iothreads,omitempty"`
	SerialPort      *bool    `json:"serial_port,omitempty"`
	GraphicsType    *string  `json:"graphics_type,omitempty"` // "vnc", "spice", or "both"
	Firmware        *string  `json:"firmware,omitempty"`
	BootOrder       *string  `json:"boot_order,omitempty"`
	Autostart       *bool    `json:"autostart,omitempty"`
	Privileged      *bool    `json:"privileged,omitempty"`
	Nesting         *bool    `json:"nesting,omitempty"`
	Profiles        []string `json:"profiles,omitempty"`
}

type DiskInfo struct {
	Device   string `json:"device"`              // disk, cdrom
	Bus      string `json:"bus"`                 // virtio, sata, scsi, ide
	Target   string `json:"target"`              // vda, sda, hda, etc.
	Source   string `json:"source"`              // file path
	BlockDev string `json:"block_dev,omitempty"` // host block device path (/dev/zvol/...)
	ZVol     string `json:"zvol,omitempty"`      // ZFS volume identifier ("pool/vol")
	Name     string `json:"name"`                // display name (root backing file basename)
	Pool     string `json:"pool,omitempty"`
	SizeGB   int64  `json:"size_gb,omitempty"`
	ReadOnly bool   `json:"readonly"`
	Type     string `json:"type"` // file, block
	WWN      string `json:"wwn,omitempty"`
	Serial   string `json:"serial,omitempty"`
	Alias    string `json:"alias,omitempty"`
}

// VolumeAttachment identifies a domain currently referencing a storage
// volume, returned by the storage delete guards when a destructive
// operation is refused.
type VolumeAttachment struct {
	VMID   string `json:"vm_id"`
	VMName string `json:"vm_name"`
	State  string `json:"state"`  // running | shutoff | ...
	Device string `json:"device"` // disk | cdrom
	Target string `json:"target"` // vda, sda...
}

// HostZVol represents a discovered ZFS volume on the host.
type HostZVol struct {
	Name    string            `json:"name"`              // "pool/vol"
	Pool    string            `json:"pool"`              // "pool"
	VolSize int64             `json:"volsize"`           // volume size in bytes
	Used    int64             `json:"used"`              // allocated bytes
	Device  string            `json:"device"`            // "/dev/zvol/pool/vol"
	UsedBy  *VolumeAttachment `json:"used_by,omitempty"` // domain using this zvol if attached
}

// HostZPool represents a discovered ZFS pool on the host.
type HostZPool struct {
	Name       string   `json:"name"`
	Size       int64    `json:"size_bytes"`
	SizeHuman  string   `json:"size_human"`
	Allocated  int64    `json:"allocated_bytes"`
	AllocHuman string   `json:"alloc_human"`
	Free       int64    `json:"free_bytes"`
	FreeHuman  string   `json:"free_human"`
	Health     string   `json:"health"`
	Devices    []string `json:"devices,omitempty"`
}

type CreateZPoolRequest struct {
	Name     string   `json:"name"`
	Topology string   `json:"topology"` // stripe, mirror, raidz1, raidz2
	Devices  []string `json:"devices"`
}

type CreateZVolRequest struct {
	Pool   string `json:"pool"`
	Name   string `json:"name"`
	SizeGB int64  `json:"size_gb"`
	Sparse bool   `json:"sparse"`
}

type CreateRAIDRequest struct {
	Name    string   `json:"name,omitempty"` // e.g. "/dev/md0" or "md0"
	Level   string   `json:"level"`          // 0, 1, 5, 6, 10 / raid0, raid1...
	Devices []string `json:"devices"`
}

type AttachDiskRequest struct {
	Device      string `json:"device"`                  // disk, cdrom
	Bus         string `json:"bus"`                     // virtio, sata, scsi, ide
	Source      string `json:"source,omitempty"`        // for cdrom: ISO path
	ZVol        string `json:"zvol,omitempty"`          // for raw block: existing ZFS volume "pool/name"
	SizeGB      int64  `json:"size_gb,omitempty"`       // for disk: new size
	Pool        string `json:"pool,omitempty"`          // storage pool for new disk
	Format      string `json:"format,omitempty"`        // for disk: qcow2, raw
	DiskCacheIO *bool  `json:"disk_cache_io,omitempty"` // cache='none' io='native'
	DiskDiscard *bool  `json:"disk_discard,omitempty"`  // discard='unmap'
	WWN         string `json:"wwn,omitempty"`           // 16 hex chars
	Serial      string `json:"serial,omitempty"`        // disk serial string
	Alias       string `json:"alias,omitempty"`         // user alias (ua-...)
	// Force overrides the "this disk already contains data" guard when
	// attaching an existing disk image. Without it, attaching a disk
	// whose image is not empty is refused (409) so the operator cannot
	// blindly reuse a disk that already holds an OS or another VM's data.
	Force bool `json:"force,omitempty"`
}

// USBDevice describes a USB device enumerated on the host, available
// for passthrough to a VM (admin only).
type USBDevice struct {
	Name      string `json:"name"`
	VendorID  string `json:"vendor_id"`  // e.g. "0x046d"
	ProductID string `json:"product_id"` // e.g. "0xc52b"
	Bus       string `json:"bus"`
	Device    string `json:"device"`
}

// PCIDevice describes a PCI device enumerated on the host, available for
// passthrough to a VM (admin only). Address is the standard lspci-style
// "DDDD:BB:SS.F" (hex) identifier, used both as the sysfs directory name
// (/sys/bus/pci/devices/<address>) and as the value AttachPCIRequest
// expects.
type PCIDevice struct {
	Name        string `json:"name"`
	Address     string `json:"address"` // e.g. "0000:01:00.0"
	VendorID    string `json:"vendor_id"`
	VendorName  string `json:"vendor_name,omitempty"`
	ProductID   string `json:"product_id"`
	ProductName string `json:"product_name,omitempty"`
	IOMMUGroup  int    `json:"iommu_group"`
	Driver      string `json:"driver,omitempty"`
	VFIOBound   bool   `json:"vfio_bound"`
	// BootVGA is true when this device is the host's own boot/console
	// GPU (/sys/bus/pci/devices/<address>/boot_vga == "1"). Passthrough
	// of this device is refused unconditionally, even for admins —
	// detaching the host's own console GPU while it's running is a real,
	// known way to hang the physical machine.
	BootVGA bool `json:"boot_vga"`
	// InUse is true when this device is already attached to a VM (found
	// in the domain XML of some other domain).
	InUse bool `json:"in_use"`
	// Assignable is true when this device can be safely passed through to a VM.
	Assignable bool `json:"assignable"`
	// BlockReason explains why passthrough is blocked when Assignable is false.
	BlockReason string `json:"block_reason,omitempty"`
	// HostCritical indicates this device is essential for host operation.
	HostCritical bool `json:"host_critical,omitempty"`
}

// PCIIOMMUGroup groups the host's PCI devices by IOMMU group — passthrough
// normally requires handing over the WHOLE group at once, not a single
// device within it, since every device in a group shares the same DMA
// isolation boundary.
type PCIIOMMUGroup struct {
	Group   int         `json:"group"`
	Devices []PCIDevice `json:"devices"`
	// AllVFIOBound is true only when every device in the group is
	// currently bound to vfio-pci.
	AllVFIOBound bool `json:"all_vfio_bound"`
	// Assignable is true only when the group as a whole can be safely passed through.
	Assignable bool `json:"assignable"`
	// BlockReason explains why the group is blocked when Assignable is false.
	BlockReason string `json:"block_reason,omitempty"`
	// HostCritical is true if any device in the group is critical to host survival.
	HostCritical bool `json:"host_critical,omitempty"`
}

// PCIPreflightInfo summarizes host IOMMU and VFIO readiness for passthrough.
type PCIPreflightInfo struct {
	IOMMUEnabled       bool   `json:"iommu_enabled"`
	IOMMUVendor        string `json:"iommu_vendor"` // "amd", "intel", "unknown"
	IOMMUMode          string `json:"iommu_mode"`   // "translated", "passthrough", "disabled"
	VFIOModuleLoaded   bool   `json:"vfio_module_loaded"`
	VFIOAvailable      bool   `json:"vfio_available"`
	GroupsTotal        int    `json:"groups_total"`
	GroupsAssignable   int    `json:"groups_assignable"`
	GroupsBridgeOnly   int    `json:"groups_bridge_only"`
	GroupsHostCritical int    `json:"groups_host_critical"`
	GroupsInUse        int    `json:"groups_in_use"`
}

// AttachPCIRequest passes through one or more PCI devices — normally
// every address in one IOMMU group at once (see PCIIOMMUGroup).
type AttachPCIRequest struct {
	Addresses []string `json:"addresses"`
}

// HostDisk describes a physical block device attached to the host.
type HostDisk struct {
	Name        string          `json:"name"`
	Path        string          `json:"path"`
	Size        int64           `json:"size_bytes"`
	SizeHuman   string          `json:"size_human"`
	Type        string          `json:"type"` // disk, loop, rom...
	FSType      string          `json:"fstype,omitempty"`
	MountPoints []string        `json:"mountpoints,omitempty"`
	Model       string          `json:"model,omitempty"`
	Serial      string          `json:"serial,omitempty"`
	Rotational  bool            `json:"rotational"`          // true = HDD, false = SSD / NVMe
	Transport   string          `json:"transport,omitempty"` // nvme, sata, usb, scsi...
	IsSystem    bool            `json:"is_system"`           // contains / or /boot
	Children    []HostPartition `json:"children,omitempty"`

	// MountState is the intent-aware verdict: "active" (mounted now),
	// "configured" (not mounted at this instant, but an armed systemd
	// automount, an fstab entry or a libvirt pool will bring it back)
	// or "free". A disk can be unmounted per the kernel and still be
	// "configured" — formatting it in that state silently destroys data
	// the moment systemd remounts the path, which is why the wipe and
	// format guards refuse anything that is not "free".
	MountState string `json:"mount_state,omitempty"`
	// MountSources names the detectors that fired ("systemd-automount",
	// "fstab", "libvirt-pool", ...) so the UI can explain the verdict.
	MountSources []string `json:"mount_sources,omitempty"`
	// MountUnits are the systemd units holding the device, verbatim, so
	// the operator can disable them directly.
	MountUnits []string `json:"mount_units,omitempty"`
	// MountPools are libvirt storage pools whose target lives here.
	MountPools []string `json:"mount_pools,omitempty"`
	// MountDetail is a short human-readable reason, empty when free.
	MountDetail string `json:"mount_detail,omitempty"`
	// SMART health and telemetry summary, populated when available.
	SMART *HostDiskSMART `json:"smart,omitempty"`
}

// HostDiskSMART describes the S.M.A.R.T. health and telemetry status for a disk.
type HostDiskSMART struct {
	Available          bool   `json:"available"`
	Healthy            bool   `json:"healthy"`
	Status             string `json:"status"` // "PASSED", "WARNING", "FAILED", "UNKNOWN"
	TemperatureC       int    `json:"temperature_c"`
	PowerOnHours       int64  `json:"power_on_hours"`
	PowerCycles        int64  `json:"power_cycles"`
	WearPercentage     int    `json:"wear_percentage"`          // 0-100%, -1 if N/A
	DataWrittenBytes   uint64 `json:"data_written_bytes"`       // in bytes (TBW)
	ReallocatedSectors int64  `json:"reallocated_sectors"`     // -1 if N/A
	PendingSectors     int64  `json:"pending_sectors"`         // -1 if N/A
	CriticalWarning    int    `json:"critical_warning"`        // NVMe bitmask
}

// HostPartition describes a single partition within a HostDisk.
type HostPartition struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Size        int64    `json:"size_bytes"`
	SizeHuman   string   `json:"size_human"`
	FSType      string   `json:"fstype,omitempty"`
	MountPoints []string `json:"mountpoints,omitempty"`
}

// InitHostDiskRequest formats and mounts a disk as a Directory storage.
type InitHostDiskRequest struct {
	DiskPath   string `json:"disk_path"`  // e.g. /dev/sdb or /dev/nvme1n1
	Filesystem string `json:"filesystem"` // ext4, xfs, btrfs or f2fs — see
	// supportedFilesystems in api/host_disks.go for the authoritative
	// list and GET /api/host/disks/filesystems for what's actually
	// installed on this host.
	VolumeName string `json:"volume_name"` // e.g. data-3tb, fast-ssd
	MountPoint string `json:"mount_point"` // e.g. /mnt/data-3tb
	CreatePool bool   `json:"create_pool"` // automatically register StoragePool
	// PoolPurpose is LEGACY: it only applies to clients that omit
	// Subfolders entirely, which then get a single pool at the mount
	// root. Current clients derive one independent pool per marked
	// subfolder instead (see createPoolsForSubfolders).
	PoolPurpose string `json:"pool_purpose"` // disk, container, iso, backup, template
	// Mode selects the operation:
	//   "format" (default) — partition + format (DESTROYS data).
	//   "mount"            — mount an existing filesystem, preserving data.
	Mode string `json:"mode,omitempty"`
	// NeedFormat is kept for backward compatibility; when false and Mode
	// is empty it behaves like "format" (historical behaviour).
	// Subfolders creates the standard Proxmox-style tree inside the
	// mount point (isos, discos, contenedores, backups, plantillas) AND
	// drives pool creation: each marked folder carrying a nature gets
	// its own independent pool.
	//
	// Absent (nil) and empty ([]) mean different things on purpose:
	//   nil — legacy client; falls back to one pool at the mount root
	//         using PoolPurpose.
	//   []  — the operator unchecked every folder; NO pool is created.
	// Never collapse the two, or an empty selection silently
	// recreates the unified multi-purpose pool this design removes.
	Subfolders []string `json:"subfolders,omitempty"`
	// RegisterBackupTarget registers the mount's backups/ subfolder as a
	// local backup destination.
	RegisterBackupTarget bool `json:"register_backup_target,omitempty"`
	// Device overrides the exact device to mount (e.g. /dev/sdb1 for a
	// pre-existing partition). Empty derives it from DiskPath.
	Device string `json:"device,omitempty"`
	// MountOptions are extra fstab-style mount options appended to the
	// defaults (noatime + boot-resilience options). Free-form strings are
	// validated against a safe allowlist to prevent injection.
	MountOptions []string `json:"mount_options,omitempty"`
	// NoFail makes boot continue even if the device is missing or fails
	// to mount (adds nofail + x-systemd.device-timeout). Default true.
	NoFail *bool `json:"nofail,omitempty"`
	// Automount mounts the filesystem lazily on first access
	// (x-systemd.automount), so a slow/unplugged disk never blocks boot.
	Automount *bool `json:"automount,omitempty"`
	// ReadOnly mounts the filesystem read-only.
	ReadOnly *bool `json:"read_only,omitempty"`
}

// SharedFolder describes a host directory shared into a VM via 9p
// (virtio-9p — chosen over virtiofs, which needs a separately-supervised
// virtiofsd helper process this codebase has no equivalent of). The guest
// mounts it explicitly:
//
//	mount -t 9p -o trans=virtio,version=9p2000.L <Tag> /mnt/<Tag>
//
// Admin-only (see Handler.validateSharedFolderPath) — unlike a single-file
// disk source, this exposes an entire directory tree recursively.
type SharedFolder struct {
	HostPath string `json:"host_path"`
	Tag      string `json:"tag"`
	ReadOnly bool   `json:"read_only"`
}

// AttachSharedFolderRequest attaches a new 9p shared folder. HostPath must
// resolve inside a known storage pool (see validateSharedFolderPath); Tag
// is the identifier the guest mounts by and must be unique per VM.
type AttachSharedFolderRequest struct {
	HostPath string `json:"host_path"`
	Tag      string `json:"tag"`
	ReadOnly bool   `json:"read_only"`
}

type NetIface struct {
	MAC     string `json:"mac"`
	Network string `json:"network"`
	Model   string `json:"model"`
	Type    string `json:"type"` // network, bridge
	Source  string `json:"source,omitempty"`
	VLANTag *int   `json:"vlan_tag,omitempty"`
	// IPs holds only THIS interface's own IPv4 addresses (matched by
	// MAC). Previously every interface row in the UI displayed the
	// VM/container's whole IP list (VM.IPs) instead of its own — with
	// more than one NIC, every row showed the same combined list.
	IPs []string `json:"ips,omitempty"`
}

type AttachNetRequest struct {
	Network string `json:"network"`
	Model   string `json:"model,omitempty"`
	VLANTag *int   `json:"vlan_tag,omitempty"`
}

type CloneVMRequest struct {
	Name    string `json:"name"`
	Pool    string `json:"pool,omitempty"`
	Network string `json:"network,omitempty"`
	// Linked, when true, creates the clone's disk as a qcow2 overlay
	// with the source disk as its backing file (copy-on-write). This
	// is near-instant and uses almost no extra space up front, but
	// the clone stays dependent on the source disk staying intact —
	// deleting or moving the source breaks the clone. Only valid when
	// the source disk is qcow2.
	//
	// When false (default), the clone gets a fully independent copy
	// of the disk (qemu-img convert) — slower and uses full disk
	// space, but has no dependency on the source.
	Linked bool `json:"linked,omitempty"`
}

type Snapshot struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	CreatedAt       string `json:"created_at"`    // legacy RFC3339; kept for compat
	CreationTime    int64  `json:"creation_time"` // epoch seconds, used by the tree UI
	ParentName      string `json:"parent_name"`   // empty = root snapshot
	Current         bool   `json:"current"`
	SizeAtSnapBytes int64  `json:"size_at_snap_bytes,omitempty"` // libvirt "Allocated" at creation
}

type CreateSnapshotRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Memory includes the VM's RAM in the snapshot (full snapshot).
	// Requires the VM to be running. Defaults to false (disk-only).
	Memory bool `json:"memory,omitempty"`
}

type StoragePool struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Path         string `json:"path"`
	Purpose      string `json:"purpose"`
	Capacity     int64  `json:"capacity"`
	Allocated    int64  `json:"allocated"`
	Available    int64  `json:"available"`
	State        string `json:"state"`
	Autostart    bool   `json:"autostart"`
	SourceHost   string `json:"source_host,omitempty"`
	SourcePort   int    `json:"source_port,omitempty"`
	SourceDevice string `json:"source_device,omitempty"`
	SourceDir    string `json:"source_dir,omitempty"`
	SourceFormat string `json:"source_format,omitempty"`
	// DeviceID identifies the underlying block device/filesystem this
	// pool's directory lives on (the stat(2) st_dev of Path). A "dir"
	// pool's Capacity/Available from libvirt are the FULL underlying
	// filesystem's, not pool-specific — two pools on the same disk (the
	// common case: an ISO pool and a disk pool both under the same
	// DATA_DIR) report the identical Capacity, so summing it across
	// pools double-counts the same physical disk. Callers aggregating
	// totals across pools must dedupe by DeviceID first.
	DeviceID uint64 `json:"device_id,omitempty"`
}

type CreatePoolRequest struct {
	Name         string `json:"name"`
	Type         string `json:"type"`                    // dir, netfs, iscsi
	Path         string `json:"path"`                    // local target path (where the mount lands, or /dev/disk/by-path for iscsi)
	SourceHost   string `json:"source_host,omitempty"`   // for netfs, iscsi
	SourcePort   int    `json:"source_port,omitempty"`   // for iscsi (default 3260)
	SourceDevice string `json:"source_device,omitempty"` // for iscsi (Target IQN)
	SourceIQN    string `json:"source_iqn,omitempty"`    // alias for source_device
	SourceDir    string `json:"source_dir,omitempty"`    // for netfs
	SourceFormat string `json:"source_format,omitempty"` // nfs, cifs (defaults to nfs)
	Purpose      string `json:"purpose"`

	// SourceUsername / SourcePassword are accepted for netfs pools
	// with SourceFormat=cifs or iscsi pools with CHAP. Both must be
	// set together (validated in the API handler with HTTP 400) or
	// the backend refuses to build the <auth> block.
	SourceUsername string `json:"source_username,omitempty"`
	SourcePassword string `json:"source_password,omitempty"`

	// SecretUUID is set internally by the libvirt layer after a
	// successful defineCIFSSecret or defineCHAPSecret. It is never
	// accepted from the client and never echoed back. Hidden from
	// JSON with `json:"-"`.
	SecretUUID string `json:"-"`
}

// UpdatePoolRequest is the body of PUT /api/storage/pools/{name}.
// All fields are optional; nil pointer means "leave unchanged".
// Pointer-based booleans are not used here because the only
// "toggle" we expose today is CifsNeedsReauth, which is itself
// a request-time signal (not a stored state) and is therefore
// represented as a plain bool with omitempty.
type UpdatePoolRequest struct {
	Path         *string `json:"path,omitempty"`
	SourceHost   *string `json:"source_host,omitempty"`
	SourcePort   *int    `json:"source_port,omitempty"`
	SourceDevice *string `json:"source_device,omitempty"`
	SourceIQN    *string `json:"source_iqn,omitempty"`
	SourceDir    *string `json:"source_dir,omitempty"`
	SourceFormat *string `json:"source_format,omitempty"`

	// CIFS/CHAP auth fields. Must come together (both nil or both set)
	// to avoid silently misconfiguring the pool. The handler
	// rejects mismatched values with HTTP 400.
	SourceUsername *string `json:"source_username,omitempty"`
	SourcePassword *string `json:"source_password,omitempty"`

	// CifsNeedsReauth: when true, the backend re-creates the
	// libvirt secret for this CIFS pool using the supplied credentials
	// and updates the pool XML to reference the new secret UUID.
	CifsNeedsReauth bool `json:"cifs-needs-reauth,omitempty"`

	// ChapNeedsReauth: when true, the backend re-creates the
	// libvirt secret for this iSCSI pool using the supplied CHAP credentials
	// and updates the pool XML to reference the new secret UUID.
	ChapNeedsReauth bool `json:"chap-needs-reauth,omitempty"`

	// Purpose retags what the pool is for ("disk", "iso",
	// "container", "backup", "template"). The purpose is NOT part of
	// the libvirt pool XML — it lives in pool-purposes.json — so
	// changing it is a metadata write, not an undefine+redefine, and
	// the pool keeps running throughout.
	//
	// The handler refuses the change when the pool already holds
	// files the new purpose would misfile (see poolRetagBlockers):
	// retagging a disk pool full of qcow2 images as "iso" would hide
	// every one of them from the volume browser.
	Purpose *string `json:"purpose,omitempty"`
}

type StorageVolume struct {
	Name      string `json:"name"`
	Pool      string `json:"pool"`
	Path      string `json:"path"`
	Format    string `json:"format"`
	Capacity  int64  `json:"capacity"`
	Allocated int64  `json:"allocated"` // IsSnapshot is true when this volume is an internal qcow2 snapshot
	// view (e.g. "vm1.snap1") rather than a real file on disk. Resize
	// and delete are not supported for snapshots.
	IsSnapshot bool `json:"is_snapshot,omitempty"`
	// SnapshotOfVMID is the UUID of the VM that owns this snapshot.
	// Empty when IsSnapshot is false.
	SnapshotOfVMID string `json:"snapshot_of_vm_id,omitempty"`
	// ParentVolume is the name of the root disk (e.g. "vm1.qcow2")
	// that this snapshot belongs to. Empty when IsSnapshot is false.
	ParentVolume string `json:"parent_volume,omitempty"`
}

type CreateVolumeRequest struct {
	Name     string `json:"name"`
	Pool     string `json:"pool"`
	Capacity int64  `json:"capacity"`
	Format   string `json:"format"`
}

// Network describes one WebKVM network: a real Linux bridge, of one of
// three kinds:
//   - "nat": an isolated bridge with a masquerade rule, so its subnet
//     reaches the internet through the host.
//   - "isolated": an isolated bridge with no internet access.
//   - "direct": a real physical/wireless NIC enslaved into a bridge with
//     a user-chosen name (like the host's own vmbr0), for VMs/containers
//     that need to be on the real LAN.
//
// Forward is kept as a deprecated alias of Kind for backward
// compatibility with callers written before Kind existed; new code
// should read/write Kind.
type Network struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"` // "nat" | "isolated" | "direct"
	Forward string `json:"forward"`
	Bridge  string `json:"bridge"`
	// Interface is the physical/wireless host NIC enslaved into a
	// "direct" bridge, e.g. "eth0". Empty for "nat"/"isolated".
	Interface string   `json:"interface,omitempty"`
	CIDR      string   `json:"cidr"`
	DHCP      bool     `json:"dhcp"`
	DHCPStart string   `json:"dhcp_start,omitempty"`
	DHCPEnd   string   `json:"dhcp_end,omitempty"`
	Gateway   string   `json:"gateway,omitempty"`
	DNS       []string `json:"dns,omitempty"` // DNS forwarders for dnsmasq
	// MTU is the bridge's link MTU (0 = kernel/bridge default, 1500).
	MTU int `json:"mtu,omitempty"`
	// Reservations are fixed MAC→IP DHCP leases served by the bridge's
	// dnsmasq (nat/isolated only, requires DHCP on).
	Reservations []DHCPReservation `json:"reservations,omitempty"`
	VLanAware    bool              `json:"vlan_aware,omitempty"`
	Slaves       []string          `json:"slaves,omitempty"` // "direct" only: the bridge's real ports
	Active       bool              `json:"active"`
	Autostart    bool              `json:"autostart"`
	// Protected is true for the host's primary bridge (the one holding
	// its LAN IP) and for any bridge WebKVM did not create itself. The
	// API refuses to delete these (and the UI greys out the delete
	// button) so a stray click can't silently cut the host off the LAN.
	Protected bool `json:"protected,omitempty"`
}

// DHCPReservation pins a MAC address to a fixed IP in the bridge's
// dnsmasq (the equivalent of a Proxmox static DHCP mapping). Name is an
// optional dnsmasq comment used to label the reservation.
type DHCPReservation struct {
	MAC  string `json:"mac"`
	IP   string `json:"ip"`
	Name string `json:"name,omitempty"`
}

type CreateNetworkRequest struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // "nat" | "isolated" | "direct"; falls back to Forward when empty
	// Forward is a deprecated alias for Kind ("" / "bridge" == "isolated").
	Forward string `json:"forward"`
	CIDR    string `json:"cidr"`              // required for "nat"/"isolated", optional for "direct"
	Gateway string `json:"gateway,omitempty"` // optional default gateway
	Bridge  string `json:"bridge,omitempty"`
	// Interface is required for kind=="direct": the physical/wireless
	// host NIC to enslave into the new bridge, e.g. "eth0".
	Interface string   `json:"interface,omitempty"`
	DHCP      *bool    `json:"dhcp,omitempty"`
	DHCPStart string   `json:"dhcp_start,omitempty"`
	DHCPEnd   string   `json:"dhcp_end,omitempty"`
	DNS       []string `json:"dns,omitempty"`
	MTU       int      `json:"mtu,omitempty"`
	// Reservations are fixed MAC→IP DHCP leases (nat/isolated + DHCP).
	Reservations []DHCPReservation `json:"reservations,omitempty"`
	// VLanAware is a pointer ("direct" only) so the API layer can tell
	// "not specified" (nil, falls back to the network.vlan_aware_default
	// setting) apart from an explicit false — a plain bool can't carry
	// that distinction. Mirrors UpdateNetworkRequest.VLanAware, already
	// a pointer for the same reason.
	VLanAware *bool `json:"vlan_aware,omitempty"`
	Autostart *bool `json:"autostart,omitempty"`
}

type UpdateNetworkRequest struct {
	Gateway   *string  `json:"gateway,omitempty"`
	DHCP      *bool    `json:"dhcp,omitempty"`
	DHCPStart string   `json:"dhcp_start,omitempty"`
	DHCPEnd   string   `json:"dhcp_end,omitempty"`
	DNS       []string `json:"dns,omitempty"`
	MTU       *int     `json:"mtu,omitempty"`
	// Reservations replaces the whole set when non-nil. An empty slice
	// clears every fixed lease.
	Reservations []DHCPReservation `json:"reservations,omitempty"`
	VLanAware    *bool             `json:"vlan_aware,omitempty"` // "direct" only
	Autostart    *bool             `json:"autostart,omitempty"`
}

type HostInfo struct {
	Hostname       string `json:"hostname"`
	Architecture   string `json:"architecture"`
	CPUCores       int    `json:"cpu_cores"`
	CPUThreads     int    `json:"cpu_threads"`
	CPUSockets     int    `json:"cpu_sockets"`
	CPUModel       string `json:"cpu_model"`
	TotalRAM       int64  `json:"total_ram"`
	FreeRAM        int64  `json:"free_ram"`
	LibvirtVersion string `json:"libvirt_version"`
	QEMUVersion    string `json:"qemu_version"`
	// PoolsDir is where a directory pool created without an explicit
	// path is rooted, on the system disk. Reported rather than
	// hardcoded in the UI because it follows DataDir, which an
	// operator can move.
	PoolsDir string `json:"pools_dir,omitempty"`
}

type HostStats struct {
	CPUUsage  float64 `json:"cpu_usage"`
	UsedRAM   int64   `json:"used_ram"`
	TotalRAM  int64   `json:"total_ram"`
	UsedDisk  int64   `json:"used_disk"`
	TotalDisk int64   `json:"total_disk"`
}

// HostInterface represents a physical/logical host network interface that
// can be used as a bridge target for libvirt bridge-mode networks.
type HostInterface struct {
	Name     string `json:"name"`
	Type     string `json:"type"`  // "ethernet", "wifi", "bond", "vlan"
	State    string `json:"state"` // "up", "down", "unknown"
	MAC      string `json:"mac"`
	IPSource string `json:"ip_source"` // "static" | "dhcp" | "none"
}

// VMMeta is the webkvm app-level metadata stored inside a
// libvirt domain's <metadata><webkvm:meta> element. Persists with
// the domain via libvirt; no separate database needed.
type VMMeta struct {
	Alias  string   `xml:"alias"     json:"alias,omitempty"`
	Notes  string   `xml:"notes"     json:"notes,omitempty"`
	Cover  string   `xml:"cover"     json:"cover,omitempty"`
	Groups []string `xml:"groups>group,omitempty" json:"groups,omitempty"`
	// Tags (V13-D-01) are RBAC policy labels: non-admins with matching
	// AllowedTags can access the VM, and backup targets can select VMs
	// by tag. They travel in the libvirt metadata XML alongside Groups.
	Tags []string `xml:"tags>tag,omitempty" json:"tags,omitempty"`
	// OwnerID is the username that owns this VM (used for per-user
	// quotas). Admin/operator-created VMs may leave it empty.
	OwnerID string `xml:"owner,omitempty" json:"owner_id,omitempty"`
	// Template marks a VM as a reusable template (hidden from the
	// normal VM list, instantiated to create new VMs).
	Template bool `xml:"template,omitempty" json:"template,omitempty"`
	// Shared (admin-only) makes a template listable and instantiable by
	// every user, regardless of owner/group/tag ACLs. It does not grant
	// visibility in the VM list nor edit/delete rights. Only meaningful
	// while Template is true; cleared when the template flag is unset.
	Shared bool `xml:"shared,omitempty" json:"shared,omitempty"`
	// CiUser is the cloud-init username provisioned at creation, used
	// for password reset via the guest agent.
	CiUser string `xml:"ci_user,omitempty" json:"ci_user,omitempty"`
	// AppInfo is a JSON blob describing a deployed appliance app
	// (name, URL path and database credentials) so the UI can show a
	// credentials pop-up. Set at deploy time.
	AppInfo   string `xml:"app_info,omitempty" json:"app_info,omitempty"`
	UpdatedAt int64  `xml:"updated_at" json:"updated_at,omitempty"`
}

// VMMetaUpdate carries partial updates from the API; nil pointers mean
// "leave unchanged". Use slice* helpers in metadata.go to apply these.
type VMMetaUpdate struct {
	Alias  *string   `json:"alias,omitempty"`
	Notes  *string   `json:"notes,omitempty"`
	Cover  *string   `json:"cover,omitempty"`
	Groups *[]string `json:"groups,omitempty"`
	// Tags *[]string replaces the VM's tag set (V13-D-01); nil =
	// unchanged, empty slice = clear.
	Tags *[]string `json:"tags,omitempty"`
	// OwnerID is settable by admins only (enforced in the handler).
	OwnerID *string `json:"owner_id,omitempty"`
	// Template toggles the template flag.
	Template *bool `json:"template,omitempty"`
	// Shared toggles "share with all users" on a template (admin-only,
	// enforced in the handler).
	Shared *bool `json:"shared,omitempty"`
	// CiUser is the cloud-init username (set at creation).
	CiUser *string `json:"ci_user,omitempty"`
	// AppInfo is the JSON blob with appliance credentials (deploy time).
	AppInfo *string `json:"app_info,omitempty"`
}

// CloudInitNetworkConfig specifies static IP assignment, default gateways and DNS servers for a NIC.
type CloudInitNetworkConfig struct {
	Interface string   `json:"interface,omitempty"` // e.g. "eth0" or "enp1s0"
	IPv4      string   `json:"ipv4,omitempty"`      // CIDR format, e.g. "192.168.1.50/24"
	Gateway4  string   `json:"gateway4,omitempty"`  // IPv4 gateway, e.g. "192.168.1.1"
	IPv6      string   `json:"ipv6,omitempty"`      // CIDR format, e.g. "2001:db8::50/64"
	Gateway6  string   `json:"gateway6,omitempty"`  // IPv6 gateway
	DNS       []string `json:"dns,omitempty"`       // Nameserver IPs
	Search    []string `json:"search,omitempty"`    // Search domains
}

// CloudInitRequest carries optional NoCloud provisioning data applied
// when creating a VM or instantiating a template.
type CloudInitRequest struct {
	User           string                   `json:"user,omitempty"`
	Password       string                   `json:"password,omitempty"`
	SSHKey         string                   `json:"ssh_key,omitempty"`
	Hostname       string                   `json:"hostname,omitempty"`
	Networks       []CloudInitNetworkConfig `json:"networks,omitempty"`
	CustomUserData string                   `json:"custom_user_data,omitempty"`
	SnippetID      string                   `json:"snippet_id,omitempty"`
	SnippetIDs     []string                 `json:"snippet_ids,omitempty"`
	// ProvisionScript, when set, is a bash script injected into the
	// cloud-init seed and executed on first boot (used by appliance
	// "apps" to install software on the base image). It is set
	// server-side only, never accepted from the client UI.
	ProvisionScript string `json:"-"`
}

// CoverUploadResponse is the response of POST /api/vms/{id}/cover.
// It carries only the public URL: the on-disk path is a server detail
// that must not leak to clients.
type CoverUploadResponse struct {
	URL    string `json:"url"`
	Format string `json:"format"`
}

// VlanSupport describes whether VLAN tagging is supported for a given
// network on this host.
type VlanSupport struct {
	Supported bool   `json:"supported"`
	Reason    string `json:"reason,omitempty"`
}

// UpdateNetIfaceRequest is the payload of PATCH /api/vms/{id}/networks/{mac}.
type UpdateNetIfaceRequest struct {
	MAC     *string `json:"mac,omitempty"`
	Network *string `json:"network,omitempty"`
	VLANTag *int    `json:"vlan_tag,omitempty"` // nil = leave, 0 = remove VLAN
}

// Group is a tag/label with a color, shared across VMs. Stored in
// /var/lib/webkvm/groups.json (one definition per app, not per VM).
type Group struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	MemberCount int    `json:"member_count"`
}

// GroupList is the response of GET /api/groups.
type GroupList struct {
	Groups []Group `json:"groups"`
}

// GroupUpsertRequest is the body of POST /api/groups and PUT /api/groups/{name}.
type GroupUpsertRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// MetricsSample is a single timestamped datapoint for a metric.
type MetricsSample struct {
	T int64   `json:"t"` // epoch seconds
	V float64 `json:"v"`
}

// MetricsSeries is a time series of samples.
type MetricsSeries struct {
	Kind   string          `json:"kind"` // "cpu" | "ram" | "disk_r" | "disk_w" | "net_rx" | "net_tx"
	Unit   string          `json:"unit"`
	Window int             `json:"window"` // seconds
	Points []MetricsSample `json:"points"`
}

// VMMetrics bundles all the metric series for a single VM. Returned by
// GET /api/vms/{id}/metrics and pushed via the "vm.metrics" SSE event.
type VMMetrics struct {
	VMID      string        `json:"vm_id"`
	SampledAt int64         `json:"sampled_at"`
	CPU       MetricsSeries `json:"cpu"`
	RAM       MetricsSeries `json:"ram"`
	DiskRead  MetricsSeries `json:"disk_read"`
	DiskWrite MetricsSeries `json:"disk_write"`
	NetRx     MetricsSeries `json:"net_rx"`
	NetTx     MetricsSeries `json:"net_tx"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// LoginResponse (V13-SEC-01) no longer carries the session JWT: it is set
// as an HttpOnly cookie, so it never reaches JavaScript. The CSRF value is
// returned so the SPA can echo it back as X-CSRF-Token on mutations.
type LoginResponse struct {
	Username           string `json:"username"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"must_change_password"`
	ExpiresAt          int64  `json:"expires_at"`
	CSRF               string `json:"csrf,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type ISOScanResult struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
	Pool string `json:"pool"`
}

// Role constants for the fixed 3-tier RBAC model.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// IsValidRole returns true if r is one of the recognised fixed roles.
func IsValidRole(r string) bool {
	return r == RoleAdmin || r == RoleOperator || r == RoleViewer
}

// User is the on-disk user record. The bcrypt hash IS persisted to
// disk (so the password survives restarts) under the field name
// "password_hash". The ToResponse() projection is used in API
// responses to keep the hash out of HTTP traffic.
//
// Note: this struct is also returned by the on-disk
// (de)serializer in `internal/user/store.go`, which uses
// json:"-" for `PasswordHash` via a shadow type to prevent the
// hash from being leaked in the in-memory API surface. See
// `userStoreFile` and the `(u) MarshalJSON` / `UnmarshalJSON`
// methods below.
// Quota caps a user's/group's resource usage. Zero values mean
// "unlimited" for that dimension.
type Quota struct {
	MaxVMs     int            `json:"max_vms,omitempty"`
	MaxVCPUs   int            `json:"max_vcpus,omitempty"`
	MaxRAMMB   int            `json:"max_ram_mb,omitempty"`
	MaxDiskGB  int            `json:"max_disk_gb,omitempty"` // global disk cap across all pools (0 = no global cap)
	PoolQuotas map[string]int `json:"pool_quotas,omitempty"` // per storage-pool disk cap in GB (0 = no cap on that pool)
}

// Enabled reports whether any quota dimension is set. A user may have
// only per-pool disk limits (MaxDiskGB == 0) and must still be subject
// to quota, so PoolQuotas is considered too.
func (q Quota) Enabled() bool {
	if q.MaxVMs > 0 || q.MaxVCPUs > 0 || q.MaxRAMMB > 0 || q.MaxDiskGB > 0 {
		return true
	}
	return len(q.PoolQuotas) > 0
}

// UserPermissions defines fine-grained capability flags for non-admin users.
// When nil, default capabilities for the role apply. When set, specific capabilities
// can be granted or revoked individually.
type UserPermissions struct {
	CanCreateVM     *bool `json:"can_create_vm,omitempty"`
	CanDeleteVM     *bool `json:"can_delete_vm,omitempty"`
	CanControlPower *bool `json:"can_control_power,omitempty"`
	CanConsole      *bool `json:"can_console,omitempty"`
	CanSnapshots    *bool `json:"can_snapshots,omitempty"`
	CanBackups      *bool `json:"can_backups,omitempty"`
	CanMedia        *bool `json:"can_media,omitempty"`
	// CanExport covers streaming a VM's full disk image out of the
	// system. It is separate from CanConsole because screen access and
	// bulk data extraction are different risks.
	CanExport *bool `json:"can_export,omitempty"`
}

// Capabilities is the canonical list of named permissions. Anything not
// in here is not a capability, and HasPermission denies it.
var Capabilities = []string{
	"create_vm",
	"delete_vm",
	"control_power",
	"console",
	"snapshots",
	"backups",
	"media",
	"export",
}

// capabilityFlag returns the tri-state flag backing perm, and whether
// perm is a known capability at all.
func (p *UserPermissions) capabilityFlag(perm string) (*bool, bool) {
	if p == nil {
		return nil, IsCapability(perm)
	}
	switch perm {
	case "create_vm":
		return p.CanCreateVM, true
	case "delete_vm":
		return p.CanDeleteVM, true
	case "control_power":
		return p.CanControlPower, true
	case "console":
		return p.CanConsole, true
	case "snapshots":
		return p.CanSnapshots, true
	case "backups":
		return p.CanBackups, true
	case "media":
		return p.CanMedia, true
	case "export":
		return p.CanExport, true
	}
	return nil, false
}

// IsCapability reports whether perm names a known capability.
func IsCapability(perm string) bool {
	for _, c := range Capabilities {
		if c == perm {
			return true
		}
	}
	return false
}

type User struct {
	Username           string           `json:"username"`
	PasswordHash       string           `json:"password_hash,omitempty"`
	Role               string           `json:"role"`
	CreatedAt          string           `json:"created_at"`
	Email              string           `json:"email,omitempty"`
	Active             bool             `json:"active"`
	MustChangePassword bool             `json:"must_change_password"`
	LastLoginAt        string           `json:"last_login_at,omitempty"`
	Permissions        *UserPermissions `json:"permissions,omitempty"`
	// Avatar is the profile picture, stored as a media reference of the
	// form "/api/media/<id>/raw". Empty means "no picture": the UI falls
	// back to the username initials. It is a reference into the media
	// library rather than a copy, so an image uploaded once can serve as
	// an avatar, a VM cover, or both.
	Avatar string `json:"avatar,omitempty"`
	// Quota limits this user's VMs. Zero fields = unlimited.
	Quota Quota `json:"quota,omitempty"`
	// AllowedPools restricts which storage pools this user may use for
	// VM/disk operations. Empty means "all pools". Admins are always
	// exempt. This is the per-user pool visibility/ACL.
	AllowedPools []string `json:"allowed_pools,omitempty"`
	// AllowedNetworks restricts which networks/bridges this user may
	// attach a VM/container NIC to. Empty means "all networks". Admins
	// are always exempt. Mirrors AllowedPools exactly, for networks.
	AllowedNetworks []string `json:"allowed_networks,omitempty"`
	// AllowedTags (V13-D-01) grants access to any VM carrying one of
	// these tags, in addition to owned VMs. Empty means "owned VMs only".
	// Admins are always exempt. This turns tags into a real RBAC policy.
	AllowedTags []string `json:"allowed_tags,omitempty"`
	// AllowedGroups grants access to any VM belonging to one of these
	// groups, in addition to owned VMs. Empty means "owned VMs only".
	// Admins are always exempt.
	AllowedGroups []string `json:"allowed_groups,omitempty"`
	// SessionEpoch is bumped by an admin's "log out everywhere" action
	// (POST /api/users/{username}/revoke-sessions). Every token issued
	// before the bump carries the old epoch and is rejected by
	// SessionEnforcer on its next request, forcing a fresh login —
	// without needing a per-token revocation list. Never exposed via
	// UserResponse or any request type; it's server-internal state,
	// changed only through the dedicated endpoint.
	SessionEpoch int `json:"session_epoch,omitempty"`

	// 2FA / TOTP support
	TOTPSecret      string   `json:"totp_secret,omitempty"`
	TOTPEnabled     bool     `json:"totp_enabled,omitempty"`
	TOTPBackupCodes []string `json:"totp_backup_codes,omitempty"`
}

// HasPermission reports whether the user is allowed to perform a specific action.
func (u *User) HasPermission(perm string) bool {
	if u.Role == RoleAdmin {
		return true
	}
	flag, known := u.Permissions.capabilityFlag(perm)
	// An unrecognised permission name is denied rather than granted.
	// The operator branch below defaults to "allowed", so a typo in a
	// call site used to hand out access silently; failing closed turns
	// that same typo into a visible 403 instead.
	if !known {
		return false
	}
	if u.Role == RoleViewer {
		// Viewers are read-only by default. Console is the one
		// capability that can be granted to them explicitly, for
		// support staff who need to see a screen without being able
		// to change anything.
		if perm == "console" {
			return flag != nil && *flag
		}
		return false
	}
	// RoleOperator: defaults to true unless explicitly overridden to false.
	if flag != nil {
		return *flag
	}
	return true
}

// UserResponse is the API-facing projection of User; it is what gets
// returned by /api/users endpoints and /api/auth/me. The hash is
// deliberately excluded.
type UserResponse struct {
	Username           string           `json:"username"`
	Role               string           `json:"role"`
	CreatedAt          string           `json:"created_at"`
	Email              string           `json:"email,omitempty"`
	Active             bool             `json:"active"`
	MustChangePassword bool             `json:"must_change_password"`
	LastLoginAt        string           `json:"last_login_at,omitempty"`
	Permissions        *UserPermissions `json:"permissions,omitempty"`
	Avatar             string           `json:"avatar,omitempty"`
	Quota              Quota            `json:"quota,omitempty"`
	AllowedPools       []string         `json:"allowed_pools,omitempty"`
	AllowedNetworks    []string         `json:"allowed_networks,omitempty"`
	AllowedTags        []string         `json:"allowed_tags,omitempty"`
	AllowedGroups      []string         `json:"allowed_groups,omitempty"`
	TOTPEnabled        bool             `json:"totp_enabled"`
}

func (u *User) ToResponse() UserResponse {
	return UserResponse{
		Username:           u.Username,
		Role:               u.Role,
		CreatedAt:          u.CreatedAt,
		Email:              u.Email,
		Active:             u.Active,
		MustChangePassword: u.MustChangePassword,
		LastLoginAt:        u.LastLoginAt,
		Permissions:        u.Permissions,
		Avatar:             u.Avatar,
		Quota:              u.Quota,
		AllowedPools:       u.AllowedPools,
		AllowedNetworks:    u.AllowedNetworks,
		AllowedTags:        u.AllowedTags,
		AllowedGroups:      u.AllowedGroups,
		TOTPEnabled:        u.TOTPEnabled,
	}
}

type CreateUserRequest struct {
	Username    string           `json:"username"`
	Password    string           `json:"password"`
	Role        string           `json:"role"`
	Email       string           `json:"email,omitempty"`
	Permissions *UserPermissions `json:"permissions,omitempty"`
	Quota       Quota            `json:"quota,omitempty"`
	// AllowedPools, when non-empty, restricts the user to these pools.
	AllowedPools []string `json:"allowed_pools,omitempty"`
	// AllowedNetworks, when non-empty, restricts the user to these
	// networks/bridges. Mirrors AllowedPools exactly, for networks.
	AllowedNetworks []string `json:"allowed_networks,omitempty"`
	// AllowedTags, when non-empty, grants tag-based access (V13-D-01).
	AllowedTags []string `json:"allowed_tags,omitempty"`
	// AllowedGroups, when non-empty, grants group-based access.
	AllowedGroups []string `json:"allowed_groups,omitempty"`
	// MustChangePassword, when true, forces the new user to set their
	// own password on first login (the admin-chosen one becomes a
	// one-time temporary credential).
	MustChangePassword bool `json:"must_change_password,omitempty"`
}

type UpdateUserRequest struct {
	Password    *string          `json:"password,omitempty"`
	Role        *string          `json:"role,omitempty"`
	Email       *string          `json:"email,omitempty"`
	Active      *bool            `json:"active,omitempty"`
	Permissions *UserPermissions `json:"permissions,omitempty"`
	// Quota is applied wholesale when non-nil (all dimensions).
	Quota *Quota `json:"quota,omitempty"`
	// AllowedPools, when non-nil, replaces the user's pool allowlist
	// (pass an empty slice to clear the restriction).
	AllowedPools *[]string `json:"allowed_pools,omitempty"`
	// AllowedNetworks, when non-nil, replaces the user's network
	// allowlist (pass an empty slice to clear the restriction). Mirrors
	// AllowedPools exactly, for networks.
	AllowedNetworks *[]string `json:"allowed_networks,omitempty"`
	// AllowedTags, when non-nil, replaces the user's tag allowlist
	// (V13-D-01).
	AllowedTags *[]string `json:"allowed_tags,omitempty"`
	// AllowedGroups, when non-nil, replaces the user's group allowlist.
	AllowedGroups *[]string `json:"allowed_groups,omitempty"`
}

// ChangeMyPasswordRequest is the body of PUT /api/users/me/password.
type ChangeMyPasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}

type DownloadISORequest struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"`
	Pool string `json:"pool,omitempty"`
}

// DownloadJob tracks progress of a background ISO download
//
// UpdatedAt (unix seconds) is bumped on every create/update so the job
// sweeper can purge finished entries older than the TTL
// without ever touching queued/running ones.
type DownloadJob struct {
	ID       string  `json:"id"`
	Name     string  `json:"name,omitempty"`
	URL      string  `json:"url,omitempty"`
	Progress float64 `json:"progress"`
	Status   string  `json:"status"`
	// Error is set only when Status is "error". Progress commentary
	// belongs in Message: several download paths used to write their
	// running narration here, which left successful jobs carrying an
	// "error" string and hid the text from clients, since they only
	// read this field on failure.
	Error string `json:"error,omitempty"`
	// Message is human-readable commentary on the current step
	// ("Finalizing and converting image..."). Purely informational.
	Message    string `json:"message,omitempty"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
	BytesDone  int64  `json:"bytes_done,omitempty"`
	BytesTotal int64  `json:"bytes_total,omitempty"`
	SpeedBps   int64  `json:"speed_bps,omitempty"`
	ETASeconds int64  `json:"eta_seconds,omitempty"`
	// Owner is the username that started the job. GetDownloadJob serves
	// a job only to its owner or to an admin: the record carries the
	// destination path, the source URL and the byte counts, and the IDs
	// are predictable (UnixNano), so a bare lookup by ID leaked another
	// user's activity to anyone authenticated. Empty on jobs created
	// before this field existed, which stay readable (see handler).
	Owner string `json:"owner,omitempty"`
	// Pool is the storage pool the job writes into. Recorded so a
	// client polling the job can report (and verify) the destination:
	// the pool used to live only in the worker's closure, leaving no
	// way to tell where a running download was actually landing.
	Pool string `json:"pool,omitempty"`
	// Stage is a coarse phase label ("move_copying", "move_cleanup")
	// for long operations. Deliberately separate from Status, which
	// stays one of queued/running/done/error: the job sweeper and
	// every polling client key off Status, so overloading it with
	// phase names would make finished work look unrecognisable.
	Stage string `json:"stage,omitempty"`
	// Result carries the job's payload on success (e.g. the cloned VM
	// or the created snapshot) for consumers that poll the job.
	Result any `json:"result,omitempty"`
}

// DHCPLease is one active lease from a "nat"/"isolated" network's
// per-bridge dnsmasq leasefile. "direct" networks get addresses from
// the LAN's own DHCP and have no lease file WebKVM controls.
type DHCPLease struct {
	MAC      string    `json:"mac"`
	IP       string    `json:"ip"`
	Hostname string    `json:"hostname,omitempty"` // "" when dnsmasq recorded "*"
	ClientID string    `json:"client_id,omitempty"`
	Expiry   time.Time `json:"expiry"`
}

// IncusImageItem represents an Incus/LXC container image with metadata,
// category, architecture, recommended resources, and local cache status.
type IncusImageItem struct {
	Ref         string `json:"ref"`                   // "ubuntu:24.04", "images:alpine/3.21", "local:<fingerprint>"
	Label       string `json:"label"`                 // "Ubuntu 24.04 LTS (Noble)"
	Category    string `json:"category"`              // "popular", "minimal", "enterprise", "dev", "local"
	Distro      string `json:"distro"`                // "ubuntu", "debian", "alpine", "arch", "fedora", "rocky", "almalinux", ...
	Description string `json:"description"`           // Human readable description
	Arch        string `json:"arch"`                  // "x86_64", "arm64"
	Badge       string `json:"badge,omitempty"`       // "LTS", "5 MB", "Local"
	IsLocal     bool   `json:"is_local"`              // true if image is cached in local Incus storage
	Fingerprint string `json:"fingerprint,omitempty"` // local image sha256 fingerprint if cached
	Size        int64  `json:"size,omitempty"`        // image size in bytes
	RecVCPUs    int    `json:"rec_vcpus,omitempty"`   // recommended vCPUs
	RecRAMMB    int64  `json:"rec_ram_mb,omitempty"`  // recommended RAM in MB
	RecDiskGB   int64  `json:"rec_disk_gb,omitempty"` // recommended disk size in GB
}

// IncusImagesResponse is the response payload for GET /api/vms/incus-images.
type IncusImagesResponse struct {
	Images       []IncusImageItem `json:"images"`
	HostArch     string           `json:"host_arch"`
	IncusEnabled bool             `json:"incus_enabled"`
}

// MediaItem represents a visual asset stored in the media pool.
type MediaItem struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Category  string    `json:"category"` // "system" or "custom"
	Path      string    `json:"path"`
	URL       string    `json:"url"`
	Size      int64     `json:"size"`
	SizeHuman string    `json:"size_human"`
	MimeType  string    `json:"mime_type"`
	IsSystem  bool      `json:"is_system"`
	CreatedAt time.Time `json:"created_at"`
}

// ApplyMediaUsageRequest is the payload for POST /api/media/apply-usage.
type ApplyMediaUsageRequest struct {
	MediaID string `json:"media_id"`
	Usage   string `json:"usage"` // "avatar", "logo", "favicon", "vm_cover"
	VMID    string `json:"vm_id,omitempty"`
}
