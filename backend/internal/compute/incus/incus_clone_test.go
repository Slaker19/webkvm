package incus

import (
	"errors"
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

func TestIncusBackendCloneDomainCopiesStoppedInstance(t *testing.T) {
	source := &api.Instance{
		Name:   "template",
		Type:   "container",
		Status: "Stopped",
		InstancePut: api.InstancePut{
			Devices: map[string]map[string]string{
				"root": {"type": "disk", "path": "/", "pool": "webkvm-incus"},
				"eth0": {"type": "nic", "nictype": "bridged", "parent": "br0"},
			},
		},
	}
	sock, fake := newFakeLXD3(t, map[string]*api.Instance{"template": source}, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	vm, err := b.CloneDomain("template", models.CloneVMRequest{
		Name: "template-copy", Pool: "target-pool",
	})
	if err != nil {
		t.Fatalf("clone instance: %v", err)
	}
	if vm.Name != "template-copy" || vm.Hypervisor != "incus" {
		t.Fatalf("cloned VM = %+v", vm)
	}

	fake.mu.Lock()
	post := fake.lastCreate
	fake.mu.Unlock()
	if post == nil || post.Source.Type != "copy" || post.Source.Source != "template" {
		t.Fatalf("copy request = %+v", post)
	}
	if post.Name != "template-copy" {
		t.Fatalf("copy target = %q, want template-copy", post.Name)
	}
	if post.Devices["root"]["pool"] != "target-pool" {
		t.Fatalf("copy root pool = %q, want target-pool", post.Devices["root"]["pool"])
	}
}

func TestIncusBackendCloneDomainRejectsLinkedAndRunningInstances(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
		linked bool
	}{
		{name: "linked clone", status: "Stopped", linked: true},
		{name: "running source", status: "Running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock, fake := newFakeLXD3(t, map[string]*api.Instance{
				"source": {Name: "source", Type: "container", Status: tc.status},
			}, nil)
			b, err := NewIncusBackend(sock)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			_, err = b.CloneDomain("source", models.CloneVMRequest{Name: "copy", Linked: tc.linked})
			if err == nil {
				t.Fatal("clone should have been rejected")
			}
			if !tc.linked && !errors.Is(err, compute.ErrDomainMustBeStoppedToClone) {
				t.Fatalf("running source error = %v, want stopped-source error", err)
			}
			fake.mu.Lock()
			created := fake.lastCreate != nil
			fake.mu.Unlock()
			if created {
				t.Fatal("rejected clone sent a create request")
			}
		})
	}
}
