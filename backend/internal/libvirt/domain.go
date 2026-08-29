package libvirt

import (
	"archive/tar"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"webkvm/internal/backupstore"
	"webkvm/internal/models"

	"github.com/google/uuid"
	"github.com/libvirt/libvirt-go"
)

const (
	nvramDir = "/var/lib/libvirt/qemu/nvram"
)

var (
	ovmfCode         string
	ovmfCodeSecboot  string
	ovmfVarsTemplate string
)

func init() {
	ovmfCode, ovmfCodeSecboot, ovmfVarsTemplate = resolveOVMFFiles()
}

func resolveOVMFFiles() (code, codeSecboot, varsTpl string) {
	candidates := []struct {
		code, codeSecboot, varsTpl string
	}{
		// Ubuntu 24.04+ / 26.04
		{
			code:        "/usr/share/OVMF/OVMF_CODE_4M.fd",
			codeSecboot: "/usr/share/OVMF/OVMF_CODE_4M.secboot.fd",
			varsTpl:     "/usr/share/OVMF/OVMF_VARS_4M.fd",
		},
		// Ubuntu 22.04 / Arch
		{
			code:        "/usr/share/edk2-ovmf/x64/OVMF_CODE.4m.fd",
			codeSecboot: "/usr/share/edk2-ovmf/x64/OVMF_CODE.secboot.4m.fd",
			varsTpl:     "/usr/share/edk2-ovmf/x64/OVMF_VARS.4m.fd",
		},
	}
	for _, c := range candidates {
		if _, err := os.Stat(c.code); err == nil {
			return c.code, c.codeSecboot, c.varsTpl
		}
	}
	// Fallback: return the modern path so libvirt reports a clear error if missing.
	return candidates[0].code, candidates[0].codeSecboot, candidates[0].varsTpl
}

func (c *Connector) ListDomains() ([]models.VM, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}

	doms, err := c.conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_ACTIVE | libvirt.CONNECT_LIST_DOMAINS_INACTIVE)
	if err != nil {
		return nil, fmt.Errorf("list domains: %w", err)
	}

	vms := make([]models.VM, 0, len(doms))
	for i := range doms {
		vm, err := c.domainToVM(&doms[i])
		doms[i].Free()
		if err != nil {
			continue
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

func (c *Connector) GetDomain(id string) (models.VM, error) {
	if err := c.ensureConnected(); err != nil {
		return models.VM{}, err
	}

	dom, err := c.lookupDomain(id)
	if err != nil {
		return models.VM{}, err
	}
	defer dom.Free()
	return c.domainToVM(dom)
}

// GetDomainXML returns the raw libvirt <domain>...</domain>
// descriptor for a domain. The Phase II backup runner calls
// this once per in-scope VM to populate the domain.xml entry
// in the per-VM archive. The XML is libvirt's authoritative
// record of the VM's hardware config, so a restore can re-
// define the VM without losing anything (CPU topology, NIC
// model, firmware, etc.).
func (c *Connector) GetDomainXML(id string) (string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return "", err
	}
	defer dom.Free()
	return dom.GetXMLDesc(0)
}

// ExportSnapshots returns snapshot metadata and overlay volume
// paths for the given domain. Used by the backup runner to include
// snapshots in per-VM archives.
//
// The current snapshot's overlay volume is the active disk (which
// is already captured in the per-VM archive as a disks/ entry), so
// we skip its VolumePath to avoid duplicating it. Non-current
// snapshots have their own overlay files that are included.
func (c *Connector) ExportSnapshots(domainID string) ([]backupstore.SnapshotBackup, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return nil, err
	}
	defer dom.Free()

	domainName, _ := dom.GetName()
	poolName := c.DiskPoolName()

	// Refresh the pool so snapshot overlay volumes are visible
	// to lookupSnapshotVolume (libvirt's in-memory cache may be
	// stale if a snapshot was just created or deleted).
	_ = c.RefreshPool(poolName)

	snaps, err := dom.ListAllSnapshots(0)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	currentSnap, _ := dom.SnapshotCurrent(0)
	var currentName string
	if currentSnap != nil {
		currentName, _ = currentSnap.GetName()
		currentSnap.Free()
	}

	result := make([]backupstore.SnapshotBackup, 0, len(snaps)+1)

	// Include the root base disk (<vmname>.qcow2 or <vmname>.img)
	// if it exists in the pool and is not the current active disk.
	// The root is the backing file origin of the entire chain and is
	// needed during restore to rebuild the backing chain correctly.
	if domainName != "" {
		for _, ext := range []string{".qcow2", ".img"} {
			if vol, err := c.lookupSnapshotVolume(poolName, domainName+ext); err == nil {
				// Synthetic snapshot named "_base" holds the root disk.
				result = append(result, backupstore.SnapshotBackup{
					Name:       "_base",
					VolumePath: vol.Path,
				})
				break
			}
		}
	}

	for i := range snaps {
		name, _ := snaps[i].GetName()
		xmlDesc, _ := snaps[i].GetXMLDesc(0)
		snaps[i].Free()

		if name == "" {
			continue
		}

		snap := backupstore.SnapshotBackup{
			Name:              name,
			DomainSnapshotXML: xmlDesc,
		}

		// For non-current snapshots, locate the overlay volume.
		// The current snapshot's volume is the active disk (already
		// captured in disks/), so we skip it to avoid duplication.
		if name != currentName && domainName != "" {
			if vol, err := c.lookupSnapshotVolume(poolName, domainName+"."+name); err == nil {
				snap.VolumePath = vol.Path
			}
		}

		result = append(result, snap)
	}

	return result, nil
}

