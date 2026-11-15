package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/libvirt"
	"webkvm/internal/models"
)

// fakeNodeVolumes is a remote node's storage as its libvirt connection would
// report it. calls counts ListStorageVolumes per pool.
type fakeNodeVolumes struct {
	pools    []models.StoragePool
	vols     map[string][]models.StorageVolume
	poolsErr error
	calls    map[string]int
}

func (f *fakeNodeVolumes) ListStoragePools() ([]models.StoragePool, error) {
	if f.poolsErr != nil {
		return nil, f.poolsErr
	}
	return f.pools, nil
}

func (f *fakeNodeVolumes) ListStorageVolumes(pool string) ([]models.StorageVolume, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[pool]++
	v, ok := f.vols[pool]
	if !ok {
		return nil, errors.New("pool inactive")
	}
	return v, nil
}

func eastNode() *fakeNodeVolumes {
	return &fakeNodeVolumes{
		pools: []models.StoragePool{
			{Name: "east-isos", Purpose: libvirt.PoolPurposeISO, Path: "/srv/isos"},
			{Name: "east-disks", Purpose: libvirt.PoolPurposeDisk, Path: "/srv/disks"},
			{Name: "east-extra", Purpose: "", Path: "/srv/extra"},
			{Name: "east-off", Purpose: libvirt.PoolPurposeDisk, Path: "/srv/off"},
		},
		vols: map[string][]models.StorageVolume{
			"east-isos": {
				{Name: "debian.iso", Pool: "east-isos", Path: "/srv/isos/debian.iso"},
				{Name: "virtio-win.iso", Pool: "east-isos", Path: "/srv/isos/virtio-win.iso"},
				{Name: "dup.iso", Pool: "east-isos", Path: "/srv/isos/dup.iso"},
			},
			"east-disks": {
				{Name: "data.qcow2", Pool: "east-disks", Path: "/srv/disks/data.qcow2"},
				{Name: "vm1.snap1", Pool: "east-disks", Path: "/srv/disks/vm1.qcow2", IsSnapshot: true},
			},
			"east-extra": {
				{Name: "dup.iso", Pool: "east-extra", Path: "/srv/extra/dup.iso"},
			},
		},
	}
}

func TestValidateRemoteMediaNames(t *testing.T) {
	bad := []models.CreateVMRequest{
		{ISO: "/etc/passwd"},
		{ISO: "/srv/isos/debian.iso"},
		{ISO: "../debian.iso"},
		{ISO: "sub/debian.iso"},
		{ISO: "a\\b.iso"},
		{ISO: "debian.qcow2"}, // not boot media
		{ISO: "x.iso\n"},
		{VirtIOISO: "../../virtio.iso"},
		{ISO: "debian.iso", ISOPool: "../etc"},
		{ISOPool: "east-isos"}, // pool without a name
		{VirtIOISOPool: "east-isos"},
		{ExistingDiskPool: "east-disks"},
		{ExistingDiskName: "data.qcow2"},
		{ExistingDiskPool: "east disks", ExistingDiskName: "data.qcow2"},
		{ExistingDiskPool: "east-disks", ExistingDiskName: "../data.qcow2"},
		{ExistingDiskPool: "east-disks", ExistingDiskName: "/srv/disks/data.qcow2"},
	}
	for i, req := range bad {
		r := req
		if e := validateRemoteMediaNames(&r); e == nil || e.status != http.StatusBadRequest || e.code != "invalid_media_name" {
			t.Errorf("#%d %+v: got %+v, want 400 invalid_media_name", i, req, e)
		}
	}
	good := []models.CreateVMRequest{
		{},
		{ISO: "debian.iso"},
		{ISO: "rpi.IMG", ISOPool: "east-isos"},
		{VirtIOISO: "virtio-win.iso", VirtIOISOPool: "east-isos"},
		{ExistingDiskPool: "east-disks", ExistingDiskName: "data.qcow2"},
	}
	for i, req := range good {
		r := req
		if e := validateRemoteMediaNames(&r); e != nil {
			t.Errorf("good #%d %+v rejected: %+v", i, req, e)
		}
	}
}

