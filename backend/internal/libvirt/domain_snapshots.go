package libvirt

import (
	"errors"
	"fmt"
	"time"

	"webkvm/internal/backupstore"
	"webkvm/internal/models"

	"libvirt.org/go/libvirt"
)

// ErrMemorySnapshotRequiresRunning is returned when a memory snapshot
// is requested for a VM that is not running.
var ErrMemorySnapshotRequiresRunning = errors.New("a memory snapshot requires the VM to be running")

// ExportSnapshots returns snapshot metadata and overlay volume
// paths for the given domain. Used by the backup runner to include
// snapshots in per-VM archives.
//
// The current snapshot's overlay volume is the active disk (which
// is already captured in the per-VM archive as a disks/ entry), so
// we skip its VolumePath to avoid duplicating it. Non-current
// snapshots have their own overlay files that are included.
func (c *Connector) ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return nil, err
	}
	defer dom.Free()

	domainName, _ := dom.GetName()
	poolName := c.diskPoolOfDomain(dom)

	// Refresh the pool so snapshot overlay volumes are visible
	// to lookupSnapshotVolume (libvirt's in-memory cache may be
	// stale if a snapshot was just created or deleted).
	_ = c.RefreshPool(poolName)

	snaps, err := dom.ListAllSnapshots(0)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	currentSnap, _ := dom.SnapshotCurrent(0)
	var currentName string
	if currentSnap != nil {
		currentName, _ = currentSnap.GetName()
		currentSnap.Free()
	}

	result := make([]backupstore.SnapshotBackup, 0, len(snaps)+1)

	// Include the root base disk (<vmname>.qcow2 or <vmname>.img)
	// if it exists in the pool and is not the current active disk.
	// The root is the backing file origin of the entire chain and is
	// needed during restore to rebuild the backing chain correctly.
	if domainName != "" {
		for _, ext := range []string{".qcow2", ".img"} {
			if vol, err := c.lookupSnapshotVolume(poolName, domainName+ext); err == nil {
				// Synthetic snapshot named "_base" holds the root disk.
				result = append(result, backupstore.SnapshotBackup{
					Name:       "_base",
					VolumePath: vol.Path,
				})
				break
			}
		}
	}

	for i := range snaps {
		name, _ := snaps[i].GetName()
		xmlDesc, _ := snaps[i].GetXMLDesc(0)
		snaps[i].Free()

		if name == "" {
			continue
		}

		snap := backupstore.SnapshotBackup{
			Name:              name,
			DomainSnapshotXML: xmlDesc,
		}

		// For non-current snapshots, locate the overlay volume.
		// The current snapshot's volume is the active disk (already
		// captured in disks/), so we skip it to avoid duplication.
		if name != currentName && domainName != "" {
			if vol, err := c.lookupSnapshotVolume(poolName, domainName+"."+name); err == nil {
				snap.VolumePath = vol.Path
			}
		}

		result = append(result, snap)
	}

	return result, nil
}

func (c *Connector) ListSnapshots(domainID string) ([]models.Snapshot, error) {
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return nil, err
	}
	defer dom.Free()

	// For each snapshot, we look up "<vmname>.<snapname>" in the disk
	// pool to read the libvirt `Allocated` (= data size when the
	// snapshot was created). The domain name is needed to build the
	// volume key; get it once up front so the per-snapshot lookup is
	// a single libvirt call.
	domainName, _ := dom.GetName()
	poolName := c.diskPoolOfDomain(dom)

	// Refresh the pool so freshly created snapshot overlays are visible
	// to lookupSnapshotVolume. Best-effort.
	_ = c.RefreshPool(poolName)

	snaps, err := dom.ListAllSnapshots(0)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	currentSnap, _ := dom.SnapshotCurrent(0)

	result := make([]models.Snapshot, 0, len(snaps))
	for i := range snaps {
		name, _ := snaps[i].GetName()
		xmlDesc, _ := snaps[i].GetXMLDesc(0)
		created := extractSnapshotCreation(xmlDesc)
		epoch := extractSnapshotEpoch(xmlDesc)
		parentName := extractSnapshotParent(xmlDesc)
		// Fall back to libvirt's GetParent if XML parsing yielded nothing.
		if parentName == "" {
			if parent, err := snaps[i].GetParent(0); err == nil && parent != nil {
				if pname, err := parent.GetName(); err == nil {
					parentName = pname
				}
				parent.Free()
			}
		}

		isCurrent := false
		if currentSnap != nil {
			curName, _ := currentSnap.GetName()
			isCurrent = curName == name
		}

		snaps[i].Free()

		// Best-effort: enrich with the data size when the snapshot
		// was created. The matching volume lives in the disk pool
		// under "<vmname>.<snapname>". If the pool isn't running
		// or the view isn't visible, the field stays 0 and the
		// frontend shows "—".
		var sizeAtSnap int64
		if domainName != "" {
			if vol, err := c.lookupSnapshotVolume(poolName, domainName+"."+name); err == nil {
				sizeAtSnap = vol.Allocated
			}
		}

		result = append(result, models.Snapshot{
			ID:              name,
			Name:            name,
			CreatedAt:       created,
			CreationTime:    epoch,
			ParentName:      parentName,
			Current:         isCurrent,
			SizeAtSnapBytes: sizeAtSnap,
		})
	}

	if currentSnap != nil {
		currentSnap.Free()
	}

	return result, nil
}

