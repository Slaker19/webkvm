package compute

import (
	"context"
	"errors"
	"io"

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
	return b.lv.UpdateDomain(id, req)
}
func (b *KVMBackend) DeleteDomain(id string) error { return kvmErr(b.lv.DeleteDomain(id)) }
func (b *KVMBackend) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	return b.lv.CloneDomain(id, req)
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
	return b.lv.GetStorageVolume(poolName, volName)
}
func (b *KVMBackend) CreateStorageVolume(req models.CreateVolumeRequest) (models.StorageVolume, error) {
	return b.lv.CreateStorageVolume(req)
}
func (b *KVMBackend) ResizeStorageVolume(poolName, volName string, newSizeGB int64) error {
	return b.lv.ResizeStorageVolume(poolName, volName, newSizeGB)
}
func (b *KVMBackend) DeleteStorageVolume(poolName, volName string) error {
	return b.lv.DeleteStorageVolume(poolName, volName)
}
func (b *KVMBackend) VolumeExists(poolName, volName string) (bool, error) {
	return b.lv.VolumeExists(poolName, volName)
}
func (b *KVMBackend) FindVolumeAttachments(poolName, volName string) ([]models.VolumeAttachment, error) {
	return b.lv.FindVolumeAttachments(poolName, volName)
}
func (b *KVMBackend) GetISOs(poolName string) ([]models.ISOScanResult, error) {
	return b.lv.GetISOs(poolName)
}
func (b *KVMBackend) RenameISO(oldName, newName, poolName string) error {
	return b.lv.RenameISO(oldName, newName, poolName)
}
func (b *KVMBackend) DeleteISO(name, poolName string) error { return b.lv.DeleteISO(name, poolName) }
func (b *KVMBackend) DeleteVMDiskFiles(vmName string) (deleted []string, skipped []string, err error) {
	return b.lv.DeleteVMDiskFiles(vmName)
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
	return b.lv.UpdateNetwork(name, req)
}
func (b *KVMBackend) DeleteNetwork(id string) error { return b.lv.DeleteNetwork(id) }
func (b *KVMBackend) StartNetwork(name string) (models.Network, error) {
	return b.lv.StartNetwork(name)
}
func (b *KVMBackend) StopNetwork(name string) (models.Network, error) {
	return b.lv.StopNetwork(name)
}
func (b *KVMBackend) CheckVLANSupport(networkName string) (models.VlanSupport, error) {
	return b.lv.CheckVLANSupport(networkName)
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
