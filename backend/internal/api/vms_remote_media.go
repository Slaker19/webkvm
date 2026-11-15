package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// remoteVolumeSource is the slice of a remote node's libvirt connection that
// the create-on-node media resolution needs. *libvirt.Connector satisfies it;
// tests use a fake.
type remoteVolumeSource interface {
	ListStoragePools() ([]models.StoragePool, error)
	ListStorageVolumes(pool string) ([]models.StorageVolume, error)
}

// remoteMediaError is a typed, client-facing failure of the remote media
// resolution.
type remoteMediaError struct {
	status int
	code   string
	msg    string
	pools  []string
}

func (e *remoteMediaError) write(w http.ResponseWriter, n nodes.Node) {
	body := map[string]any{"error": e.msg, "code": e.code, "node": n.Name, "node_id": n.ID}
	if len(e.pools) > 0 {
		body["pools"] = e.pools
	}
	jsonResp(w, e.status, body)
}

// validateRemoteMediaNames is the syntactic gate run BEFORE connecting to the
// node: on a remote node ISO / virtio ISO / existing disk are volume NAMES of
// that node's pools, never filesystem paths (a path would be resolved against
// nothing we can confine). Pool names use the libvirt pool-name rule.
func validateRemoteMediaNames(req *models.CreateVMRequest) *remoteMediaError {
	bad := func(field string, err error) *remoteMediaError {
		return &remoteMediaError{status: http.StatusBadRequest, code: "invalid_media_name",
			msg: fmt.Sprintf("%s: %v (on a remote node pick a volume of its storage pools by name)", field, err)}
	}
	checkName := func(field, v string, media bool) *remoteMediaError {
		if v == "" {
			return nil
		}
		clean, err := safeISOFilename(v)
		if err != nil {
			return bad(field, err)
		}
		if clean != v {
			return bad(field, fmt.Errorf("must be a plain volume name"))
		}
		if media && !libvirt.IsMediaVolumeName(v) {
			return bad(field, fmt.Errorf("must be an .iso or .img volume"))
		}
		return nil
	}
	checkPool := func(field, v string) *remoteMediaError {
		if v != "" && !libvirt.ValidPoolName(v) {
			return bad(field, fmt.Errorf("invalid storage pool name"))
		}
		return nil
	}
	if e := checkName("iso", req.ISO, true); e != nil {
		return e
	}
	if e := checkName("virtio_iso", req.VirtIOISO, true); e != nil {
		return e
	}
	if e := checkPool("iso_pool", req.ISOPool); e != nil {
		return e
	}
	if e := checkPool("virtio_iso_pool", req.VirtIOISOPool); e != nil {
		return e
	}
	if req.ISOPool != "" && req.ISO == "" || req.VirtIOISOPool != "" && req.VirtIOISO == "" {
		return &remoteMediaError{status: http.StatusBadRequest, code: "invalid_media_name", msg: "a media pool was given without a media name"}
	}
	if (req.ExistingDiskPool == "") != (req.ExistingDiskName == "") {
		return &remoteMediaError{status: http.StatusBadRequest, code: "invalid_media_name",
			msg: "existing_disk_pool and existing_disk_name must be given together"}
	}
	if e := checkPool("existing_disk_pool", req.ExistingDiskPool); e != nil {
		return e
	}
	return checkName("existing_disk_name", req.ExistingDiskName, false)
}

// poolIsISOOnly reports whether a pool purpose designates only install media.
func poolIsISOOnly(purpose string) bool {
	p := strings.TrimSpace(purpose)
	return p == libvirt.PoolPurposeISO
}

