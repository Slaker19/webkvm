package libvirt

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"webkvm/internal/models"

	"libvirt.org/go/libvirt"
)

func (c *Connector) ListStoragePools() ([]models.StoragePool, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}

	pools, err := c.conn.ListAllStoragePools(libvirt.CONNECT_LIST_STORAGE_POOLS_ACTIVE | libvirt.CONNECT_LIST_STORAGE_POOLS_INACTIVE)
	if err != nil {
		return nil, fmt.Errorf("list pools: %w", err)
	}

	result := make([]models.StoragePool, 0, len(pools))
	for i := range pools {
		p, err := c.storagePoolToModel(&pools[i])
		pools[i].Free()
		if err != nil {
			continue
		}
		result = append(result, p)
	}
	return result, nil
}

func (c *Connector) CreateStoragePool(ctx context.Context, req models.CreatePoolRequest) (models.StoragePool, error) {
	if err := c.ensureConnected(); err != nil {
		return models.StoragePool{}, err
	}

	poolType := req.Type
	if poolType == "" {
		poolType = "dir"
	}

	// WebKVM no longer mounts remote shares: NFS/SMB mounts must be
	// set up on the host (via /etc/fstab or a systemd mount unit)
	// and point at a local mountpoint. A legacy "netfs" request is
	// accepted as a plain directory pool over that mountpoint,
	// ignoring source/credentials (which webkvm used to manage
	// through libvirt secrets).
	if poolType == "netfs" {
		poolType = "dir"
	}

	// Sanitize the path/name. xmlEscape handles the rest.
	xmlStr, err := buildPoolXML(poolType, req)
	if err != nil {
		return models.StoragePool{}, err
	}

	pool, err := c.conn.StoragePoolDefineXML(xmlStr, 0)
	if err != nil {
		return models.StoragePool{}, fmt.Errorf("define pool: %w", err)
	}
	defer pool.Free()

	if err := pool.Build(0); err != nil {
		pool.Undefine()
		return models.StoragePool{}, fmt.Errorf("build pool: %w", err)
	}

	if err := pool.Create(0); err != nil {
		pool.Undefine()
		return models.StoragePool{}, fmt.Errorf("create pool: %w", err)
	}

	purpose := req.Purpose
	if purpose == "" {
		purpose = InferPoolPurpose(req.Name)
	}
	if c.purposes != nil {
		if err := c.purposes.Set(req.Name, purpose); err != nil {
			// Pool is created; purpose save failure is a soft
			// problem. Don't roll back the secret for it.
			return models.StoragePool{}, fmt.Errorf("save purpose: %w", err)
		}
	}

	return c.storagePoolToModel(pool)
}

