package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"webkvm/internal/backupstore"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"

	lvraw "libvirt.org/go/libvirt"
)

// KVMBackend is the KVM adapter for the ComputeBackend interface (v1.4
// Fase 0). It is a THIN delegation shim over the existing
// *libvirt.Connector: every method forwards to it, converting between
// the neutral compute.* types and libvirt's. No behaviour change — this
// is the same code the handlers called directly before the seam.
type KVMBackend struct {
	lv *libvirt.Connector
}

// NewKVMBackend wraps a libvirt Connector in the ComputeBackend seam.
func NewKVMBackend(lv *libvirt.Connector) *KVMBackend {
	return &KVMBackend{lv: lv}
}

// LV exposes the raw connector for hypervisor-infrastructure use only
// (metrics collectors, event loop, connectivity checks, raw libvirt
// queries in host/system handlers). Instance operations must go through
// the Backend interface.
func (b *KVMBackend) LV() *libvirt.Connector { return b.lv }

// kvmErr translates hypervisor sentinel errors to their neutral
// compute.* equivalents so handlers can errors.Is() them generically.
func kvmErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, libvirt.ErrDomainNotRunning):
		return ErrDomainNotRunning
	case errors.Is(err, libvirt.ErrNoSerialDevice):
		return ErrNoSerialDevice
	case errors.Is(err, libvirt.ErrDomainNotPaused):
		return ErrDomainNotPaused
	case errors.Is(err, libvirt.ErrDomainAlreadyRunning):
		return ErrDomainAlreadyRunning
	case errors.Is(err, libvirt.ErrDomainMustBeStoppedToRename):
		return ErrDomainMustBeStoppedToRename
	case errors.Is(err, libvirt.ErrDomainMustBeStoppedToClone):
		return ErrDomainMustBeStoppedToClone
	case errors.Is(err, libvirt.ErrDomainMustBeStoppedToMove):
		return ErrDomainMustBeStoppedToMove
	case errors.Is(err, libvirt.ErrSamePool):
		return ErrSamePool
	case errors.Is(err, libvirt.ErrVolumeInUse):
		return ErrVolumeInUse
	case errors.Is(err, libvirt.ErrBackingCheckUnavailable):
		return ErrBackingCheckUnavailable
	case errors.Is(err, libvirt.ErrVolumeHasDependents):
		// Wrapped: the libvirt side names the clones that block the
		// move, which is what tells the operator what to deal with.
		return fmt.Errorf("%w: %s", ErrVolumeHasDependents, strings.TrimPrefix(err.Error(),
			libvirt.ErrVolumeHasDependents.Error()+": "))
	case errors.Is(err, libvirt.ErrInsufficientSpace):
		// Wrapped, not replaced: the libvirt side appends the actual
		// numbers ("need 4096 MB, 1200 MB available"), which is the
		// part that tells the operator what to free up.
		return fmt.Errorf("%w: %s", ErrInsufficientSpace, strings.TrimPrefix(err.Error(),
			libvirt.ErrInsufficientSpace.Error()+": "))
	case errors.Is(err, libvirt.ErrMemorySnapshotRequiresRunning):
		return ErrMemorySnapshotRequiresRunning
	}
	return err
}

// --- Instance lifecycle ---

