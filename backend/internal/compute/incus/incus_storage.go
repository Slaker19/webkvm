package incus

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/compute"
	"webkvm/internal/fsutil"
	"webkvm/internal/models"
)

// --- Storage / pools / volumes / ISO ---

func (b *IncusBackend) ListStoragePools() ([]models.StoragePool, error) {
	if b.client == nil {
		return nil, compute.ErrNotImplemented
	}
	pools, err := b.client.GetStoragePools()
	if err != nil {
		return nil, err
	}
	result := make([]models.StoragePool, 0, len(pools))
	for _, p := range pools {
		sp := models.StoragePool{
			Name:      p.Name,
			Type:      p.Driver,
			Purpose:   "container",
			State:     strings.ToLower(p.Status),
			Autostart: true,
		}
		if sp.State == "" {
			sp.State = "active"
		}
		if src, ok := p.Config["source"]; ok && src != "" {
			sp.Path = src
		} else {
			sp.Path = "/var/lib/incus/storage-pools/" + p.Name
		}
		if res, err := b.client.GetStoragePoolResources(p.Name); err == nil && res != nil {
			sp.Capacity = int64(res.Space.Total)
			sp.Allocated = int64(res.Space.Used)
			sp.Available = int64(res.Space.Total - res.Space.Used)
		}
		sp.DeviceID = fsutil.DeviceID(sp.Path)
		result = append(result, sp)
	}
	return result, nil
}
func (b *IncusBackend) CreateStoragePool(ctx context.Context, req models.CreatePoolRequest) (models.StoragePool, error) {
	if b.client == nil {
		return models.StoragePool{}, compute.ErrNotImplemented
	}
	driver := strings.TrimPrefix(strings.TrimSpace(req.Type), "incus-")
	if driver == "" {
		driver = "dir"
	}

	configMap := make(map[string]string)
	if req.Path != "" {
		if strings.Contains(req.Path, "..") {
			return models.StoragePool{}, fmt.Errorf("invalid pool path %q: traversal not allowed", req.Path)
		}
		if driver == "dir" {
			if err := os.MkdirAll(req.Path, 0755); err != nil {
				return models.StoragePool{}, fmt.Errorf("create pool directory %q: %w", req.Path, err)
			}
		}
		configMap["source"] = req.Path
	}

	post := api.StoragePoolsPost{
		Name:   req.Name,
		Driver: driver,
		StoragePoolPut: api.StoragePoolPut{
			Config: configMap,
		},
	}

	if err := b.client.CreateStoragePool(post); err != nil {
		return models.StoragePool{}, err
	}

	pool, _, err := b.client.GetStoragePool(req.Name)
	if err != nil {
		return models.StoragePool{
			Name:     req.Name,
			Type:     driver,
			Purpose:  compute.PoolPurposeContainer,
			State:    "active",
			Path:     req.Path,
			DeviceID: fsutil.DeviceID(req.Path),
		}, nil
	}

	sp := models.StoragePool{
		Name:      pool.Name,
		Type:      pool.Driver,
		Purpose:   compute.PoolPurposeContainer,
		State:     strings.ToLower(pool.Status),
		Autostart: true,
	}
	if sp.State == "" {
		sp.State = "active"
	}
	if src, ok := pool.Config["source"]; ok && src != "" {
		sp.Path = src
	} else {
		sp.Path = "/var/lib/incus/storage-pools/" + pool.Name
	}
	if res, err := b.client.GetStoragePoolResources(pool.Name); err == nil && res != nil {
		sp.Capacity = int64(res.Space.Total)
		sp.Allocated = int64(res.Space.Used)
		sp.Available = int64(res.Space.Total - res.Space.Used)
	}
	sp.DeviceID = fsutil.DeviceID(sp.Path)
	return sp, nil
}
func (b *IncusBackend) UpdateStoragePool(ctx context.Context, name string, req models.UpdatePoolRequest) (models.StoragePool, error) {
	return models.StoragePool{}, compute.ErrNotImplemented
}
func (b *IncusBackend) DeletePool(name string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	return b.client.DeleteStoragePool(name)
}
func (b *IncusBackend) RefreshPool(name string) error { return compute.ErrNotImplemented }
func (b *IncusBackend) GetPoolPath(name string) (string, error) {
	if b.client == nil {
		return "", compute.ErrNotImplemented
	}
	pool, _, err := b.client.GetStoragePool(name)
	if err != nil {
		return "", err
	}
	// Return the source path from pool config, or the default path
	if src, ok := pool.Config["source"]; ok && src != "" {
		return src, nil
	}
	return "/var/lib/incus/storage-pools/" + name, nil
}
func (b *IncusBackend) DiskPoolName() string { return "" }
func (b *IncusBackend) ISOPoolName() string  { return "" }
func (b *IncusBackend) ListStorageVolumes(poolName string) ([]models.StorageVolume, error) {
	if b.client == nil {
		return nil, compute.ErrNotImplemented
	}
	// Try GetStoragePoolVolumesFull first
	volsFull, err := b.client.GetStoragePoolVolumesFull(poolName)
	if err == nil {
		result := make([]models.StorageVolume, 0, len(volsFull))
		for _, v := range volsFull {
			format := v.ContentType
			if format == "" {
				format = v.Type
			}
			path := "/var/lib/incus/storage-pools/" + poolName + "/" + v.Type + "s/" + v.Name
			var capBytes, allocBytes int64
			if v.State != nil && v.State.Usage != nil {
				capBytes = v.State.Usage.Total
				allocBytes = int64(v.State.Usage.Used)
			}
			result = append(result, models.StorageVolume{
				Name:      v.Name,
				Pool:      poolName,
				Path:      path,
				Format:    format,
				Capacity:  capBytes,
				Allocated: allocBytes,
			})
		}
		return result, nil
	}

	// Fallback to GetStoragePoolVolumes
	vols, err := b.client.GetStoragePoolVolumes(poolName)
	if err != nil {
		return nil, err
	}
	result := make([]models.StorageVolume, 0, len(vols))
	for _, v := range vols {
		format := v.ContentType
		if format == "" {
			format = v.Type
		}
		path := "/var/lib/incus/storage-pools/" + poolName + "/" + v.Type + "s/" + v.Name
		var capBytes, allocBytes int64
		if st, err := b.client.GetStoragePoolVolumeState(poolName, v.Type, v.Name); err == nil && st != nil && st.Usage != nil {
			capBytes = st.Usage.Total
			allocBytes = int64(st.Usage.Used)
		}
		result = append(result, models.StorageVolume{
			Name:      v.Name,
			Pool:      poolName,
			Path:      path,
			Format:    format,
			Capacity:  capBytes,
			Allocated: allocBytes,
		})
	}
	return result, nil
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
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	if err := b.client.DeleteStoragePoolVolume(poolName, "custom", volName); err == nil {
		return nil
	}
	return b.client.DeleteStoragePoolVolume(poolName, "container", volName)
}
func (b *IncusBackend) DeleteIncusImage(fingerprint string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	op, err := b.client.DeleteImage(fingerprint)
	if err != nil {
		return err
	}
	return op.Wait()
}

