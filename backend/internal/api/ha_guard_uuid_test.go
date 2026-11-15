package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

const webUUID = "6f1c2a4e-1111-4222-8333-944455556666"

// uuidBackend resolves a domain by UUID or name, like libvirt's lookupDomain.
type uuidBackend struct {
	compute.Backend
	vms     []models.VM
	lookups int
}

func (b *uuidBackend) GetDomain(id string) (models.VM, error) {
	b.lookups++
	for _, vm := range b.vms {
		if vm.ID == id || vm.Name == id {
			return vm, nil
		}
	}
	return models.VM{}, errors.Join(compute.ErrNotFound, errors.New("no domain"))
}

func guardHandler(t *testing.T) (*Handler, *uuidBackend) {
	t.Helper()
	useRunner(t, &wlRunner{hasCP: true, vm: true, started: true})
	h := haModuleHandler(t)
	be := &uuidBackend{vms: []models.VM{
		{ID: webUUID, Name: "web", Hypervisor: "kvm"},
		{ID: "0bad0bad-0000-4000-8000-000000000000", Name: "free", Hypervisor: "kvm"},
	}}
	h.compute = be
	return h, be
}

func guardRejects(h *Handler, target, id string) (bool, string) {
	rec := httptest.NewRecorder()
	ok := h.rejectPacemakerManaged(rec, withVMID(httptest.NewRequest(http.MethodPost, target, nil), id), id)
	return ok, rec.Body.String()
}

// v11 live defect: the UI addresses KVM VMs by UUID and the guard compared
// the URL id with the Pacemaker workload name, so it never matched.
func TestPacemakerGuardResolvesUUIDToWorkloadName(t *testing.T) {
	h, _ := guardHandler(t)
	for _, id := range []string{"web", webUUID} {
		for _, q := range []string{"", "?hv=kvm"} {
			rejected, body := guardRejects(h, "/api/vms/"+id+"/shutdown"+q, id)
			if !rejected || !strings.Contains(body, "pacemaker_managed") || !strings.Contains(body, `"ha_key":"kvm:web"`) {
				t.Fatalf("id=%s%s must be rejected: %v %s", id, q, rejected, body)
			}
		}
	}
	for _, id := range []string{"free", "0bad0bad-0000-4000-8000-000000000000", "missing"} {
		if rejected, body := guardRejects(h, "/api/vms/"+id+"/shutdown", id); rejected {
			t.Fatalf("%s is not an HA workload: %s", id, body)
		}
	}
	if (&Handler{settings: h.settings}).isHAOwned(httptest.NewRequest(http.MethodDelete, "/api/vms/x", nil), webUUID) {
		t.Fatal("no backend: a UUID cannot be resolved and must not panic")
	}
	if !h.isHAOwned(httptest.NewRequest(http.MethodDelete, "/api/vms/"+webUUID, nil), webUUID) {
		t.Fatal("isHAOwned must resolve the UUID too")
	}
}

func TestPacemakerGuardUUIDThroughLifecycleHandlers(t *testing.T) {
	h, _ := guardHandler(t)
	for name, fn := range map[string]http.HandlerFunc{
		"shutdown": h.ShutdownVM, "forceoff": h.ForceOffVM, "start": h.StartVM, "reboot": h.RebootVM,
	} {
		req := withVMID(httptest.NewRequest(http.MethodPost, "/api/vms/"+webUUID+"/"+name+"?hv=kvm", nil), webUUID)
		req.Header.Set("X-Role", models.RoleAdmin)
		rec := httptest.NewRecorder()
		fn(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "pacemaker_managed") {
			t.Fatalf("%s by UUID: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}

// The local node addressed by its registry id (what vm.node can carry) is
// still local: the guard applies.
func TestPacemakerGuardLocalNodeByID(t *testing.T) {
	h, _ := guardHandler(t)
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	h.nodes = reg
	localID := ""
	for _, n := range reg.List() {
		if n.IsLocal() {
			localID = n.ID
		}
	}
	if localID == "" {
		t.Fatal("no local node")
	}
	if rejected, body := guardRejects(h, "/api/vms/"+webUUID+"/forceoff?hv=kvm&node="+localID, webUUID); !rejected {
		t.Fatalf("local node by id must be guarded: %s", body)
	}
}

// A remote libvirt node that is a member of this Pacemaker cluster shares the
// CIB: its HA domains are guarded too. A node outside the cluster is not.
func TestPacemakerGuardRemoteClusterMember(t *testing.T) {
	h, be := guardHandler(t)
	member := *h
	member.remote = &remoteTarget{node: nodes.Node{ID: "n2", Name: "ha2", URI: "qemu+libssh2://root@10.0.0.2/system"}}
	for _, id := range []string{"web", webUUID} {
		if rejected, body := guardRejects(&member, "/api/vms/"+id+"/shutdown?node=n2&hv=kvm", id); !rejected || !strings.Contains(body, "pacemaker_managed") {
			t.Fatalf("remote member, id=%s: %v %s", id, rejected, body)
		}
	}
	byHost := *h
	byHost.remote = &remoteTarget{node: nodes.Node{ID: "n3", Name: "node-three", URI: "qemu+ssh://root@ha3/system"}}
	if rejected, _ := guardRejects(&byHost, "/api/vms/"+webUUID+"/shutdown?node=n3", webUUID); !rejected {
		t.Fatal("remote member matched by URI host must be guarded")
	}
	outside := *h
	outside.remote = &remoteTarget{node: nodes.Node{ID: "n9", Name: "lab-other", URI: "qemu+ssh://root@10.9.9.9/system"}}
	before := be.lookups
	if rejected, body := guardRejects(&outside, "/api/vms/"+webUUID+"/shutdown?node=n9", webUUID); rejected {
		t.Fatalf("a node outside the cluster has its own manager: %s", body)
	}
	if be.lookups != before {
		t.Fatal("no domain lookup is needed for a node outside the cluster")
	}
}

// The list badge follows the same rule: a VM listed by a remote libvirt node
// that is a Pacemaker member is marked; one outside the cluster is not.
func TestApplyPacemakerManagementRemoteMember(t *testing.T) {
	useRunner(t, &wlRunner{hasCP: true, vm: true, started: true})
	vms := []models.VM{
		{Name: "web", Hypervisor: "kvm", Node: "ha2", NodeID: "n2"},
		{Name: "web", Hypervisor: "kvm", Node: "lab-other", NodeID: "n9"},
	}
	applyPacemakerManagement(context.Background(), vms)
	if vms[0].ManagedBy != "pacemaker" || vms[0].HAKey != "kvm:web" {
		t.Fatalf("member: %+v", vms[0])
	}
	if vms[1].ManagedBy != "" {
		t.Fatalf("outside the cluster: %+v", vms[1])
	}
}