func (b *KVMBackend) ListDomains() ([]models.VM, error) { return b.lv.ListDomains() }
func (b *KVMBackend) GetDomain(id string) (models.VM, error) {
	v, err := b.lv.GetDomain(id)
	return v, kvmErr(err)
}
func (b *KVMBackend) DomainExists(name string) (bool, error) { return b.lv.DomainExists(name) }
func (b *KVMBackend) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	return b.lv.CreateDomain(req)
}
func (b *KVMBackend) UpdateDomain(id string, req models.UpdateVMRequest) (models.VM, error) {
	vm, err := b.lv.UpdateDomain(id, req)
	if err != nil {
		return models.VM{}, kvmErr(err)
	}
	return vm, nil
}
func (b *KVMBackend) DeleteDomain(id string) error { return kvmErr(b.lv.DeleteDomain(id)) }
func (b *KVMBackend) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	vm, err := b.lv.CloneDomain(id, req)
	return vm, kvmErr(err)
}
func (b *KVMBackend) StartDomain(id string) error    { return kvmErr(b.lv.StartDomain(id)) }
func (b *KVMBackend) ShutdownDomain(id string) error { return kvmErr(b.lv.ShutdownDomain(id)) }
func (b *KVMBackend) ForceOffDomain(id string) error { return kvmErr(b.lv.ForceOffDomain(id)) }
func (b *KVMBackend) RebootDomain(id string) error   { return kvmErr(b.lv.RebootDomain(id)) }
func (b *KVMBackend) SuspendDomain(id string) error  { return kvmErr(b.lv.SuspendDomain(id)) }
func (b *KVMBackend) ResumeDomain(id string) error   { return kvmErr(b.lv.ResumeDomain(id)) }
func (b *KVMBackend) SetDomainAutostart(id string, enabled bool) error {
	return b.lv.SetDomainAutostart(id, enabled)
}
func (b *KVMBackend) GetDomainAutostart(id string) (bool, error) {
	return b.lv.GetDomainAutostart(id)
}
func (b *KVMBackend) SetBootDevice(id string, device string) error {
	return b.lv.SetBootDevice(id, device)
}
func (b *KVMBackend) GetBootDevice(id string) (string, error) { return b.lv.GetBootDevice(id) }
func (b *KVMBackend) ValidateDomainDisks(id string) error {
	return b.lv.ValidateDomainDisks(id)
}
func (b *KVMBackend) GetDomainLog(id string, lines int) (string, error) {
	return b.lv.GetDomainLog(id, lines)
}
func (b *KVMBackend) ListIncusProfiles() ([]string, error) {
	// KVM has no Incus profiles.
	return []string{}, nil
}
func (b *KVMBackend) ListIncusImages() ([]models.IncusImageItem, error) {
	// KVM has no Incus images.
	return []models.IncusImageItem{}, nil
}
func (b *KVMBackend) PullIncusImage(ref string) error {
	return ErrNotImplemented
}
func (b *KVMBackend) DeleteIncusImage(fingerprint string) error {
	return ErrNotImplemented
}
func (b *KVMBackend) GetIncusImagesVolume() (string, error) {
	return "", ErrNotImplemented
}
func (b *KVMBackend) SetIncusImagesVolume(pool, volume string) error {
	return ErrNotImplemented
}

// --- Disks / devices / USB ---

func (b *KVMBackend) AttachDisk(id string, req models.AttachDiskRequest) error {
	return b.lv.AttachDisk(id, req)
}
func (b *KVMBackend) DetachDisk(id, target string) error { return b.lv.DetachDisk(id, target) }
func (b *KVMBackend) ChangeDiskBus(id, target, newBus string) error {
	return b.lv.ChangeDiskBus(id, target, newBus)
}
func (b *KVMBackend) UpdateDiskSource(id, target, source string) error {
	return b.lv.UpdateDiskSource(id, target, source)
}
func (b *KVMBackend) ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error) {
	return b.lv.ResizeDomainDisk(ctx, id, target, newSizeGB)
}
func (b *KVMBackend) RunCloudInitReprovision(id string) error {
	return b.lv.RunCloudInitReprovision(id)
}
func (b *KVMBackend) AttachNetworkIface(id string, req models.AttachNetRequest) error {
	return b.lv.AttachNetworkIface(id, req)
}
func (b *KVMBackend) DetachNetworkIface(id, mac string) error {
	return b.lv.DetachNetworkIface(id, mac)
}
func (b *KVMBackend) UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error {
	return b.lv.UpdateNetworkIface(id, oldMAC, req)
}
func (b *KVMBackend) AttachUSBDevice(id, vendorID, productID string) error {
	return b.lv.AttachUSBDevice(id, vendorID, productID)
}
func (b *KVMBackend) DetachUSBDevice(id, vendorID, productID string) error {
	return b.lv.DetachUSBDevice(id, vendorID, productID)
}
func (b *KVMBackend) ListHostUSBDevices() ([]models.USBDevice, error) {
	return b.lv.ListHostUSBDevices()
}
func (b *KVMBackend) AttachPCIDevice(id string, addresses []string) error {
	return b.lv.AttachPCIDevice(id, addresses)
}
func (b *KVMBackend) DetachPCIDevice(id, address string) error {
	return b.lv.DetachPCIDevice(id, address)
}
func (b *KVMBackend) ListHostPCIDevices() ([]models.PCIIOMMUGroup, error) {
	return b.lv.ListHostPCIDevices()
}
func (b *KVMBackend) GetHostPCIPreflight() (models.PCIPreflightInfo, error) {
	return b.lv.GetHostPCIPreflight()
}
func (b *KVMBackend) AttachSharedFolder(id, hostPath, tag string, readOnly bool) error {
	return b.lv.AttachSharedFolder(id, hostPath, tag, readOnly)
}
func (b *KVMBackend) DetachSharedFolder(id, tag string) error {
	return b.lv.DetachSharedFolder(id, tag)
}

