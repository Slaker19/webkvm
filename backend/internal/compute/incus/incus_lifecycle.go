package incus

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lxc/incus/v7/shared/api"

	"webkvm/internal/cloudinit"
	"webkvm/internal/compute"
	"webkvm/internal/models"
)

// --- Instance lifecycle ---

// ListDomains lists every LXD instance (containers + VMs) and maps them to
// the neutral domain model.
func (b *IncusBackend) ListDomains() ([]models.VM, error) {
	instances, err := b.client.GetInstances(api.InstanceTypeAny)
	if err != nil {
		return nil, err
	}
	out := make([]models.VM, 0, len(instances))
	for i := range instances {
		vm := instanceToVM(&instances[i])
		// N+1: surface the container's LAN IP (Incus GetInstance does not include it).
		if st, _, err := b.client.GetInstanceState(instances[i].Name); err == nil {
			vm.IP = instanceIP(st)
			vm.IPs = instanceIPs(st)
			vm.UptimeSec = instanceUptimeSec(st)
			macIPs := instanceIfaceIPs(st)
			for j := range vm.Networks {
				vm.Networks[j].IPs = macIPs[strings.ToLower(vm.Networks[j].MAC)]
			}
		}
		out = append(out, vm)
	}
	return out, nil
}

func (b *IncusBackend) GetDomain(id string) (models.VM, error) {
	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return models.VM{}, err
	}
	vm := instanceToVM(inst)
	// Surface the container's IP from the live instance state (eth0 IPv4).
	if st, _, err := b.client.GetInstanceState(id); err == nil {
		vm.IP = instanceIP(st)
		vm.IPs = instanceIPs(st)
		vm.UptimeSec = instanceUptimeSec(st)
		macIPs := instanceIfaceIPs(st)
		for j := range vm.Networks {
			vm.Networks[j].IPs = macIPs[strings.ToLower(vm.Networks[j].MAC)]
		}
	}
	return vm, nil
}

func (b *IncusBackend) DomainExists(name string) (bool, error) {
	_, _, err := b.client.GetInstance(name)
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

func (b *IncusBackend) DeleteDomain(id string) error {
	op, err := b.client.DeleteInstance(id)
	if err != nil {
		return err
	}
	return waitOperation(op)
}

func (b *IncusBackend) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	return models.VM{}, compute.ErrNotImplemented
}

func (b *IncusBackend) StartDomain(id string) error { return b.setState(id, "start", -1, false) }

func (b *IncusBackend) ShutdownDomain(id string) error {
	// Timeout 30s matches KVM graceful shutdown window; force=false requests
	// clean guest OS shutdown via SIGPWR/systemd rather than hard kill.
	return b.setState(id, "stop", 30, false)
}

func (b *IncusBackend) ForceOffDomain(id string) error {
	// Force=true is equivalent to pulling the plug (SIGKILL from the
	// container's perspective). Timeout is irrelevant when forcing.
	//
	// Idempotency: if the container is ALREADY stopped, LXD returns
	// "The instance is already stopped" as an error. For an idempotent
	// kill (e.g. clean-up after a failed test, or operator hammering
	// Force Off on an unresponsive UI), that is not a failure — the
	// desired state (container not running) is already met.
	err := b.setState(id, "stop", -1, true)
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "already stopped") {
		return nil
	}
	return err
}

func (b *IncusBackend) RebootDomain(id string) error {
	return b.setState(id, "restart", 30, false)
}

func (b *IncusBackend) SuspendDomain(id string) error { return b.setState(id, "freeze", 30, false) }
func (b *IncusBackend) ResumeDomain(id string) error  { return b.setState(id, "unfreeze", 30, false) }

func (b *IncusBackend) SetDomainAutostart(id string, enabled bool) error {
	return b.setMetaConfig(id, func(cfg map[string]string) { cfg["boot.autostart"] = strconv.FormatBool(enabled) })
}

func (b *IncusBackend) GetDomainAutostart(id string) (bool, error) {
	inst, _, err := b.client.GetInstance(id)
	if err != nil {
		return false, err
	}
	return inst.Config["boot.autostart"] == "true", nil
}

func (b *IncusBackend) SetBootDevice(id string, device string) error {
	return compute.ErrNotImplemented
}

func (b *IncusBackend) GetBootDevice(id string) (string, error) { return "", compute.ErrNotImplemented }
func (b *IncusBackend) ValidateDomainDisks(id string) error     { return compute.ErrNotImplemented }