func TestResolveRemoteMediaFromNodeVolumes(t *testing.T) {
	src := eastNode()
	req := models.CreateVMRequest{ISO: "debian.iso", VirtIOISO: "virtio-win.iso", VirtIOISOPool: "east-isos",
		ExistingDiskPool: "east-disks", ExistingDiskName: "data.qcow2"}
	if e := resolveRemoteMedia(src, &req); e != nil {
		t.Fatalf("unexpected error %+v", e)
	}
	if req.ISO != "/srv/isos/debian.iso" || req.VirtIOISO != "/srv/isos/virtio-win.iso" {
		t.Fatalf("media not rewritten to node volume paths: %q %q", req.ISO, req.VirtIOISO)
	}
	if req.ExistingDiskPool != "east-disks" || req.ExistingDiskName != "data.qcow2" {
		t.Fatalf("existing disk mutated: %+v", req)
	}
	// Each pool is listed at most once per request.
	for p, n := range src.calls {
		if n != 1 {
			t.Errorf("pool %s listed %d times", p, n)
		}
	}
}

func TestResolveRemoteMediaPoolScopedAndAmbiguous(t *testing.T) {
	req := models.CreateVMRequest{ISO: "dup.iso"}
	e := resolveRemoteMedia(eastNode(), &req)
	if e == nil || e.status != http.StatusConflict || e.code != "media_ambiguous" ||
		strings.Join(e.pools, ",") != "east-extra,east-isos" {
		t.Fatalf("ambiguous: %+v", e)
	}
	req = models.CreateVMRequest{ISO: "dup.iso", ISOPool: "east-extra"}
	if e := resolveRemoteMedia(eastNode(), &req); e != nil || req.ISO != "/srv/extra/dup.iso" {
		t.Fatalf("pool-scoped: %+v %q", e, req.ISO)
	}
}

func TestResolveRemoteMediaTypedFailures(t *testing.T) {
	cases := []struct {
		name   string
		req    models.CreateVMRequest
		src    *fakeNodeVolumes
		status int
		code   string
	}{
		{"iso missing", models.CreateVMRequest{ISO: "nope.iso"}, eastNode(), 404, "media_not_found"},
		{"iso pool missing", models.CreateVMRequest{ISO: "debian.iso", ISOPool: "ghost"}, eastNode(), 404, "media_not_found"},
		{"iso in other pool", models.CreateVMRequest{ISO: "debian.iso", ISOPool: "east-extra"}, eastNode(), 404, "media_not_found"},
		{"virtio missing", models.CreateVMRequest{VirtIOISO: "nope.iso"}, eastNode(), 404, "media_not_found"},
		{"disk pool missing", models.CreateVMRequest{ExistingDiskPool: "ghost", ExistingDiskName: "data.qcow2"}, eastNode(), 404, "existing_disk_not_found"},
		{"disk missing", models.CreateVMRequest{ExistingDiskPool: "east-disks", ExistingDiskName: "nope.qcow2"}, eastNode(), 404, "existing_disk_not_found"},
		{"disk is a snapshot view", models.CreateVMRequest{ExistingDiskPool: "east-disks", ExistingDiskName: "vm1.snap1"}, eastNode(), 404, "existing_disk_not_found"},
		{"disk in inactive pool", models.CreateVMRequest{ExistingDiskPool: "east-off", ExistingDiskName: "data.qcow2"}, eastNode(), 404, "existing_disk_not_found"},
		{"disk in ISO pool", models.CreateVMRequest{ExistingDiskPool: "east-isos", ExistingDiskName: "debian.iso"}, eastNode(), 400, "existing_disk_pool_invalid"},
		{"node storage down", models.CreateVMRequest{ISO: "debian.iso"}, &fakeNodeVolumes{poolsErr: errors.New("rpc")}, 502, "node_storage_unavailable"},
	}
	for _, c := range cases {
		r := c.req
		e := resolveRemoteMedia(c.src, &r)
		if e == nil || e.status != c.status || e.code != c.code {
			t.Errorf("%s: got %+v want %d %s", c.name, e, c.status, c.code)
		}
	}
}

func TestResolveRemoteMediaNoMediaTouchesNothing(t *testing.T) {
	src := &fakeNodeVolumes{poolsErr: errors.New("must not be called")}
	req := models.CreateVMRequest{Name: "blank"}
	if e := resolveRemoteMedia(src, &req); e != nil || len(src.calls) != 0 {
		t.Fatalf("blank-disk create must not list storage: %+v %v", e, src.calls)
	}
}