// --- Snapshots ---

func (b *KVMBackend) ListSnapshots(domainID string) ([]models.Snapshot, error) {
	return b.lv.ListSnapshots(domainID)
}
func (b *KVMBackend) CreateSnapshot(domainID string, req models.CreateSnapshotRequest) (models.Snapshot, error) {
	s, err := b.lv.CreateSnapshot(domainID, req)
	return s, kvmErr(err)
}
func (b *KVMBackend) DeleteSnapshot(domainID, snapID string) (int64, error) {
	return b.lv.DeleteSnapshot(domainID, snapID)
}
func (b *KVMBackend) RevertSnapshot(domainID, snapID string) error {
	return b.lv.RevertSnapshot(domainID, snapID)
}
func (b *KVMBackend) ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error) {
	return b.lv.ExportSnapshots(domainID)
}

// --- Storage / pools / volumes / ISO ---

func (b *KVMBackend) ListStoragePools() ([]models.StoragePool, error) {
	return b.lv.ListStoragePools()
}
func (b *KVMBackend) CreateStoragePool(ctx context.Context, req models.CreatePoolRequest) (models.StoragePool, error) {
	return b.lv.CreateStoragePool(ctx, req)
}
func (b *KVMBackend) UpdateStoragePool(ctx context.Context, name string, req models.UpdatePoolRequest) (models.StoragePool, error) {
	return b.lv.UpdateStoragePool(ctx, name, req)
}
func (b *KVMBackend) DeletePool(name string) error            { return b.lv.DeletePool(name) }
func (b *KVMBackend) RefreshPool(name string) error           { return b.lv.RefreshPool(name) }
func (b *KVMBackend) GetPoolPath(name string) (string, error) { return b.lv.GetPoolPath(name) }
func (b *KVMBackend) DiskPoolName() string                    { return b.lv.DiskPoolName() }
func (b *KVMBackend) ISOPoolName() string                     { return b.lv.ISOPoolName() }
func (b *KVMBackend) ListStorageVolumes(poolName string) ([]models.StorageVolume, error) {
	return b.lv.ListStorageVolumes(poolName)
}
func (b *KVMBackend) GetStorageVolume(poolName, volName string) (models.StorageVolume, error) {
	v, err := b.lv.GetStorageVolume(poolName, volName)
	if err != nil {
		if errors.Is(err, libvirt.ErrVolumeNotFound) {
			return models.StorageVolume{}, fmt.Errorf("%w: %v", ErrVolumeNotFound, err)
		}
		return models.StorageVolume{}, err
	}
	return v, nil
}
func (b *KVMBackend) CreateStorageVolume(req models.CreateVolumeRequest) (models.StorageVolume, error) {
	return b.lv.CreateStorageVolume(req)
}
func (b *KVMBackend) ResizeStorageVolume(poolName, volName string, newSizeGB int64) error {
	if err := b.lv.ResizeStorageVolume(poolName, volName, newSizeGB); err != nil {
		if errors.Is(err, libvirt.ErrVolumeNotFound) {
			return fmt.Errorf("%w: %v", ErrVolumeNotFound, err)
		}
		return err
	}
	return nil
}
func (b *KVMBackend) DeleteStorageVolume(poolName, volName string) error {
	if err := b.lv.DeleteStorageVolume(poolName, volName); err != nil {
		if errors.Is(err, libvirt.ErrVolumeNotFound) {
			return fmt.Errorf("%w: %v", ErrVolumeNotFound, err)
		}
		return err
	}
	return nil
}
func (b *KVMBackend) VolumeExists(poolName, volName string) (bool, error) {
	return b.lv.VolumeExists(poolName, volName)
}
func (b *KVMBackend) FindVolumeAttachments(poolName, volName string) ([]models.VolumeAttachment, error) {
	return b.lv.FindVolumeAttachments(poolName, volName)
}

