package incus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// --- Disks / Devices / USB / PCI / Shared Folders ---

func (b *IncusBackend) AttachDisk(id string, req models.AttachDiskRequest) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	if inst.Devices == nil {
		inst.Devices = make(map[string]map[string]string)
	}

	devName := req.Alias
	if devName == "" {
		for i := 1; i <= 64; i++ {
			candidate := fmt.Sprintf("disk%d", i)
			if _, exists := inst.Devices[candidate]; !exists {
				devName = candidate
				break
			}
		}
	}
	if devName == "" {
		devName = fmt.Sprintf("disk-%d", timeNow().Unix())
	}

	dev := map[string]string{
		"type": "disk",
		"path": "/mnt/" + devName,
	}
	if req.Pool != "" {
		dev["pool"] = req.Pool
	}
	if req.Source != "" {
		dev["source"] = req.Source
	}
	if req.SizeGB > 0 {
		dev["size"] = fmt.Sprintf("%dGiB", req.SizeGB)
	}

	inst.Devices[devName] = dev
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) DetachDisk(id, target string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	if target == "root" {
		return fmt.Errorf("cannot detach container root device")
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	found := false
	for name, dev := range inst.Devices {
		if dev["type"] == "disk" && (name == target || dev["path"] == target || dev["source"] == target) {
			delete(inst.Devices, name)
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("disk %q not found on container", target)
	}
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) ChangeDiskBus(id, target, newBus string) error {
	return compute.ErrNotImplemented
}

func (b *IncusBackend) UpdateDiskSource(id, target, source string) error {
	return compute.ErrNotImplemented
}

func (b *IncusBackend) RunCloudInitReprovision(id string) error {
	return compute.ErrNotImplemented
}

func (b *IncusBackend) ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error) {
	if target != "" && target != "root" {
		return 0, fmt.Errorf("only the root device can be resized on a container: %w", compute.ErrNotImplemented)
	}
	if newSizeGB <= 0 {
		return 0, errors.New("size must be positive")
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return 0, err
	}
	root, ok := inst.Devices["root"]
	if !ok {
		return 0, errors.New("container has no root device")
	}
	root["size"] = fmt.Sprintf("%dGB", newSizeGB)
	inst.Devices["root"] = root
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return 0, err
	}
	if err := waitOperation(op); err != nil {
		return 0, err
	}
	return newSizeGB, nil
}

