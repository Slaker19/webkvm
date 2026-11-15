package incus

import (
	"testing"

	"github.com/lxc/incus/v7/shared/api"
)

// A stock container keeps its root disk in the profile; the neutral model
// must see it so clones resolve the pool and quota counts the disk.
func TestInstanceToVMReadsRootDiskFromExpandedDevices(t *testing.T) {
	inst := &api.Instance{
		Name:        "web",
		Type:        "container",
		Status:      "Stopped",
		InstancePut: api.InstancePut{Devices: map[string]map[string]string{}},
		ExpandedDevices: map[string]map[string]string{
			"root": {"type": "disk", "path": "/", "pool": "webkvm-incus", "size": "8GB"},
			"eth0": {"type": "nic", "nictype": "bridged", "parent": "br0"},
		},
	}

	vm := instanceToVM(inst)

	found := false
	for _, disk := range vm.Disks {
		if (disk.Target == "root" || disk.Target == "/") && disk.Pool == "webkvm-incus" {
			found = true
		}
	}
	if !found {
		t.Fatalf("root disk from the profile missing: %+v", vm.Disks)
	}
}