// CreateDomain creates an Incus instance (container) from an image reference.
func (b *IncusBackend) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	if strings.TrimSpace(req.Image) == "" {
		return models.VM{}, errors.New("an LXD image reference is required to create a container (e.g. ubuntu:24.04)")
	}
	if b.client == nil {
		return models.VM{}, compute.ErrNotImplemented
	}
	fingerprint := ""
	var server, alias string
	if isImageFingerprint(req.Image) {
		fingerprint = strings.ToLower(strings.TrimSpace(req.Image))
		if _, _, err := b.client.GetImage(fingerprint); err != nil {
			return models.VM{}, fmt.Errorf("no local image with fingerprint %q", req.Image)
		}
	} else {
		var err error
		server, alias, err = parseImageRef(req.Image)
		if err != nil {
			return models.VM{}, err
		}
	}
	if needsCloudInit(req.CloudInit) && fingerprint == "" {
		alias = cloudVariantAlias(server, alias)
	}
	bridge, err := b.bridgeForNetwork(req.Network)
	if err != nil {
		return models.VM{}, err
	}
	if err := b.authorizeSpec(req.Scope, req.Privileged, req.Nesting, req.Profiles); err != nil {
		return models.VM{}, err
	}
	nestingDefault := "false"
	if req.Scope.Admin || req.Scope.CanNesting {
		nestingDefault = "true"
	}
	config := map[string]string{
		"boot.autostart":   "true",
		"security.nesting": nestingDefault,
	}
	if req.VCPUs > 0 {
		config["limits.cpu"] = strconv.Itoa(req.VCPUs)
	}
	if req.RAMMB > 0 {
		config["limits.memory"] = formatMemoryMB(req.RAMMB)
	}
	if req.Autostart != nil {
		config["boot.autostart"] = strconv.FormatBool(*req.Autostart)
	}
	if req.Privileged != nil {
		config["security.privileged"] = strconv.FormatBool(*req.Privileged)
	}
	if req.Nesting != nil {
		config["security.nesting"] = strconv.FormatBool(*req.Nesting)
	}
	profiles := req.Profiles
	if len(profiles) == 0 {
		profiles = []string{"default"}
	}
	if err := validateProfiles(profiles); err != nil {
		return models.VM{}, err
	}
	devices := map[string]map[string]string{}
	if req.DiskGB > 0 {
		pool := strings.TrimSpace(req.StoragePool)
		if pool == "" {
			pool = b.rootPoolForProfiles(profiles)
		}
		if pool == "" {
			logWarnNoPool(req.Name, profiles, req.DiskGB)
		} else {
			devices["root"] = map[string]string{
				"type": "disk",
				"path": "/",
				"pool": pool,
				"size": fmt.Sprintf("%dGB", req.DiskGB),
			}
		}
	}
	devices["eth0"] = map[string]string{
		"type":    "nic",
		"nictype": "bridged",
		"parent":  bridge,
		"name":    "eth0",
	}
	if req.GPU != nil && *req.GPU {
		devices["gpu0"] = map[string]string{"type": "gpu"}
	}
	if req.CloudInit != nil {
		ci := cloudinit.Config{
			User:            req.CloudInit.User,
			Password:        req.CloudInit.Password,
			SSHKey:          req.CloudInit.SSHKey,
			Hostname:        req.CloudInit.Hostname,
			ProvisionScript: req.CloudInit.ProvisionScript,
			CustomUserData:  req.CloudInit.CustomUserData,
			SnippetID:       req.CloudInit.SnippetID,
			SkipGuestAgent:  true,
		}
		if err := ci.Validate(); err != nil {
			return models.VM{}, err
		}
		cloudKeys, _ := lxdCloudInitConfig(ci, bridge)
		for k, v := range cloudKeys {
			config[k] = v
		}
	}
	config["user.network-config"] = lxdNetworkConfig(devices)
	source := api.InstanceSource{Type: "image"}
	if fingerprint != "" {
		source.Fingerprint = fingerprint
	} else {
		source.Protocol = "simplestreams"
		source.Server = server
		source.Alias = alias
	}
	post := api.InstancesPost{
		Name:   req.Name,
		Type:   api.InstanceTypeContainer,
		Source: source,
		InstancePut: api.InstancePut{
			Config:   config,
			Devices:  devices,
			Profiles: profiles,
		},
	}
	op, err := b.client.CreateInstance(post)
	if err != nil {
		return models.VM{}, err
	}
	if err := waitOperation(op); err != nil {
		return models.VM{}, err
	}
	return b.GetDomain(req.Name)
}

