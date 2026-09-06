package compute

import (
	"context"
	"io"
	"sync"

	"webkvm/internal/backupstore"
	"webkvm/internal/models"
)

// Combined is a compute.Backend that runs a PRIMARY backend (KVM) plus
// an optional SECONDARY backend (LXD) side by side (v1.4 Fase 1). The
// read path (ListDomains, GetDomain) merges both; every instance-scoped
// operation is ROUTED to the backend that owns the instance, so a
// container operation lands on the LXD backend (returning
// ErrNotImplemented → 501 until implemented) instead of leaking a
// primary "domain not found".
//
// The secondary is never a hard dependency: if LXD is unreachable the
// caller simply wires Combined{primary} and behaviour is identical to
// KVM-only.
type Combined struct {
	Backend   // embedded interface: non-instance ops delegate to primary
	secondary Backend

	mu     sync.Mutex
	owners map[string]Backend // instance id -> owning backend
}

// NewCombined builds the merged backend. secondary may be nil (KVM-only).
func NewCombined(primary, secondary Backend) *Combined {
	return &Combined{Backend: primary, secondary: secondary, owners: map[string]Backend{}}
}

// remember caches which backend owns an instance.
func (c *Combined) remember(id string, b Backend) {
	if b == nil {
		return
	}
	c.mu.Lock()
	c.owners[id] = b
	c.mu.Unlock()
}

// route returns the backend that owns id: a cached owner, else the
// secondary if it can resolve the instance, else the primary.
func (c *Combined) route(id string) Backend {
	c.mu.Lock()
	if b, ok := c.owners[id]; ok {
		c.mu.Unlock()
		return b
	}
	c.mu.Unlock()
	if c.secondary != nil {
		if _, err := c.secondary.GetDomain(id); err == nil {
			c.remember(id, c.secondary)
			return c.secondary
		}
	}
	return c.Backend
}

// ListDomains merges the primary's instances with the secondary's. A
// secondary failure is NON-FATAL: it must never hide KVM VMs or break
// the list if the container daemon is down mid-flight.
func (c *Combined) ListDomains() ([]models.VM, error) {
	primary, err := c.Backend.ListDomains()
	if err != nil {
		return nil, err
	}
	for i := range primary {
		c.remember(primary[i].ID, c.Backend)
	}
	if c.secondary == nil {
		return primary, nil
	}
	extra, err := c.secondary.ListDomains()
	if err != nil {
		return primary, nil // degrade gracefully
	}
	for i := range extra {
		c.remember(extra[i].ID, c.secondary)
	}
	return append(primary, extra...), nil
}

// GetDomain searches the primary first, then the secondary.
func (c *Combined) GetDomain(id string) (models.VM, error) {
	vm, err := c.Backend.GetDomain(id)
	if err == nil {
		c.remember(id, c.Backend)
		return vm, nil
	}
	if c.secondary == nil {
		return vm, err
	}
	extra, lerr := c.secondary.GetDomain(id)
	if lerr == nil {
		c.remember(id, c.secondary)
		return extra, nil
	}
	return vm, err
}

// --- Instance-scoped operations: routed to the owning backend ---

func (c *Combined) DomainExists(name string) (bool, error) {
	return c.route(name).DomainExists(name)
}
func (c *Combined) UpdateDomain(id string, req models.UpdateVMRequest) (models.VM, error) {
	return c.route(id).UpdateDomain(id, req)
}
func (c *Combined) DeleteDomain(id string) error { return c.route(id).DeleteDomain(id) }
func (c *Combined) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	return c.route(id).CloneDomain(id, req)
}
func (c *Combined) StartDomain(id string) error    { return c.route(id).StartDomain(id) }
func (c *Combined) ShutdownDomain(id string) error { return c.route(id).ShutdownDomain(id) }
func (c *Combined) ForceOffDomain(id string) error { return c.route(id).ForceOffDomain(id) }
func (c *Combined) RebootDomain(id string) error   { return c.route(id).RebootDomain(id) }
func (c *Combined) SuspendDomain(id string) error  { return c.route(id).SuspendDomain(id) }
func (c *Combined) ResumeDomain(id string) error   { return c.route(id).ResumeDomain(id) }
func (c *Combined) SetDomainAutostart(id string, enabled bool) error {
	return c.route(id).SetDomainAutostart(id, enabled)
}
func (c *Combined) GetDomainAutostart(id string) (bool, error) {
	return c.route(id).GetDomainAutostart(id)
}
func (c *Combined) SetBootDevice(id string, device string) error {
	return c.route(id).SetBootDevice(id, device)
}
func (c *Combined) GetBootDevice(id string) (string, error) {
	return c.route(id).GetBootDevice(id)
}
func (c *Combined) ValidateDomainDisks(id string) error {
	return c.route(id).ValidateDomainDisks(id)
}