// GetIncusImagesVolume reports where Incus caches downloaded images.
//
// An empty answer means the daemon default, /var/lib/incus/images,
// which sits on the system disk: on a host whose storage was
// deliberately put elsewhere, that is a cache growing on the one disk
// the operator was trying to keep free, and nothing in the UI said so.
func (b *IncusBackend) GetIncusImagesVolume() (string, error) {
	if b.client == nil {
		return "", compute.ErrNotImplemented
	}
	s, _, err := b.client.GetServer()
	if err != nil {
		return "", err
	}
	if s.Config == nil {
		return "", nil
	}
	return s.Config["storage.images_volume"], nil
}

// SetIncusImagesVolume repoints the image cache at a custom volume.
//
// Incus moves the existing cache itself when the key changes, so this
// does not copy anything — but it validates hard first, because the
// daemon rejects the key outright if the pool or volume is wrong and
// the error it returns is not one an operator can act on.
//
// An empty pool clears the key and restores the default location.
func (b *IncusBackend) SetIncusImagesVolume(pool, volume string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	s, etag, err := b.client.GetServer()
	if err != nil {
		return err
	}
	cfg := make(map[string]string, len(s.Config)+1)
	for k, v := range s.Config {
		cfg[k] = v
	}

	if pool == "" {
		delete(cfg, "storage.images_volume")
	} else {
		if volume == "" {
			volume = "images"
		}
		// The pool has to exist before the volume can, and a missing
		// pool is the mistake worth naming: the daemon's own error for
		// it does not say which pool it could not find.
		if _, _, err := b.client.GetStoragePool(pool); err != nil {
			return fmt.Errorf("storage pool %q not found in Incus: %w", pool, err)
		}
		// Create the volume only when it is not already there, so
		// re-pointing at a volume that already holds the cache is not
		// an error.
		if _, _, err := b.client.GetStoragePoolVolume(pool, "custom", volume); err != nil {
			if cerr := b.client.CreateStoragePoolVolume(pool, api.StorageVolumesPost{
				Name: volume,
				Type: "custom",
				StorageVolumePut: api.StorageVolumePut{
					Description: "WebKVM: Incus image cache",
				},
			}); cerr != nil {
				return fmt.Errorf("create volume %q in pool %q: %w", volume, pool, cerr)
			}
		}
		cfg["storage.images_volume"] = pool + "/" + volume
	}

	return b.client.UpdateServer(api.ServerPut{Config: cfg}, etag)
}