// UpdateStoragePool handles the supported subset of pool updates.
// Today this is intentionally narrow: only CIFS secret rotation is
// supported. Changing the pool's path/source is not supported in
// this revision because it requires undefine+redefine on a live
// pool, which is unsafe while volumes are mounted. Return 400
// for unsupported fields so callers learn the limit quickly.
func (c *Connector) UpdateStoragePool(ctx context.Context, name string, req models.UpdatePoolRequest) (models.StoragePool, error) {
	if err := c.ensureConnected(); err != nil {
		return models.StoragePool{}, err
	}

	if req.Path != nil || req.SourceHost != nil || req.SourceDir != nil || req.SourceFormat != nil {
		return models.StoragePool{}, fmt.Errorf(
			"updating path/source is not supported; create a new pool and migrate volumes")
	}

	// Reauth path: caller is asking us to (re)create the libvirt
	// secret for this pool. We need username+password together.
	hasUser := req.SourceUsername != nil
	hasPass := req.SourcePassword != nil
	if hasUser != hasPass {
		return models.StoragePool{}, fmt.Errorf(
			"cifs auth requires both username and password")
	}

	if hasUser && *req.SourceUsername == "" {
		return models.StoragePool{}, fmt.Errorf("source_username cannot be empty when reauthing")
	}

	// Look up the existing pool to make sure it's a CIFS pool before
	// we touch libvirt secrets.
	pool, err := c.conn.LookupStoragePoolByName(name)
	if err != nil {
		return models.StoragePool{}, fmt.Errorf("lookup pool: %w", err)
	}
	xmlDesc, _ := pool.GetXMLDesc(0)
	pType := extractPoolType(xmlDesc)
	if pType != "netfs" {
		pool.Free()
		return models.StoragePool{}, fmt.Errorf("reauth only supported for netfs pools (got %q)", pType)
	}
	format := extractPoolFormatFromXML(xmlDesc)
	if format != "cifs" {
		pool.Free()
		return models.StoragePool{}, fmt.Errorf("reauth only supported for cifs pools (got format %q)", format)
	}
	pool.Free()

	// Refuse if the pool is currently running — libvirt cannot
	// redefine a pool while it's active, and we don't want to
	// silently tear down users' mounts.
	if p, err := c.conn.LookupStoragePoolByName(name); err == nil {
		if info, err := p.GetInfo(); err == nil && info.State == libvirt.STORAGE_POOL_RUNNING {
			p.Free()
			return models.StoragePool{}, fmt.Errorf(
				"pool is running; stop it before reauthing to avoid mount disruption")
		} else if p != nil {
			p.Free()
		}
	}

	// Resolve credentials. If the caller provided them, use them.
	// Otherwise, fall back to the values from the pool's existing
	// XML (this is the cifs-needs-reauth-without-credentials case
	// for the "rebuild secret after libvirtd reinstall" workflow).
	user := ""
	pass := ""
	if hasUser {
		user = *req.SourceUsername
		pass = *req.SourcePassword
	} else {
		// Reauth without re-supplying credentials: extract from
		// the existing <auth>/<name> block in the pool XML. This
		// only works if the auth was originally set; if not, the
		// caller must supply new credentials.
		p, err := c.conn.LookupStoragePoolByName(name)
		if err != nil {
			return models.StoragePool{}, fmt.Errorf("lookup pool: %w", err)
		}
		xmlDesc, _ := p.GetXMLDesc(0)
		p.Free()
		user = extractAuthUsernameFromXML(xmlDesc)
		if user == "" {
			return models.StoragePool{}, fmt.Errorf(
				"no credentials supplied and pool has no <auth> block to recover from; send source_username and source_password")
		}
		// We can't recover the password from the pool XML; the
		// libvirt secret is the only place it lives. Refuse.
		return models.StoragePool{}, fmt.Errorf(
			"cifs-needs-reauth without credentials cannot recover the password; send source_username and source_password")
	}

	// Define the new secret (defineCIFSSecret is idempotent — it
	// replaces any pre-existing secret for this pool).
	if _, err := defineCIFSSecret(ctx, c, name, "cifs-"+name, user, pass); err != nil {
		return models.StoragePool{}, fmt.Errorf("cifs secret rotation: %w", err)
	}

	// The pool XML embeds the secret UUID at define time. libvirt
	// does not live-update that reference; the pool has to be
	// redefined for the new UUID to take effect. We do that here
	// by undefine + redefine while inactive.
	secretUUID, _ := lookupCIFSSecretRef(c.cfg.DataDir, name)
	if secretUUID == nil {
		return models.StoragePool{}, fmt.Errorf("cifs: secret not found after rotation")
	}

	p, err := c.conn.LookupStoragePoolByName(name)
	if err != nil {
		return models.StoragePool{}, fmt.Errorf("lookup pool for redefine: %w", err)
	}
	poolXML, _ := p.GetXMLDesc(0)
	p.Free()
	autostart := extractAutostartFromXML(poolXML)

	req2 := models.CreatePoolRequest{
		Name:           name,
		Type:           pType,
		Path:           extractPoolPath(poolXML),
		SourceHost:     extractSourceHost(poolXML),
		SourceDir:      extractSourceDir(poolXML),
		SourceFormat:   format,
		SourceUsername: user,
		SecretUUID:     secretUUID.SecretUUID,
	}
	newXML, err := buildPoolXML(pType, req2)
	if err != nil {
		return models.StoragePool{}, err
	}
	if _, err := c.conn.StoragePoolDefineXML(newXML, 0); err != nil {
		return models.StoragePool{}, fmt.Errorf("redefine pool with new secret: %w", err)
	}
	// Re-apply autostart (DefineXML on an existing pool keeps the
	// autostart flag, but be defensive).
	if p2, err := c.conn.LookupStoragePoolByName(name); err == nil {
		_ = p2.SetAutostart(autostart)
		p2.Free()
	}

	if p3, err := c.conn.LookupStoragePoolByName(name); err == nil {
		defer p3.Free()
		return c.storagePoolToModel(p3)
	}
	return c.storagePoolToModelByName(name)
}