// resolveRemoteMedia resolves the media of a create-on-node request against
// the TARGET node's libvirt storage volumes: ISO and virtio ISO names are
// rewritten to the volume paths the node reports (the libvirt layer confirms
// them again with LookupStorageVolByPath), and an existing disk must be a
// real, non-snapshot volume of a non-ISO pool of that node. Nothing is read
// from this host's filesystem. Call validateRemoteMediaNames first.
func resolveRemoteMedia(src remoteVolumeSource, req *models.CreateVMRequest) *remoteMediaError {
	if req.ISO == "" && req.VirtIOISO == "" && req.ExistingDiskName == "" {
		return nil
	}
	pools, err := src.ListStoragePools()
	if err != nil {
		return &remoteMediaError{status: http.StatusBadGateway, code: "node_storage_unavailable",
			msg: "cannot list the storage pools of the node"}
	}
	byName := map[string]models.StoragePool{}
	for _, p := range pools {
		byName[p.Name] = p
	}
	volumesOf := map[string][]models.StorageVolume{}
	listVols := func(pool string) []models.StorageVolume {
		if v, ok := volumesOf[pool]; ok {
			return v
		}
		v, err := src.ListStorageVolumes(pool)
		if err != nil {
			v = nil
		}
		volumesOf[pool] = v
		return v
	}

	resolveMedia := func(field, name, pool string) (string, *remoteMediaError) {
		candidates := pools
		if pool != "" {
			p, ok := byName[pool]
			if !ok {
				return "", &remoteMediaError{status: http.StatusNotFound, code: "media_not_found",
					msg: fmt.Sprintf("%s: storage pool %q does not exist on the node", field, pool)}
			}
			candidates = []models.StoragePool{p}
		}
		var paths, in []string
		for _, p := range candidates {
			for _, v := range listVols(p.Name) {
				if v.Name == name && !v.IsSnapshot && v.Path != "" {
					paths = append(paths, v.Path)
					in = append(in, p.Name)
				}
			}
		}
		switch len(paths) {
		case 0:
			return "", &remoteMediaError{status: http.StatusNotFound, code: "media_not_found",
				msg: fmt.Sprintf("%s: %q is not a volume of the node's storage pools", field, name)}
		case 1:
			return paths[0], nil
		}
		sort.Strings(in)
		return "", &remoteMediaError{status: http.StatusConflict, code: "media_ambiguous", pools: in,
			msg: fmt.Sprintf("%s: %q exists in several pools of the node (%s); choose the pool", field, name, strings.Join(in, ", "))}
	}

	if req.ISO != "" {
		p, e := resolveMedia("iso", req.ISO, req.ISOPool)
		if e != nil {
			return e
		}
		req.ISO = p
	}
	if req.VirtIOISO != "" {
		p, e := resolveMedia("virtio_iso", req.VirtIOISO, req.VirtIOISOPool)
		if e != nil {
			return e
		}
		req.VirtIOISO = p
	}
	if req.ExistingDiskName != "" {
		p, ok := byName[req.ExistingDiskPool]
		if !ok {
			return &remoteMediaError{status: http.StatusNotFound, code: "existing_disk_not_found",
				msg: fmt.Sprintf("storage pool %q does not exist on the node", req.ExistingDiskPool)}
		}
		if poolIsISOOnly(p.Purpose) {
			return &remoteMediaError{status: http.StatusBadRequest, code: "existing_disk_pool_invalid",
				msg: fmt.Sprintf("pool %q holds install media, not VM disks", p.Name)}
		}
		found := false
		for _, v := range listVols(p.Name) {
			if v.Name == req.ExistingDiskName && !v.IsSnapshot {
				found = true
				break
			}
		}
		if !found {
			return &remoteMediaError{status: http.StatusNotFound, code: "existing_disk_not_found",
				msg: fmt.Sprintf("disk %q does not exist in pool %q of the node", req.ExistingDiskName, p.Name)}
		}
	}
	return nil
}

// cloudInitRemoteUnsupported answers a cloud-init request for a remote
// libvirt node: the NoCloud seed ISO is built and kept on THIS host's data
// dir, and there is no safe channel yet to place it as a volume on the node.
func cloudInitRemoteUnsupported(w http.ResponseWriter, n nodes.Node) {
	body := map[string]string{
		"error": "cloud-init is not available when creating on a remote node; install from an ISO of the node, or create it from that node's own WebKVM",
		"code":  "cloud_init_remote_unsupported",
		"hint":  "open_node_ui",
		"node":  n.Name, "node_id": n.ID,
	}
	if n.UIURL != "" {
		body["ui_url"] = n.UIURL
	}
	jsonResp(w, http.StatusNotImplemented, body)
}
