// Package lxd is the LXD container backend (v1.4 Fase 1).
//
// LXDBackend implements the full compute.Backend seam against the LXD
// daemon via the official Canonical Go client (github.com/canonical/lxd
// /client). Connection targets the local unix socket — the snap path
// /var/snap/lxd/common/lxd/unix.socket by default.
//
// Implementation is GRADUAL and fail-safe: Fase 1 implements the
// read path (ListVMs) so containers appear alongside VMs in the API;
// every other operation returns compute.ErrNotImplemented, which the
// handlers surface as HTTP 501 Not Implemented. As features land they
// replace the stub one at a time without touching the seam or the
// handlers.
package lxd

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	lxd "github.com/canonical/lxd/client"
	"github.com/canonical/lxd/shared/api"

	"webkvm/internal/backupstore"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// DefaultSocketPath is the default LXD daemon unix socket (snap install
// path, with the apt path as fallback).
func DefaultSocketPath() string {
	snap := "/var/snap/lxd/common/lxd/unix.socket"
	if st, err := os.Stat(snap); err == nil && !st.IsDir() {
		return snap
	}
	return "/var/lib/lxd/unix.socket"
}

// LXDBackend is the compute.Backend adapter for the LXD daemon.
type LXDBackend struct {
	client lxd.InstanceServer
}

// NewLXDBackend connects to the LXD daemon over a unix socket. An empty
// path uses DefaultSocketPath(). Returns an error (including
// compute.ErrNotImplemented if LXD is unreachable) when the daemon
// cannot be reached, so the caller can degrade to KVM-only.
func NewLXDBackend(socketPath string) (*LXDBackend, error) {
	if socketPath == "" {
		socketPath = DefaultSocketPath()
	}
	client, err := lxd.ConnectLXDUnix(socketPath, &lxd.ConnectionArgs{})
	if err != nil {
		return nil, err
	}
	return &LXDBackend{client: client}, nil
}

// ServerInfo returns the LXD daemon version for the status page.
func (b *LXDBackend) ServerInfo() (string, error) {
	s, _, err := b.client.GetServer()
	if err != nil {
		return "", err
	}
	return s.Environment.ServerVersion, nil
}

// Close closes the underlying client connections.
func (b *LXDBackend) Close() {
	b.client.Disconnect()
}

// --- Instance lifecycle ---

// ListVMs lists every LXD instance (containers + VMs) and maps them to
// the neutral domain model. This is the Fase 1 MVP: it makes containers
// appear in the unified VM list.
func (b *LXDBackend) ListDomains() ([]models.VM, error) {
	instances, err := b.client.GetInstances(lxd.GetInstancesArgs{InstanceType: api.InstanceTypeAny})
	if err != nil {
		return nil, err
	}
	out := make([]models.VM, 0, len(instances))
	for i := range instances {
		out = append(out, instanceToVM(&instances[i]))
	}
	return out, nil
}

func (b *LXDBackend) GetDomain(id string) (models.VM, error) {
	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return models.VM{}, err
	}
	return instanceToVM(inst), nil
}

