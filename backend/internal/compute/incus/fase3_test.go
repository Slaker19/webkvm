package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// fakeLXD3 is a stateful minimal LXD daemon (unix socket) supporting the
// Fase 3 surface: server info, list/get/create/update/rename instances,
// the operation endpoints and the raw export stream.
type fakeLXD3 struct {
	mu         sync.Mutex
	instances  map[string]*api.Instance
	etag       map[string]string
	backups    map[string]map[string]bool // instance -> backup name -> exists
	seq        int
	exportData []byte
	exportHits int
	deletes    int

	lastCreate *api.InstancesPost
	lastPut    *api.InstancePut
	lastRename *api.InstancePost

	// profiles mirrors a real Incus install, where the "default" profile
	// already carries the root disk and its pool. CreateDomain reads this
	// to size the root disk without inventing a pool name.
	profiles map[string]*api.Profile
}

func newFakeLXD3(t *testing.T, initial map[string]*api.Instance, exportData []byte) (sock string, srv *fakeLXD3) {
	t.Helper()
	srv = &fakeLXD3{
		instances:  map[string]*api.Instance{},
		etag:       map[string]string{},
		backups:    map[string]map[string]bool{},
		exportData: exportData,
		profiles: map[string]*api.Profile{
			"default": {
				Name: "default",
				ProfilePut: api.ProfilePut{
					Devices: map[string]map[string]string{
						"root": {"type": "disk", "path": "/", "pool": "webkvm-incus"},
					},
				},
			},
		},
	}
	for name, inst := range initial {
		c := *inst
		c.Config = cloneMap(inst.Config)
		c.Devices = inst.Devices
		srv.instances[name] = &c
		srv.etag[name] = `"etag-` + name + `"`
	}
	dir := t.TempDir()
	sock = filepath.Join(dir, "unix.socket")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/1.0", srv.handleServer)
	mux.HandleFunc("/1.0/instances", srv.handleInstances)
	mux.HandleFunc("/1.0/instances/", srv.handleInstance)
	mux.HandleFunc("/1.0/operations/", srv.handleOperation)
	mux.HandleFunc("/1.0/profiles/", srv.handleProfile)
	go http.Serve(l, mux)
	t.Cleanup(func() { _ = l.Close(); _ = os.Remove(sock) })
	return sock, srv
}

func (f *fakeLXD3) op(id string, status string) string {
	class := "task"
	if status == "Success" {
		status = "Success"
	}
	return fmt.Sprintf(`{"id":"%s","class":"%s","description":"","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:01Z","status":"%s","status_code":%d,"err":"","location":""}`,
		id, class, status, map[string]int{"Success": 200, "Failure": 400, "Running": 100}[status])
}