func (b *IncusBackend) VolumeExists(poolName, volName string) (bool, error) {
	return false, compute.ErrNotImplemented
}
func (b *IncusBackend) FindVolumeAttachments(poolName, volName string) ([]models.VolumeAttachment, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) FindZVolAttachments(zvolName string) ([]models.VolumeAttachment, error) {
	return nil, nil
}

// BackingDependents has no meaning here: Incus volumes are managed by
// the daemon (zfs/btrfs/lvm/dir), not raw qcow2 files an operator can
// layer overlays on, and the daemon refuses to delete a volume that
// still has dependants itself. No dependants, no error, so callers do
// not have to special-case the container backend.
func (b *IncusBackend) BackingDependents(poolName, volName string) ([]string, error) {
	return nil, nil
}

func (b *IncusBackend) BackingDependentsOfPath(path string) ([]string, error) {
	return nil, nil
}
func (b *IncusBackend) GetISOs(poolName string) ([]models.ISOScanResult, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) RenameISO(oldName, newName, poolName string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) DeleteISO(name, poolName string) error { return compute.ErrNotImplemented }

// MoveDomainStorage relocates a container's root disk to another Incus
// pool using the daemon's native cross-pool move.
//
// Incus copies the filesystem into the destination pool and switches the
// instance over atomically, so there is no window where the container
// references a half-written rootfs. A running container is NOT a problem
// here — unlike a libvirt VM, whose disk file cannot be copied while
// QEMU holds it — but Incus does require it to be stopped for a local
// pool move, so the caller is told plainly when it is not.
func (b *IncusBackend) MoveDomainStorage(id, destPool string, onProgress func(pct float64, stage string)) error {
	if b.client == nil {
		return errors.New("incus client not initialized")
	}
	report := func(pct float64, stage string) {
		if onProgress != nil {
			onProgress(pct, stage)
		}
	}

	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return fmt.Errorf("get instance %q: %w", id, err)
	}
	if inst.StatusCode == api.Running {
		return compute.ErrDomainMustBeStoppedToMove
	}

	// Refusing a same-pool move is not pedantry: Incus would happily
	// perform a full copy of the rootfs onto itself.
	if cur := b.instanceRootPool(inst); cur != "" && cur == destPool {
		return compute.ErrSamePool
	}
	if _, _, err := b.client.GetStoragePool(destPool); err != nil {
		return fmt.Errorf("destination pool %q: %w", destPool, err)
	}

	report(5, "move_preparing")
	// Migration MUST be true even though nothing leaves this host:
	// the SDK reads a false here as "this is a rename" and refuses the
	// call outright. Name repeats the current one because a local
	// pool move keeps the instance's identity; only its storage moves.
	// InstanceOnly stays false so snapshots travel with the instance
	// instead of being silently dropped.
	op, err := b.client.MigrateInstance(id, api.InstancePost{
		Name:         id,
		Pool:         destPool,
		Migration:    true,
		InstanceOnly: false,
	})
	if err != nil {
		return fmt.Errorf("move instance to pool %q: %w", destPool, err)
	}

	// The daemon reports real byte progress; surfacing it keeps a
	// multi-GB move from looking hung. Progress is mapped into the
	// 10-95 band left free by the surrounding stages, so the bar never
	// jumps back to 0 or claims completion before op.Wait returns.
	_, _ = op.AddHandler(func(o api.Operation) {
		txt, _ := o.Metadata["move_progress"].(string)
		if pct, ok := parseTransferPercent(txt); ok {
			report(10+pct*0.85, "move_copying")
			return
		}
		report(-1, "move_copying")
	})
	report(10, "move_copying")
	if err := op.Wait(); err != nil {
		return fmt.Errorf("move instance to pool %q: %w", destPool, err)
	}
	report(100, "move_done")
	return nil
}