func (b *IncusBackend) AttachNetworkIface(id string, req models.AttachNetRequest) error {
	bridge, err := b.bridgeForNetwork(req.Network)
	if err != nil {
		return err
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	nicName := nextNicName(inst.Devices)
	devMap := map[string]string{
		"type":    "nic",
		"nictype": "bridged",
		"parent":  bridge,
		"name":    nicName,
	}
	if req.VLANTag != nil && *req.VLANTag > 0 {
		devMap["vlan"] = strconv.Itoa(*req.VLANTag)
	}
	inst.Devices[nicName] = devMap
	inst.Config["user.network-config"] = lxdNetworkConfig(inst.Devices)
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	if err := waitOperation(op); err != nil {
		return err
	}
	b.afterAttachConfigureGuestNetwork(id, nicName)
	return nil
}

func (b *IncusBackend) afterAttachConfigureGuestNetwork(id, nicName string) {
	state, _, err := b.client.GetInstanceState(id)
	if err != nil || state.Status != "Running" {
		return
	}
	dhcpScript := fmt.Sprintf(
		`ip link set %s up 2>/dev/null; `+
			`if command -v dhclient >/dev/null 2>&1; then dhclient -1 %s; `+
			`elif command -v udhcpc >/dev/null 2>&1; then udhcpc -i %s -n -q -t 3; fi`,
		nicName, nicName, nicName,
	)
	if _, err := b.runInGuest(id, []string{"sh", "-c", dhcpScript}); err != nil {
		slog.Debug("incus_attach_guest_dhcp_kick_failed", "instance", id, "nic", nicName, "err", err)
	}
	if b.guestUsesNetworkd(id) {
		unit := fmt.Sprintf("[Match]\nName=%s\n\n[Network]\nDHCP=true\n", nicName)
		err := b.client.CreateInstanceFile(id, "/etc/systemd/network/"+nicName+".network", incus.InstanceFileArgs{
			Content: strings.NewReader(unit),
			Mode:    0644,
			Type:    "file",
		})
		if err != nil {
			slog.Debug("incus_attach_networkd_unit_push_failed", "instance", id, "nic", nicName, "err", err)
			return
		}
		if _, err := b.runInGuest(id, []string{"networkctl", "reload"}); err != nil {
			slog.Debug("incus_attach_networkd_reload_failed", "instance", id, "nic", nicName, "err", err)
		}
	}
}

func (b *IncusBackend) runInGuest(id string, cmd []string) (exitCode int, err error) {
	op, err := b.client.ExecInstance(id, api.InstanceExecPost{Command: cmd}, nil)
	if err != nil {
		return -1, err
	}
	if err := waitOperation(op); err != nil {
		return -1, err
	}
	if rc, ok := op.Get().Metadata["return"].(float64); ok {
		return int(rc), nil
	}
	return -1, nil
}

func (b *IncusBackend) guestUsesNetworkd(id string) bool {
	rc, err := b.runInGuest(id, []string{"sh", "-c", "ls /etc/systemd/network/*.network >/dev/null 2>&1"})
	return err == nil && rc == 0
}

func (b *IncusBackend) DetachNetworkIface(id, mac string) error {
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	devName := nicDeviceByMAC(inst, mac)
	if devName == "" {
		return fmt.Errorf("no network interface with MAC %s", mac)
	}
	delete(inst.Devices, devName)
	inst.Config["user.network-config"] = lxdNetworkConfig(inst.Devices)
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	if err := waitOperation(op); err != nil {
		return err
	}
	if state, _, err := b.client.GetInstanceState(id); err == nil && state.Status == "Running" {
		_, _ = b.runInGuest(id, []string{"rm", "-f", "/etc/systemd/network/" + devName + ".network"})
	}
	return nil
}

func (b *IncusBackend) UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error {
	if req.MAC != nil && *req.MAC != oldMAC {
		return fmt.Errorf("changing a container NIC MAC is not supported: %w", compute.ErrNotImplemented)
	}

	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	devName := nicDeviceByMAC(inst, oldMAC)
	if devName == "" {
		return fmt.Errorf("no network interface with MAC %s", oldMAC)
	}

	needsUpdate := false
	if req.VLANTag != nil {
		if *req.VLANTag > 0 {
			inst.Devices[devName]["vlan"] = strconv.Itoa(*req.VLANTag)
		} else {
			delete(inst.Devices[devName], "vlan")
		}
		needsUpdate = true
	}
	if req.Network != nil && *req.Network != "" {
		bridge, err := b.bridgeForNetwork(*req.Network)
		if err != nil {
			return err
		}
		inst.Devices[devName]["parent"] = bridge
		needsUpdate = true
	}

	if req.LinkState != nil && *req.LinkState != "" {
		ls := strings.ToLower(strings.TrimSpace(*req.LinkState))
		if ls != "up" && ls != "down" {
			return fmt.Errorf("invalid link state %q (expected 'up' or 'down')", *req.LinkState)
		}
		inst.Devices[devName]["link_state"] = ls
		needsUpdate = true
	}

	if !needsUpdate {
		return nil
	}

	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) AttachUSBDevice(id, vendorID, productID string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	if inst.Devices == nil {
		inst.Devices = make(map[string]map[string]string)
	}

	vClean := strings.TrimPrefix(strings.ToLower(vendorID), "0x")
	pClean := strings.TrimPrefix(strings.ToLower(productID), "0x")
	devName := fmt.Sprintf("usb-%s-%s", vClean, pClean)

	inst.Devices[devName] = map[string]string{
		"type":      "usb",
		"vendorid":  vClean,
		"productid": pClean,
	}

	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) DetachUSBDevice(id, vendorID, productID string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}

	vClean := strings.TrimPrefix(strings.ToLower(vendorID), "0x")
	pClean := strings.TrimPrefix(strings.ToLower(productID), "0x")

	found := false
	for name, dev := range inst.Devices {
		if dev["type"] == "usb" {
			devV := strings.TrimPrefix(strings.ToLower(dev["vendorid"]), "0x")
			devP := strings.TrimPrefix(strings.ToLower(dev["productid"]), "0x")
			if devV == vClean && devP == pClean {
				delete(inst.Devices, name)
				found = true
				break
			}
		}
	}
	if !found {
		return fmt.Errorf("USB device %s:%s not found on container", vendorID, productID)
	}

	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) ListHostUSBDevices() ([]models.USBDevice, error) {
	return nil, compute.ErrNotImplemented
}