func (c *Combined) AttachDisk(id string, req models.AttachDiskRequest) error {
	return c.route(id).AttachDisk(id, req)
}
func (c *Combined) DetachDisk(id, target string) error { return c.route(id).DetachDisk(id, target) }
func (c *Combined) ChangeDiskBus(id, target, newBus string) error {
	return c.route(id).ChangeDiskBus(id, target, newBus)
}
func (c *Combined) UpdateDiskSource(id, target, source string) error {
	return c.route(id).UpdateDiskSource(id, target, source)
}
func (c *Combined) ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error) {
	return c.route(id).ResizeDomainDisk(ctx, id, target, newSizeGB)
}
func (c *Combined) AttachNetworkIface(id string, req models.AttachNetRequest) error {
	return c.route(id).AttachNetworkIface(id, req)
}
func (c *Combined) DetachNetworkIface(id, mac string) error {
	return c.route(id).DetachNetworkIface(id, mac)
}
func (c *Combined) UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error {
	return c.route(id).UpdateNetworkIface(id, oldMAC, req)
}
func (c *Combined) AttachUSBDevice(id, vendorID, productID string) error {
	return c.route(id).AttachUSBDevice(id, vendorID, productID)
}
func (c *Combined) DetachUSBDevice(id, vendorID, productID string) error {
	return c.route(id).DetachUSBDevice(id, vendorID, productID)
}

func (c *Combined) ListSnapshots(domainID string) ([]models.Snapshot, error) {
	return c.route(domainID).ListSnapshots(domainID)
}
func (c *Combined) CreateSnapshot(domainID string, req models.CreateSnapshotRequest) (models.Snapshot, error) {
	return c.route(domainID).CreateSnapshot(domainID, req)
}
func (c *Combined) DeleteSnapshot(domainID, snapID string) (int64, error) {
	return c.route(domainID).DeleteSnapshot(domainID, snapID)
}
func (c *Combined) RevertSnapshot(domainID, snapID string) error {
	return c.route(domainID).RevertSnapshot(domainID, snapID)
}
func (c *Combined) ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error) {
	return c.route(domainID).ExportSnapshots(domainID)
}

func (c *Combined) GetVMMeta(uuid string) (models.VMMeta, error) {
	return c.route(uuid).GetVMMeta(uuid)
}
func (c *Combined) SetVMMeta(uuid string, meta models.VMMeta) error {
	return c.route(uuid).SetVMMeta(uuid, meta)
}
func (c *Combined) UpdateVMMeta(uuid string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	return c.route(uuid).UpdateVMMeta(uuid, upd)
}
func (c *Combined) GetVNCInfo(id string) (GraphicsInfo, error) {
	return c.route(id).GetVNCInfo(id)
}
func (c *Combined) GetDomainIP(id string) string { return c.route(id).GetDomainIP(id) }
func (c *Combined) GetDomainXML(id string) (string, error) {
	return c.route(id).GetDomainXML(id)
}
func (c *Combined) GuestGetClipboard(id string) (string, error) {
	return c.route(id).GuestGetClipboard(id)
}
func (c *Combined) GuestSetClipboard(id, text string) error {
	return c.route(id).GuestSetClipboard(id, text)
}
func (c *Combined) OpenSerialConsole(id string) (ConsoleStream, error) {
	return c.route(id).OpenSerialConsole(id)
}
func (c *Combined) SetUserPassword(id, user, password string) error {
	return c.route(id).SetUserPassword(id, user, password)
}

func (c *Combined) ExportDomain(ctx context.Context, id string, opts ExportBackupOptions, w io.Writer) (backupstore.ProducerResult, error) {
	return c.route(id).ExportDomain(ctx, id, opts, w)
}
func (c *Combined) ExportDomainOVA(ctx context.Context, id string, opts OVAOptions, w io.Writer) error {
	return c.route(id).ExportDomainOVA(ctx, id, opts, w)
}
func (c *Combined) EstimateExportSize(ctx context.Context, id string, compress bool) (int64, error) {
	return c.route(id).EstimateExportSize(ctx, id, compress)
}
func (c *Combined) EstimateOVASize(ctx context.Context, id string, target OVATarget) (int64, error) {
	return c.route(id).EstimateOVASize(ctx, id, target)
}
func (c *Combined) DeleteVMDiskFiles(vmName string) (deleted []string, skipped []string, err error) {
	return c.route(vmName).DeleteVMDiskFiles(vmName)
}
