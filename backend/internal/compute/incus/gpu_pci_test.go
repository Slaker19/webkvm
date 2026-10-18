package incus

import (
	"testing"

	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/models"
)

// TestInstanceToVM_GPUAndPCI ensures that instanceToVM correctly surfaces
// GPU devices and PCI devices from the Incus instance definition.
func TestInstanceToVM_GPUAndPCI(t *testing.T) {
	inst := &api.Instance{
		Name:   "ct-gpu",
		Status: "Stopped",
		Type:   "container",
		InstancePut: api.InstancePut{
			Devices: map[string]map[string]string{
				"root": {"type": "disk", "path": "/", "pool": "default"},
				"gpu0": {"type": "gpu"},
				"pci-0000-01-00-0": {
					"type":    "pci",
					"address": "0000:01:00.0",
				},
			},
		},
	}

	vm := instanceToVM(inst)
	if !vm.GPU {
		t.Errorf("expected vm.GPU to be true, got false")
	}
	if len(vm.PCIDevices) != 1 {
		t.Fatalf("expected 1 PCIDevice, got %d", len(vm.PCIDevices))
	}
	if vm.PCIDevices[0].Address != "0000:01:00.0" {
		t.Errorf("expected PCI address 0000:01:00.0, got %s", vm.PCIDevices[0].Address)
	}
}

// TestIncusBackendUpdateDomainGPU verifies enabling and disabling GPU acceleration on a container.
func TestIncusBackendUpdateDomainGPU(t *testing.T) {
	initial := map[string]*api.Instance{
		"ct1": {
			Name:   "ct1",
			Status: "Stopped",
			Type:   "container",
			InstancePut: api.InstancePut{
				Devices: map[string]map[string]string{
					"root": {"type": "disk", "path": "/", "pool": "default"},
				},
			},
		},
	}
	sock, fake := newFakeLXD3(t, initial, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// 1. Enable GPU
	enable := true
	vm, err := b.UpdateDomain("ct1", models.UpdateVMRequest{GPU: &enable})
	if err != nil {
		t.Fatalf("enable GPU: %v", err)
	}
	if !vm.GPU {
		t.Errorf("expected vm.GPU true after update")
	}

	fake.mu.Lock()
	gpuDev, ok := fake.instances["ct1"].Devices["gpu0"]
	fake.mu.Unlock()
	if !ok || gpuDev["type"] != "gpu" {
		t.Errorf("expected gpu0 device with type=gpu, got %+v", gpuDev)
	}

	// 2. Disable GPU
	disable := false
	vm, err = b.UpdateDomain("ct1", models.UpdateVMRequest{GPU: &disable})
	if err != nil {
		t.Fatalf("disable GPU: %v", err)
	}
	if vm.GPU {
		t.Errorf("expected vm.GPU false after update")
	}

	fake.mu.Lock()
	_, ok = fake.instances["ct1"].Devices["gpu0"]
	fake.mu.Unlock()
	if ok {
		t.Errorf("expected gpu0 device to be deleted")
	}
}

// TestIncusBackendAttachDetachPCIDevice verifies attaching and detaching PCI devices.
func TestIncusBackendAttachDetachPCIDevice(t *testing.T) {
	initial := map[string]*api.Instance{
		"ct-pci": {
			Name:   "ct-pci",
			Status: "Stopped",
			Type:   "container",
			InstancePut: api.InstancePut{
				Devices: map[string]map[string]string{
					"root": {"type": "disk", "path": "/", "pool": "default"},
				},
			},
		},
	}
	sock, fake := newFakeLXD3(t, initial, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// 1. Attach PCI
	pciAddr := "0000:02:00.0"
	if err := b.AttachPCIDevice("ct-pci", []string{pciAddr}); err != nil {
		t.Fatalf("attach PCI: %v", err)
	}

	fake.mu.Lock()
	pciDev, ok := fake.instances["ct-pci"].Devices["pci-0000-02-00-0"]
	fake.mu.Unlock()
	if !ok {
		t.Fatalf("expected pci-0000-02-00-0 device to exist")
	}
	if pciDev["type"] != "pci" || pciDev["address"] != pciAddr {
		t.Errorf("unexpected PCI device config: %+v", pciDev)
	}

	vm, err := b.GetDomain("ct-pci")
	if err != nil {
		t.Fatalf("get domain: %v", err)
	}
	if len(vm.PCIDevices) != 1 || vm.PCIDevices[0].Address != pciAddr {
		t.Errorf("expected vm.PCIDevices with address %s, got %+v", pciAddr, vm.PCIDevices)
	}

	// 2. Detach PCI
	if err := b.DetachPCIDevice("ct-pci", pciAddr); err != nil {
		t.Fatalf("detach PCI: %v", err)
	}

	fake.mu.Lock()
	_, ok = fake.instances["ct-pci"].Devices["pci-0000-02-00-0"]
	fake.mu.Unlock()
	if ok {
		t.Errorf("expected pci-0000-02-00-0 device to be deleted")
	}

	vm, err = b.GetDomain("ct-pci")
	if err != nil {
		t.Fatalf("get domain: %v", err)
	}
	if len(vm.PCIDevices) != 0 {
		t.Errorf("expected 0 PCIDevices, got %d", len(vm.PCIDevices))
	}
}