func (c *Connector) CreateDomain(req models.CreateVMRequest) (models.VM, error) {
	if err := c.ensureConnected(); err != nil {
		return models.VM{}, err
	}

	usingExistingDisk := req.ExistingDiskPool != "" && req.ExistingDiskName != ""

	poolName := req.StoragePool
	if poolName == "" {
		poolName = c.DiskPoolName()
	}

	poolPath, err := c.GetPoolPath(poolName)
	if err != nil {
		return models.VM{}, fmt.Errorf("get pool path: %w", err)
	}

	diskFormat := req.DiskFormat
	if diskFormat == "" {
		diskFormat = "qcow2"
	}
	diskExt := ".qcow2"
	if diskFormat == "raw" {
		diskExt = ".img"
	}
	diskName := req.Name + diskExt
	diskFullPath := poolPath + "/" + diskName

	if usingExistingDisk {
		vol, err := c.GetStorageVolume(req.ExistingDiskPool, req.ExistingDiskName)
		if err != nil {
			return models.VM{}, fmt.Errorf("get existing disk: %w", err)
		}
		diskFullPath = vol.Path
		if diskFullPath == "" {
			return models.VM{}, fmt.Errorf("existing disk has no source path")
		}
		diskFormat = vol.Format
		if diskFormat == "" {
			diskFormat = "qcow2"
		}
	}

	diskBus := req.DiskBus
	if diskBus == "" {
		diskBus = "virtio"
	}
	targetDev := "vda"
	if diskBus == "sata" || diskBus == "scsi" || diskBus == "ide" {
		targetDev = "sda"
	}

	cpuMode := req.CPUMode
	if cpuMode == "" {
		cpuMode = "host-passthrough"
	}
	var topologyXML string
	if req.CPUSockets != nil || req.CPUCores != nil || req.CPUThreads != nil {
		if req.CPUSockets == nil || req.CPUCores == nil || req.CPUThreads == nil {
			return models.VM{}, fmt.Errorf("cpu_sockets, cpu_cores and cpu_threads must all be set together")
		}
		if *req.CPUSockets <= 0 || *req.CPUCores <= 0 || *req.CPUThreads <= 0 {
			return models.VM{}, fmt.Errorf("cpu_sockets, cpu_cores and cpu_threads must be positive")
		}
		if (*req.CPUSockets)*(*req.CPUCores)*(*req.CPUThreads) != req.VCPUs {
			return models.VM{}, fmt.Errorf("cpu_sockets * cpu_cores * cpu_threads (%d) must equal vcpus (%d)", (*req.CPUSockets)*(*req.CPUCores)*(*req.CPUThreads), req.VCPUs)
		}
		topologyXML = fmt.Sprintf("<topology sockets='%d' cores='%d' threads='%d'/>", *req.CPUSockets, *req.CPUCores, *req.CPUThreads)
	}

	var cpuXML string
	if cpuMode == "custom" && req.CPUModel != "" {
		cpuXML = fmt.Sprintf("<cpu mode='custom' match='exact'><model>%s</model>%s</cpu>", xmlEscape(req.CPUModel), topologyXML)
	} else {
		if topologyXML != "" {
			cpuXML = fmt.Sprintf("<cpu mode='%s' check='none'>%s</cpu>", xmlEscape(cpuMode), topologyXML)
		} else {
			cpuXML = fmt.Sprintf("<cpu mode='%s' check='none'/>", xmlEscape(cpuMode))
		}
	}

	videoModel := req.VideoModel
	if videoModel == "" {
		videoModel = "virtio"
	}
	var videoXML string
	if videoModel == "none" {
		videoXML = ""
	} else {
		videoXML = fmt.Sprintf("<video><model type='%s' heads='1' vram='16384'/></video>", xmlEscape(videoModel))
	}

	networkModel := req.NetworkModel
	if networkModel == "" {
		networkModel = "virtio"
	}

	isoXML := ""
	if req.ISO != "" {
		isoXML = fmt.Sprintf(`<disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>`, xmlEscape(req.ISO))
	}

	virtioISOXML := ""
	if req.VirtIOISO != "" {
		virtioISOXML = fmt.Sprintf(`<disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='sdb' bus='sata'/>
      <readonly/>
    </disk>`, xmlEscape(req.VirtIOISO))
	}

	chipset := req.Chipset
	if chipset == "" {
		chipset = "q35"
	}

	machine := chipset
	if machine == "i440fx" {
		machine = "pc"
	}

	secureBoot := req.SecureBoot != nil && *req.SecureBoot
	tpmEnabled := req.TPMEnabled != nil && *req.TPMEnabled

	firmware := req.Firmware
	if firmware == "" {
		if chipset == "q35" {
			firmware = "uefi"
		} else {
			firmware = "seabios"
		}
	}

	var osXML string
	var featuresXML string
	var devicesExtra string

	if firmware == "uefi" && chipset == "q35" {
		nvramPath := nvramDir + "/" + req.Name + "_VARS.fd"
		var loaderStr string
		var smmStr string
		if secureBoot {
			loaderStr = fmt.Sprintf("<loader readonly='yes' secure='yes' type='pflash'>%s</loader>", ovmfCodeSecboot)
			smmStr = "\n    <smm/>"
		} else {
			loaderStr = fmt.Sprintf("<loader readonly='yes' type='pflash'>%s</loader>", ovmfCode)
			smmStr = ""
		}
		osXML = fmt.Sprintf(`<os>
    <type arch='x86_64' machine='q35'>hvm</type>
    %s
    <nvram template='%s'>%s</nvram>
    <boot dev='hd'/>
  </os>`, loaderStr, ovmfVarsTemplate, nvramPath)
		featuresXML = fmt.Sprintf(`<features>
    <acpi/>
    <apic/>%s
  </features>`, smmStr)
	} else {
		osXML = fmt.Sprintf(`<os>
    <type arch='x86_64' machine='%s'>hvm</type>
    <boot dev='hd'/>
  </os>`, xmlEscape(machine))
		featuresXML = `<features>
    <acpi/>
    <apic/>
  </features>`
	}

	var controllerXML string
	if chipset == "q35" {
		controllerXML = `<controller type='pci' model='pcie-root'/>
    <controller type='pci' model='pcie-root-port'/>`
	} else {
		controllerXML = `<controller type='pci' model='pci-root'/>
    <controller type='pci' model='pci-bridge'/>`
	}

	if chipset == "q35" && tpmEnabled {
		devicesExtra += `<tpm model='tpm-crb'>
      <backend type='emulator' version='2.0'/>
    </tpm>
    `
	}

	devicesExtra += `<rng model='virtio'>
      <backend model='random'>/dev/urandom</backend>
    </rng>
    `

	diskDriverAttrs := diskDriverXMLAttrs(diskFormat, req.DiskCacheIO, req.DiskDiscard)

	uuidStr := uuid.New().String()
	title := xmlEscape(req.Name)
	if req.OSType != "" {
		title += " [OSType=" + xmlEscape(req.OSType)
		if req.OSVersion != "" {
			title += " OSVersion=" + xmlEscape(req.OSVersion)
		}
		title += "]"
	}
	xmlConfig := fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <uuid>%s</uuid>
  <title>%s</title>
  <memory unit='MiB'>%d</memory>
  <vcpu placement='static'>%d</vcpu>
  %s
  %s
  %s
  <clock offset='utc'>
    <timer name='rtc' tickpolicy='catchup'/>
    <timer name='pit' tickpolicy='delay'/>
    <timer name='hpet' present='no'/>
  </clock>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <controller type='usb' model='qemu-xhci'/>
    %s
    <disk type='file' device='disk'>
      <driver %s/>
      <source file='%s'/>
      <target dev='%s' bus='%s'/>
    </disk>
    %s
    %s
    <interface type='network'>
      <source network='%s'/>
      <model type='%s'/>
    </interface>
    <serial type='pty'>
      <target port='0'/>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
    <channel type='unix'>
      <target type='virtio' name='org.qemu.guest_agent.0'/>
    </channel>
    <graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'>
      <listen type='address' address='0.0.0.0'/>
    </graphics>
    %s
    %s
  </devices>
</domain>`, xmlEscape(req.Name), uuidStr, title, req.RAMMB, req.VCPUs, osXML, featuresXML, cpuXML, controllerXML, diskDriverAttrs, xmlEscape(diskFullPath), xmlEscape(targetDev), xmlEscape(diskBus), isoXML, virtioISOXML, xmlEscape(defaultNetwork(req.Network)), xmlEscape(networkModel), videoXML, devicesExtra)

	dom, err := c.conn.DomainDefineXML(xmlConfig)
	if err != nil {
		return models.VM{}, fmt.Errorf("define domain: %w", err)
	}
	defer dom.Free()

	if !usingExistingDisk && req.DiskGB > 0 {
		if err := c.createDiskInPool(poolName, diskName, req.DiskGB*1024, diskFormat); err != nil {
			dom.Undefine()
			return models.VM{}, fmt.Errorf("create disk: %w", err)
		}
	}

	return c.domainToVM(dom)
}

func (c *Connector) StartDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	return dom.Create()
}

func (c *Connector) ShutdownDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	return dom.Shutdown()
}

func (c *Connector) ForceOffDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	return dom.Destroy()
}

func (c *Connector) RebootDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	return dom.Reboot(0)
}

func (c *Connector) SuspendDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	return dom.Suspend()
}

func (c *Connector) ResumeDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	return dom.Resume()
}

func (c *Connector) DeleteDomain(id string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	state, _, err := dom.GetState()
	if err == nil && state == libvirt.DOMAIN_RUNNING {
		dom.Destroy()
	}

	flags := libvirt.DOMAIN_UNDEFINE_MANAGED_SAVE | libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_NVRAM
	return dom.UndefineFlags(flags)
}

func (c *Connector) ListSnapshots(domainID string) ([]models.Snapshot, error) {
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return nil, err
	}
	defer dom.Free()

	// For each snapshot, we look up "<vmname>.<snapname>" in the disk
	// pool to read the libvirt `Allocated` (= data size when the
	// snapshot was created). The domain name is needed to build the
	// volume key; get it once up front so the per-snapshot lookup is
	// a single libvirt call.
	domainName, _ := dom.GetName()
	poolName := c.DiskPoolName()

	snaps, err := dom.ListAllSnapshots(0)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	currentSnap, _ := dom.SnapshotCurrent(0)

	result := make([]models.Snapshot, 0, len(snaps))
	for i := range snaps {
		name, _ := snaps[i].GetName()
		xmlDesc, _ := snaps[i].GetXMLDesc(0)
		created := extractSnapshotCreation(xmlDesc)
		epoch := extractSnapshotEpoch(xmlDesc)
		parentName := extractSnapshotParent(xmlDesc)
		// Fall back to libvirt's GetParent if XML parsing yielded nothing.
		if parentName == "" {
			if parent, err := snaps[i].GetParent(0); err == nil && parent != nil {
				if pname, err := parent.GetName(); err == nil {
					parentName = pname
				}
				parent.Free()
			}
		}

		isCurrent := false
		if currentSnap != nil {
			curName, _ := currentSnap.GetName()
			isCurrent = curName == name
		}

		snaps[i].Free()

		// Best-effort: enrich with the data size when the snapshot
		// was created. The matching volume lives in the disk pool
		// under "<vmname>.<snapname>". If the pool isn't running
		// or the view isn't visible, the field stays 0 and the
		// frontend shows "—".
		var sizeAtSnap int64
		if domainName != "" {
			if vol, err := c.lookupSnapshotVolume(poolName, domainName+"."+name); err == nil {
				sizeAtSnap = vol.Allocated
			}
		}

		result = append(result, models.Snapshot{
			ID:              name,
			Name:            name,
			CreatedAt:       created,
			CreationTime:    epoch,
			ParentName:      parentName,
			Current:         isCurrent,
			SizeAtSnapBytes: sizeAtSnap,
		})
	}

	if currentSnap != nil {
		currentSnap.Free()
	}

	return result, nil
}

// ErrMemorySnapshotRequiresRunning is returned when a memory snapshot
// is requested for a VM that is not running.
var ErrMemorySnapshotRequiresRunning = errors.New("a memory snapshot requires the VM to be running")

func (c *Connector) CreateSnapshot(domainID string, req models.CreateSnapshotRequest) (models.Snapshot, error) {
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return models.Snapshot{}, err
	}
	defer dom.Free()

	xmlStr := fmt.Sprintf(`<domainsnapshot>
  <name>%s</name>
  <description>%s</description>
</domainsnapshot>`, xmlEscape(req.Name), xmlEscape(req.Description))

	if req.Memory {
		// Full snapshot: disk + RAM. Requires the VM to be running —
		// libvirt would otherwise create a disk-only snapshot silently,
		// which is misleading. Fail fast with a clear message instead.
		state, _, err := dom.GetState()
		if err != nil {
			return models.Snapshot{}, fmt.Errorf("get domain state: %w", err)
		}
		if state != libvirt.DOMAIN_RUNNING {
			return models.Snapshot{}, ErrMemorySnapshotRequiresRunning
		}
		if _, err := dom.CreateSnapshotXML(xmlStr, 0); err != nil {
			return models.Snapshot{}, fmt.Errorf("create memory snapshot: %w", err)
		}
	} else {
		// Disk-only. If the VM is running, try to quiesce the
		// filesystem via the guest agent first (fsfreeze/fsthaw,
		// handled atomically by libvirt itself) for a
		// filesystem-consistent snapshot instead of merely
		// crash-consistent. Best-effort: VMs without a responsive
		// guest agent fall back to the existing behavior.
		state, _, _ := dom.GetState()
		var err error
		if state == libvirt.DOMAIN_RUNNING {
			_, err = dom.CreateSnapshotXML(xmlStr, libvirt.DOMAIN_SNAPSHOT_CREATE_DISK_ONLY|libvirt.DOMAIN_SNAPSHOT_CREATE_QUIESCE)
		} else {
			err = fmt.Errorf("not running")
		}
		if err != nil {
			// Fall back: disk-only without quiesce, then a full
			// snapshot if disk-only itself isn't supported.
			_, err = dom.CreateSnapshotXML(xmlStr, libvirt.DOMAIN_SNAPSHOT_CREATE_DISK_ONLY)
			if err != nil {
				_, err = dom.CreateSnapshotXML(xmlStr, 0)
				if err != nil {
					return models.Snapshot{}, fmt.Errorf("create snapshot: %w", err)
				}
			}
		}
	}

	snap := models.Snapshot{
		ID:          req.Name,
		Name:        req.Name,
		Description: req.Description,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}

	// Best-effort: enrich the response with the data size at
	// creation (libvirt `Allocated` on the new volume view). The
	// volume may not be visible to ListAllStorageVolumes for a
	// moment after CreateSnapshotXML; if the lookup fails, the
	// field stays 0 and the next ListSnapshots call will fill it.
	//
	// Force a pool refresh first: the libvirt connection's pool
	// handle has its own in-memory volume cache (separate from
	// libvirtd's on-disk state), and a freshly-created snapshot
	// volume isn't visible to that cache until a refresh. The
	// refresh is best-effort — a failure here is non-fatal and
	// just leaves SizeAtSnapBytes at 0, which the next
	// ListSnapshots call (with its own ListAllStorageVolumes)
	// will populate.
	if name, _ := dom.GetName(); name != "" {
		_ = c.RefreshPool(c.DiskPoolName())
		if vol, err := c.lookupSnapshotVolume(c.DiskPoolName(), name+"."+req.Name); err == nil {
			snap.SizeAtSnapBytes = vol.Allocated
		}
	}
	return snap, nil
}

// DeleteSnapshot removes a snapshot and returns the libvirt
// "Allocated" of the volume that backed it, so the caller can record
// the freed space in the audit log. Returns 0 when the volume wasn't
// visible (e.g. pool inactive) — the snapshot was still removed.
func (c *Connector) DeleteSnapshot(domainID, snapID string) (int64, error) {
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return 0, err
	}
	defer dom.Free()

	var allocated int64
	if name, _ := dom.GetName(); name != "" {
		// Same pool-cache caveat as CreateSnapshot: a refresh
		// makes sure the volume's current Allocated size is
		// visible before we record it. Best-effort.
		_ = c.RefreshPool(c.DiskPoolName())
		if vol, err := c.lookupSnapshotVolume(c.DiskPoolName(), name+"."+snapID); err == nil {
			allocated = vol.Allocated
		}
	}

	snap, err := dom.SnapshotLookupByName(snapID, 0)
	if err != nil {
		return 0, fmt.Errorf("lookup snapshot: %w", err)
	}
	defer snap.Free()

	if err := deleteSnapshotRecursive(snap); err != nil {
		return 0, err
	}
	return allocated, nil
}

func deleteSnapshotRecursive(snap *libvirt.DomainSnapshot) error {
	children, err := snap.ListAllChildren(0)
	if err != nil {
		return fmt.Errorf("list children: %w", err)
	}
	for i := range children {
		if err := deleteSnapshotRecursive(&children[i]); err != nil {
			for j := i; j < len(children); j++ {
				children[j].Free()
			}
			return err
		}
		children[i].Free()
	}
	return snap.Delete(0)
}

func (c *Connector) RevertSnapshot(domainID, snapID string) error {
	dom, err := c.lookupDomain(domainID)
	if err != nil {
		return err
	}
	defer dom.Free()

	snap, err := dom.SnapshotLookupByName(snapID, 0)
	if err != nil {
		return fmt.Errorf("lookup snapshot: %w", err)
	}
	defer snap.Free()

	return snap.RevertToSnapshot(0)
}

func (c *Connector) UpdateDomain(id string, req models.UpdateVMRequest) (models.VM, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return models.VM{}, err
	}

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	dom.Free()
	if err != nil {
		return models.VM{}, fmt.Errorf("get domain xml: %w", err)
	}

	if req.RAMMB != nil {
		newValMiB := fmt.Sprintf("<memory unit='MiB'>%d</memory>", *req.RAMMB)
		newValKiB := fmt.Sprintf("<memory unit='KiB'>%d</memory>", *req.RAMMB*1024)
		reMiB := regexp.MustCompile(`<memory unit='MiB'>\d+</memory>`)
		reKiB := regexp.MustCompile(`<memory unit='KiB'>\d+</memory>`)
		xmlDesc = reMiB.ReplaceAllString(xmlDesc, newValMiB)
		xmlDesc = reKiB.ReplaceAllString(xmlDesc, newValKiB)
		reCurrentMiB := regexp.MustCompile(`<currentMemory unit='MiB'>\d+</currentMemory>`)
		reCurrentKiB := regexp.MustCompile(`<currentMemory unit='KiB'>\d+</currentMemory>`)
		xmlDesc = reCurrentMiB.ReplaceAllString(xmlDesc, fmt.Sprintf("<currentMemory unit='MiB'>%d</currentMemory>", *req.RAMMB))
		xmlDesc = reCurrentKiB.ReplaceAllString(xmlDesc, fmt.Sprintf("<currentMemory unit='KiB'>%d</currentMemory>", *req.RAMMB*1024))
	}
	if req.VCPUs != nil {
		newVal := fmt.Sprintf("<vcpu placement='static'>%d</vcpu>", *req.VCPUs)
		re := regexp.MustCompile(`<vcpu placement='static'>\d+</vcpu>`)
		xmlDesc = re.ReplaceAllString(xmlDesc, newVal)
	}
	if req.CPUMode != nil {
		re := regexp.MustCompile(`<cpu [^>]*mode='[^']+'`)
		xmlDesc = re.ReplaceAllString(xmlDesc, "<cpu mode='"+xmlEscape(*req.CPUMode)+"'")
		if *req.CPUMode != "host-passthrough" && *req.CPUMode != "maximum" {
			xmlDesc = strings.ReplaceAll(xmlDesc, " migratable='on'", "")
		}
	}
	if req.VideoModel != nil {
		re := regexp.MustCompile(`<video>[^<]*<model type='[^']+'`)
		xmlDesc = re.ReplaceAllString(xmlDesc, "<video>\n      <model type='"+xmlEscape(*req.VideoModel)+"'")
	}
	if req.Network != nil {
		re := regexp.MustCompile(`network='[^']+'`)
		xmlDesc = re.ReplaceAllString(xmlDesc, "network='"+xmlEscape(*req.Network)+"'")
	}
	if req.NetworkModel != nil {
		re := regexp.MustCompile(`(<interface\b[^>]*>[\s\S]*?<model\s+type=')[^']+(')`)
		xmlDesc = re.ReplaceAllString(xmlDesc, "${1}"+xmlEscape(*req.NetworkModel)+"${2}")
	}
	if req.Name != nil {
		xmlDesc = regexp.MustCompile(`<name>[^<]+</name>`).ReplaceAllString(xmlDesc, "<name>"+xmlEscape(*req.Name)+"</name>")
	}
	if req.Chipset != nil {
		machine := *req.Chipset
		if machine == "i440fx" {
			machine = "pc"
		}
		xmlDesc = regexp.MustCompile(`machine='[^']+'`).ReplaceAllString(xmlDesc, "machine='"+xmlEscape(machine)+"'")
	}
	if req.SecureBoot != nil {
		if *req.SecureBoot {
			if !strings.Contains(xmlDesc, "secure='yes'") {
				xmlDesc = regexp.MustCompile(`<loader[^>]*/>`).ReplaceAllString(xmlDesc,
					fmt.Sprintf("<loader readonly='yes' secure='yes' type='pflash'>%s</loader>", ovmfCodeSecboot))
				// Add SMM feature if not present
				if !strings.Contains(xmlDesc, "<smm>") {
					xmlDesc = strings.Replace(xmlDesc, "<features>", "<features>\n    <smm/>", 1)
				}
				// Add NVRAM
				name := extractTagValue(xmlDesc, "name")
				if !strings.Contains(xmlDesc, "<nvram>") {
					xmlDesc = strings.Replace(xmlDesc, "</os>", fmt.Sprintf("\n    <nvram template='%s'>%s/%s_VARS.fd</nvram>\n  </os>", ovmfVarsTemplate, nvramDir, name), 1)
				}
			}
		} else {
			xmlDesc = strings.ReplaceAll(xmlDesc, " secure='yes'", "")
			xmlDesc = strings.ReplaceAll(xmlDesc, ` secure="yes"`, "")
			xmlDesc = regexp.MustCompile(`<smm/>\s*`).ReplaceAllString(xmlDesc, "")
		}
	}
	if req.Firmware != nil {
		if *req.Firmware == "seabios" {
			xmlDesc = regexp.MustCompile(`<loader\b[^>]*>.*?</loader>\s*`).ReplaceAllString(xmlDesc, "")
			xmlDesc = regexp.MustCompile(`<nvram>.*?</nvram>\s*`).ReplaceAllString(xmlDesc, "")
			xmlDesc = regexp.MustCompile(`<smm/>\s*`).ReplaceAllString(xmlDesc, "")
			xmlDesc = strings.ReplaceAll(xmlDesc, " secure='yes'", "")
		} else if *req.Firmware == "uefi" && !strings.Contains(xmlDesc, "<loader") {
			xmlDesc = strings.Replace(xmlDesc, "</type>", "</type>\n    "+fmt.Sprintf("<loader readonly='yes' type='pflash'>%s</loader>", ovmfCode), 1)
			name := extractTagValue(xmlDesc, "name")
			if !strings.Contains(xmlDesc, "<nvram>") {
				xmlDesc = strings.Replace(xmlDesc, "</os>", fmt.Sprintf("\n    <nvram template='%s'>%s/%s_VARS.fd</nvram>\n  </os>", ovmfVarsTemplate, nvramDir, name), 1)
			}
		}
	}
	if req.TPMEnabled != nil {
		if *req.TPMEnabled {
			if !strings.Contains(xmlDesc, "<tpm") {
				tpmXML := `<tpm model='tpm-crb'>
      <backend type='emulator' version='2.0'/>
    </tpm>`
				xmlDesc = strings.Replace(xmlDesc, "</devices>", "    "+tpmXML+"\n  </devices>", 1)
			}
		} else {
			xmlDesc = regexp.MustCompile(`<tpm\b[^>]*>.*?</tpm>\s*`).ReplaceAllString(xmlDesc, "")
		}
	}
	if req.OSType != nil || req.OSVersion != nil {
		title := extractTagValue(xmlDesc, "title")
		if req.OSType != nil {
			title = updateOSTag(title, "OSType", *req.OSType)
		}
		if req.OSVersion != nil {
			title = updateOSTag(title, "OSVersion", *req.OSVersion)
		}
		xmlDesc = regexp.MustCompile(`<title>[^<]*</title>`).ReplaceAllString(xmlDesc, "<title>"+xmlEscape(title)+"</title>")
	}

	newDom, err := c.conn.DomainDefineXML(xmlDesc)
	if err != nil {
		return models.VM{}, fmt.Errorf("define domain: %w", err)
	}
	defer newDom.Free()

	return c.domainToVM(newDom)
}