// storagePoolToModelByName is a fallback for when storagePoolToModel
// already had its pool freed (used after a redefine).
func (c *Connector) storagePoolToModelByName(name string) (models.StoragePool, error) {
	p, err := c.conn.LookupStoragePoolByName(name)
	if err != nil {
		return models.StoragePool{}, err
	}
	defer p.Free()
	return c.storagePoolToModel(p)
}

// RefreshCIFSSecretIfNeeded re-creates the libvirt secret for a
// pool that lost it (e.g. libvirtd reinstall). Returns the
// existing ref if it still exists in libvirt, or an error if the
// caller needs to re-supply credentials (we cannot recover the
// password from anywhere we don't store it).
func (c *Connector) RefreshCIFSSecretIfNeeded(ctx context.Context, poolName string) (*SecretRef, error) {
	if c.cfg == nil {
		return nil, fmt.Errorf("cifs: no config")
	}
	ref, err := lookupCIFSSecretRef(c.cfg.DataDir, poolName)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return nil, fmt.Errorf("cifs: no on-disk mapping for %q", poolName)
	}
	// Verify the secret still exists in libvirt.
	conn := c.Get()
	if conn == nil {
		return nil, fmt.Errorf("libvirt not connected")
	}
	if s, err := conn.LookupSecretByUUIDString(ref.SecretUUID); err == nil && s != nil {
		s.Free()
		return ref, nil
	}
	return nil, fmt.Errorf("cifs: libvirt secret %q missing for pool %q; reauth required", ref.SecretUUID, poolName)
}

func (c *Connector) GetStorageVolume(poolName, volName string) (models.StorageVolume, error) {
	if err := c.ensureConnected(); err != nil {
		return models.StorageVolume{}, err
	}

	pool, err := c.conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return models.StorageVolume{}, fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()

	vol, err := pool.LookupStorageVolByName(volName)
	if err != nil {
		return models.StorageVolume{}, fmt.Errorf("lookup volume: %w", err)
	}
	defer vol.Free()

	return volumeToModel(vol, poolName)
}

func (c *Connector) ListStorageVolumes(poolName string) ([]models.StorageVolume, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}

	pool, err := c.conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return nil, fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()

	vols, err := pool.ListAllStorageVolumes(0)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}

	// Build a name -> UUID map for snapshot classification. Longest
	// names first so "<vm>-1" doesn't shadow "<vm>". Callers only see
	// the result of classifyVolume; the raw VM map is internal.
	vms, err := c.snapshotClassifierVms()
	if err != nil {
		// Non-fatal: if we can't list domains, just skip the snapshot
		// classification and return plain volumes.
		vms = nil
	}

	result := make([]models.StorageVolume, 0, len(vols))
	for i := range vols {
		v, err := c.classifyVolume(&vols[i], poolName, vms)
		vols[i].Free()
		if err != nil {
			continue
		}
		result = append(result, v)
	}
	return result, nil
}