func (c *Connector) CreateSnapshot(domainID string, req models.CreateSnapshotRequest) (models.Snapshot, error) {
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return models.Snapshot{}, err
	}
	defer dom.Free()

	xmlStr := fmt.Sprintf(`<domainsnapshot>
  <name>%s</name>
  <description>%s</description>
</domainsnapshot>`, xmlEscape(req.Name), xmlEscape(req.Description))

	if req.Memory {
		// Full snapshot: disk + RAM. Requires the VM to be running —
		// libvirt would otherwise create a disk-only snapshot silently,
		// which is misleading. Fail fast with a clear message instead.
		state, _, err := dom.GetState()
		if err != nil {
			return models.Snapshot{}, fmt.Errorf("get domain state: %w", err)
		}
		if state != libvirt.DOMAIN_RUNNING {
			return models.Snapshot{}, ErrMemorySnapshotRequiresRunning
		}
		if _, err := dom.CreateSnapshotXML(xmlStr, 0); err != nil {
			return models.Snapshot{}, fmt.Errorf("create memory snapshot: %w", err)
		}
	} else {
		// Disk-only. If the VM is running, try to quiesce the
		// filesystem via the guest agent first (fsfreeze/fsthaw,
		// handled atomically by libvirt itself) for a
		// filesystem-consistent snapshot instead of merely
		// crash-consistent. Best-effort: VMs without a responsive
		// guest agent fall back to the existing behavior.
		state, _, _ := dom.GetState()
		var err error
		if state == libvirt.DOMAIN_RUNNING {
			_, err = dom.CreateSnapshotXML(xmlStr, libvirt.DOMAIN_SNAPSHOT_CREATE_DISK_ONLY|libvirt.DOMAIN_SNAPSHOT_CREATE_QUIESCE)
		} else {
			err = fmt.Errorf("not running")
		}
		if err != nil {
			// Fall back: disk-only without quiesce, then a full
			// snapshot if disk-only itself isn't supported.
			_, err = dom.CreateSnapshotXML(xmlStr, libvirt.DOMAIN_SNAPSHOT_CREATE_DISK_ONLY)
			if err != nil {
				_, err = dom.CreateSnapshotXML(xmlStr, 0)
				if err != nil {
					return models.Snapshot{}, fmt.Errorf("create snapshot: %w", err)
				}
			}
		}
	}

	snap := models.Snapshot{
		ID:          req.Name,
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	// Best-effort: enrich the response with the data size at
	// creation (libvirt `Allocated` on the new volume view).
	if name, _ := dom.GetName(); name != "" {
		poolName := c.diskPoolOfDomain(dom)
		_ = c.RefreshPool(poolName)
		if vol, err := c.lookupSnapshotVolume(poolName, name+"."+req.Name); err == nil {
			snap.SizeAtSnapBytes = vol.Allocated
		}
	}
	return snap, nil
}

// DeleteSnapshot removes a snapshot and returns the libvirt
// "Allocated" of the volume that backed it, so the caller can record
// the freed space in the audit log. Returns 0 when the volume wasn't
// visible (e.g. pool inactive) — the snapshot was still removed.
func (c *Connector) DeleteSnapshot(domainID, snapID string) (int64, error) {
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return 0, err
	}
	defer dom.Free()

	var allocated int64
	if name, _ := dom.GetName(); name != "" {
		poolName := c.diskPoolOfDomain(dom)
		_ = c.RefreshPool(poolName)
		if vol, err := c.lookupSnapshotVolume(poolName, name+"."+snapID); err == nil {
			allocated = vol.Allocated
		}
	}

	snap, err := dom.SnapshotLookupByName(snapID, 0)
	if err != nil {
		return 0, fmt.Errorf("lookup snapshot: %w", err)
	}
	defer snap.Free()

	if err := deleteSnapshotRecursive(snap); err != nil {
		return 0, err
	}
	return allocated, nil
}

func deleteSnapshotRecursive(snap *libvirt.DomainSnapshot) error {
	children, err := snap.ListAllChildren(0)
	if err != nil {
		return fmt.Errorf("list children: %w", err)
	}
	for i := range children {
		if err := deleteSnapshotRecursive(&children[i]); err != nil {
			for j := i; j < len(children); j++ {
				children[j].Free()
			}
			return err
		}
		children[i].Free()
	}
	return snap.Delete(0)
}

func (c *Connector) RevertSnapshot(domainID, snapID string) error {
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return err
	}
	defer dom.Free()

	snap, err := dom.SnapshotLookupByName(snapID, 0)
	if err != nil {
		return fmt.Errorf("lookup snapshot: %w", err)
	}
	defer snap.Free()

	return snap.RevertToSnapshot(0)
}