func (b *KVMBackend) BackingDependents(poolName, volName string) ([]string, error) {
	return b.lv.BackingDependents(poolName, volName)
}

func (b *KVMBackend) BackingDependentsOfPath(path string) ([]string, error) {
	return b.lv.BackingDependentsOfPath(path)
}
func (b *KVMBackend) GetISOs(poolName string) ([]models.ISOScanResult, error) {
	return b.lv.GetISOs(poolName)
}
func (b *KVMBackend) RenameISO(oldName, newName, poolName string) error {
	if err := b.lv.RenameISO(oldName, newName, poolName); err != nil {
		if errors.Is(err, libvirt.ErrVolumeNotFound) {
			return fmt.Errorf("%w: %v", ErrVolumeNotFound, err)
		}
		return err
	}
	return nil
}
func (b *KVMBackend) DeleteISO(name, poolName string) error {
	if err := b.lv.DeleteISO(name, poolName); err != nil {
		if errors.Is(err, libvirt.ErrVolumeNotFound) {
			return fmt.Errorf("%w: %v", ErrVolumeNotFound, err)
		}
		return err
	}
	return nil
}
func (b *KVMBackend) DeleteVMDiskFiles(vmName string, exact ...string) (deleted []string, skipped []string, err error) {
	return b.lv.DeleteVMDiskFiles(vmName, exact...)
}
func (b *KVMBackend) MoveDomainStorage(id, destPool string, onProgress func(pct float64, stage string)) error {
	return kvmErr(b.lv.MoveDomainStorage(id, destPool, onProgress))
}
func (b *KVMBackend) MoveVolume(srcPool, volName, destPool string, opts MoveVolumeOpts) error {
	return kvmErr(b.lv.MoveVolume(srcPool, volName, destPool, libvirt.MoveVolumeOpts{
		Kind:       opts.Kind,
		NewName:    opts.NewName,
		KeepSource: opts.KeepSource,
		OnProgress: opts.OnProgress,
	}))
}
func (b *KVMBackend) RefreshCIFSSecretIfNeeded(ctx context.Context, poolName string) (*SecretRef, error) {
	ref, err := b.lv.RefreshCIFSSecretIfNeeded(ctx, poolName)
	if err != nil || ref == nil {
		return nil, err
	}
	return &SecretRef{PoolName: ref.PoolName, SecretUUID: ref.SecretUUID, CreatedAt: ref.CreatedAt, LastUsedAt: ref.LastUsedAt}, nil
}

// --- Networking ---