// snapshotClassifierVms returns a slice of {name, uuid} entries for
// every domain known to libvirt, sorted by name length descending so
// "<vm>-1" is tried before "<vm>" and the longest matching prefix
// wins. Used internally by ListStorageVolumes to detect which
// volumes are internal qcow2 snapshots of which VM.
type vmClassifier struct {
	name string
	uuid string
}

func (c *Connector) snapshotClassifierVms() ([]vmClassifier, error) {
	doms, err := c.conn.ListAllDomains(0)
	if err != nil {
		return nil, err
	}
	out := make([]vmClassifier, 0, len(doms))
	for i := range doms {
		n, _ := doms[i].GetName()
		u, _ := doms[i].GetUUIDString()
		if n != "" {
			out = append(out, vmClassifier{name: n, uuid: u})
		}
		doms[i].Free()
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i].name) > len(out[j].name) })
	return out, nil
}

// classifyVolume wraps volumeToModel and tags volumes that are
// internal qcow2 snapshots of a known VM. Naming convention used by
// the libvirt/qemu driver for internal snapshots: "<vmname>.<snap>"
// (no extension). Main disks use "<vmname>.qcow2" or "<vmname>.img".
// Attached disks use "<vmname>-<dev>.qcow2" (no dot before the
// extension, so they don't match the snapshot rule).
func (c *Connector) classifyVolume(vol *libvirt.StorageVol, poolName string, vms []vmClassifier) (models.StorageVolume, error) {
	v, err := volumeToModel(vol, poolName)
	if err != nil {
		return v, err
	}
	for _, vm := range vms {
		prefix := vm.name + "."
		if !strings.HasPrefix(v.Name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(v.Name, prefix)
		// Main disk: "<vmname>.qcow2" / "<vmname>.img".
		// Attached disk: never matches this rule.
		// Snapshot: "<vmname>.<snap>" with non-empty rest that
		// doesn't end in a known disk extension.
		if rest == "" || rest == "qcow2" || rest == "img" {
			return v, nil
		}
		v.IsSnapshot = true
		v.SnapshotOfVMID = vm.uuid
		v.ParentVolume = vm.name + ".qcow2"
		return v, nil
	}
	return v, nil
}

// lookupSnapshotVolume finds a volume by name in the given pool and
// returns just the metadata needed to enrich a snapshot record
// (currently the libvirt "Allocation", which is the data size when
// the snapshot was created). Returns an error if the pool or volume
// is not found.
func (c *Connector) lookupSnapshotVolume(poolName, volName string) (models.StorageVolume, error) {
	return c.GetStorageVolume(poolName, volName)
}

func (c *Connector) CreateStorageVolume(req models.CreateVolumeRequest) (models.StorageVolume, error) {
	if err := c.ensureConnected(); err != nil {
		return models.StorageVolume{}, err
	}

	if !nameRE.MatchString(req.Name) {
		return models.StorageVolume{}, fmt.Errorf("invalid volume name %q (allowed: A-Z a-z 0-9 . _ -)", req.Name)
	}

	// Pool "purpose" (disk vs iso) is an informational label only — any
	// pool can hold both VM disks and ISOs, so volume creation is not
	// gated on it.
	pool, err := c.conn.LookupStoragePoolByName(req.Pool)
	if err != nil {
		return models.StorageVolume{}, fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()

	format := req.Format
	if format == "" {
		format = "qcow2"
	}

	xmlStr := fmt.Sprintf(`<volume>
  <name>%s</name>
  <capacity unit='G'>%d</capacity>
  <target>
    <format type='%s'/>
    <permissions>
      <mode>0644</mode>
    </permissions>
  </target>
</volume>`, req.Name, req.Capacity, format)

	vol, err := pool.StorageVolCreateXML(xmlStr, libvirt.STORAGE_VOL_CREATE_PREALLOC_METADATA)
	if err != nil {
		return models.StorageVolume{}, fmt.Errorf("create volume: %w", err)
	}
	defer vol.Free()

	return volumeToModel(vol, req.Pool)
}

func (c *Connector) DeleteStorageVolume(poolName, volName string) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}

	pool, err := c.conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()

	vol, err := pool.LookupStorageVolByName(volName)
	if err != nil {
		return fmt.Errorf("lookup volume: %w", err)
	}
	defer vol.Free()

	return vol.Delete(libvirt.STORAGE_VOL_DELETE_NORMAL)
}