func updateOSTag(title, tag, value string) string {
	re := regexp.MustCompile(`\[` + tag + `=[^\]]*\]`)
	result := re.FindString(title)
	if result != "" {
		return re.ReplaceAllString(title, "["+tag+"="+value+"]")
	}
	return title + " [" + tag + "=" + value + "]"
}

func (c *Connector) lookupDomain(id string) (*libvirt.Domain, error) {
	dom, err := c.conn.LookupDomainByUUIDString(id)
	if err != nil {
		dom, err = c.conn.LookupDomainByName(id)
		if err != nil {
			return nil, fmt.Errorf("domain not found: %w", err)
		}
	}
	return dom, nil
}

func (c *Connector) domainToVM(dom *libvirt.Domain) (models.VM, error) {
	name, err := dom.GetName()
	if err != nil {
		return models.VM{}, err
	}

	uuidStr, err := dom.GetUUIDString()
	if err != nil {
		return models.VM{}, err
	}

	state, reason, err := dom.GetState()
	if err != nil {
		return models.VM{}, err
	}

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		xmlDesc, _ = dom.GetXMLDesc(0)
	}

	info, err := dom.GetInfo()
	if err != nil {
		return models.VM{}, err
	}

	vmState := stateToVMState(state)

	var uptime int64
	if state == libvirt.DOMAIN_RUNNING {
		ctrlInfo, err := dom.GetControlInfo(0)
		if err == nil && ctrlInfo.StateTime > 0 {
			since := time.Since(time.UnixMicro(int64(ctrlInfo.StateTime)))
			uptime = int64(since.Seconds())
			if uptime < 0 {
				uptime = 0
			}
		}
		if uptime == 0 {
			uptime = int64(info.CpuTime / 1e9)
		}
	}

	machine := attrValue(xmlDesc, "machine")
	chipset := "q35"
	if machine == "pc" || strings.Contains(machine, "i440fx") {
		chipset = "i440fx"
	}

	secureBoot := strings.Contains(xmlDesc, "secure='yes'") || strings.Contains(xmlDesc, `secure="yes"`)
	tpmEnabled := strings.Contains(xmlDesc, "<tpm")

	firmware := "seabios"
	if strings.Contains(xmlDesc, "<loader") {
		firmware = "uefi"
	}

	cpuMode := "host-passthrough"
	if m := regexp.MustCompile(`<cpu[^>]*mode='([^']*)'`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		cpuMode = m[1]
	} else if m := regexp.MustCompile(`<cpu[^>]*mode="([^"]*)"`).FindStringSubmatch(xmlDesc); len(m) > 1 {
		cpuMode = m[1]
	}

	videoModel := "virtio"
	if vb := regexp.MustCompile(`<video>([\s\S]*?)</video>`).FindString(xmlDesc); vb != "" {
		if m := regexp.MustCompile(`<model\s+type='([^']*)'`).FindStringSubmatch(vb); len(m) > 1 {
			videoModel = m[1]
		} else if m := regexp.MustCompile(`<model\s+type="([^"]*)"`).FindStringSubmatch(vb); len(m) > 1 {
			videoModel = m[1]
		}
	}

	vm := models.VM{
		ID:         uuidStr,
		Name:       name,
		State:      vmState,
		VCPUs:      int(info.NrVirtCpu),
		RAMMB:      int64(info.MaxMem) / 1024,
		UptimeSec:  uptime,
		CPUUsage:   calculateCPUUsage(dom, info),
		RAMUsedMB:  int64(info.Memory) / 1024,
		OSIcon:     detectOSIcon(name, xmlDesc),
		OSType:     detectOSType(name, xmlDesc),
		OSVersion:  detectOSVersion(xmlDesc),
		Chipset:    chipset,
		SecureBoot: secureBoot,
		TPMEnabled: tpmEnabled,
		// Autostart: queried separately because libvirt's
		// GetAutostart returns an error for transient states
		// (e.g. the domain is being shut off). Default to false
		// on error — a missing autostart flag in the UI is less
		// surprising than a domain that silently restarts on
		// every boot.
		Autostart:  autostartEnabledOrFalse(dom),
		Firmware:   firmware,
		CPUMode:    cpuMode,
		VideoModel: videoModel,
	}

	diskGB := extractDiskSize(xmlDesc)
	if diskGB > 0 {
		vm.DiskGB = diskGB
	}

	vm.Disks = c.parseDisks(xmlDesc)
	vm.Networks = parseNetworks(xmlDesc)
	vm.USBDevices = parseUSBHostdevs(xmlDesc)

	// Fallback: extractDiskSize silently returns 0 for disks that
	// don't have a <capacity unit=...> child (LVM, zvols, OVA imports
	// before first boot, etc.). In that case, take the largest disk
	// from the parsed list — the per-disk table on the UI is reliable
	// because parseDisksFiltered populates SizeGB via volPoolAndSize.
	// The user-facing "disk_gb" field shouldn't read 0 when the data
	// is right there.
	if vm.DiskGB == 0 && len(vm.Disks) > 0 {
		var max int64
		for i := range vm.Disks {
			d := &vm.Disks[i]
			if d.Type == "file" && d.SizeGB > max {
				max = d.SizeGB
			}
		}
		vm.DiskGB = max
	}

	// Load WebKVM metadata (alias, cover, groups). Failure is non-fatal;
	// the VM list still works without app-level metadata.
	if meta, err := c.GetVMMeta(uuidStr); err == nil {
		vm.Alias = meta.Alias
		vm.Cover = meta.Cover
		vm.Groups = meta.Groups
	}

	if state == libvirt.DOMAIN_RUNNING {
		ifaces, err := dom.ListAllInterfaceAddresses(libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_LEASE)
		if err == nil {
			for _, iface := range ifaces {
				for _, a := range iface.Addrs {
					if a.Type == libvirt.IP_ADDR_TYPE_IPV4 {
						vm.IP = a.Addr
						break
					}
				}
				if vm.IP != "" {
					break
				}
			}
		}
		if vm.IP == "" {
			ifaces, err = dom.ListAllInterfaceAddresses(libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_ARP)
			if err == nil {
				for _, iface := range ifaces {
					for _, a := range iface.Addrs {
						if a.Type == libvirt.IP_ADDR_TYPE_IPV4 {
							vm.IP = a.Addr
							break
						}
					}
					if vm.IP != "" {
						break
					}
				}
			}
		}
	}

	_ = reason
	return vm, nil
}

func stateToVMState(state libvirt.DomainState) models.VMState {
	switch state {
	case libvirt.DOMAIN_RUNNING:
		return models.VMStateRunning
	case libvirt.DOMAIN_SHUTOFF, libvirt.DOMAIN_SHUTDOWN:
		return models.VMStateShutoff
	case libvirt.DOMAIN_PAUSED:
		return models.VMStatePaused
	case libvirt.DOMAIN_CRASHED:
		return models.VMStateCrashed
	default:
		return models.VMStateUnknown
	}
}

func defaultNetwork(net string) string {
	if net == "" {
		return "default"
	}
	return net
}

type GraphicsInfo struct {
	Type      string `json:"type"`
	Port      int    `json:"port"`
	WebSocket int    `json:"websocket,omitempty"`
	Host      string `json:"host"`
}

func (c *Connector) GetDomainIP(id string) string {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return ""
	}
	defer dom.Free()

	ifaces, err := dom.ListAllInterfaceAddresses(libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_LEASE)
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		for _, a := range iface.Addrs {
			if a.Type == libvirt.IP_ADDR_TYPE_IPV4 {
				return a.Addr
			}
		}
	}
	return ""
}

func (c *Connector) GetVNCInfo(id string) (GraphicsInfo, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return GraphicsInfo{}, err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return GraphicsInfo{}, fmt.Errorf("get xml: %w", err)
	}

	graphicsRe := regexp.MustCompile(`<graphics[^>]+>`)
	graphicsMatch := graphicsRe.FindString(xmlDesc)
	if graphicsMatch == "" {
		return GraphicsInfo{}, fmt.Errorf("no graphics device found")
	}

	gType := attrValue(graphicsMatch, "type")
	portStr := attrValue(graphicsMatch, "port")
	host := attrValue(graphicsMatch, "listen")

	port := 5900
	if portStr != "" {
		p, err := strconv.Atoi(portStr)
		if err == nil && p > 0 {
			port = p
		}
	}
	if host == "" {
		host = "127.0.0.1"
	}

	return GraphicsInfo{
		Type: gType,
		Port: port,
		Host: host,
	}, nil
}