func (b *KVMBackend) ListNetworks() ([]models.Network, error) { return b.lv.ListNetworks() }
func (b *KVMBackend) CreateNetwork(req models.CreateNetworkRequest) (models.Network, error) {
	return b.lv.CreateNetwork(req)
}
func (b *KVMBackend) UpdateNetwork(name string, req models.UpdateNetworkRequest) (models.Network, error) {
	net, err := b.lv.UpdateNetwork(name, req)
	if err != nil {
		if errors.Is(err, libvirt.ErrNetworkNotFound) {
			return models.Network{}, fmt.Errorf("%w: %v", ErrNetworkNotFound, err)
		}
		return models.Network{}, err
	}
	return net, nil
}
func (b *KVMBackend) DeleteNetwork(id string) error {
	if err := b.lv.DeleteNetwork(id); err != nil {
		if errors.Is(err, libvirt.ErrNetworkInUse) {
			return fmt.Errorf("%w: %v", ErrNetworkInUse, err)
		}
		if errors.Is(err, libvirt.ErrNetworkNotFound) {
			return fmt.Errorf("%w: %v", ErrNetworkNotFound, err)
		}
		return err
	}
	return nil
}
func (b *KVMBackend) StartNetwork(name string) (models.Network, error) {
	net, err := b.lv.StartNetwork(name)
	if err != nil {
		if errors.Is(err, libvirt.ErrNetworkNotFound) {
			return models.Network{}, fmt.Errorf("%w: %v", ErrNetworkNotFound, err)
		}
		return models.Network{}, err
	}
	return net, nil
}
func (b *KVMBackend) StopNetwork(name string) (models.Network, error) {
	net, err := b.lv.StopNetwork(name)
	if err != nil {
		if errors.Is(err, libvirt.ErrNetworkNotFound) {
			return models.Network{}, fmt.Errorf("%w: %v", ErrNetworkNotFound, err)
		}
		return models.Network{}, err
	}
	return net, nil
}
func (b *KVMBackend) CheckVLANSupport(networkName string) (models.VlanSupport, error) {
	return b.lv.CheckVLANSupport(networkName)
}
func (b *KVMBackend) NetworkLeases(name string) ([]models.DHCPLease, error) {
	leases, err := b.lv.NetworkLeases(name)
	if err != nil {
		if errors.Is(err, libvirt.ErrNetworkNotFound) {
			return nil, fmt.Errorf("%w: %v", ErrNetworkNotFound, err)
		}
		return nil, err
	}
	return leases, nil
}
func (b *KVMBackend) ReleaseNetworkLease(br, ip, mac string) error {
	if err := b.lv.ReleaseNetworkLease(br, ip, mac); err != nil {
		if errors.Is(err, libvirt.ErrLeaseNotFound) {
			return fmt.Errorf("%w: %v", ErrLeaseNotFound, err)
		}
		if errors.Is(err, libvirt.ErrNetworkNotFound) {
			return fmt.Errorf("%w: %v", ErrNetworkNotFound, err)
		}
		return err
	}
	return nil
}

// --- Console / cloud-init / metadata ---

// lvConsoleStream adapts a libvirt Stream to the neutral ConsoleStream
// so the serial-console handler never touches libvirt types.
type lvConsoleStream struct{ st *lvraw.Stream }

func (s *lvConsoleStream) Recv(buf []byte) (int, error) { return s.st.Recv(buf) }
func (s *lvConsoleStream) Send(b []byte) (int, error)   { return s.st.Send(b) }
func (s *lvConsoleStream) Finish() error                { return s.st.Finish() }
func (s *lvConsoleStream) Free()                        { s.st.Free() }

func (b *KVMBackend) OpenSerialConsole(id string) (ConsoleStream, error) {
	dom, st, err := b.lv.OpenSerialConsole(id)
	if dom != nil {
		dom.Free()
	}
	if err != nil {
		return nil, kvmErr(err)
	}
	return &lvConsoleStream{st: st}, nil
}
func (b *KVMBackend) SetUserPassword(id, user, password string) error {
	return b.lv.SetUserPassword(id, user, password)
}
func (b *KVMBackend) GetVMMeta(uuid string) (models.VMMeta, error) { return b.lv.GetVMMeta(uuid) }
func (b *KVMBackend) SetVMMeta(uuid string, meta models.VMMeta) error {
	return b.lv.SetVMMeta(uuid, meta)
}
func (b *KVMBackend) UpdateVMMeta(uuid string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	return b.lv.UpdateVMMeta(uuid, upd)
}
func (b *KVMBackend) GetVNCInfo(id string) (GraphicsInfo, error) {
	gi, err := b.lv.GetVNCInfo(id)
	return GraphicsInfo{Type: gi.Type, Port: gi.Port, WebSocket: gi.WebSocket, Host: gi.Host}, err
}
func (b *KVMBackend) GetDomainIP(id string) string { return b.lv.GetDomainIP(id) }
func (b *KVMBackend) GetDomainXML(id string) (string, error) {
	return b.lv.GetDomainXML(id)
}
func (b *KVMBackend) GuestGetClipboard(id string) (string, error) {
	return b.lv.GuestGetClipboard(id)
}
func (b *KVMBackend) GetGuestInfo(id string) (GuestInfo, error) {
	gi, err := b.lv.GetGuestInfo(id)
	if err != nil {
		return GuestInfo{}, err
	}
	out := GuestInfo{
		Available: gi.Available,
		Error:     gi.Error,
		Hostname:  gi.Hostname,
	}
	if gi.OS != nil {
		out.OS = &GuestOSInfo{
			ID: gi.OS.ID, Name: gi.OS.Name, PrettyName: gi.OS.PrettyName,
			Version: gi.OS.Version, VersionID: gi.OS.VersionID,
			KernelRelease: gi.OS.KernelRelease, KernelVersion: gi.OS.KernelVersion,
			Machine: gi.OS.Machine,
		}
	}
	for _, f := range gi.Filesystems {
		out.Filesystems = append(out.Filesystems, GuestFilesystem{
			Name: f.Name, Mountpoint: f.Mountpoint, Type: f.Type,
			TotalBytes: f.TotalBytes, UsedBytes: f.UsedBytes, UsedPct: f.UsedPct,
		})
	}
	for _, n := range gi.Interfaces {
		out.Interfaces = append(out.Interfaces, GuestNetworkInterface{
			Name: n.Name, MAC: n.MAC, IPv4: n.IPv4, IPv6: n.IPv6,
		})
	}
	for _, u := range gi.Users {
		out.Users = append(out.Users, GuestUser{
			User:      u.User,
			LoginTime: u.LoginTime,
			Domain:    u.Domain,
		})
	}
	if gi.Timezone != nil {
		out.Timezone = &GuestTimezone{
			Zone:   gi.Timezone.Zone,
			Offset: gi.Timezone.Offset,
		}
	}
	return out, nil
}