// FindVolumeAttachments scans every domain's XML for disks or cdroms
// whose source resolves to the given volume's path. The storage delete
// guards use this so a volume still referenced by any VM — running or
// not — can never be removed.
func (c *Connector) FindVolumeAttachments(poolName, volName string) ([]models.VolumeAttachment, error) {
	vol, err := c.GetStorageVolume(poolName, volName)
	if err != nil {
		return nil, err
	}
	return c.findPathAttachments(vol.Path)
}

func (c *Connector) findPathAttachments(path string) ([]models.VolumeAttachment, error) {
	doms, err := c.ListDomains()
	if err != nil {
		return nil, err
	}
	out := []models.VolumeAttachment{}
	for _, vm := range doms {
		xmlDesc, err := c.GetDomainXML(vm.ID)
		if err != nil {
			continue // unreachable domain: skip, never fail the scan
		}
		for _, d := range c.parseDisks(xmlDesc) {
			if d.Source == "" || d.Source != path {
				continue
			}
			out = append(out, models.VolumeAttachment{
				VMID:   vm.ID,
				VMName: vm.Name,
				State:  string(vm.State),
				Device: d.Device,
				Target: d.Target,
			})
		}
	}
	return out, nil
}

// DeleteVMDiskFiles removes storage volumes that follow the WebKVM disk
// naming convention for the given VM across ALL active pools:
//
//	<vm>.qcow2 | <vm>.img | <vm>-<N>.qcow2      (main / re-imports)
//	<vm>-<dev>.qcow2                             (attached disks)
//
// A candidate is deleted only when no other domain references its path;
// anything still attached (or snapshot views) is skipped and reported.
// Used by the optional "delete disks" flag on VM deletion.
func (c *Connector) DeleteVMDiskFiles(vmName string) (deleted []string, skipped []string, err error) {
	if vmName == "" {
		return nil, nil, nil
	}
	mainRe := regexp.MustCompile(fmt.Sprintf(`^%s(?:-\d+)?\.(?:qcow2|img)$`, regexp.QuoteMeta(vmName)))
	devRe := regexp.MustCompile(fmt.Sprintf(`^%s-[a-z]{2,4}\d*\.qcow2$`, regexp.QuoteMeta(vmName)))

	pools, err := c.ListStoragePools()
	if err != nil {
		return nil, nil, err
	}
	for _, p := range pools {
		vols, verr := c.ListStorageVolumes(p.Name)
		if verr != nil {
			continue
		}
		for _, v := range vols {
			if v.IsSnapshot || v.Name == vmName {
				continue
			}
			if !mainRe.MatchString(v.Name) && !devRe.MatchString(v.Name) {
				continue
			}
			atts, aerr := c.FindVolumeAttachments(p.Name, v.Name)
			if aerr != nil || len(atts) > 0 {
				skipped = append(skipped, v.Name)
				continue
			}
			if derr := c.DeleteStorageVolume(p.Name, v.Name); derr != nil {
				skipped = append(skipped, v.Name)
				continue
			}
			deleted = append(deleted, v.Pool+"/"+v.Name)
		}
	}
	return deleted, skipped, nil
}