// postCreate POSTs a CreateVM body as role to the dispatch fixture handler.
func postCreate(t *testing.T, f dispatchFixture, role string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/vms", bytes.NewReader(b))
	req.Header.Set("X-Role", role)
	req.Header.Set("X-User", "u1")
	res := httptest.NewRecorder()
	f.h.CreateVM(res, req)
	out := map[string]any{}
	_ = json.Unmarshal(res.Body.Bytes(), &out)
	return res, out
}

func TestCreateVMRemoteCloudInitTypedError(t *testing.T) {
	f := newDispatchFixture(t)
	res, b := postCreate(t, f, models.RoleAdmin, map[string]any{
		"name": "vm1", "node": f.remote.ID, "network": "vmbr0",
		"cloud_init": map[string]any{"user": "ops"},
	})
	if res.Code != http.StatusNotImplemented || b["code"] != "cloud_init_remote_unsupported" ||
		b["hint"] != "open_node_ui" || b["node"] != "remote" {
		t.Fatalf("got %d %v", res.Code, b)
	}
}

func TestCreateVMRemoteRejectsPathsBeforeConnecting(t *testing.T) {
	f := newDispatchFixture(t)
	// Any connect attempt would surface as a node error instead of the 400.
	*f.fail = errors.New("must not connect")
	for _, body := range []map[string]any{
		{"iso": "/etc/shadow"},
		{"iso": "../../etc/passwd.iso"},
		{"virtio_iso": "/var/lib/libvirt/images/virtio.iso"},
		{"existing_disk_pool": "east-disks", "existing_disk_name": "/dev/sda"},
		{"iso": "debian.iso", "iso_pool": "../x"},
	} {
		body["name"], body["node"], body["network"] = "vm1", f.remote.ID, "vmbr0"
		res, b := postCreate(t, f, models.RoleAdmin, body)
		if res.Code != http.StatusBadRequest || b["code"] != "invalid_media_name" {
			t.Errorf("%v: got %d %v", body, res.Code, b)
		}
	}
}

func TestCreateVMRemoteMediaKeepsGuards(t *testing.T) {
	f := newDispatchFixture(t)
	// Non-admins are refused before anything else, media or not.
	if res, _ := postCreate(t, f, models.RoleOperator, map[string]any{
		"name": "vm1", "node": f.remote.ID, "network": "vmbr0", "iso": "debian.iso",
	}); res.Code != http.StatusForbidden {
		t.Fatalf("operator: %d", res.Code)
	}
	// A valid media name no longer answers 501: it goes on to the
	// explicit-network rule and then to the node connection.
	res, b := postCreate(t, f, models.RoleAdmin, map[string]any{
		"name": "vm1", "node": f.remote.ID, "iso": "debian.iso",
	})
	if res.Code != http.StatusBadRequest || b["code"] != "network_required" {
		t.Fatalf("network rule: %d %v", res.Code, b)
	}
	*f.fail = errors.New("dial tcp: connection refused")
	res, b = postCreate(t, f, models.RoleAdmin, map[string]any{
		"name": "vm1", "node": f.remote.ID, "network": "vmbr0", "iso": "debian.iso",
		"existing_disk_pool": "east-disks", "existing_disk_name": "data.qcow2",
	})
	if res.Code == http.StatusNotImplemented || b["code"] == "remote_unsupported" || res.Code < 400 {
		t.Fatalf("media should reach the node connection, got %d %v", res.Code, b)
	}
}

// The ISO and volume listings the create form uses for a remote node honour
// ?node= with the node-selector guards (403 before 404, admin-only).
func TestISOAndVolumeListingsHonourNodeSelector(t *testing.T) {
	f := newDispatchFixture(t)
	for _, hf := range []http.HandlerFunc{
		f.h.withNodeSelector(func(x *Handler) http.HandlerFunc { return x.ListISOs }),
		f.h.withNodeSelector(func(x *Handler) http.HandlerFunc { return x.ListVolumes }),
	} {
		for _, role := range []string{models.RoleOperator, ""} {
			for _, n := range []string{"remote", "ghost"} {
				if res := doSel(hf, "/x?node="+n, role); res.Code != http.StatusForbidden {
					t.Fatalf("%s as %q: %d want 403", n, role, res.Code)
				}
			}
		}
		if res := doSel(hf, "/x?node=ghost", models.RoleAdmin); res.Code != http.StatusNotFound || decode(t, res)["code"] != "node_not_found" {
			t.Fatalf("ghost admin: %d %v", res.Code, decode(t, res))
		}
	}
}