// parseTransferPercent extracts a 0-100 figure from Incus's progress
// metadata, which is a human string rather than a number — the shapes
// seen in practice are "45% (123.45MB/s)" and "1.20GB (45.00MB/s)".
// Only the first carries a percentage; the second reports bytes with no
// total, so there is nothing to compute a fraction from and the caller
// falls back to "stage changed, percentage unknown".
func parseTransferPercent(s string) (float64, bool) {
	i := strings.IndexByte(s, '%')
	if i <= 0 {
		return 0, false
	}
	pct, err := strconv.ParseFloat(strings.TrimSpace(s[:i]), 64)
	if err != nil || pct < 0 || pct > 100 {
		return 0, false
	}
	return pct, true
}

// instanceRootPool returns the pool backing an instance's root disk,
// looking at the expanded devices so a pool inherited from a profile is
// seen too.
func (b *IncusBackend) instanceRootPool(inst *api.Instance) string {
	if inst == nil {
		return ""
	}
	for _, devs := range []map[string]map[string]string{inst.ExpandedDevices, inst.Devices} {
		for name, dev := range devs {
			if name == "root" && dev["type"] == "disk" {
				if p := strings.TrimSpace(dev["pool"]); p != "" {
					return p
				}
			}
		}
	}
	return ""
}

// MoveVolume relocates a custom storage volume between Incus pools.
//
// Container root disks are not custom volumes — those move with the
// instance, via MoveDomainStorage.
func (b *IncusBackend) MoveVolume(srcPool, volName, destPool string, opts compute.MoveVolumeOpts) error {
	if b.client == nil {
		return errors.New("incus client not initialized")
	}
	if srcPool == destPool {
		return compute.ErrSamePool
	}
	// Incus has no ISO pool concept; only libvirt serves those.
	if opts.Kind == "iso" {
		return compute.ErrNotImplemented
	}

	src, _, err := b.client.GetStoragePoolVolume(srcPool, "custom", volName)
	if err != nil {
		return fmt.Errorf("get volume %q in pool %q: %w", volName, srcPool, err)
	}
	newName := opts.NewName
	if newName == "" {
		newName = volName
	}
	if opts.OnProgress != nil {
		opts.OnProgress(10, "move_copying")
	}

	// Both calls return a RemoteOperation (they can cross servers, even
	// though here source and destination are the same daemon), which is
	// a different interface from the local Operation waitOperation
	// takes.
	copyArgs := incus.StoragePoolVolumeCopyArgs{Name: newName, VolumeOnly: false}
	verb := "move"
	var op incus.RemoteOperation
	if opts.KeepSource {
		verb = "copy"
		op, err = b.client.CopyStoragePoolVolume(destPool, b.client, srcPool, *src, &copyArgs)
	} else {
		op, err = b.client.MoveStoragePoolVolume(destPool, b.client, srcPool, *src,
			&incus.StoragePoolVolumeMoveArgs{StoragePoolVolumeCopyArgs: copyArgs})
	}
	if err != nil {
		return fmt.Errorf("%s volume to %q: %w", verb, destPool, err)
	}
	if err := op.Wait(); err != nil {
		return fmt.Errorf("%s volume to %q: %w", verb, destPool, err)
	}
	if opts.OnProgress != nil {
		opts.OnProgress(100, "move_done")
	}
	return nil
}
func (b *IncusBackend) DeleteVMDiskFiles(vmName string, exact ...string) (deleted []string, skipped []string, err error) {
	return nil, nil, compute.ErrNotImplemented
}
func (b *IncusBackend) RefreshCIFSSecretIfNeeded(ctx context.Context, poolName string) (*compute.SecretRef, error) {
	return nil, compute.ErrNotImplemented
}

// --- Networking ---