func attrValue(xml, attr string) string {
	re := regexp.MustCompile(attr + `='([^']+)'`)
	m := re.FindStringSubmatch(xml)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

func (c *Connector) createDiskInPool(poolName, volName string, sizeMB int64, format string) error {
	pool, err := c.conn.LookupStoragePoolByName(poolName)
	if err != nil {
		return fmt.Errorf("lookup pool: %w", err)
	}
	defer pool.Free()

	// Validate format
	validFormats := []string{"qcow2", "raw"}
	valid := false
	for _, f := range validFormats {
		if format == f {
			valid = true
			break
		}
	}
	if !valid {
		format = "qcow2" // Default to qcow2 if invalid
	}

	volXML := fmt.Sprintf(`<volume>
        <name>%s</name>
        <capacity unit='M'>%d</capacity>
        <target>
            <format type='%s'/>
        </target>
    </volume>`, volName, sizeMB, format)

	_, err = pool.StorageVolCreateXML(volXML, libvirt.STORAGE_VOL_CREATE_PREALLOC_METADATA)
	return err
}

func calculateCPUUsage(dom *libvirt.Domain, info *libvirt.DomainInfo) float64 {
	if info.NrVirtCpu == 0 {
		return 0
	}
	cpuTime := info.CpuTime
	elapsed := float64(time.Second)
	if cpuTime > 0 {
		usage := (float64(cpuTime) / elapsed / float64(info.NrVirtCpu)) * 100
		return math.Min(usage, 100)
	}
	return 0
}

func detectOSIcon(name, xmlDesc string) string {
	nameLower := strings.ToLower(name)
	switch {
	case strings.Contains(nameLower, "ubuntu"):
		return "ubuntu"
	case strings.Contains(nameLower, "debian"):
		return "debian"
	case strings.Contains(nameLower, "fedora"):
		return "fedora"
	case strings.Contains(nameLower, "centos"), strings.Contains(nameLower, "rhel"):
		return "centos"
	case strings.Contains(nameLower, "arch"):
		return "arch"
	case strings.Contains(nameLower, "windows"):
		return "windows"
	case strings.Contains(nameLower, "win"):
		return "windows"
	case strings.Contains(nameLower, "freebsd"):
		return "freebsd"
	case strings.Contains(nameLower, "opensuse"), strings.Contains(nameLower, "suse"):
		return "suse"
	case strings.Contains(nameLower, "alpine"):
		return "alpine"
	default:
		return "linux"
	}
}

func detectOSType(name, xmlDesc string) string {
	title := extractTagValue(xmlDesc, "title")
	if title != "" {
		re := regexp.MustCompile(`OSType=([^\s\]]+)`)
		m := re.FindStringSubmatch(title)
		if len(m) > 1 {
			return m[1]
		}
	}
	return detectOSIcon(name, xmlDesc)
}

func detectOSVersion(xmlDesc string) string {
	title := extractTagValue(xmlDesc, "title")
	if title != "" {
		re := regexp.MustCompile(`OSVersion=([^\s\]]+)`)
		m := re.FindStringSubmatch(title)
		if len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

func extractSnapshotCreation(xmlDesc string) string {
	for _, line := range strings.Split(xmlDesc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<creationTime>") {
			ts := strings.TrimSuffix(strings.TrimPrefix(line, "<creationTime>"), "</creationTime>")
			sec, err := strconv.ParseInt(ts, 10, 64)
			if err != nil {
				return ts
			}
			return time.Unix(sec, 0).UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// extractSnapshotEpoch returns the <creationTime> as a raw epoch-seconds
// int64 (0 if missing/unparseable). Used by the tree UI for sortable
// timestamps.
func extractSnapshotEpoch(xmlDesc string) int64 {
	for _, line := range strings.Split(xmlDesc, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<creationTime>") {
			ts := strings.TrimSuffix(strings.TrimPrefix(line, "<creationTime>"), "</creationTime>")
			sec, err := strconv.ParseInt(ts, 10, 64)
			if err == nil {
				return sec
			}
			return 0
		}
	}
	return 0
}

// extractSnapshotParent returns the <parent><name>...</name></parent>
// value, or "" if there is no parent (i.e. this is a root snapshot).
func extractSnapshotParent(xmlDesc string) string {
	s := strings.Index(xmlDesc, "<parent>")
	if s < 0 {
		s = strings.Index(xmlDesc, "<parent ")
		if s < 0 {
			return ""
		}
	}
	block := xmlDesc[s:]
	e := strings.Index(block, "</parent>")
	if e < 0 {
		return ""
	}
	block = block[:e]
	ns := strings.Index(block, "<name>")
	if ns < 0 {
		ns = strings.Index(block, "<name ")
		if ns < 0 {
			return ""
		}
	}
	block = block[ns:]
	ne := strings.Index(block, "</name>")
	if ne < 0 {
		return ""
	}
	name := strings.TrimPrefix(block[:ne], "<name>")
	name = strings.TrimSpace(name)
	// Handle <name attr='x'>name</name> form.
	if strings.HasPrefix(name, " ") {
		// The "<name " form: extract just the inner text.
		closeTag := strings.Index(name, ">")
		if closeTag >= 0 {
			name = name[closeTag+1:]
		}
	}
	return strings.TrimSpace(name)
}

func extractDiskSize(xmlDesc string) int64 {
	// Simple extraction from domain XML
	lines := strings.Split(xmlDesc, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.Contains(line, "<capacity unit=") {
			capStr := extractTagValue(line, "capacity")
			unit := extractTagAttr(line, "unit")
			val, err := strconv.ParseInt(capStr, 10, 64)
			if err != nil {
				continue
			}
			switch strings.ToLower(unit) {
			case "bytes", "b":
				return val / (1024 * 1024 * 1024)
			case "kb", "k":
				return val / (1024 * 1024)
			case "mb", "m":
				return val / 1024
			case "gb", "g":
				return val
			}
			return val
		}
	}
	return 0
}

func extractTagValue(xml, tag string) string {
	re := regexp.MustCompile(`<` + tag + `[^>]*>(.*?)</` + tag + `>`)
	m := re.FindStringSubmatch(xml)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// volPoolAndSize resolves the storage pool name and capacity (in GB) for a
// given volume file path using libvirt's canonical LookupStorageVolByPath API.
func (c *Connector) volPoolAndSize(volPath string) (pool string, sizeGB int64) {
	if volPath == "" {
		return "", 0
	}
	vol, err := c.conn.LookupStorageVolByPath(volPath)
	if err != nil {
		return "", 0
	}
	defer vol.Free()
	poolObj, err := vol.LookupPoolByVolume()
	if err != nil {
		return "", 0
	}
	defer poolObj.Free()
	pool, _ = poolObj.GetName()
	info, err := vol.GetInfo()
	if err == nil && info.Capacity > 0 {
		sizeGB = int64(info.Capacity / (1024 * 1024 * 1024))
	}
	return pool, sizeGB
}

func (c *Connector) parseDisks(xmlDesc string) []models.DiskInfo {
	return c.parseDisksFiltered(xmlDesc, false)
}

// parseDisksFiltered returns the list of <disk> elements. When
// disksOnly is true, CD-ROMs (device='cdrom') are excluded; the rest
// of the codebase passes false because the UI shows the VM's full
// disk list (including the optical drive), but exports and disk
// resize must skip CD-ROMs because they aren't real disks.

// deepestSourcePath extracts the root (deepest backing file) source path from a
// disk XML fragment when libvirt includes the <backingStore> chain.
func deepestSourcePath(diskXML string) string {
	re := regexp.MustCompile(`<source\b[^>]*file='([^']+)'`)
	matches := re.FindAllStringSubmatch(diskXML, -1)
	// A single source means there is no backing chain in the XML; rely on qemu-img.
	if len(matches) <= 1 {
		return ""
	}
	// The last <source file> in the fragment is the root of the backing chain.
	return matches[len(matches)-1][1]
}

// rootDiskName returns the base file name of a disk image, walking the backing
// chain with qemu-img when the XML does not expose it. This gives stable names
// (e.g. "ubuntu-1.qcow2") even when the active source is a snapshot overlay.
func rootDiskName(path string) string {
	if path == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", "--backing-chain", path).Output()
	if err != nil {
		return filepath.Base(path)
	}
	var chain []struct {
		Filename string `json:"filename"`
	}
	if err := json.Unmarshal(out, &chain); err != nil || len(chain) == 0 {
		return filepath.Base(path)
	}
	return filepath.Base(chain[len(chain)-1].Filename)
}

func (c *Connector) parseDisksFiltered(xmlDesc string, disksOnly bool) []models.DiskInfo {
	disks := make([]models.DiskInfo, 0)
	// Use [\s\S]*? to match multiline XML (Go regex default is DOTALL=false)
	re := regexp.MustCompile(`<disk\b([^>]*)>[\s\S]*?</disk>`)
	matches := re.FindAllString(xmlDesc, -1)
	for _, d := range matches {
		device := attrValue(d, "device")
		if disksOnly && device == "cdrom" {
			continue
		}
		dType := attrValue(d, "type")
		bus := ""
		var targetDev string
		targetRe := regexp.MustCompile(`<target\b[^>]*dev='([^']+)'[^>]*bus='([^']+)'`)
		tm := targetRe.FindStringSubmatch(d)
		if len(tm) > 2 && tm[2] != "" {
			targetDev = tm[1]
			bus = tm[2]
		} else if len(tm) > 1 {
			targetDev = tm[1]
			switch {
			case strings.HasPrefix(targetDev, "vd"):
				bus = "virtio"
			case strings.HasPrefix(targetDev, "hd"):
				bus = "ide"
			case strings.HasPrefix(targetDev, "sd"):
				bus = "sata"
			}
		}
		source := ""
		sourceRe := regexp.MustCompile(`<source\b[^>]*file='([^']+)'`)
		sm := sourceRe.FindStringSubmatch(d)
		if len(sm) > 1 {
			source = sm[1]
		}
		readOnly := strings.Contains(d, "<readonly/>") || strings.Contains(d, "<readonly>")
		pool, sizeGB := c.volPoolAndSize(source)
		displaySource := source
		if ds := deepestSourcePath(d); ds != "" {
			displaySource = ds
		} else if source != "" {
			displaySource = rootDiskName(source)
		}
		disks = append(disks, models.DiskInfo{
			Device:   device,
			Bus:      bus,
			Target:   targetDev,
			Source:   source,
			Name:     filepath.Base(displaySource),
			Pool:     pool,
			SizeGB:   sizeGB,
			ReadOnly: readOnly,
			Type:     dType,
		})
	}
	return disks
}

func parseNetworks(xmlDesc string) []models.NetIface {
	ifaces := make([]models.NetIface, 0)
	// Use [\s\S]*? to match multiline XML (Go regex default is DOTALL=false)
	re := regexp.MustCompile(`<interface\b([^>]*)>[\s\S]*?</interface>`)
	matches := re.FindAllString(xmlDesc, -1)
	for _, i := range matches {
		iface := models.NetIface{
			Type:    attrValue(i, "type"),
			MAC:     attrValue(i, "address"),
			Model:   attrValue(i, "type"),
			Network: attrValue(i, "network"),
			Source:  attrValue(i, "bridge"),
		}
		// also check <source network='xxx'>
		if iface.Network == "" {
			iface.Network = extractTagAttr(i, "network")
		}
		if iface.Source == "" {
			iface.Source = extractTagAttr(i, "bridge")
		}
		// model from <model type='xxx'>
		modelRe := regexp.MustCompile(`<model\b[^>]*type='([^']+)'`)
		mm := modelRe.FindStringSubmatch(i)
		if len(mm) > 1 {
			iface.Model = mm[1]
		}
		ifaces = append(ifaces, iface)
	}
	return ifaces
}

func (c *Connector) AttachDisk(id string, req models.AttachDiskRequest) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	format := req.Format
	if format == "" {
		format = "qcow2"
	}
	volExt := ".qcow2"
	if format == "raw" {
		volExt = ".img"
	}

	var devLetter string
	if req.Device == "cdrom" {
		devLetter = nextSCSIDev(dom, "sd")
	} else {
		devLetter = nextVirtioDev(dom, "vd")
	}

	var sourceXML string
	if req.Source != "" {
		sourceXML = fmt.Sprintf("<source file='%s'/>", xmlEscape(req.Source))
	}

	var driverXML string
	var diskType string
	if req.Device == "cdrom" {
		driverXML = "<driver name='qemu' type='raw'/>"
		diskType = "file"
	} else if req.Source != "" {
		driverXML = fmt.Sprintf("<driver %s/>", diskDriverXMLAttrs(format, req.DiskCacheIO, req.DiskDiscard))
		diskType = "file"
	} else {
		// Create new volume
		poolName := req.Pool
		if poolName == "" {
			poolName = c.DiskPoolName()
		}
		poolPath, err := c.GetPoolPath(poolName)
		if err != nil {
			return fmt.Errorf("get pool path: %w", err)
		}
		domName, _ := dom.GetName()
		volName := domName + "-" + devLetter + volExt
		diskPath := poolPath + "/" + volName
		diskType = "file"
		sizeMB := req.SizeGB
		if sizeMB <= 0 {
			sizeMB = 10
		}
		if err := c.createDiskInPool(poolName, volName, sizeMB*1024, format); err != nil {
			dom.Undefine() // Clean up domain if disk creation fails
			return fmt.Errorf("create disk: %w", err)
		}
		sourceXML = fmt.Sprintf("<source file='%s'/>", xmlEscape(diskPath))
		driverXML = fmt.Sprintf("<driver %s/>", diskDriverXMLAttrs(format, req.DiskCacheIO, req.DiskDiscard))
	}

	busType := req.Bus
	if busType == "" {
		if req.Device == "cdrom" {
			busType = "sata"
		} else {
			busType = "virtio"
		}
	}

	devXML := fmt.Sprintf(`<disk type='%s' device='%s'>
  %s
  <target dev='%s' bus='%s'/>
  %s
</disk>`, xmlEscape(diskType), xmlEscape(req.Device), driverXML, xmlEscape(devLetter), xmlEscape(busType), sourceXML)

	if req.Device == "cdrom" {
		devXML = fmt.Sprintf(`<disk type='%s' device='cdrom'>
  %s
  %s
  <target dev='%s' bus='%s'/>
  <readonly/>
</disk>`, xmlEscape(diskType), driverXML, sourceXML, xmlEscape(devLetter), xmlEscape(busType))
	}

	flags := libvirt.DOMAIN_DEVICE_MODIFY_CURRENT | libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	domState, _, err := dom.GetState()
	if err == nil && domState != libvirt.DOMAIN_RUNNING {
		flags = libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	}
	return dom.AttachDeviceFlags(devXML, flags)
}

func (c *Connector) DetachDisk(id, target string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	flags := libvirt.DOMAIN_DEVICE_MODIFY_CURRENT | libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	domState, _, err := dom.GetState()
	if err == nil && domState != libvirt.DOMAIN_RUNNING {
		flags = libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	}

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}

	diskStartRe := regexp.MustCompile(`<disk\b`)
	targetRe := regexp.MustCompile(`<target\b[^>]*dev='` + regexp.QuoteMeta(target) + `'[^>]*/>`)

	loc := targetRe.FindStringIndex(xmlDesc)
	if loc == nil {
		return fmt.Errorf("disk with target '%s' not found", target)
	}

	allStarts := diskStartRe.FindAllStringIndex(xmlDesc[:loc[0]], -1)
	if len(allStarts) == 0 {
		return fmt.Errorf("could not find disk start for target '%s'", target)
	}
	start := allStarts[len(allStarts)-1][0]

	end := strings.Index(xmlDesc[loc[1]:], "</disk>")
	if end == -1 {
		return fmt.Errorf("could not find disk end for target '%s'", target)
	}
	end = loc[1] + end + 7

	diskXML := xmlDesc[start:end]
	return dom.DetachDeviceFlags(diskXML, flags)
}

var (
	usbVendorRE  = regexp.MustCompile(`<vendor id='(0x[0-9a-fA-F]+)'(?:>([^<]*)</vendor>|/>)`)
	usbProductRE = regexp.MustCompile(`<product id='(0x[0-9a-fA-F]+)'(?:>([^<]*)</product>|/>)`)
	usbBusRE     = regexp.MustCompile(`<bus>(\d+)</bus>`)
	usbDeviceRE  = regexp.MustCompile(`<device>(\d+)</device>`)
)

// hostUSBRootHubVendor is the Linux Foundation vendor ID shared by
// every USB root hub; passing one through is never useful, so it's
// filtered out (same convention virt-manager uses).
const hostUSBRootHubVendor = "0x1d6b"

// ListHostUSBDevices enumerates USB devices on the host available
// for passthrough, excluding generic root hubs.
func (c *Connector) ListHostUSBDevices() ([]models.USBDevice, error) {
	if err := c.ensureConnected(); err != nil {
		return nil, err
	}
	devs, err := c.conn.ListAllNodeDevices(libvirt.CONNECT_LIST_NODE_DEVICES_CAP_USB_DEV)
	if err != nil {
		return nil, fmt.Errorf("list host USB devices: %w", err)
	}
	out := make([]models.USBDevice, 0, len(devs))
	for i := range devs {
		xmlDesc, err := devs[i].GetXMLDesc(0)
		name, _ := devs[i].GetName()
		devs[i].Free()
		if err != nil {
			continue
		}
		vendorID, vendorName := usbMatch(usbVendorRE, xmlDesc)
		productID, productName := usbMatch(usbProductRE, xmlDesc)
		if vendorID == "" || productID == "" || vendorID == hostUSBRootHubVendor {
			continue
		}
		bus := ""
		if m := usbBusRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			bus = m[1]
		}
		device := ""
		if m := usbDeviceRE.FindStringSubmatch(xmlDesc); len(m) > 1 {
			device = m[1]
		}
		label := strings.TrimSpace(vendorName + " " + productName)
		if label == "" {
			label = name
		}
		out = append(out, models.USBDevice{
			Name:      label,
			VendorID:  vendorID,
			ProductID: productID,
			Bus:       bus,
			Device:    device,
		})
	}
	return out, nil
}

func usbMatch(re *regexp.Regexp, xmlDesc string) (id, label string) {
	m := re.FindStringSubmatch(xmlDesc)
	if len(m) < 2 {
		return "", ""
	}
	if len(m) > 2 {
		label = strings.TrimSpace(m[2])
	}
	return m[1], label
}

var usbHostdevBlockRE = regexp.MustCompile(`(?s)<hostdev\b[^>]*type=['"]usb['"][^>]*>.*?</hostdev>`)

// parseUSBHostdevs extracts the USB devices already passed through
// to a domain from its XML, so the UI can offer a "Detach" action.
func parseUSBHostdevs(xmlDesc string) []models.USBDevice {
	blocks := usbHostdevBlockRE.FindAllString(xmlDesc, -1)
	out := make([]models.USBDevice, 0, len(blocks))
	for _, b := range blocks {
		vendorID, _ := usbMatch(usbVendorRE, b)
		productID, _ := usbMatch(usbProductRE, b)
		if vendorID == "" || productID == "" {
			continue
		}
		out = append(out, models.USBDevice{VendorID: vendorID, ProductID: productID})
	}
	return out
}

func usbHostdevXML(vendorID, productID string) string {
	return fmt.Sprintf(`<hostdev mode='subsystem' type='usb' managed='yes'>
  <source>
    <vendor id='%s'/>
    <product id='%s'/>
  </source>
</hostdev>`, xmlEscape(vendorID), xmlEscape(productID))
}

// AttachUSBDevice passes a host USB device (identified by vendor and
// product ID) through to a VM, live if running.
func (c *Connector) AttachUSBDevice(id, vendorID, productID string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	// Unlike AttachDisk/DetachDisk's CURRENT|CONFIG (CURRENT==0, so
	// that combination is really just CONFIG — takes effect on next
	// boot only), USB passthrough's whole point is being usable
	// against a running VM, so this explicitly requests LIVE too.
	flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	state, _, err := dom.GetState()
	if err == nil && state == libvirt.DOMAIN_RUNNING {
		flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
	}
	return dom.AttachDeviceFlags(usbHostdevXML(vendorID, productID), flags)
}

// DetachUSBDevice removes a previously passed-through USB device from a VM.
func (c *Connector) DetachUSBDevice(id, vendorID, productID string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	// Unlike AttachDisk/DetachDisk's CURRENT|CONFIG (CURRENT==0, so
	// that combination is really just CONFIG — takes effect on next
	// boot only), USB passthrough's whole point is being usable
	// against a running VM, so this explicitly requests LIVE too.
	flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	state, _, err := dom.GetState()
	if err == nil && state == libvirt.DOMAIN_RUNNING {
		flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
	}
	return dom.DetachDeviceFlags(usbHostdevXML(vendorID, productID), flags)
}

func (c *Connector) UpdateDiskSource(id, target, source string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	flags := libvirt.DOMAIN_DEVICE_MODIFY_LIVE | libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	domState, _, err := dom.GetState()
	if err == nil && domState != libvirt.DOMAIN_RUNNING {
		flags = libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	}

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}

	diskStartRe := regexp.MustCompile(`<disk\b`)
	targetRe := regexp.MustCompile(`<target\b[^>]*dev='` + regexp.QuoteMeta(target) + `'[^>]*/>`)

	loc := targetRe.FindStringIndex(xmlDesc)
	if loc == nil {
		return fmt.Errorf("disk with target '%s' not found", target)
	}

	allStarts := diskStartRe.FindAllStringIndex(xmlDesc[:loc[0]], -1)
	if len(allStarts) == 0 {
		return fmt.Errorf("could not find disk start for target '%s'", target)
	}
	start := allStarts[len(allStarts)-1][0]

	end := strings.Index(xmlDesc[loc[1]:], "</disk>")
	if end == -1 {
		return fmt.Errorf("could not find disk end for target '%s'", target)
	}
	end = loc[1] + end + 7

	diskXML := xmlDesc[start:end]

	sourceRe := regexp.MustCompile(`<source\b[^>]*/>`)
	if source == "" {
		diskXML = regexp.MustCompile(`\s*<source\b[^>]*/>`).ReplaceAllString(diskXML, "")
	} else {
		newSource := fmt.Sprintf("<source file='%s'/>", xmlEscape(source))
		if sourceRe.MatchString(diskXML) {
			diskXML = sourceRe.ReplaceAllString(diskXML, newSource)
		} else {
			diskXML = regexp.MustCompile(`(<target\b)`).ReplaceAllString(diskXML, "  "+newSource+"\n$1")
		}
	}

	return dom.UpdateDeviceFlags(diskXML, flags)
}

func (c *Connector) AttachNetworkIface(id string, req models.AttachNetRequest) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	model := req.Model
	if model == "" {
		model = "virtio"
	}

	ifaceXML := fmt.Sprintf(`<interface type='network'>
  <source network='%s'/>
  <model type='%s'/>
</interface>`, xmlEscape(req.Network), xmlEscape(model))

	flags := libvirt.DOMAIN_DEVICE_MODIFY_CURRENT | libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	domState, _, err := dom.GetState()
	if err == nil && domState != libvirt.DOMAIN_RUNNING {
		flags = libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	}
	return dom.AttachDeviceFlags(ifaceXML, flags)
}