func (c *Connector) GetISOs(poolName string) ([]models.ISOScanResult, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}

	pool, err := c.conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return nil, fmt.Errorf("lookup pool %s: %w", poolName, err)
	}
	defer pool.Free()

	poolPath := extractPoolPathFromPool(pool)
	vols, err := pool.ListAllStorageVolumes(0)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}

	result := make([]models.ISOScanResult, 0)
	for i := range vols {
		name, _ := vols[i].GetName()
		if !strings.HasSuffix(strings.ToLower(name), ".iso") {
			vols[i].Free()
			continue
		}
		info, err := vols[i].GetInfo()
		if err != nil {
			vols[i].Free()
			continue
		}
		fullPath := name
		if poolPath != "" && !strings.HasPrefix(name, "/") {
			fullPath = poolPath + "/" + name
		}
		result = append(result, models.ISOScanResult{
			Path: fullPath,
			Name: name,
			Size: int64(info.Capacity),
			Pool: poolName,
		})
		vols[i].Free()
	}

	return result, nil
}

func (c *Connector) RefreshPool(name string) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}
	pool, err := c.conn.LookupStoragePoolByName(name)
	if err != nil {
		return fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()
	return pool.Refresh(0)
}

func (c *Connector) GetPoolPath(name string) (string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", err
	}
	pool, err := c.conn.LookupStoragePoolByName(name)
	if err != nil {
		return "", fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()
	xmlDesc, err := pool.GetXMLDesc(0)
	if err != nil {
		return "", fmt.Errorf("get pool XML: %w", err)
	}
	poolPath := extractPoolPath(xmlDesc)
	if poolPath == "" {
		return "", fmt.Errorf("pool %q has no filesystem path", name)
	}
	return poolPath, nil
}

func extractPoolPathFromPool(pool *libvirt.StoragePool) string {
	xmlDesc, _ := pool.GetXMLDesc(0)
	return extractPoolPath(xmlDesc)
}

func (c *Connector) poolPurpose(name string) string {
	if c.purposes != nil {
		if p := c.purposes.Get(name); p != "" {
			return p
		}
	}
	return InferPoolPurpose(name)
}

func (c *Connector) storagePoolToModel(pool *libvirt.StoragePool) (models.StoragePool, error) {
	name, _ := pool.GetName()

	xmlDesc, _ := pool.GetXMLDesc(0)
	pType := extractPoolType(xmlDesc)
	pPath := extractPoolPath(xmlDesc)

	info, err := pool.GetInfo()
	if err != nil {
		return models.StoragePool{}, err
	}

	autostart, _ := pool.GetAutostart()

	state := "inactive"
	if info.State == libvirt.STORAGE_POOL_RUNNING {
		state = "active"
	}

	return models.StoragePool{
		Name:      name,
		Type:      pType,
		Path:      pPath,
		Purpose:   c.poolPurpose(name),
		Capacity:  int64(info.Capacity),
		Allocated: int64(info.Allocation),
		Available: int64(info.Available),
		State:     state,
		Autostart: autostart,
	}, nil
}

func volumeToModel(vol *libvirt.StorageVol, poolName string) (models.StorageVolume, error) {
	name, _ := vol.GetName()
	info, err := vol.GetInfo()
	if err != nil {
		return models.StorageVolume{}, err
	}

	xmlDesc, _ := vol.GetXMLDesc(0)
	format := extractVolumeFormat(xmlDesc)

	path, _ := vol.GetPath()

	return models.StorageVolume{
		Name:      name,
		Pool:      poolName,
		Path:      path,
		Format:    format,
		Capacity:  int64(info.Capacity),
		Allocated: int64(info.Allocation),
	}, nil
}

func extractPoolType(xml string) string {
	start := `<pool type='`
	s := strings.Index(xml, start)
	if s < 0 {
		start = `<pool type="`
		s = strings.Index(xml, start)
		if s < 0 {
			return "dir"
		}
	}
	s += len(start)
	e := strings.Index(xml[s:], "'")
	if e < 0 {
		e = strings.Index(xml[s:], "\"")
		if e < 0 {
			return "dir"
		}
	}
	return xml[s : s+e]
}

func extractPoolPath(xml string) string {
	start := `<path>`
	s := strings.Index(xml, start)
	if s < 0 {
		return ""
	}
	s += len(start)
	e := strings.Index(xml[s:], "</path>")
	if e < 0 {
		return ""
	}
	return xml[s : s+e]
}