func (b *KVMBackend) FSTrim(id string) (GuestFSTrimResult, error) {
	res, err := b.lv.FSTrim(id)
	if err != nil {
		return GuestFSTrimResult{}, err
	}
	out := GuestFSTrimResult{}
	for _, p := range res.Paths {
		out.Paths = append(out.Paths, GuestTrimmedPath{
			Path:    p.Path,
			Trimmed: p.Trimmed,
			Minimum: p.Minimum,
			Error:   p.Error,
		})
	}
	return out, nil
}

func (b *KVMBackend) GuestSetClipboard(id, text string) error {
	return b.lv.GuestSetClipboard(id, text)
}

// --- Backup / export / OVA / import ---

func (b *KVMBackend) ExportDomain(ctx context.Context, id string, opts ExportBackupOptions, w io.Writer) (backupstore.ProducerResult, error) {
	return b.lv.ExportDomain(ctx, id, libvirt.ExportBackupOptions{
		Compress:    opts.Compress,
		ZstdLevel:   opts.ZstdLevel,
		RepackDisks: opts.RepackDisks,
	}, w)
}
func (b *KVMBackend) ExportDomainOVA(ctx context.Context, id string, opts OVAOptions, w io.Writer) error {
	return b.lv.ExportDomainOVA(ctx, id, libvirt.OVAOptions{
		Target:    libvirt.OVATarget(opts.Target),
		Compress:  libvirt.OVACompress(opts.Compress),
		ZstdLevel: opts.ZstdLevel,
	}, w)
}
func (b *KVMBackend) EstimateExportSize(ctx context.Context, id string, compress bool) (int64, error) {
	return b.lv.EstimateExportSize(ctx, id, compress)
}
func (b *KVMBackend) EstimateOVASize(ctx context.Context, id string, target OVATarget) (int64, error) {
	return b.lv.EstimateOVASize(ctx, id, libvirt.OVATarget(target))
}
func (b *KVMBackend) ImportDomain(tarPath, newName, poolName string, opts ImportOpts) (string, string, []string, error) {
	return b.lv.ImportDomain(tarPath, newName, poolName, libvirt.ImportOpts{
		Network:    opts.Network,
		VCPUs:      opts.VCPUs,
		RAMMB:      opts.RAMMB,
		Autostart:  opts.Autostart,
		SourceSize: opts.SourceSize,
		OnProgress: opts.OnProgress,
	})
}
func (b *KVMBackend) ImportOVA(ovaPath, newName, poolName string) (string, string, error) {
	return b.lv.ImportOVA(ovaPath, newName, poolName)
}

// Capabilities reports the KVM feature set (everything is supported).
func (b *KVMBackend) Capabilities() Capabilities {
	return Capabilities{
		SupportsOVA:             true,
		SupportsSnapshots:       true,
		SupportsVNC:             true,
		SupportsSerialConsole:   true,
		SupportsUSB:             true,
		SupportsNoCloudISO:      true,
		SupportsQemuGuestAgent:  true,
		SupportsNftPortForwards: true,
	}
}