func (c *Connector) DetachNetworkIface(id, mac string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}

	ifaceRe := regexp.MustCompile(`<interface\b[^>]*>[\s\S]*?<mac\b[^>]*address='` + regexp.QuoteMeta(mac) + `'[^>]*/>[\s\S]*?</interface>`)
	ifaceXML := ifaceRe.FindString(xmlDesc)
	if ifaceXML == "" {
		return fmt.Errorf("network interface with mac '%s' not found", mac)
	}

	flags := libvirt.DOMAIN_DEVICE_MODIFY_CURRENT | libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	domState, _, err := dom.GetState()
	if err == nil && domState != libvirt.DOMAIN_RUNNING {
		flags = libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
	}
	return dom.DetachDeviceFlags(ifaceXML, flags)
}

// UpdateNetworkIface updates an existing network interface on a domain.
// The VM must be shutoff — live updates of MAC/VLAN/network are unreliable
// across virtio drivers and rejected here. If newMAC collides with any
// interface on any other VM, returns an error and the change is not
// applied. The caller can use CheckMACCollision first to preflight and
// surface a friendlier error.
func (c *Connector) UpdateNetworkIface(id, oldMAC string, req models.UpdateNetIfaceRequest) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	// Enforce shutoff: live virtio updates are unreliable.
	domState, _, _ := dom.GetState()
	if domState == libvirt.DOMAIN_RUNNING || domState == libvirt.DOMAIN_PAUSED {
		return fmt.Errorf("VM must be shut off to edit network interface")
	}

	// Fleet-wide MAC collision check (skipped when not changing MAC).
	if req.MAC != nil && *req.MAC != "" && *req.MAC != oldMAC {
		if err := c.assertMACUnique(*req.MAC, oldMAC); err != nil {
			return err
		}
	}

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}
	ifaceRe := regexp.MustCompile(`<interface\b[^>]*>[\s\S]*?<mac\b[^>]*address='` + regexp.QuoteMeta(oldMAC) + `'[^>]*/>[\s\S]*?</interface>`)
	ifaceXML := ifaceRe.FindString(xmlDesc)
	if ifaceXML == "" {
		return fmt.Errorf("network interface with mac '%s' not found", oldMAC)
	}

	updated := ifaceXML

	// Patch <mac> address if requested.
	if req.MAC != nil && *req.MAC != "" && *req.MAC != oldMAC {
		updated = regexp.MustCompile(`<mac\b[^>]*address='[^']*'[^>]*/>`).
			ReplaceAllString(updated, "<mac address='"+xmlEscape(*req.MAC)+"'/>")
	}

	// Patch <source network='...'/> if requested.
	if req.Network != nil && *req.Network != "" {
		if regexp.MustCompile(`<source\s+network='[^']*'\s*/>`).MatchString(updated) {
			updated = regexp.MustCompile(`<source\s+network='[^']*'\s*/>`).
				ReplaceAllString(updated, "<source network='"+xmlEscape(*req.Network)+"'/>")
		} else {
			// Possibly a bridge type: <source bridge='...'/> stays as-is.
			// We don't support converting between network/bridge types here.
		}
	}

	// Patch VLAN: 0 means remove; positive means add/replace; nil means leave.
	if req.VLANTag != nil {
		// Strip any existing <vlan>...</vlan> block.
		updated = regexp.MustCompile(`<vlan>[\s\S]*?</vlan>\s*`).ReplaceAllString(updated, "")
		if *req.VLANTag > 0 {
			vlanBlock := fmt.Sprintf("<vlan><tag id='%d'/></vlan>\n  ", *req.VLANTag)
			// Insert before closing </interface>.
			updated = strings.Replace(updated, "</interface>", vlanBlock+"</interface>", 1)
		}
	}

	return dom.UpdateDeviceFlags(updated, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG)
}

// assertMACUnique returns nil if `mac` is not used by any interface of any
// other domain. The `selfMAC` is the current MAC of the iface being edited
// (so it doesn't conflict with itself).
func (c *Connector) assertMACUnique(mac, selfMAC string) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}
	doms, err := c.conn.ListAllDomains(0)
	if err != nil {
		return err
	}
	defer func() {
		for i := range doms {
			doms[i].Free()
		}
	}()
	macLower := strings.ToLower(mac)
	for i := range doms {
		xmlDesc, err := doms[i].GetXMLDesc(0)
		if err != nil {
			continue
		}
		for _, m := range extractAllMACs(xmlDesc) {
			if strings.ToLower(m) == macLower && strings.ToLower(m) != strings.ToLower(selfMAC) {
				// Find the other domain's name for the error.
				otherName, _ := doms[i].GetName()
				return fmt.Errorf("MAC %s already in use by VM '%s'", mac, otherName)
			}
		}
	}
	return nil
}