func (b *LXDBackend) DomainExists(name string) (bool, error) {
	_, _, err := b.client.GetInstance(name)
	if err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (b *LXDBackend) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	return models.VM{}, compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateDomain(id string, req models.UpdateVMRequest) (models.VM, error) {
	return models.VM{}, compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteDomain(id string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	return models.VM{}, compute.ErrNotImplemented
}
func (b *LXDBackend) StartDomain(id string) error    { return compute.ErrNotImplemented }
func (b *LXDBackend) ShutdownDomain(id string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) ForceOffDomain(id string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) RebootDomain(id string) error   { return compute.ErrNotImplemented }
func (b *LXDBackend) SuspendDomain(id string) error  { return compute.ErrNotImplemented }
func (b *LXDBackend) ResumeDomain(id string) error   { return compute.ErrNotImplemented }
func (b *LXDBackend) SetDomainAutostart(id string, enabled bool) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) GetDomainAutostart(id string) (bool, error) {
	return false, compute.ErrNotImplemented
}
func (b *LXDBackend) SetBootDevice(id string, device string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) GetBootDevice(id string) (string, error)      { return "", compute.ErrNotImplemented }
func (b *LXDBackend) ValidateDomainDisks(id string) error          { return compute.ErrNotImplemented }

// --- Disks / devices / USB ---

func (b *LXDBackend) AttachDisk(id string, req models.AttachDiskRequest) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DetachDisk(id, target string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) ChangeDiskBus(id, target, newBus string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateDiskSource(id, target, source string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *LXDBackend) AttachNetworkIface(id string, req models.AttachNetRequest) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DetachNetworkIface(id, mac string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) AttachUSBDevice(id, vendorID, productID string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DetachUSBDevice(id, vendorID, productID string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) ListHostUSBDevices() ([]models.USBDevice, error) {
	return nil, compute.ErrNotImplemented
}

// --- Snapshots ---

func (b *LXDBackend) ListSnapshots(domainID string) ([]models.Snapshot, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) CreateSnapshot(domainID string, req models.CreateSnapshotRequest) (models.Snapshot, error) {
	return models.Snapshot{}, compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteSnapshot(domainID, snapID string) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *LXDBackend) RevertSnapshot(domainID, snapID string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error) {
	return nil, compute.ErrNotImplemented
}

// --- Storage / pools / volumes / ISO ---

func (b *LXDBackend) ListStoragePools() ([]models.StoragePool, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) CreateStoragePool(ctx context.Context, req models.CreatePoolRequest) (models.StoragePool, error) {
	return models.StoragePool{}, compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateStoragePool(ctx context.Context, name string, req models.UpdatePoolRequest) (models.StoragePool, error) {
	return models.StoragePool{}, compute.ErrNotImplemented
}
func (b *LXDBackend) DeletePool(name string) error            { return compute.ErrNotImplemented }
func (b *LXDBackend) RefreshPool(name string) error           { return compute.ErrNotImplemented }
func (b *LXDBackend) GetPoolPath(name string) (string, error) { return "", compute.ErrNotImplemented }
func (b *LXDBackend) DiskPoolName() string                    { return "" }
func (b *LXDBackend) ISOPoolName() string                     { return "" }
func (b *LXDBackend) ListStorageVolumes(poolName string) ([]models.StorageVolume, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) GetStorageVolume(poolName, volName string) (models.StorageVolume, error) {
	return models.StorageVolume{}, compute.ErrNotImplemented
}
func (b *LXDBackend) CreateStorageVolume(req models.CreateVolumeRequest) (models.StorageVolume, error) {
	return models.StorageVolume{}, compute.ErrNotImplemented
}
func (b *LXDBackend) ResizeStorageVolume(poolName, volName string, newSizeGB int64) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteStorageVolume(poolName, volName string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) VolumeExists(poolName, volName string) (bool, error) {
	return false, compute.ErrNotImplemented
}
func (b *LXDBackend) FindVolumeAttachments(poolName, volName string) ([]models.VolumeAttachment, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) GetISOs(poolName string) ([]models.ISOScanResult, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) RenameISO(oldName, newName, poolName string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteISO(name, poolName string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) DeleteVMDiskFiles(vmName string) (deleted []string, skipped []string, err error) {
	return nil, nil, compute.ErrNotImplemented
}
func (b *LXDBackend) RefreshCIFSSecretIfNeeded(ctx context.Context, poolName string) (*compute.SecretRef, error) {
	return nil, compute.ErrNotImplemented
}

// --- Networking ---

func (b *LXDBackend) ListNetworks() ([]models.Network, error) { return nil, compute.ErrNotImplemented }
func (b *LXDBackend) CreateNetwork(req models.CreateNetworkRequest) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateNetwork(name string, req models.UpdateNetworkRequest) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *LXDBackend) DeleteNetwork(id string) error { return compute.ErrNotImplemented }
func (b *LXDBackend) StartNetwork(name string) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *LXDBackend) StopNetwork(name string) (models.Network, error) {
	return models.Network{}, compute.ErrNotImplemented
}
func (b *LXDBackend) CheckVLANSupport(networkName string) (models.VlanSupport, error) {
	return models.VlanSupport{}, compute.ErrNotImplemented
}

// --- Console / cloud-init / metadata ---

func (b *LXDBackend) OpenSerialConsole(id string) (compute.ConsoleStream, error) {
	return nil, compute.ErrNotImplemented
}
func (b *LXDBackend) SetUserPassword(id, user, password string) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) GetVMMeta(uuid string) (models.VMMeta, error) {
	return models.VMMeta{}, compute.ErrNotImplemented
}
func (b *LXDBackend) SetVMMeta(uuid string, meta models.VMMeta) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) UpdateVMMeta(uuid string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	return models.VMMeta{}, compute.ErrNotImplemented
}
func (b *LXDBackend) GetVNCInfo(id string) (compute.GraphicsInfo, error) {
	return compute.GraphicsInfo{}, compute.ErrNotImplemented
}
func (b *LXDBackend) GetDomainIP(id string) string { return "" }
func (b *LXDBackend) GetDomainXML(id string) (string, error) {
	return "", compute.ErrNotImplemented
}
func (b *LXDBackend) GuestGetClipboard(id string) (string, error) {
	return "", compute.ErrNotImplemented
}
func (b *LXDBackend) GuestSetClipboard(id, text string) error { return compute.ErrNotImplemented }

// --- Backup / export / OVA / import ---

func (b *LXDBackend) ExportDomain(ctx context.Context, id string, opts compute.ExportBackupOptions, w io.Writer) (backupstore.ProducerResult, error) {
	return backupstore.ProducerResult{}, compute.ErrNotImplemented
}
func (b *LXDBackend) ExportDomainOVA(ctx context.Context, id string, opts compute.OVAOptions, w io.Writer) error {
	return compute.ErrNotImplemented
}
func (b *LXDBackend) EstimateExportSize(ctx context.Context, id string, compress bool) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *LXDBackend) EstimateOVASize(ctx context.Context, id string, target compute.OVATarget) (int64, error) {
	return 0, compute.ErrNotImplemented
}
func (b *LXDBackend) ImportDomain(tarPath, newName, poolName string, opts compute.ImportOpts) (string, string, []string, error) {
	return "", "", nil, compute.ErrNotImplemented
}
func (b *LXDBackend) ImportOVA(ovaPath, newName, poolName string) (string, string, error) {
	return "", "", compute.ErrNotImplemented
}

// Capabilities reports what LXD supports (Fase 1: read-only listing).
func (b *LXDBackend) Capabilities() compute.Capabilities {
	return compute.Capabilities{}
}

// --- helpers ---

// instanceToVM maps an LXD instance to the neutral domain model. Pure
// and unit-tested (the fake-server integration test feeds real payloads).
func instanceToVM(i *api.Instance) models.VM {
	vm := models.VM{
		ID:         i.Name,
		Name:       i.Name,
		Type:       "container",
		Hypervisor: "lxd",
		State:      lxdState(i.Status),
		VCPUs:      parseIntConfig(i.Config["limits.cpu"]),
		RAMMB:      parseMemoryMB(i.Config["limits.memory"]),
		Autostart:  i.Config["boot.autostart"] == "true",
	}
	if i.Type == string(api.InstanceTypeVM) {
		vm.Type = "vm"
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