func (b *IncusBackend) AttachPCIDevice(id string, addresses []string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	if inst.Devices == nil {
		inst.Devices = make(map[string]map[string]string)
	}

	for _, addr := range addresses {
		addrClean := strings.TrimSpace(addr)
		if addrClean == "" {
			continue
		}
		safeName := "pci-" + strings.ReplaceAll(strings.ReplaceAll(addrClean, ":", "-"), ".", "-")
		inst.Devices[safeName] = map[string]string{
			"type":    "pci",
			"address": addrClean,
		}
	}

	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) DetachPCIDevice(id, address string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}

	addrClean := strings.TrimSpace(address)
	found := false
	for name, dev := range inst.Devices {
		if dev["type"] == "pci" && strings.EqualFold(dev["address"], addrClean) {
			delete(inst.Devices, name)
			found = true
			break
		}
	}
	if !found {
		safeName := "pci-" + strings.ReplaceAll(strings.ReplaceAll(addrClean, ":", "-"), ".", "-")
		if _, ok := inst.Devices[safeName]; ok {
			delete(inst.Devices, safeName)
			found = true
		}
	}
	if !found {
		return fmt.Errorf("PCI device %s not found on container", address)
	}

	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) ListHostPCIDevices() ([]models.PCIIOMMUGroup, error) {
	return nil, compute.ErrNotImplemented
}
func (b *IncusBackend) GetHostPCIPreflight() (models.PCIPreflightInfo, error) {
	return models.PCIPreflightInfo{}, compute.ErrNotImplemented
}
func (b *IncusBackend) BindHostPCIDeviceVFIO(address string) error {
	return compute.ErrNotImplemented
}
func (b *IncusBackend) UnbindHostPCIDeviceVFIO(address string) error {
	return compute.ErrNotImplemented
}

func (b *IncusBackend) AttachSharedFolder(id, hostPath, tag string, readOnly bool) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	if inst.Devices == nil {
		inst.Devices = make(map[string]map[string]string)
	}

	devName := "shared-" + tag
	ro := "false"
	if readOnly {
		ro = "true"
	}

	mountPath := "/mnt/" + tag
	inst.Devices[devName] = map[string]string{
		"type":     "disk",
		"source":   hostPath,
		"path":     mountPath,
		"readonly": ro,
	}

	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) DetachSharedFolder(id, tag string) error {
	if b.client == nil {
		return compute.ErrNotImplemented
	}
	inst, etag, err := b.client.GetInstance(id)
	if err != nil {
		return err
	}
	devName := "shared-" + tag
	found := false
	if _, ok := inst.Devices[devName]; ok {
		delete(inst.Devices, devName)
		found = true
	} else {
		for name, dev := range inst.Devices {
			if dev["type"] == "disk" && (name == tag || dev["path"] == "/mnt/"+tag) {
				delete(inst.Devices, name)
				found = true
				break
			}
		}
	}
	if !found {
		return fmt.Errorf("shared folder %q not found on container", tag)
	}
	op, err := b.client.UpdateInstance(id, api.InstancePut{
		Config:      inst.Config,
		Devices:     inst.Devices,
		Profiles:    inst.Profiles,
		Description: inst.Description,
	}, etag)
	if err != nil {
		return err
	}
	return waitOperation(op)
}