// extractAllMACs returns every <mac address='...'/> value in a domain XML.
func extractAllMACs(xmlDesc string) []string {
	re := regexp.MustCompile(`<mac\s+address='([^']*)'`)
	matches := re.FindAllStringSubmatch(xmlDesc, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// CheckVLANSupport reports whether VLAN tagging is supported for the
// given libvirt network on this host. VLAN on a plain bridge requires
// libvirt ≥ 11.0.0 with macTableManager='libvirt' (or an OVS bridge).
func (c *Connector) CheckVLANSupport(networkName string) (models.VlanSupport, error) {
	if err := c.ensureConnected(); err != nil {
		return models.VlanSupport{}, err
	}

	// Libvirt version check.
	libVer, err := c.conn.GetLibVersion()
	if err != nil {
		return models.VlanSupport{}, fmt.Errorf("get libvirt version: %w", err)
	}
	major := int(libVer / 1_000_000)
	if major < 11 {
		return models.VlanSupport{
			Supported: false,
			Reason:    fmt.Sprintf("libvirt %d.%d.%d detected; VLAN tagging on plain bridges requires libvirt ≥ 11.0.0", major, (libVer/1000)%1000, libVer%1000),
		}, nil
	}

	// Network must exist.
	net, err := c.conn.LookupNetworkByName(networkName)
	if err != nil {
		return models.VlanSupport{Supported: false, Reason: "network not found"}, nil
	}
	defer net.Free()
	xmlDesc, err := net.GetXMLDesc(0)
	if err != nil {
		return models.VlanSupport{Supported: false, Reason: "cannot read network XML"}, nil
	}

	// Check for macTableManager='libvirt' or OVS.
	hasMacTableLibvirt := strings.Contains(xmlDesc, `macTableManager='libvirt'`)
	hasOVS := strings.Contains(xmlDesc, `type='openvswitch'`) || strings.Contains(xmlDesc, `type="openvswitch"`)
	if hasMacTableLibvirt || hasOVS {
		return models.VlanSupport{Supported: true}, nil
	}
	return models.VlanSupport{
		Supported: false,
		Reason:    "this bridge does not have macTableManager='libvirt' (or an Open vSwitch); VLAN tagging would be silently dropped by the kernel bridge",
	}, nil
}

func (c *Connector) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return models.VM{}, err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		xmlDesc, _ = dom.GetXMLDesc(0)
	}
	if xmlDesc == "" {
		return models.VM{}, fmt.Errorf("cannot get domain XML")
	}

	// Patch XML: replace name, title, UUID, remove MACs, generate new disk
	newUUID := uuid.New().String()
	newName := req.Name

	// Replace name
	xmlDesc = regexp.MustCompile(`<name>[^<]+</name>`).ReplaceAllString(xmlDesc, "<name>"+xmlEscape(newName)+"</name>")
	// Replace UUID
	xmlDesc = regexp.MustCompile(`<uuid>[^<]+</uuid>`).ReplaceAllString(xmlDesc, "<uuid>"+newUUID+"</uuid>")
	// Replace title
	xmlDesc = regexp.MustCompile(`<title>[^<]*</title>`).ReplaceAllString(xmlDesc, "")
	// Remove MAC addresses so libvirt generates new ones
	xmlDesc = regexp.MustCompile(`<mac\b[^/]*/>\s*`).ReplaceAllString(xmlDesc, "")
	// Remove NVRAM path
	xmlDesc = regexp.MustCompile(`<nvram>[^<]*</nvram>`).ReplaceAllString(xmlDesc, "")

	// Override network if requested
	if req.Network != "" {
		xmlDesc = regexp.MustCompile(`(<source\s+network=)['"][^'"]+['"]`).
			ReplaceAllString(xmlDesc, "${1}'"+xmlEscape(req.Network)+"'")
	}

	// Replace disk source
	poolName := req.Pool
	if poolName == "" {
		poolName = c.DiskPoolName()
	}
	poolPath, err := c.GetPoolPath(poolName)
	if err != nil {
		return models.VM{}, fmt.Errorf("get pool path: %w", err)
	}

	// Find the first disk and replace it
	diskRe := regexp.MustCompile(`<disk\b[^>]+device='disk'[^>]*>.*?<source\b[^>]*file='([^']+)'[^>]*/>.*?</disk>`)
	dm := diskRe.FindStringSubmatch(xmlDesc)
	if len(dm) > 0 {
		oldDiskPath := dm[1]
		newDiskName := newName + ".qcow2"
		newDiskPath := poolPath + "/" + newDiskName
		xmlDesc = strings.Replace(xmlDesc, oldDiskPath, newDiskPath, 1)

		sizeGB := extractDiskSize(xmlDesc)
		if sizeGB <= 0 {
			sizeGB = 10
		}
		// Detect original disk format
		originalFormat := "qcow2" // default
		formatMatch := regexp.MustCompile(`<driver[^>]*type='([^']+)'`).FindStringSubmatch(dm[0])
		if len(formatMatch) > 1 {
			originalFormat = formatMatch[1]
		}
		if err := c.createDiskInPool(poolName, newDiskName, sizeGB*1024, originalFormat); err != nil {
			return models.VM{}, fmt.Errorf("create cloned disk: %w", err)
		}
	}

	newDom, err := c.conn.DomainDefineXML(xmlDesc)
	if err != nil {
		return models.VM{}, fmt.Errorf("define cloned domain: %w", err)
	}
	defer newDom.Free()

	return c.domainToVM(newDom)
}

func (c *Connector) GetBootDevice(id string) (string, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return "", err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return "", err
	}

	re := regexp.MustCompile(`<boot dev='([^']*)'/>`)
	m := re.FindStringSubmatch(xmlDesc)
	if len(m) > 1 {
		return m[1], nil
	}
	return "hd", nil // default
}

func (c *Connector) SetBootDevice(id string, device string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return err
	}

	newBoot := fmt.Sprintf("<boot dev='%s'/>", device)
	re := regexp.MustCompile(`<boot dev='[^']*'/>`)
	if re.MatchString(xmlDesc) {
		xmlDesc = re.ReplaceAllString(xmlDesc, newBoot)
	} else {
		// Add boot tag before </os>
		xmlDesc = strings.Replace(xmlDesc, "</os>", fmt.Sprintf("%s\n  </os>", newBoot), 1)
	}

	_, err = c.conn.DomainDefineXML(xmlDesc)
	return err
}

// SetDomainAutostart toggles whether libvirtd will start this
// domain automatically when the host boots. Backed by
// `virDomainSetAutostart` — does not affect the domain's current
// running state.
func (c *Connector) SetDomainAutostart(id string, enabled bool) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()
	return dom.SetAutostart(enabled)
}

// GetDomainAutostart returns the autostart flag for a domain.
func (c *Connector) GetDomainAutostart(id string) (bool, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return false, err
	}
	defer dom.Free()
	return dom.GetAutostart()
}

// autostartEnabledOrFalse is the best-effort autostart probe used
// inside domainToVM. libvirt's GetAutostart returns an error for
// transient states (e.g. the domain is in the middle of shutting
// off), and the rest of domainToVM already plows through the
// domain's data with the same resilience — a missing autostart
// field on a one-frame stale listing is far less disruptive than
// failing the entire VM list because one VM is mid-transition.
// Operators who care about the live value can still hit
// GET /api/vms/{id}/autostart, which uses GetDomainAutostart and
// surfaces the error verbatim.
func autostartEnabledOrFalse(dom *libvirt.Domain) bool {
	enabled, err := dom.GetAutostart()
	if err != nil {
		return false
	}
	return enabled
}

// ResizeDomainDisk grows (or shrinks) the backing file of a VM disk
// identified by its target device (e.g. "vda", "sdb").
//
// The VM must be shut off — qemu-img can't take a write lock on a
// running VM's qcow2 file. After the resize, the domain XML's
// <capacity> is updated on next start (libvirt re-derives it from
// the file size).
//
// Returns the new size in bytes as reported by `qemu-img info`.
func (c *Connector) ResizeDomainDisk(ctx context.Context, id, target string, newSizeGB int64) (int64, error) {
	if newSizeGB <= 0 {
		return 0, fmt.Errorf("size must be positive")
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return 0, err
	}
	defer dom.Free()

	disks := c.parseDisksFiltered(domGetXMLDescOrEmpty(dom), false)
	var disk *models.DiskInfo
	for i := range disks {
		if disks[i].Target == target {
			disk = &disks[i]
			break
		}
	}
	if disk == nil {
		return 0, fmt.Errorf("disk %q not found on VM %q", target, id)
	}
	if disk.Source == "" {
		return 0, fmt.Errorf("disk %q has no source file (probably a block device or empty cdrom)", target)
	}

	newBytes := uint64(newSizeGB) * 1024 * 1024 * 1024

	state, _, _ := dom.GetState()
	if state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED {
		// Online resize only supports growing, and relies on QEMU's
		// own block_resize (libvirt's BlockResize) to grow the
		// qcow2 header/metadata in place — no host file truncation
		// needed for qcow2. Raw disks have no such header and are
		// not safe to grow this way while running.
		if newSizeGB < disk.SizeGB {
			return 0, fmt.Errorf("cannot shrink a disk while the VM is running; shut it down first")
		}
		if err := dom.BlockResize(target, newBytes, libvirt.DOMAIN_BLOCK_RESIZE_BYTES); err != nil {
			return 0, fmt.Errorf("live resize failed (only qcow2 disks can grow while running): %w", err)
		}
		return int64(newBytes), nil
	}

	cmd := exec.CommandContext(ctx, "qemu-img", "resize", disk.Source, fmt.Sprintf("%d", newBytes))
	if out, err := cmd.CombinedOutput(); err != nil {
		return 0, fmt.Errorf("qemu-img resize failed: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return int64(newBytes), nil
}

// domGetXMLDescOrEmpty returns the inactive XML or "" on error.
func domGetXMLDescOrEmpty(dom *libvirt.Domain) string {
	out, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		out, _ = dom.GetXMLDesc(0)
	}
	return out
}

func nextVirtioDev(dom *libvirt.Domain, prefix string) string {
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return prefix + "b"
	}
	re := regexp.MustCompile(`<target\b[^>]*dev='(` + prefix + `[a-z]+)'`)
	matches := re.FindAllStringSubmatch(xmlDesc, -1)
	used := make(map[byte]bool)
	for _, m := range matches {
		if len(m[1]) > 2 {
			used[m[1][len(prefix)]] = true
		}
	}
	for c := byte('a'); c <= 'z'; c++ {
		if !used[c] {
			return prefix + string(c)
		}
	}
	return prefix + "z"
}

func nextSCSIDev(dom *libvirt.Domain, prefix string) string {
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return prefix + "a"
	}
	re := regexp.MustCompile(`<target\b[^>]*dev='(` + prefix + `[a-z]+)'`)
	matches := re.FindAllStringSubmatch(xmlDesc, -1)
	used := make(map[byte]bool)
	for _, m := range matches {
		if len(m[1]) > 2 {
			used[m[1][len(prefix)]] = true
		}
	}
	for c := byte('a'); c <= 'z'; c++ {
		if !used[c] {
			return prefix + string(c)
		}
	}
	return prefix + "z"
}

func extractTagAttr(xml, attr string) string {
	search := attr + "='"
	s := strings.Index(xml, search)
	if s < 0 {
		search = attr + "=\""
		s = strings.Index(xml, search)
		if s < 0 {
			return ""
		}
	}
	s += len(search)
	e := strings.Index(xml[s:], "'")
	if e < 0 {
		e = strings.Index(xml[s:], "\"")
		if e < 0 {
			return ""
		}
	}
	return xml[s : s+e]
}

// ExportBackupOptions controls the existing WebKVM backup format
// (tar with domain.xml + disk files). The legacy on-disk format is
// preserved when Compress is "gzip"; new exports default to zstd for
// better ratio and faster throughput.
type ExportBackupOptions struct {
	Compress    string // "gzip" (legacy) or "zstd" (default for new exports)
	ZstdLevel   int    // 1..22, default 19
	RepackDisks bool   // if true, run qemu-img convert -c on each disk first
}

// ExportDomain streams a backup of the domain (XML + disk files) to w.
// By default it uses zstd compression (much faster + better ratio than
// gzip); pass opts.Compress="gzip" for backward compatibility with
// archives produced by older versions of WebKVM.
//
// The output is a tar containing domain.xml, optional re-packed
// qcow2 files in disks/, and a manifest.txt. When opts.RepackDisks
// is true, each disk is re-packed with `qemu-img convert -c -O qcow2`
// (cluster packing) which usually shrinks the backup by 20-50%.
// qemu-img writes to stdout and the bytes are piped straight into the
// tar — no temporary file is ever created on disk.
//
// Phase II: this function is now a thin shim over the shared
// producer in internal/backupstore. The bytes it produces are
// byte-for-byte identical to what the backup runner stores
// for a per-VM tar (the runner and the browser export use the
// same ProduceVMArchive). The libvirt-specific glue — domain
// lookup, XML fetch, disk validation, repack-via-qemu-img —
// stays here; only the tar+compress step moved.
//
// The VM must be shut off before exporting.
func (c *Connector) ExportDomain(ctx context.Context, id string, opts ExportBackupOptions, w io.Writer) (backupstore.ProducerResult, error) {
	if err := c.ensureConnected(); err != nil {
		return backupstore.ProducerResult{}, err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return backupstore.ProducerResult{}, err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return backupstore.ProducerResult{}, fmt.Errorf("get xml: %w", err)
	}

	if err := c.validateDisksReadable(xmlDesc); err != nil {
		return backupstore.ProducerResult{}, err
	}

	disks := c.parseDisksFiltered(xmlDesc, true)
	vmBackup := backupstore.VMBackup{
		ID:        id,
		DomainXML: xmlDesc,
		Disks:     disks,
	}
	producerOpts := backupstore.ProducerOpts{
		Compression:   opts.Compress,
		ZstdLevel:     opts.ZstdLevel,
		RepackDisks:   opts.RepackDisks,
		DiskSizeLimit: 0, // export has no size cap (the request body can be huge)
	}
	return backupstore.ProduceVMArchive(ctx, vmBackup, producerOpts, w)
}