func (c *Connector) ResizeStorageVolume(poolName, volName string, newSizeGB int64) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}

	pool, err := c.conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()
	vol, err := pool.LookupStorageVolByName(volName)
	if err != nil {
		return fmt.Errorf("lookup volume: %w", err)
	}
	defer vol.Free()
	return vol.Resize(uint64(newSizeGB)*1024*1024*1024, libvirt.StorageVolResizeFlags(0))
}

func (c *Connector) DeletePool(name string) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}
	pool, err := c.conn.LookupStoragePoolByName(name)
	if err != nil {
		return fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()

	info, err := pool.GetInfo()
	if err == nil && info.State == libvirt.STORAGE_POOL_RUNNING {
		pool.Destroy()
	}

	if err := pool.Undefine(); err != nil {
		return err
	}

	// Drop the purpose metadata for this pool so pool-purposes.json
	// doesn't accumulate stale entries. The actual directory and any
	// ISO/volume files inside it are intentionally left in place —
	// re-creating the pool with the same path picks them up
	// automatically.
	if c.purposes != nil {
		if err := c.purposes.Delete(name); err != nil {
			slog.Warn("pool_purpose_drop_failed", "pool", name, "err", err)
		}
	}

	// Clean up the libvirt secret and the on-disk mapping if this
	// pool had CIFS auth. unsetCIFSSecret is idempotent; safe to
	// call even when no secret exists for the pool.
	_ = unsetCIFSSecret(context.Background(), c, name)
	return nil
}

func (c *Connector) DeleteISO(name, poolName string) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}

	pool, err := c.conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return fmt.Errorf("lookup pool %s: %w", poolName, err)
	}
	defer pool.Free()

	vol, err := pool.LookupStorageVolByName(name)
	if err != nil {
		return fmt.Errorf("lookup ISO: %w", err)
	}
	defer vol.Free()

	return vol.Delete(libvirt.STORAGE_VOL_DELETE_NORMAL)
}

// RenameISO renames an ISO file in a pool. The name must end with .iso.
// The new name must not exist in the pool.
func (c *Connector) RenameISO(oldName, newName, poolName string) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}

	if newName == "" || newName == oldName {
		return fmt.Errorf("invalid new name")
	}
	if !strings.HasSuffix(strings.ToLower(newName), ".iso") {
		return fmt.Errorf("new name must end with .iso")
	}
	if strings.ContainsAny(newName, "/\\\x00") {
		return fmt.Errorf("new name contains invalid characters")
	}

	pool, err := c.conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return fmt.Errorf("lookup pool %s: %w", poolName, err)
	}
	defer pool.Free()

	// Refuse if a volume with the new name already exists.
	if _, err := pool.LookupStorageVolByName(newName); err == nil {
		return fmt.Errorf("an ISO named %q already exists in pool %q", newName, poolName)
	}

	vol, err := pool.LookupStorageVolByName(oldName)
	if err != nil {
		return fmt.Errorf("lookup ISO %q: %w", oldName, err)
	}
	defer vol.Free()

	oldPath, err := vol.GetPath()
	if err != nil || oldPath == "" {
		return fmt.Errorf("get ISO path: %w", err)
	}
	newPath := filepath.Join(filepath.Dir(oldPath), newName)

	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename file: %w", err)
	}

	if err := pool.Refresh(0); err != nil {
		// Try to roll back
		_ = os.Rename(newPath, oldPath)
		return fmt.Errorf("refresh pool: %w", err)
	}
	return nil
}

func extractVolumeFormat(xml string) string {
	start := `<format type='`
	s := strings.Index(xml, start)
	if s < 0 {
		start = `<format type="`
		s = strings.Index(xml, start)
		if s < 0 {
			return "raw"
		}
	}
	s += len(start)
	e := strings.Index(xml[s:], "'")
	if e < 0 {
		e = strings.Index(xml[s:], "\"")
		if e < 0 {
			return "raw"
		}
	}
	return xml[s : s+e]
}