func (f *fakeLXD3) handleServer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"type":"sync","status":"Success","status_code":200,"metadata":{"api_extensions":["instances","snapshots","resources","networks","container_backup","instance_create_start"],"api_status":"stable","api_version":"1.0","auth":"trusted","public":false,"environment":{"server_version":"6.9.0-fake","certificate":""}}}`)
}

func (f *fakeLXD3) handleInstances(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		list := make([]api.Instance, 0, len(f.instances))
		for _, inst := range f.instances {
			list = append(list, *inst)
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		meta, _ := json.Marshal(list)
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, meta)
	case http.MethodPost:
		var post api.InstancesPost
		if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
			http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.seq++
		id := fmt.Sprintf("op-%d", f.seq)
		f.lastCreate = &post
		inst := &api.Instance{
			Name:   post.Name,
			Type:   string(post.Type),
			Status: "Stopped",
			InstancePut: api.InstancePut{
				Config:  cloneMap(post.Config),
				Devices: post.Devices,
			},
		}
		f.instances[post.Name] = inst
		f.etag[post.Name] = `"etag-` + post.Name + `"`
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"async","status":"Success","status_code":100,"metadata":%s}`, f.op(id, "Success"))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (f *fakeLXD3) handleInstance(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/1.0/instances/")
	if rest == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	// /1.0/instances/{name}/backups[/{backup}[/export]]
	if strings.Contains(rest, "/backups") {
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) < 2 {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		name, tail := parts[0], ""
		if len(parts) == 3 {
			tail = parts[2]
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.instances[name]; !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		switch {
		case tail == "" && r.Method == http.MethodPost:
			// POST /1.0/instances/{name}/backups — create backup.
			var post api.InstanceBackupsPost
			if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
				http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
				return
			}
			if f.backups[name] == nil {
				f.backups[name] = map[string]bool{}
			}
			bname := post.Name
			if bname == "" {
				bname = "backup0"
			}
			f.backups[name][bname] = true
			f.seq++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"type":"async","status":"Success","status_code":100,"metadata":%s}`, f.op(fmt.Sprintf("op-%d", f.seq), "Success"))
		case strings.HasSuffix(tail, "/export"):
			// GET .../backups/{backup}/export — raw stream.
			bname := strings.TrimSuffix(tail, "/export")
			if !f.backups[name][bname] {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			f.exportHits++
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(f.exportData)
		case r.Method == http.MethodDelete:
			// DELETE .../backups/{backup}.
			if !f.backups[name][tail] {
				http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
				return
			}
			delete(f.backups[name], tail)
			f.deletes++
			f.seq++
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"type":"async","status":"Success","status_code":100,"metadata":%s}`, f.op(fmt.Sprintf("op-%d", f.seq), "Success"))
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	if strings.HasSuffix(rest, "/export") {
		name := strings.TrimSuffix(rest, "/export")
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.instances[name]; !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		f.exportHits++
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(f.exportData)
		return
	}
	switch r.Method {
	case http.MethodGet:
		f.mu.Lock()
		inst, ok := f.instances[rest]
		var body []byte
		var etag string
		if ok {
			body, _ = json.Marshal(inst)
			etag = f.etag[rest]
		}
		f.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, body)
	case http.MethodPut:
		var put api.InstancePut
		if err := json.NewDecoder(r.Body).Decode(&put); err != nil {
			http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		inst, ok := f.instances[rest]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		f.lastPut = &put
		inst.Config = cloneMap(put.Config)
		inst.Devices = put.Devices
		if put.Description != "" {
			inst.Description = put.Description
		}
		f.seq++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"async","status":"Success","status_code":100,"metadata":%s}`, f.op(fmt.Sprintf("op-%d", f.seq), "Success"))
	case http.MethodPost:
		var post api.InstancePost
		if err := json.NewDecoder(r.Body).Decode(&post); err != nil {
			http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		inst, ok := f.instances[rest]
		if !ok {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		f.lastRename = &post
		if post.Name != "" {
			delete(f.instances, rest)
			inst.Name = post.Name
			f.instances[post.Name] = inst
			f.etag[post.Name] = `"etag-` + post.Name + `"`
		}
		f.seq++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"type":"async","status":"Success","status_code":100,"metadata":%s}`, f.op(fmt.Sprintf("op-%d", f.seq), "Success"))
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleProfile serves GET /1.0/profiles/<name>, the lookup CreateDomain
// uses to find which pool the instance's root disk already lands on.
func (f *fakeLXD3) handleProfile(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/1.0/profiles/")
	f.mu.Lock()
	prof, ok := f.profiles[name]
	f.mu.Unlock()
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	body, _ := json.Marshal(prof)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, body)
}