// resolveUniqueDomainName returns name if no existing domain has it, or
// "<name>-N" (incrementing N from 1) until a free name is found. Used by
// the import paths so a re-imported VM never collides with the original.
func (c *Connector) resolveUniqueDomainName(name string) (string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", err
	}
	if name == "" {
		return "", fmt.Errorf("empty name")
	}
	if _, err := c.conn.LookupDomainByName(name); err != nil {
		// Not found: original is fine.
		return name, nil
	}
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if _, err := c.conn.LookupDomainByName(candidate); err != nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a free name after 1000 attempts starting at %s", name)
}

// normalizeMachineType rewrites versioned machine attributes (e.g.
// "pc-q35-10.2", "pc-i440fx-7.1") to the short canonical form
// ("q35", "pc") so libvirt resolves them to whatever the destination
// qemu actually supports. Without this, a VM exported from a host with
// a newer qemu (e.g. 10.2.x) will fail to import on a host with an
// older qemu (e.g. 10.0.x) because the destination qemu does not know
// the versioned machine type. Idempotent: re-applying it is a no-op.
//
// Handles both single-quoted ('machine=...') and double-quoted
// ("machine=...") attribute syntax, and both major chipsets.
func normalizeMachineType(xmlStr string) string {
	xmlStr = regexp.MustCompile(`machine='pc-q35-[0-9]+(\.[0-9]+)*'`).ReplaceAllString(xmlStr, "machine='q35'")
	xmlStr = regexp.MustCompile(`machine='pc-i440fx-[0-9]+(\.[0-9]+)*'`).ReplaceAllString(xmlStr, "machine='pc'")
	xmlStr = regexp.MustCompile(`machine="pc-q35-[0-9]+(\.[0-9]+)*"`).ReplaceAllString(xmlStr, `machine="q35"`)
	xmlStr = regexp.MustCompile(`machine="pc-i440fx-[0-9]+(\.[0-9]+)*"`).ReplaceAllString(xmlStr, `machine="pc"`)
	return xmlStr
}

// ImportDomain restores a VM from a WebKVM backup produced by ExportDomain.
// The archive may be gzip (.tar.gz) or zstd (.tar.zst) compressed, or a
// plain tar. Disk files from the archive are placed into the requested
// pool (or "default").
//
// Returns the new VM's uuid, resolved name, and a list of non-fatal
// warnings (e.g. CDROM source files that do not exist on the
// destination — the VM can be defined but will fail to start until
// the user uploads the missing ISO). An empty warning slice means a
// clean import.
// ImportOpts carries optional overrides applied while importing a
// VM from a backup archive (or OVA). Zero values mean "keep whatever
// is inside the archive" — the behaviour before these options
// existed. Autostart is a pointer so callers can distinguish
// "don't touch" (nil) from "explicitly set false" (*false).
type ImportOpts struct {
	// Network rewrites every <source network='...'> on the VM's
	// interfaces. Empty string keeps the archive's network.
	Network string
	// VCPUs overrides the <vcpu> element. 0 keeps the archive's.
	VCPUs int
	// RAMMB overrides <memory> (KiB in the XML). 0 keeps it.
	RAMMB int
	// Autostart sets libvirt's domain autostart flag after the
	// VM is defined. nil leaves the libvirt default untouched.
	Autostart *bool
	// SourceSize is the compressed archive size in bytes. Used as
	// the denominator when reporting disk-copy progress.
	SourceSize int64
	// OnProgress is invoked with a 0-100 percentage, a stable stage
	// code (restore_copying, restore_define, done...) and its
	// interpolation vars as the import progresses. Optional.
	OnProgress func(pct int, stage string, vars map[string]any)
}

func (o ImportOpts) report(pct int, stage string, vars map[string]any) {
	if o.OnProgress != nil {
		o.OnProgress(pct, stage, vars)
	}
}

// countingReader wraps an io.Reader and reports the cumulative
// percentage of bytes read against a known total. Used to give the
// UI a live bar while the (multi-GB) disk bodies stream out of the
// archive into the pool. Reporting is rate-limited to whole
// percentage points so UpdateJob isn't hammered thousands of times.
type countingReader struct {
	r     io.Reader
	total int64
	seen  int64
	base  int
	span  int
	last  int
	on    func(pct int)
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.seen += int64(n)
	if c.total > 0 {
		pct := c.base + int(float64(c.seen)/float64(c.total)*float64(c.span))
		if pct > c.base+c.span {
			pct = c.base + c.span
		}
		if pct > c.last {
			c.last = pct
			c.on(pct)
		}
	}
	return n, err
}

func (c *Connector) ImportDomain(tarPath, newName, poolName string, opts ImportOpts) (string, string, []string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", "", nil, err
	}
	if poolName == "" {
		poolName = c.DiskPoolName()
	}
	poolPath, err := c.GetPoolPath(poolName)
	if err != nil {
		return "", "", nil, fmt.Errorf("get pool path: %w", err)
	}

	// Auto-detect gzip/zstd/plain tar. Reuse the OVA stream opener
	// — it does the same magic-byte sniffing we need here.
	raw, cleanup, err := openOVAStream(tarPath)
	if err != nil {
		return "", "", nil, err
	}
	defer cleanup()
	tr := tar.NewReader(raw)

	var xmlBytes []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", nil, err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		// We only need domain.xml from the first pass. Disk bodies are
		// streamed straight to the pool in a second pass; loading them
		// into a []byte would OOM the process on multi-GB imports.
		if hdr.Name == "domain.xml" {
			xmlBytes, err = io.ReadAll(tr)
			if err != nil {
				return "", "", nil, err
			}
		}
	}
	if len(xmlBytes) == 0 {
		return "", "", nil, fmt.Errorf("archive missing domain.xml")
	}
	opts.report(5, "restore_copying", nil)

	xmlStr := string(xmlBytes)
	// Strip the original UUID and NVRAM path so libvirt generates
	// fresh ones for the imported VM. Otherwise DomainDefineXML fails
	// when the original VM still exists, and the new VM would share
	// the original's NVRAM file.
	xmlStr = regexp.MustCompile(`(?s)<uuid>[^<]*</uuid>\n?`).ReplaceAllString(xmlStr, "")
	xmlStr = regexp.MustCompile(`<nvram\s+([^>]*)>[^<]*</nvram>`).ReplaceAllString(xmlStr, "<nvram $1/>")

	// Determine the effective name: caller-supplied, else <name> in the archive.
	effectiveName := newName
	if effectiveName == "" {
		if m := regexp.MustCompile(`<name>([^<]*)</name>`).FindStringSubmatch(xmlStr); len(m) > 1 {
			effectiveName = m[1]
		}
	}
	if effectiveName == "" {
		return "", "", nil, fmt.Errorf("archive missing domain name")
	}
	resolvedName, err := c.resolveUniqueDomainName(effectiveName)
	if err != nil {
		return "", "", nil, err
	}
	// Always rewrite the <name> in the XML to the resolved value (handles
	// both caller-supplied and archive-supplied names, and the rename case).
	// xmlEscape guards against a caller- or archive-supplied name breaking
	// out of the <name> element (XML/QEMU-arg injection).
	re := regexp.MustCompile(`<name>[^<]*</name>`)
	if loc := re.FindStringIndex(xmlStr); loc != nil {
		xmlStr = xmlStr[:loc[0]] + "<name>" + xmlEscape(resolvedName) + "</name>" + xmlStr[loc[1]:]
	}
	// Also update the title if present.
	titleRe := regexp.MustCompile(`<title>[^<]*</title>`)
	if loc := titleRe.FindStringIndex(xmlStr); loc != nil {
		xmlStr = xmlStr[:loc[0]] + "<title>" + xmlEscape(resolvedName) + "</title>" + xmlStr[loc[1]:]
	}
	// Update NVRAM file hint if the name appears there.
	xmlStr = regexp.MustCompile(`<nvram[^>]*template[^>]*/>`).ReplaceAllStringFunc(xmlStr, func(m string) string {
		return regexp.MustCompile(`/[^/]+_VARS\.fd`).ReplaceAllString(m, "/"+resolvedName+"_VARS.fd")
	})

	// Normalize the machine type to the short canonical form so libvirt
	// resolves it to whatever the destination's qemu actually supports.
	// The exporter may have been a host with a newer qemu (e.g. pc-q35-10.2
	// on a 10.2.x build) and the importer's qemu (e.g. 10.0.x) does not
	// know that version. "q35" / "pc" are the short forms that libvirt
	// always resolves to the latest available machine type for the chipset.
	xmlStr = normalizeMachineType(xmlStr)

	// Optional overrides: rewiring the NICs to a different libvirt
	// network and/or overriding the vCPU / RAM allocation. Each is a
	// no-op when the caller leaves the corresponding field zeroed.
	// The regexes are safe: `<source network=` only ever appears on
	// <interface> elements (disk sources use `file=`), and the
	// vcpu/memory tags are single-element in libvirt XML.
	if opts.Network != "" {
		xmlStr = regexp.MustCompile(`<source\s+network=['"][^'"]*['"]\s*/>`).
			ReplaceAllString(xmlStr, "<source network='"+xmlEscape(opts.Network)+"'/>")
	}
	if opts.VCPUs > 0 {
		xmlStr = regexp.MustCompile(`<vcpu[^>]*>[\s\S]*?</vcpu>`).
			ReplaceAllString(xmlStr, fmt.Sprintf("<vcpu placement='static'>%d</vcpu>", opts.VCPUs))
	}
	if opts.RAMMB > 0 {
		xmlStr = regexp.MustCompile(`<memory[^>]*>[\s\S]*?</memory>`).
			ReplaceAllString(xmlStr, fmt.Sprintf("<memory unit='KiB'>%d</memory>", opts.RAMMB*1024))
	}

	// Build the set of disk basenames the XML actually references, so
	// the second pass only writes disks that will be used (avoids
	// orphan files in the pool when an archive carries extras).
	diskRe := regexp.MustCompile(`<source\s+file=(["'])([^"']+)(["'])([^/>]*)/>`)
	referenced := map[string]bool{}
	for _, m := range diskRe.FindAllStringSubmatch(xmlStr, -1) {
		if len(m) >= 3 {
			referenced[filepath.Base(m[2])] = true
		}
	}

	// Second pass: re-open the archive and stream every referenced disk
	// straight from the tar body to its destination in the pool. This
	// avoids the multi-GB []byte allocation that used to OOM-kill the
	// backend on large imports.
	raw2, cleanup2, err := openOVAStream(tarPath)
	if err != nil {
		return "", "", nil, err
	}
	defer cleanup2()
	// Wrap the stream so the UI gets a live percentage while the
	// disk bodies stream into the pool. total = compressed size.
	if opts.SourceSize > 0 && opts.OnProgress != nil {
		raw2 = &countingReader{
			r:     raw2,
			total: opts.SourceSize,
			base:  5,
			span:  85,
			on: func(pct int) {
				opts.report(pct, "restore_copying", nil)
			},
		}
	}
	tr2 := tar.NewReader(raw2)
	diskWritten := map[string]string{} // arcName -> dst path

	// The destination basename is the new VM name, with a
	// counter suffix on collision. The first disk lands as
	// "<newName>.qcow2"; subsequent disks (or re-imports) land
	// as "<newName>-2.qcow2", "<newName>-3.qcow2", etc.
	// The arcName is preserved in diskWritten so the post-loop
	// XML rewrite can map each <source file='...'> reference
	// to its new on-disk path.
	diskCounter := 0

	for {
		hdr, err := tr2.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", nil, err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(hdr.Name, "disks/") {
			continue
		}
		base := filepath.Base(hdr.Name)
		if !referenced[base] {
			// Skip disk bodies not referenced by the XML.
			continue
		}
		var dst string
		var derr error
		// Use resolvedName (not newName) so the disk file
		// matches the actual VM name libvirt will register.
		// If the caller asked for "myvm" and a "myvm"
		// already exists, resolveUniqueDomainName returns
		// "myvm-1"; the disk should also be "myvm-1.qcow2"
		// to keep the VM and its disk in sync.
		if diskCounter == 0 {
			dst, derr = VMDiskPath(poolPath, resolvedName, ".qcow2")
		} else {
			// Subsequent disks of the same VM: use a counter
			// suffix derived from the import order. The
			// collision counter is global within this import
			// (it doesn't re-scan the pool between disks), so
			// the second disk becomes "<resolvedName>-2.qcow2",
			// the third "-3", etc.
			probe := fmt.Sprintf("%s-%d", resolvedName, diskCounter+1)
			dst, derr = VMDiskPath(poolPath, probe, ".qcow2")
		}
		if derr != nil {
			return "", "", nil, fmt.Errorf("resolve disk path: %w", derr)
		}
		if !strings.HasPrefix(filepath.Clean(dst), filepath.Clean(poolPath)+string(os.PathSeparator)) {
			return "", "", nil, fmt.Errorf("invalid disk path %q", dst)
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return "", "", nil, fmt.Errorf("create disk %s: %w", dst, err)
		}
		if _, err := io.Copy(f, tr2); err != nil {
			f.Close()
			return "", "", nil, fmt.Errorf("write disk %s: %w", dst, err)
		}
		if err := f.Close(); err != nil {
			return "", "", nil, fmt.Errorf("close disk %s: %w", dst, err)
		}
		diskWritten[hdr.Name] = dst
		diskCounter++
	}

	// Re-register the new files with libvirt. We wrote them directly
	// to the pool directory (to avoid a multi-GB []byte allocation),
	// which bypasses the volume registry. Without a refresh, the
	// files are "ghost" disks — visible on disk, referenced by the
	// domain XML, but invisible to `virsh vol-list` and to
	// /api/storage/volumes. One refresh at the end of the write
	// loop picks up every disk we just wrote; subsequent calls
	// within the same import would be no-ops.
	if len(diskWritten) > 0 {
		if pool, err := c.conn.LookupStoragePoolByName(poolName); err == nil {
			if rerr := pool.Refresh(0); rerr != nil {
				// Not fatal: the domain definition below will
				// still succeed (libvirt does not require a
				// volume entry to use a file in a dir pool).
				// Log so the operator can manually refresh
				// if the vol listing stays empty.
				slog.Warn("import_pool_refresh_failed",
					"pool", poolName, "err", rerr.Error())
			}
			pool.Free()
		} else {
			slog.Warn("import_pool_lookup_failed",
				"pool", poolName, "err", err.Error())
		}
	}

	xmlStr = diskRe.ReplaceAllStringFunc(xmlStr, func(match string) string {
		sm := diskRe.FindStringSubmatch(match)
		if len(sm) < 5 {
			return match
		}
		base := filepath.Base(sm[2])
		dst, ok := diskWritten["disks/"+base]
		if !ok {
			return match
		}
		return "<source file=" + sm[1] + dst + sm[3] + sm[4] + "/>"
	})

	// Strip every <disk type='file' device='cdrom'>...</disk>
	// block from the XML before defining the domain. The exporter
	// captured the CDROM source path verbatim, but that path is
	// almost certainly wrong on the destination host (different
	// pool, different basename because of upload-time dedup, or
	// the user simply never re-uploaded the ISO). Leaving it in
	// would make the import succeed but the VM refuse to start
	// with a hard-to-diagnose "Cannot access storage file" error.
	//
	// Stripping is a stronger guarantee than warning: the VM is
	// always startable after a clean import, and the user can
	// attach any ISO via the Storage page + Add Disk dialog.
	// We match the full <disk ...>...</disk> block (with `(?s)`
	// so the dot matches newlines) and only when the device
	// attribute is 'cdrom' — disk devices and any other device
	// types are left alone.
	cdromBlockRe := regexp.MustCompile(`(?s)<disk\s+type='file'\s+device='cdrom'\s*>\s*.*?</disk>`)
	stripped := cdromBlockRe.FindAllString(xmlStr, -1)
	xmlStr = cdromBlockRe.ReplaceAllString(xmlStr, "")

	var warnings []string
	if len(stripped) > 0 {
		// Build a short, single warning that lists the paths we
		// dropped, so the operator knows what they need to
		// re-attach if they want boot-from-ISO to work.
		pathRe := regexp.MustCompile(`<source\s+file='([^']+)'`)
		var paths []string
		for _, blk := range stripped {
			if m := pathRe.FindStringSubmatch(blk); len(m) >= 2 {
				paths = append(paths, filepath.Base(m[1]))
			}
		}
		switch {
		case len(paths) == 1:
			warnings = append(warnings, fmt.Sprintf("CDROM device was discarded on import (source: %s). Attach an ISO via the Storage page → Add Disk if you want boot-from-ISO.", paths[0]))
		default:
			warnings = append(warnings, fmt.Sprintf("%d CDROM devices were discarded on import (%s). Attach ISOs via the Storage page → Add Disk if you want boot-from-ISO.", len(paths), strings.Join(paths, ", ")))
		}
	}

	opts.report(90, "restore_define", nil)
	dom, err := c.conn.DomainDefineXML(xmlStr)
	if err != nil {
		return "", "", nil, fmt.Errorf("define domain: %w", err)
	}
	defer dom.Free()
	uuid, err := dom.GetUUIDString()
	if err != nil {
		return "", "", nil, err
	}

	// Optional autostart override. The VM is already defined, so a
	// failure here is non-fatal — log it but keep the import result.
	if opts.Autostart != nil {
		if err := dom.SetAutostart(*opts.Autostart); err != nil {
			slog.Warn("import_autostart_failed",
				"uuid", uuid, "autostart", *opts.Autostart, "err", err.Error())
		}
	}

	// Third pass: restore snapshot metadata and overlay volumes from
	// the archive. This is best-effort — failures produce a warning
	// but do not abort the import (the VM is already defined and the
	// active disk is usable without the snapshot history).
	opts.report(95, "restore_define", nil)
	snapWarn := c.restoreSnapshots(tarPath, resolvedName, poolPath, dom)
	if snapWarn != "" {
		warnings = append(warnings, snapWarn)
	}
	opts.report(100, "done", nil)

	return uuid, resolvedName, warnings, nil
}

