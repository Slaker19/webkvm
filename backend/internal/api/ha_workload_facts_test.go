package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/ha"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

const sampleDomainXML = `<domain type="kvm"><name>web</name><devices>
<disk type="file" device="disk"><source file="/mnt/vm/web.qcow2"/></disk>
<disk type="file" device="cdrom"><source file="/isos/a.iso"/></disk>
<disk type="block" device="disk"><source dev="/dev/vg0/data"/></disk>
<disk type="network" device="disk"><source protocol="rbd" name="pool/img"/></disk>
<disk type="file" device="cdrom"/>
<interface type="bridge"><source bridge="br0"/></interface>
<interface type="network"><source network="default"/></interface>
<interface type="bridge"><source bridge="br0"/></interface>
</devices></domain>`

type fakeKVMBackend struct {
	compute.Backend
	vm      models.VM
	getErr  error
	xml     string
	nets    []models.Network
	xmlCall int
}

func (f *fakeKVMBackend) GetDomain(string) (models.VM, error) { return f.vm, f.getErr }
func (f *fakeKVMBackend) GetDomainXML(string) (string, error) { f.xmlCall++; return f.xml, nil }
func (f *fakeKVMBackend) ListNetworks() ([]models.Network, error) {
	return f.nets, nil
}

type fakeIncusSecondary struct {
	compute.Backend
	insp    ha.IncusInspector
	members []string
	err     error
	project string
}

func (f *fakeIncusSecondary) HAInspector(_ context.Context, project, _ string) (ha.IncusInspector, []string, error) {
	f.project = project
	return f.insp, f.members, f.err
}

func TestParseDomainFacts(t *testing.T) {
	disks, bridges, networks, err := parseDomainFacts(sampleDomainXML)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/mnt/vm/web.qcow2", "/isos/a.iso", "/dev/vg0/data", "rbd:pool/img"}; !reflect.DeepEqual(disks, want) {
		t.Fatalf("disks = %v, want %v", disks, want)
	}
	if !reflect.DeepEqual(bridges, []string{"br0"}) || !reflect.DeepEqual(networks, []string{"default"}) {
		t.Fatalf("bridges=%v networks=%v", bridges, networks)
	}
	if _, _, _, err := parseDomainFacts("<domain"); err == nil {
		t.Fatal("malformed XML must fail")
	}
}

func TestKVMFactsFromBackend(t *testing.T) {
	dir := t.TempDir()
	prev := libvirtDefinitionDir
	libvirtDefinitionDir = dir
	t.Cleanup(func() { libvirtDefinitionDir = prev })
	real := filepath.Join(t.TempDir(), "web.xml")
	if err := os.WriteFile(real, []byte(sampleDomainXML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "web.xml")); err != nil {
		t.Fatal(err)
	}
	kvm := &fakeKVMBackend{vm: models.VM{Name: "web", Hypervisor: "kvm", State: models.VMStateRunning}, xml: sampleDomainXML,
		nets: []models.Network{{Name: "default", Bridge: "virbr0"}, {Name: "lan", Bridge: "br0"}}}
	// Wrapped in Combined: the facts must come from the KVM side only.
	secondary := &fakeIncusSecondary{}
	f := &backendWorkloadFacts{h: &Handler{compute: compute.NewCombined(kvm, secondary)}}
	got, err := f.KVM(context.Background(), "web")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Exists || !got.Running || got.XMLPath != real || len(got.DiskPaths) != 4 {
		t.Fatalf("facts = %+v", got)
	}
	if !reflect.DeepEqual(got.Bridges, []string{"br0", "virbr0"}) {
		t.Fatalf("bridges = %v (libvirt network default must resolve to virbr0)", got.Bridges)
	}
	nb, err := f.NodeBridges(context.Background())
	if err != nil || !reflect.DeepEqual(nb[localNodeName()], []string{"br0", "virbr0"}) || len(nb) != 1 {
		t.Fatalf("node bridges = %v %v", nb, err)
	}

	kvm.getErr = compute.ErrNotFound
	if got, err = f.KVM(context.Background(), "web"); err != nil || got.Exists {
		t.Fatalf("missing VM: %+v %v", got, err)
	}
	kvm.getErr = errors.New("libvirt down")
	if _, err = f.KVM(context.Background(), "web"); err == nil {
		t.Fatal("a backend failure must surface")
	}
	if factsErr(err, "VM").Code != "facts_failed" {
		t.Fatalf("code = %s", factsErr(err, "VM").Code)
	}
}

func TestKVMFactsNeverReadLocalFSForRemoteNode(t *testing.T) {
	kvm := &fakeKVMBackend{vm: models.VM{Name: "web"}, xml: sampleDomainXML}
	h := (&Handler{compute: kvm}).forBackend(kvm, nil, nodes.Node{Name: "peer", ID: "n1"})
	f := &backendWorkloadFacts{h: h}
	if _, err := f.KVM(context.Background(), "web"); !errors.Is(err, errNotOnThisNode) {
		t.Fatalf("err = %v", err)
	}
	if kvm.xmlCall != 0 {
		t.Fatal("remote node: the XML must not be read")
	}
	e := factsErr(errNotOnThisNode, "VM")
	if e.Status != 409 || e.Code != "vm_not_on_this_node" {
		t.Fatalf("%+v", e)
	}
	// The handler maps it before touching any facts.
	prev := newWorkloadFacts
	t.Cleanup(func() { newWorkloadFacts = prev })
	newWorkloadFacts = func(h *Handler) workloadFacts { t.Fatal("facts must not be built"); return nil }
	if _, _, ae := h.evaluate(context.Background(), ha.WorkloadKey{Kind: ha.KindKVM, Name: "web"}); ae == nil || ae.Code != "vm_not_on_this_node" {
		t.Fatalf("evaluate: %+v", ae)
	}
}

func TestIncusFactsFindsProviderThroughCombinedOrDirectly(t *testing.T) {
	insp := fakeInspector{driver: "linstor"}
	sec := &fakeIncusSecondary{insp: insp, members: []string{"a", "b"}}
	for name, be := range map[string]compute.Backend{
		"combined": compute.NewCombined(&fakeKVMBackend{}, sec),
		"direct":   sec,
	} {
		f := &backendWorkloadFacts{h: &Handler{compute: be}}
		got, members, err := f.Incus(context.Background(), "team", "c1")
		if err != nil || got == nil || len(members) != 2 || sec.project != "team" {
			t.Fatalf("%s: %v %v %v project=%s", name, got, members, err, sec.project)
		}
	}
	f := &backendWorkloadFacts{h: &Handler{compute: compute.NewCombined(&fakeKVMBackend{}, nil)}}
	if _, _, err := f.Incus(context.Background(), "default", "c1"); !errors.Is(err, compute.ErrNotImplemented) {
		t.Fatalf("no Incus: %v", err)
	}
	if e := factsErr(compute.ErrNotImplemented, "Incus instance"); e.Status != 503 || e.Code != "facts_unavailable" {
		t.Fatalf("%+v", e)
	}
}