func (f *fakeLXD3) handleOperation(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/1.0/operations/")
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"type":"sync","status":"Success","status_code":200,"metadata":%s}`, f.op(id, "Success"))
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// TestParseImageRef splits remote:alias references and rejects junk.
func TestParseImageRef(t *testing.T) {
	server, alias, err := parseImageRef("ubuntu:24.04")
	if err != nil || server != "https://cloud-images.ubuntu.com/releases" || alias != "24.04" {
		t.Errorf("ubuntu:24.04 -> %q/%q (%v)", server, alias, err)
	}
	server, alias, err = parseImageRef("images:alpine/3.20")
	if err != nil || !strings.HasSuffix(server, "images.linuxcontainers.org") || alias != "alpine/3.20" {
		t.Errorf("images:alpine/3.20 -> %q/%q (%v)", server, alias, err)
	}
	server, alias, err = parseImageRef("https://cloud-images.ubuntu.com/releases:24.04")
	if err != nil || alias != "24.04" {
		t.Errorf("full-url:alias -> %q/%q (%v)", server, alias, err)
	}
	if _, _, err := parseImageRef("bogus:24.04"); err == nil || !strings.Contains(err.Error(), "unknown LXD image remote") {
		t.Errorf("unknown remote must be rejected, got %v", err)
	}
	if _, _, err := parseImageRef("ubuntu"); err == nil {
		t.Error("bare reference without alias must be rejected")
	}
}

// TestIsImageFingerprint pins which bare refs name a LOCAL image.
//
// ListIncusImages hands back the short fingerprint as the ref of any
// cached image that carries no alias, so this is the shape that comes
// straight back out of our own catalog and must be accepted. Everything
// that merely looks hex-ish (wrong length, uppercase-only concerns
// aside, non-hex characters) must not be, or a typo'd alias would be
// mistaken for an image ID.
func TestIsImageFingerprint(t *testing.T) {
	for _, ref := range []string{
		"845ea2433473", // short form, as the API returns it
		"769902B13A64", // case-insensitive
		strings.Repeat("a", 64),
		"  845ea2433473  ", // surrounding blanks are not the user's fault
	} {
		if !isImageFingerprint(ref) {
			t.Errorf("%q should be recognised as a fingerprint", ref)
		}
	}
	for _, ref := range []string{
		"",
		"images:ubuntu/24.04",
		"ubuntu:24.04",
		"845ea243347",   // 11 chars
		"845ea24334733", // 13 chars
		"845ea2433z73",  // z is not hex
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
	} {
		if isImageFingerprint(ref) {
			t.Errorf("%q must NOT be treated as a fingerprint", ref)
		}
	}
}

// TestIncusBackendCreateDomain is the Fase 3 creation gate: image-based
// creation with cloud-init injected as native config keys. WebKVM requires
// a PHYSICAL bridge (vmbr0/br0), so the test needs one on the host.
func TestIncusBackendCreateDomain(t *testing.T) {
	real := findAnyPhysicalBridge()
	if real == "" {
		t.Skip("no physical Linux bridge on the host; container creation requires shared L2")
	}
	sock, fake := newFakeLXD3(t, nil, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	vm, err := b.CreateDomain(models.CreateVMRequest{
		Name:   "web",
		Image:  "ubuntu:24.04",
		VCPUs:  2,
		RAMMB:  2048,
		DiskGB: 10,
		CloudInit: &models.CloudInitRequest{
			User: "demo", Password: "s3cret",
			SSHKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAII8RqcYyIx5t4W8q3kY9v4VQ6rZ0JQ9bKjzF3sM7G3qY demo@web",
			Hostname: "web",
		},
	})
	if err != nil {
		t.Fatalf("create container: %v", err)
	}
	fake.mu.Lock()
	post := fake.lastCreate
	fake.mu.Unlock()
	if post == nil {
		t.Fatal("no create request captured")
	}
	if post.Name != "web" || post.Type != api.InstanceTypeContainer {
		t.Errorf("post name/type = %q/%q", post.Name, post.Type)
	}
	if post.Source.Type != "image" || post.Source.Protocol != "simplestreams" ||
		post.Source.Server != "https://cloud-images.ubuntu.com/releases" || post.Source.Alias != "24.04" {
		t.Errorf("image source = %+v", post.Source)
	}
	if post.Config["limits.cpu"] != "2" || post.Config["limits.memory"] != "2048MiB" {
		t.Errorf("limits = %q/%q", post.Config["limits.cpu"], post.Config["limits.memory"])
	}
	if post.Config["boot.autostart"] != "true" {
		t.Error("autostart must default to true")
	}
	// Cloud-init injected into the native keys (Fase 2 mapper).
	ud := post.Config["user.user-data"]
	if ud == "" || !strings.Contains(ud, "#cloud-config") || !strings.Contains(ud, "demo") {
		t.Errorf("user.user-data not injected: %q", ud)
	}
	if nc := post.Config["user.network-config"]; nc == "" || !strings.Contains(nc, "dhcp4") {
		t.Errorf("user.network-config not injected: %q", nc)
	}
	// Devices: root disk with the requested size + shared-L2 NIC. With no
	// explicit network, the container lands on the host's physical bridge
	// (vmbr0/br0). The NIC is strictly eth0 (overrides the profile's NIC).
	wantParent := incusMainBridge()
	if wantParent == "" {
		t.Fatal("expected a physical bridge (test would have skipped otherwise)")
	}
	if eth0 := post.Devices["eth0"]; eth0["parent"] != wantParent || eth0["nictype"] != "bridged" || eth0["name"] != "eth0" {
		t.Errorf("eth0 device = %+v (want parent %q, name eth0)", post.Devices["eth0"], wantParent)
	}
	if len(post.Devices) != 2 {
		t.Errorf("expected exactly root + eth0 (single NIC, no eth1 duplicate), got %d devices: %v", len(post.Devices), post.Devices)
	}
	// The root device overrides the profile's own, so it must name the
	// pool the profile already uses. Hardcoding "default" here made every
	// create fail with "Storage pool not found" on a stock Incus install,
	// where no pool by that name exists.
	if root := post.Devices["root"]; root["pool"] != "webkvm-incus" || root["size"] != "10GB" {
		t.Errorf("root device = %+v (want pool webkvm-incus, size 10GB)", root)
	}
	if vm.ID != "web" || vm.Name != "web" || vm.Hypervisor != "incus" {
		t.Errorf("mapped vm = %+v", vm)
	}
}

// TestIncusBackendCreateDomainWithoutCloudInit ensures a container created
// with the cloud-init fields left blank still gets a user.network-config
// key covering its NICs (eth0), same as one created with cloud-init. Without
// this, a later AttachNetworkIface call has no existing network-config to
// regenerate from and a second NIC never gets DHCPed in the guest — see
// TestIncusBackendAttachNetworkIfaceWithoutPriorCloudInit.
func TestIncusBackendCreateDomainWithoutCloudInit(t *testing.T) {
	real := findAnyPhysicalBridge()
	if real == "" {
		t.Skip("no physical Linux bridge on the host; container creation requires shared L2")
	}
	sock, fake := newFakeLXD3(t, nil, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if _, err := b.CreateDomain(models.CreateVMRequest{
		Name: "web2", Image: "ubuntu:24.04", VCPUs: 1, RAMMB: 1024, DiskGB: 10,
	}); err != nil {
		t.Fatalf("create container: %v", err)
	}
	fake.mu.Lock()
	post := fake.lastCreate
	fake.mu.Unlock()
	if post == nil {
		t.Fatal("no create request captured")
	}
	if post.Config["user.user-data"] != "" {
		t.Errorf("user.user-data should be empty with no cloud-init request, got %q", post.Config["user.user-data"])
	}
	if nc := post.Config["user.network-config"]; nc == "" || !strings.Contains(nc, "eth0:") {
		t.Errorf("user.network-config must still be set for eth0 even without cloud-init, got: %q", nc)
	}
}

// TestIncusBackendCreateDomainRootPool covers how the root disk's storage
// pool is chosen, which is what decides whether a create succeeds at all:
// an explicit request wins, otherwise the pool is inherited from the
// profile, and when nothing supplies one the root device is omitted so the
// profile chain keeps whatever it already had.
func TestIncusBackendCreateDomainRootPool(t *testing.T) {
	if findAnyPhysicalBridge() == "" {
		t.Skip("no physical Linux bridge on the host; container creation requires shared L2")
	}

	t.Run("explicit pool wins over profile", func(t *testing.T) {
		sock, fake := newFakeLXD3(t, nil, nil)
		b, err := NewIncusBackend(sock)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		if _, err := b.CreateDomain(models.CreateVMRequest{
			Name: "web", Image: "ubuntu:24.04", DiskGB: 10, StoragePool: "Lexar-Contenedores",
		}); err != nil {
			t.Fatalf("create container: %v", err)
		}
		fake.mu.Lock()
		root := fake.lastCreate.Devices["root"]
		fake.mu.Unlock()
		if root["pool"] != "Lexar-Contenedores" {
			t.Errorf("root pool = %q, want the operator's explicit choice", root["pool"])
		}
	})

	t.Run("last profile defining root wins", func(t *testing.T) {
		sock, fake := newFakeLXD3(t, nil, nil)
		fake.profiles["fast"] = &api.Profile{
			Name: "fast",
			ProfilePut: api.ProfilePut{
				Devices: map[string]map[string]string{
					"root": {"type": "disk", "path": "/", "pool": "nvme"},
				},
			},
		}
		b, err := NewIncusBackend(sock)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		if _, err := b.CreateDomain(models.CreateVMRequest{
			Name: "web", Image: "ubuntu:24.04", DiskGB: 10,
			Profiles: []string{"default", "fast"},
		}); err != nil {
			t.Fatalf("create container: %v", err)
		}
		fake.mu.Lock()
		root := fake.lastCreate.Devices["root"]
		fake.mu.Unlock()
		if root["pool"] != "nvme" {
			t.Errorf("root pool = %q; Incus applies profiles in order, so the last one wins", root["pool"])
		}
	})

	t.Run("no pool anywhere leaves the profile root alone", func(t *testing.T) {
		sock, fake := newFakeLXD3(t, nil, nil)
		fake.profiles["default"] = &api.Profile{Name: "default"}
		b, err := NewIncusBackend(sock)
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		if _, err := b.CreateDomain(models.CreateVMRequest{
			Name: "web", Image: "ubuntu:24.04", DiskGB: 10,
		}); err != nil {
			t.Fatalf("create container: %v", err)
		}
		fake.mu.Lock()
		devices := fake.lastCreate.Devices
		fake.mu.Unlock()
		if _, ok := devices["root"]; ok {
			t.Errorf("root device = %+v; with no pool to name, inventing one only breaks the create", devices["root"])
		}
	})
}

// TestIncusBackendCreateDomainGPU covers container GPU sharing. The
// device is deliberately unfiltered: it grants the container the host's
// render nodes without detaching anything, which is what separates it
// from a VM's exclusive PCI passthrough.
func TestIncusBackendCreateDomainGPU(t *testing.T) {
	if findAnyPhysicalBridge() == "" {
		t.Skip("no physical Linux bridge on the host; container creation requires shared L2")
	}
	yes, no := true, false
	for name, tc := range map[string]struct {
		gpu  *bool
		want bool
	}{
		"requested":   {&yes, true},
		"declined":    {&no, false},
		"unspecified": {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			sock, fake := newFakeLXD3(t, nil, nil)
			b, err := NewIncusBackend(sock)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			if _, err := b.CreateDomain(models.CreateVMRequest{
				Name: "web", Image: "ubuntu:24.04", DiskGB: 10, GPU: tc.gpu,
			}); err != nil {
				t.Fatalf("create container: %v", err)
			}
			fake.mu.Lock()
			dev, present := fake.lastCreate.Devices["gpu0"]
			fake.mu.Unlock()
			if present != tc.want {
				t.Fatalf("gpu device present = %v, want %v", present, tc.want)
			}
			if tc.want && dev["type"] != "gpu" {
				t.Errorf("gpu device = %+v, want type gpu", dev)
			}
		})
	}
}

// TestNeedsCloudInit pins which seed fields actually require cloud-init
// to be present in the guest. Hostname is set by Incus itself, so it must
// not drag in a different image; everything else is delivered by
// cloud-init alone.
func TestNeedsCloudInit(t *testing.T) {
	if needsCloudInit(nil) {
		t.Error("no cloud-init request means nothing to run")
	}
	if needsCloudInit(&models.CloudInitRequest{Hostname: "web"}) {
		t.Error("hostname is applied by Incus; it must not force a cloud image")
	}
	for name, ci := range map[string]*models.CloudInitRequest{
		"user":      {User: "demo"},
		"password":  {Password: "s3cret"},
		"ssh key":   {SSHKey: "ssh-ed25519 AAAA"},
		"provision": {ProvisionScript: "#!/bin/bash\n"},
		"user-data": {CustomUserData: "#cloud-config\n"},
		"snippet":   {SnippetID: "snip-1"},
	} {
		if !needsCloudInit(ci) {
			t.Errorf("%s is delivered only by cloud-init, so it must require it", name)
		}
	}
}

// TestCloudVariantAlias covers the alias rewrite in isolation. An
// unreachable image server must leave the alias untouched: refusing to
// create because a *better* image could not be confirmed would be worse
// than creating with the image the caller actually asked for.
func TestCloudVariantAlias(t *testing.T) {
	const dead = "http://127.0.0.1:1"
	if got := cloudVariantAlias(dead, "debian/13"); got != "debian/13" {
		t.Errorf("unreachable server: alias = %q, want it left alone", got)
	}
	if got := cloudVariantAlias(dead, "debian/13/cloud"); got != "debian/13/cloud" {
		t.Errorf("already a cloud variant: alias = %q, want no double suffix", got)
	}
	if got := cloudVariantAlias(dead, ""); got != "" {
		t.Errorf("empty alias = %q, want empty", got)
	}
}

// TestIncusBackendCreateDomainNoImage: creating a container without an
// image is a clean validation error, not a 501.
func TestIncusBackendCreateDomainNoImage(t *testing.T) {
	b := &IncusBackend{}
	_, err := b.CreateDomain(models.CreateVMRequest{Name: "x"})
	if err == nil || strings.Contains(err.Error(), "image reference") == false {
		t.Fatalf("expected image-required error, got %v", err)
	}
}

// TestIncusBackendUpdateDomain covers rename + CPU/RAM limits and the
// rejection of KVM-only fields.
func TestIncusBackendUpdateDomain(t *testing.T) {
	sock, fake := newFakeLXD3(t, map[string]*api.Instance{
		"web": {Name: "web", Type: string(api.InstanceTypeContainer), Status: "Stopped",
			InstancePut: api.InstancePut{Config: map[string]string{"limits.cpu": "1", "limits.memory": "512MiB"}}},
	}, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	vcpus, ram := 4, int64(4096)
	vm, err := b.UpdateDomain("web", models.UpdateVMRequest{VCPUs: &vcpus, RAMMB: &ram})
	if err != nil {
		t.Fatalf("update limits: %v", err)
	}
	fake.mu.Lock()
	put := fake.lastPut
	fake.mu.Unlock()
	if put == nil || put.Config["limits.cpu"] != "4" || put.Config["limits.memory"] != "4096MiB" {
		t.Errorf("limits not updated: %+v", put)
	}
	// Rename through the same call.
	newName := "web-prod"
	vm, err = b.UpdateDomain("web", models.UpdateVMRequest{Name: &newName})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	fake.mu.Lock()
	rn := fake.lastRename
	fake.mu.Unlock()
	if rn == nil || rn.Name != "web-prod" {
		t.Errorf("rename body = %+v", rn)
	}
	if vm.Name != "web-prod" {
		t.Errorf("mapped name = %q", vm.Name)
	}
	// A KVM-only field must not be silently ignored.
	chipset := "q35"
	if _, err := b.UpdateDomain("web-prod", models.UpdateVMRequest{Chipset: &chipset}); !strings.Contains(err.Error(), "not applicable") {
		t.Errorf("chipset should be rejected, got %v", err)
	}
}

// TestIncusBackendVMMeta round-trips tags and the rest of the metadata
// through the user.webkvm.tags / user.webkvm.desc config keys.
func TestIncusBackendVMMeta(t *testing.T) {
	sock, fake := newFakeLXD3(t, map[string]*api.Instance{
		"web": {Name: "web", Type: string(api.InstanceTypeContainer), Status: "Stopped",
			InstancePut: api.InstancePut{Config: map[string]string{}}},
	}, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	meta := models.VMMeta{
		Alias: "prod-web", Notes: "el contenedor de producción",
		Groups: []string{"frontend"}, Tags: []string{"prod", "web"},
		OwnerID: "demo",
	}
	if err := b.SetVMMeta("web", meta); err != nil {
		t.Fatalf("set meta: %v", err)
	}
	fake.mu.Lock()
	put := fake.lastPut
	fake.mu.Unlock()
	if put.Config[metaTagsKey] != "prod,web" {
		t.Errorf("tags key = %q, want prod,web", put.Config[metaTagsKey])
	}
	if !strings.Contains(put.Config[metaDescKey], `"alias":"prod-web"`) ||
		!strings.Contains(put.Config[metaDescKey], `"owner_id":"demo"`) {
		t.Errorf("desc key = %q", put.Config[metaDescKey])
	}

	got, err := b.GetVMMeta("web")
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	if got.Alias != "prod-web" || got.OwnerID != "demo" || len(got.Groups) != 1 || got.Groups[0] != "frontend" {
		t.Errorf("meta mismatch: %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "prod" || got.Tags[1] != "web" {
		t.Errorf("tags mismatch: %v", got.Tags)
	}

	// Partial update: replace the tag set, leave the rest untouched.
	newTags := []string{"web"}
	updated, err := b.UpdateVMMeta("web", models.VMMetaUpdate{Tags: &newTags})
	if err != nil {
		t.Fatalf("update meta: %v", err)
	}
	if len(updated.Tags) != 1 || updated.Tags[0] != "web" {
		t.Errorf("updated tags = %v", updated.Tags)
	}
	if updated.Alias != "prod-web" {
		t.Errorf("alias must survive a tags-only update, got %q", updated.Alias)
	}
	if updated.UpdatedAt == 0 {
		t.Error("UpdatedAt should be set")
	}

	// Clearing the tags removes the key.
	clearTags := []string{}
	if _, err := b.UpdateVMMeta("web", models.VMMetaUpdate{Tags: &clearTags}); err != nil {
		t.Fatalf("clear tags: %v", err)
	}
	fake.mu.Lock()
	put2 := fake.lastPut
	fake.mu.Unlock()
	if _, present := put2.Config[metaTagsKey]; present {
		t.Error("empty tag set must delete the tags key")
	}
}

// TestIncusBackendExportDomain is the streaming gate: the container's
// native LXD export is copied byte-for-byte into w with a single GET —
// no double buffering, no temp download.
func TestIncusBackendExportDomain(t *testing.T) {
	// Real gzip bytes (garbage is fine: we only assert the stream).
	export := []byte("\x1f\x8b\x08\x00fake-container-export-stream-bytes-0123456789")
	sock, fake := newFakeLXD3(t, map[string]*api.Instance{
		"web": {Name: "web", Type: string(api.InstanceTypeContainer), Status: "Running",
			InstancePut: api.InstancePut{
				Config: map[string]string{},
				Devices: map[string]map[string]string{
					"root": {"type": "disk", "path": "/", "size": "10GB"},
				},
			}},
	}, export)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	var buf bytes.Buffer
	res, err := b.ExportDomain(context.Background(), "web", compute.ExportBackupOptions{Compress: "gzip"}, &buf)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	fake.mu.Lock()
	hits := fake.exportHits
	dels := fake.deletes
	backups := len(fake.backups["web"])
	fake.mu.Unlock()
	if hits != 1 {
		t.Fatalf("export endpoint hit %d times, want exactly 1 (single stream)", hits)
	}
	if dels != 1 {
		t.Errorf("temporary backup must be deleted after streaming (deletes=%d)", dels)
	}
	if backups != 0 {
		t.Errorf("temporary backup left behind: %d", backups)
	}
	if !bytes.Equal(buf.Bytes(), export) {
		t.Errorf("stream differs from the daemon's (len %d vs %d)", buf.Len(), len(export))
	}
	if res.TotalBytes != int64(len(export)) || res.DisksIncluded != 1 {
		t.Errorf("producer result = %+v", res)
	}

	// zstd request must be re-encoded with a streaming compressor.
	var zbuf bytes.Buffer
	if _, err := b.ExportDomain(context.Background(), "web", compute.ExportBackupOptions{Compress: "zstd", ZstdLevel: 19}, &zbuf); err != nil {
		t.Fatalf("zstd export: %v", err)
	}
	if !bytes.HasPrefix(zbuf.Bytes(), []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Error("zstd export must start with the zstd magic")
	}

	// Size estimate from the root disk device.
	est, err := b.EstimateExportSize(context.Background(), "web", true)
	if err != nil || est != 10*int64(1<<30) {
		t.Errorf("estimate = %d (%v), want 10GiB", est, err)
	}
}

// TestIncusBackendExportDomainMissingInstance: exporting a non-existent
// container fails cleanly (the daemon 404s).
func TestIncusBackendExportDomainMissingInstance(t *testing.T) {
	sock, _ := newFakeLXD3(t, nil, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := b.ExportDomain(context.Background(), "nope", compute.ExportBackupOptions{}, &buf); err == nil {
		t.Fatal("expected error for a missing instance")
	}
}

// TestInstanceToVMTagsAndAlias: the unified list surfaces webkvm tags
// (RBAC) and the alias straight from the config keys.
func TestInstanceToVMTagsAndAlias(t *testing.T) {
	inst := &api.Instance{
		Name: "web", Status: "Running", Type: string(api.InstanceTypeContainer),
		InstancePut: api.InstancePut{Config: map[string]string{
			metaTagsKey: "prod,web",
			metaDescKey: `{"alias":"prod-web","owner_id":"demo"}`,
		}},
	}
	vm := instanceToVM(inst)
	if len(vm.Tags) != 2 || vm.Tags[0] != "prod" || vm.Tags[1] != "web" {
		t.Errorf("tags = %v", vm.Tags)
	}
	if vm.Alias != "prod-web" {
		t.Errorf("alias = %q", vm.Alias)
	}
}

// Ensure the whole exported surface still satisfies the seam.
var _ = io.Discard