// writeArchiveBody streams an archive entry (r) straight to path
// without buffering it in memory. Snapshot overlays / base disks can
// be gigabytes; io.ReadAll on them would OOM the backend.
func writeArchiveBody(r io.Reader, path string) error {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || strings.Contains(cleaned, "..") {
		return fmt.Errorf("invalid archive path %q", path)
	}
	f, err := os.Create(cleaned)
	if err != nil {
		return err
	}
	if _, cerr := io.Copy(f, r); cerr != nil {
		_ = f.Close()
		_ = os.Remove(cleaned)
		return cerr
	}
	if cerr := f.Close(); cerr != nil {
		_ = os.Remove(cleaned)
		return cerr
	}
	return nil
}

// restoreSnapshots reads snapshot entries from a backup archive,
// copies overlay volumes to the pool, fixes the qcow2 backing chain,
// and recreates each snapshot via DomainSnapshotCreateXML with the
// REDEFINE flag. Returns a non-fatal warning string, or empty string
// on success.
func (c *Connector) restoreSnapshots(tarPath, resolvedName, poolPath string, dom *libvirt.Domain) string {
	raw3, cleanup3, err := openOVAStream(tarPath)
	if err != nil {
		return fmt.Sprintf("restore snapshots: open archive: %v", err)
	}
	defer cleanup3()

	tr3 := tar.NewReader(raw3)

	// Collect snapshot data from the archive.
	type snapRestore struct {
		xmlBytes      []byte
		volumeArcName string // archive entry name for the overlay file
		snapName      string // snapshot name (e.g. "snap1")
	}
	snapshots := map[string]*snapRestore{} // snapName → restore data
	var baseVolumeArc string               // "_base" volume archive entry
	var restoreOrder []string              // snapshot names in archive order

	for {
		hdr, err := tr3.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Sprintf("restore snapshots: read tar: %v", err)
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		if !strings.HasPrefix(hdr.Name, "snapshots/") || hdr.Name == "snapshots/" {
			continue
		}
		rest := strings.TrimPrefix(hdr.Name, "snapshots/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) < 1 || parts[0] == "" {
			continue
		}
		snapName := parts[0]

		if snapName == "_base" {
			// Root base disk of the backing chain.
			if len(parts) == 2 {
				basePath := filepath.Join(poolPath, resolvedName+".base")
				if werr := writeArchiveBody(tr3, basePath); werr != nil {
					return fmt.Sprintf("restore snapshots: write _base: %v", werr)
				}
				baseVolumeArc = basePath
			}
			continue
		}

		if len(parts) == 1 && strings.HasSuffix(parts[0], ".xml") {
			// Snapshot metadata XML.
			body, rerr := io.ReadAll(tr3)
			if rerr != nil {
				return fmt.Sprintf("restore snapshots: read %s: %v", hdr.Name, rerr)
			}
			name := strings.TrimSuffix(snapName, ".xml")
			if _, ok := snapshots[name]; !ok {
				snapshots[name] = &snapRestore{snapName: name}
				restoreOrder = append(restoreOrder, name)
			}
			snapshots[name].xmlBytes = body
		} else if len(parts) == 2 {
			// Snapshot overlay volume. Stream it straight to the
			// pool — io.ReadAll here OOMs the backend on VMs with
			// multi-GB snapshots (the overlay bodies are GBs).
			if _, ok := snapshots[snapName]; !ok {
				snapshots[snapName] = &snapRestore{snapName: snapName}
				restoreOrder = append(restoreOrder, snapName)
			}
			snapshots[snapName].volumeArcName = hdr.Name
			// Write the overlay to the pool.
			volPath := filepath.Join(poolPath, resolvedName+"."+snapName)
			if werr := writeArchiveBody(tr3, volPath); werr != nil {
				return fmt.Sprintf("restore snapshots: write %s: %v", snapName, werr)
			}
		}
	}

	if len(snapshots) == 0 && baseVolumeArc == "" {
		return "" // no snapshot data in archive
	}

	// Fix the qcow2 backing chain. The archive chain order is:
	//   _base/<root>  ←  snap1/<overlay1>  ←  ...  ←  disks/<active>
	//
	// Each overlay's internal backing-file path points to the
	// original source, which doesn't exist on this machine. We
	// rebase each overlay to point to the correct file in the pool.
	//
	// The active disk (from disks/) is already at
	// pool/<resolvedName>.qcow2 — its backing file needs to point
	// to the last snapshot overlay (or the base if no snapshots).
	qemuImg, qerr := exec.LookPath("qemu-img")
	if qerr != nil {
		return "restore snapshots: qemu-img not found, snapshots not restored"
	}

	// Walk in archive order (oldest → newest). Each overlay's
	// backing file should be the previous file in the chain.
	var prevPath string
	if baseVolumeArc != "" {
		prevPath = baseVolumeArc // pool/<resolvedName>.base
	}
	for _, name := range restoreOrder {
		sr := snapshots[name]
		if sr.volumeArcName == "" {
			continue // no overlay volume for this snapshot
		}
		volPath := filepath.Join(poolPath, resolvedName+"."+name)
		if prevPath != "" {
			// Fix the backing file reference.
			cmd := exec.Command(qemuImg, "rebase", "-u", "-b", prevPath, volPath)
			if output, rerr := cmd.CombinedOutput(); rerr != nil {
				return fmt.Sprintf("restore snapshots: rebase %s: %v: %s", name, rerr, string(output))
			}
		} else {
			// No backing file reference — make it standalone.
			cmd := exec.Command(qemuImg, "rebase", "-u", "-b", "", volPath)
			_ = cmd.Run()
		}
		prevPath = volPath
	}

	// Rebase the active disk (pool/<resolvedName>.qcow2) to point
	// to the last overlay in the chain (or the base if no overlays).
	activePath := filepath.Join(poolPath, resolvedName+".qcow2")
	if prevPath != "" {
		cmd := exec.Command(qemuImg, "rebase", "-u", "-b", prevPath, activePath)
		if output, rerr := cmd.CombinedOutput(); rerr != nil {
			return fmt.Sprintf("restore snapshots: rebase active disk: %v: %s", rerr, string(output))
		}
	} else if baseVolumeArc != "" {
		// Only the base exists — rebase active to base.
		cmd := exec.Command(qemuImg, "rebase", "-u", "-b", baseVolumeArc, activePath)
		if output, rerr := cmd.CombinedOutput(); rerr != nil {
			return fmt.Sprintf("restore snapshots: rebase active to base: %v: %s", rerr, string(output))
		}
	}

	// Recreate each snapshot via VIR_DOMAIN_SNAPSHOT_CREATE_REDEFINE.
	// The overlay volumes are already in place with correct backing
	// chain; REDEFINE just registers the metadata with libvirt.
	for _, name := range restoreOrder {
		sr := snapshots[name]
		if len(sr.xmlBytes) == 0 {
			continue
		}
		_, serr := dom.CreateSnapshotXML(string(sr.xmlBytes), 1 /* REDEFINE */)
		if serr != nil {
			return fmt.Sprintf("restore snapshots: redefine %s: %v", name, serr)
		}
	}

	// Refresh the pool so libvirt discovers the new snapshot volumes.
	if pool, perr := c.conn.LookupStoragePoolByName(c.DiskPoolName()); perr == nil {
		_ = pool.Refresh(0)
		pool.Free()
	}

	return ""
}

func writeTarEntry(tw *tar.Writer, name string, data []byte, mode int64) error {
	hdr := &tar.Header{
		Name:    name,
		Mode:    mode,
		Size:    int64(len(data)),
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	return nil
}

// The Phase II cleanup removed the sparse-aware tar helpers
// (writeTarFile, writeTarFileSparse, writeDiskToTar, seekData,
// seekHole, lseekWhence, writeTarRemainder, whenceData, whenceHole).
// They were used only by the old inline ExportDomain producer.
// The new ExportDomain is a thin shim over
// internal/backupstore.ProduceVMArchive, which has its own
// (sparse-aware) writeTarFileSparse in producer.go. The OVA
// path still uses writeTarEntry above.