// UpdateDomain updates a container's writable properties: name, CPU/RAM, and options.
func (b *IncusBackend) UpdateDomain(id string, req models.UpdateVMRequest) (models.VM, error) {
	if err := b.authorizeSpec(req.Scope, req.Privileged, req.Nesting, req.Profiles); err != nil {
		return models.VM{}, err
	}
	for name, v := range map[string]bool{
		"CPUMode": req.CPUMode != nil, "VideoModel": req.VideoModel != nil,
		"OSType": req.OSType != nil, "OSVersion": req.OSVersion != nil,
		"Chipset": req.Chipset != nil, "SecureBoot": req.SecureBoot != nil,
		"TPMEnabled": req.TPMEnabled != nil, "Firmware": req.Firmware != nil,
		"NetworkModel": req.NetworkModel != nil, "Network": req.Network != nil,
		"BootOrder":       req.BootOrder != nil,
		"TPMVersion":      req.TPMVersion != nil,
		"WatchdogEnabled": req.WatchdogEnabled != nil,
	} {
		if v {
			return models.VM{}, fmt.Errorf("field %s is not applicable to a container: %w", name, compute.ErrNotImplemented)
		}
	}

	if req.Name != nil && *req.Name != id {
		op, err := b.client.RenameInstance(id, api.InstancePost{Name: *req.Name})
		if err != nil {
			return models.VM{}, err
		}
		if err := waitOperation(op); err != nil {
			return models.VM{}, err
		}
		id = *req.Name
	}
	if req.VCPUs != nil || req.RAMMB != nil {
		inst, etag, err := b.client.GetInstance(id)
		if err != nil {
			return models.VM{}, err
		}
		cfg := inst.Config
		if req.VCPUs != nil {
			if *req.VCPUs > 0 {
				cfg["limits.cpu"] = strconv.Itoa(*req.VCPUs)
			} else {
				delete(cfg, "limits.cpu")
			}
		}
		if req.RAMMB != nil {
			if *req.RAMMB > 0 {
				cfg["limits.memory"] = formatMemoryMB(*req.RAMMB)
			} else {
				delete(cfg, "limits.memory")
			}
		}
		op, err := b.client.UpdateInstance(id, api.InstancePut{
			Config:      cfg,
			Devices:     inst.Devices,
			Profiles:    inst.Profiles,
			Description: inst.Description,
		}, etag)
		if err != nil {
			return models.VM{}, err
		}
		if err := waitOperation(op); err != nil {
			return models.VM{}, err
		}
	}
	if req.Privileged != nil || req.Nesting != nil || req.Autostart != nil || req.Profiles != nil || req.GPU != nil {
		inst, etag, err := b.client.GetInstance(id)
		if err != nil {
			return models.VM{}, err
		}
		cfg := inst.Config
		if req.Privileged != nil {
			if inst.StatusCode == api.Running {
				return models.VM{}, fmt.Errorf("cannot change security.privileged while the container is running — stop it first")
			}
			cfg["security.privileged"] = strconv.FormatBool(*req.Privileged)
		}
		if req.Nesting != nil {
			cfg["security.nesting"] = strconv.FormatBool(*req.Nesting)
		}
		if req.Autostart != nil {
			cfg["boot.autostart"] = strconv.FormatBool(*req.Autostart)
		}
		profiles := inst.Profiles
		if req.Profiles != nil {
			if err := validateProfiles(req.Profiles); err != nil {
				return models.VM{}, err
			}
			profiles = req.Profiles
		}
		devices := inst.Devices
		if devices == nil {
			devices = make(map[string]map[string]string)
		}
		if req.GPU != nil {
			if *req.GPU {
				devices["gpu0"] = map[string]string{"type": "gpu"}
			} else {
				delete(devices, "gpu0")
				for devName, dev := range devices {
					if dev["type"] == "gpu" && dev["pci"] == "" && dev["id"] == "" {
						delete(devices, devName)
					}
				}
			}
		}
		op, err := b.client.UpdateInstance(id, api.InstancePut{
			Config:      cfg,
			Devices:     devices,
			Profiles:    profiles,
			Description: inst.Description,
		}, etag)
		if err != nil {
			return models.VM{}, err
		}
		if err := waitOperation(op); err != nil {
			return models.VM{}, err
		}
	}
	return b.GetDomain(id)
}

func (b *IncusBackend) setState(id, action string, timeout int, force bool) error {
	op, err := b.client.UpdateInstanceState(id, api.InstanceStatePut{
		Action:  action,
		Timeout: timeout,
		Force:   force,
	}, "")
	if err != nil {
		return mapLXErr(err)
	}
	return waitOperation(op)
}
