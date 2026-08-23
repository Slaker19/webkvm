package libvirt

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// OVATarget identifies which hypervisor family the OVA is being generated
// for. The two families require different disk formats and different OVF
// metadata, and we don't try to bridge them with a single descriptor.
type OVATarget string

const (
	OVATargetVMware OVATarget = "vmware" // VirtualBox, VMware Workstation/ESXi
	OVATargetLibvirt OVATarget = "libvirt" // Proxmox, libvirt, GNOME Boxes, this app
)

// OVA compression level applied to the surrounding tar. zstd is the only
// option for OVA; gzip is kept only for legacy WebKVM backups.
type OVACompress string

const (
	OVACompressZstd OVACompress = "zstd"
)

// OVAOptions controls the writer. Sensible defaults are applied by
// ExportDomainOVA when the caller passes a zero-valued struct.
type OVAOptions struct {
	Target     OVATarget     // "vmware" (default) or "libvirt"
	Compress   OVACompress   // "zstd" (default) — currently the only option
	ZstdLevel  int            // 1..22, default 19
}

// diskEntry is the per-disk metadata that flows through OVF building
// and the tar writer. Declared at package scope so buildOVF can use
// it from outside ExportDomainOVA.
//
// size is the on-disk size of the converted disk (compressed VMDK
// or repacked qcow2). virtualSize is the disk's virtual size as the
// guest sees it (the value the OVF <Disk ovf:capacity> attribute
// must carry — a 30 GB virtual disk in a 5 GB streamOptimized VMDK
// has size=5GB, virtualSize=30GB).
type diskEntry struct {
	srcPath     string
	base        string
	arcPath     string
	size        int64
	virtualSize int64
}

// ExportDomainOVA streams a portable OVA archive of the domain to w. The
// resulting file is a tar containing domain.ovf, a manifest of SHA256
// checksums (domain.mf), and one disk per entry. No temporary files are
// created on disk: qemu-img writes its output to stdout and we pipe
// those bytes straight into the tar writer.
//
// For target=vmware, each disk is converted to VMDK streamOptimized
// (compressed, monolithic, single-extent — designed to be embedded in
// OVAs). For target=libvirt, each disk is re-packed to a more compact
// qcow2 via qemu-img convert -c -O qcow2 (cluster packing, no zero
// blocks). Both transformations are done by streaming; nothing is
// written to a temporary file on disk.
//
// The VM must be shut off before exporting.
func (c *Connector) ExportDomainOVA(ctx context.Context, id string, opts OVAOptions, w io.Writer) error {
	if err := c.ensureConnected(); err != nil {
		return err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("get xml: %w", err)
	}

	if err := c.validateDisksReadable(xmlDesc); err != nil {
		return err
	}

	disks := c.parseDisksFiltered(xmlDesc, true)
	memKB, _ := strconv.ParseInt(extractField(xmlDesc, "<memory>"), 10, 64)
	vcpus := countVCPUs(xmlDesc)
	guestOS := detectGuestOS(xmlDesc)

	// OVA is a plain tar archive (per the DMTF OVF/OVA spec).
	// zstd/gzip are NOT standard for OVA: VMware/VirtualBox/Proxmox
	// expect an uncompressed tar containing the OVF, disk(s) and
	// optional manifest. The disk images themselves are already
	// compressed (streamOptimized VMDK / qcow2 with -c), so the tar
	// wrapper adds negligible size.
	tw := tar.NewWriter(w)

	// Build the OVF before any disk entry so we can include each disk's
	// final size in the descriptor.
	var entries []diskEntry
	for _, d := range disks {
		if d.Source == "" {
			continue
		}
		if _, err := os.Stat(d.Source); err != nil {
			continue
		}
		base := filepath.Base(d.Source)
		// The tar entry must have a format extension (.vmdk for the
		// vmware target, .qcow2 for the libvirt target) so external
		// consumers like VirtualBox and Proxmox can identify the disk
		// format from the filename alone. Without the extension,
		// VBoxManage rejects the OVA with "Unsupported medium format
		// for disk image". The OVF's ovf:href is built from arcPath
		// below, so updating the path here also fixes the OVF.
		ext := ".vmdk"
		if opts.Target == OVATargetLibvirt {
			ext = ".qcow2"
		}
		entries = append(entries, diskEntry{
			srcPath: d.Source,
			base:    base,
			arcPath: fmt.Sprintf("disks/%s%s", base, ext),
		})
	}

	// Phase 1: estimate each disk's compressed size. We use the
	// current qcow2 allocation as the upper bound for the libvirt
	// target (since re-packing with -c usually shrinks it) and the
	// qcow2 virtual size for the vmware target (VMDK streamOptimized
	// is roughly that size, sometimes larger; we use the upper
	// bound and patch the OVF after the conversion is done).
	// We also record the virtual size once here (the OVF
	// <Disk ovf:capacity> needs it and qcow2VirtualSize is a fork
	// into qemu-img, so we don't want to call it twice).
	for i := range entries {
		fi, err := os.Stat(entries[i].srcPath)
		if err != nil {
			return err
		}
		vs, _ := qcow2VirtualSize(entries[i].srcPath)
		if vs == 0 {
			vs = fi.Size() // fallback for non-qcow2 sources
		}
		entries[i].virtualSize = vs
		switch opts.Target {
		case OVATargetLibvirt:
			entries[i].size = fi.Size()
		default:
			entries[i].size = vs
			if entries[i].size == 0 {
				entries[i].size = fi.Size()
			}
		}
	}

	// Phase 2: convert + write each disk, capturing the real
	// resulting size. The OVF is written LAST so we can fill in
	// the actual ovf:size from the real conversion. (We can't
	// write the OVF first because we don't know the exact disk
	// sizes until qemu-img is done.)
	sums := map[string]string{}
	realSizes := make([]int64, len(entries))
	for i, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, sha, err := streamDiskToTar(ctx, tw, e.srcPath, e.arcPath, 0, opts.Target)
		if err != nil {
			return fmt.Errorf("write disk %s: %w", e.base, err)
		}
		sums[e.arcPath] = sha
		realSizes[i] = n
	}
	for i := range entries {
		entries[i].size = realSizes[i]
	}

	// Phase 3: OVF descriptor (written last, with the real sizes).
	ovf := buildOVF(id, entries, memKB, vcpus, guestOS, string(opts.Target))
	ovfPath := "domain.ovf"
	// Hash the OVF while it's still in memory so the manifest can
	// include it. VBoxManage rejects manifests that don't list every
	// tar entry (including domain.ovf) with a parse error.
	ovfBytes := []byte(ovf)
	ovfSum := sha256Hex(ovfBytes)
	if err := writeTarEntry(tw, ovfPath, ovfBytes, 0644); err != nil {
		return err
	}

	// Phase 4: manifest with SHA256 of the OVF and every disk. The
	// manifest is optional in the OVA spec — most consumers don't
	// verify it — but VirtualBox refuses to import if it's present
	// but malformed (e.g. missing the OVF entry), so we keep it
	// complete and well-formed.
	manifest := buildManifest(ovfPath, ovfSum, sums)
	if err := writeTarEntry(tw, "domain.mf", []byte(manifest), 0644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	// Explicit close: the tar terminator (1024 zero bytes) must be
	// written so external tar readers know the archive is complete.
	if err := tw.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	return nil
}

// EstimateOVASize returns the upper bound on the size (in bytes) of the
// .ova that ExportDomainOVA will produce for the given target. Used to
// set Content-Length on the HTTP response so the browser can show a
// progress bar.
func (c *Connector) EstimateOVASize(ctx context.Context, id string, target OVATarget) (int64, error) {
	if err := c.ensureConnected(); err != nil {
		return 0, err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return 0, err
	}
	defer dom.Free()
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return 0, err
	}
	if err := c.validateDisksReadable(xmlDesc); err != nil {
		return 0, err
	}
	disks := c.parseDisksFiltered(xmlDesc, true)
	var total int64
	for _, d := range disks {
		if d.Source == "" {
			continue
		}
		fi, err := os.Stat(d.Source)
		if err != nil {
			continue
		}
		switch target {
		case OVATargetLibvirt:
			// The re-packed qcow2 will be at most the current
			// allocation. (It's usually smaller.)
			total += fi.Size()
		default:
			// VMDK streamOptimized is roughly the qcow2
			// virtual size, sometimes larger. Use the
			// virtual size as the bound and add a 5% margin.
			v, _ := qcow2VirtualSize(d.Source)
			if v == 0 {
				v = fi.Size()
			}
			total += v + v/20
		}
	}
	// OVF (~20KB) + manifest (~1KB) + zstd framing overhead.
	total += 64 * 1024
	return total, nil
}

// EstimateExportSize returns the upper bound on the size of the
// existing .tar.gz WebKVM backup (no re-packing). Used for
// Content-Length on /api/vms/{id}/export.
func (c *Connector) EstimateExportSize(ctx context.Context, id string, compress bool) (int64, error) {
	if err := c.ensureConnected(); err != nil {
		return 0, err
	}
	dom, err := c.lookupDomain(id)
	if err != nil {
		return 0, err
	}
	defer dom.Free()
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return 0, err
	}
	if err := c.validateDisksReadable(xmlDesc); err != nil {
		return 0, err
	}
	disks := c.parseDisksFiltered(xmlDesc, true)
	var total int64
	for _, d := range disks {
		if d.Source == "" {
			continue
		}
		fi, err := os.Stat(d.Source)
		if err != nil {
			continue
		}
		if compress {
			// re-packed qcow2: at most the current allocation
			total += fi.Size()
		} else {
			// sparse-aware: the tar will hold the on-disk
			// allocated bytes. du -b would give us the exact
			// figure, but fi.Size() is a good upper bound
			// since qcow2 doesn't have many holes.
			total += fi.Size()
		}
	}
	// XML + manifest overhead
	total += 16 * 1024
	return total, nil
}

// streamDiskToTar runs the appropriate qemu-img conversion (if any)
// and pipes the resulting bytes into a single tar entry. The function
// returns the actual byte count written to the tar plus the SHA256
// checksum of those bytes (for the manifest).
//
// qemu-img convert doesn't reliably support `-` (stdout) as the
// output destination for some conversions (notably with -c and with
// the vmdk streamOptimized subformat), so this implementation runs
// qemu-img into a temporary file in the same pool directory and
// then copies that file into the tar. The temp file is always
// removed (even on error) so no leftover data accumulates on disk.
func streamDiskToTar(ctx context.Context, tw *tar.Writer, srcPath, arcName string, declaredSize int64, target OVATarget) (int64, string, error) {
	tmpPath, cleanup, err := convertToTempFile(ctx, srcPath, target)
	if err != nil {
		return 0, "", err
	}
	defer cleanup()
	return streamFileToTar(tw, tmpPath, arcName, declaredSize)
}

// streamRepackedDisk is the same idea as streamDiskToTar but without
// the SHA256 hashing (used for the WebKVM backup format which doesn't
// have a manifest of checksums).
func streamRepackedDisk(ctx context.Context, tw *tar.Writer, srcPath, arcName string, target OVATarget) error {
	tmpPath, cleanup, err := convertToTempFile(ctx, srcPath, target)
	if err != nil {
		return err
	}
	defer cleanup()
	_, _, err = streamFileToTar(tw, tmpPath, arcName, 0)
	return err
}

// convertToTempFile runs qemu-img on srcPath and returns the path of
// a temporary file containing the result. The caller MUST call the
// returned cleanup function to remove the file.
//
// We use /var/tmp (on the root filesystem, typically hundreds of GB)
// rather than os.TempDir() (/tmp, which is often a size-limited tmpfs
// and can't hold a full-disk VMDK conversion). /var/tmp is the
// standard location for temp files that may be large and is writable
// by any user (mode 1777).
func convertToTempFile(ctx context.Context, srcPath string, target OVATarget) (string, func(), error) {
	_ = os.Chmod(srcPath, 0644) // best-effort

	tmpDir := "/var/tmp"
	// Pick a temp-file suffix that matches the target format so
	// anyone inspecting /var/tmp during an export (e.g. to confirm
	// qemu-img is making progress) sees the right extension.
	suffix := "*.qcow2"
	if target == OVATargetVMware {
		suffix = "*.vmdk"
	}
	tmp, err := os.CreateTemp(tmpDir, "webkvm-export-"+suffix)
	if err != nil {
		return "", nil, fmt.Errorf("create temp file in %s: %w", tmpDir, err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()

	var cmd *exec.Cmd
	switch target {
	case OVATargetLibvirt:
		// -c: cluster packing, drops zero blocks
		// -O qcow2: keep the same format but re-pack densely
		cmd = exec.CommandContext(ctx, "qemu-img", "convert", "-c", "-O", "qcow2", "-q", srcPath, tmpPath)
	default:
		// streamOptimized: monolithic, single-extent, grain
		// table at the start — designed to live inside an OVA.
		cmd = exec.CommandContext(ctx, "qemu-img", "convert", "-O", "vmdk", "-o", "subformat=streamOptimized", "-q", srcPath, tmpPath)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmpPath)
		return "", nil, fmt.Errorf("qemu-img: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return tmpPath, func() { _ = os.Remove(tmpPath) }, nil
}

// streamFileToTar copies an existing file into a tar entry,
// returning the byte count and the SHA256 of the data.
func streamFileToTar(tw *tar.Writer, srcPath, arcName string, declaredSize int64) (int64, string, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	fi, _ := f.Stat()
	hdrSize := declaredSize
	if hdrSize == 0 && fi != nil {
		hdrSize = fi.Size()
	}
	hdr := &tar.Header{
		Name:    arcName,
		Mode:    0644,
		Size:    hdrSize,
		ModTime: time.Now(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return 0, "", err
	}
	hasher := sha256.New()
	mw := io.MultiWriter(tw, hasher)
	n, err := io.Copy(mw, f)
	if err != nil {
		return n, "", err
	}
	// Pad to a 512-byte boundary so the tar entry is well-formed.
	if pad := n % 512; pad != 0 {
		if _, err := tw.Write(make([]byte, 512-pad)); err != nil {
			return n, "", err
		}
	}
	return n, hex.EncodeToString(hasher.Sum(nil)), nil
}

// buildOVF produces a minimal OVF descriptor for the given target.
// We don't try to expose every QEMU feature through OVF — the goal is
// to produce a descriptor that VirtualBox / VMware / Proxmox will
// accept, not to round-trip every QEMU option.
func buildOVF(name string, entries []diskEntry, memKB, vcpus int64, guestOS, targetStr string) string {
	// 1 MiB = 1048576 bytes; OVF memory unit.
	memMB := memKB / 1024
	if memMB == 0 {
		memMB = 512
	}
	if vcpus == 0 {
		vcpus = 1
	}
	ovfOS := ovfOSType(guestOS, targetStr)
	ns := "http://schemas.dmtf.org/ovf/envelope/1"
	rasd := "http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_ResourceAllocationSettingData"
	vssd := "http://schemas.dmtf.org/wbem/wscim/1/cim-schema/2/CIM_VirtualSystemSettingData"
	xsi := "http://www.w3.org/2001/XMLSchema-instance"

	// Disk section
	var refs strings.Builder
	var disks strings.Builder
	for i, e := range entries {
		fileID := fmt.Sprintf("file%d", i+1)
		diskID := fmt.Sprintf("vmdisk%d", i+1)
		fmt.Fprintf(&refs, `    <File ovf:id="%s" ovf:href="%s" ovf:size="%d"/>
`, fileID, e.arcPath, e.size)
		format := "http://www.vmware.com/interfaces/specifications/vmdk.html#streamOptimized"
		if targetStr == string(OVATargetLibvirt) {
			format = "http://libvirt.org/ovf/qcow2.html"
		}
		// ovf:capacity must be the disk's virtual size (what the
		// guest sees), NOT the on-disk size of the VMDK/qcow2 file.
		// VirtualBox/Proxmox use it to size the resulting disk; if
		// we set it to the compressed size here, the imported VM
		// ends up with a 5 GB disk instead of the original 30 GB.
		fmt.Fprintf(&disks, `      <Disk ovf:capacity="%d" ovf:capacityAllocationUnits="byte" ovf:diskId="%s" ovf:fileRef="%s" ovf:format="%s"/>
`, e.virtualSize, diskID, fileID, format)
	}

	// Hardware items
	var hw strings.Builder
	// CPU
	fmt.Fprintf(&hw, `        <Item>
          <rasd:AllocationUnits>hertz * 10^6</rasd:AllocationUnits>
          <rasd:Description>Number of Virtual CPUs</rasd:Description>
          <rasd:ElementName>%d virtual CPU(s)</rasd:ElementName>
          <rasd:InstanceID>1</rasd:InstanceID>
          <rasd:ResourceType>3</rasd:ResourceType>
          <rasd:VirtualQuantity>%d</rasd:VirtualQuantity>
        </Item>
`, vcpus, vcpus)
	// Memory
	fmt.Fprintf(&hw, `        <Item>
          <rasd:AllocationUnits>byte * 2^20</rasd:AllocationUnits>
          <rasd:Description>Memory Size</rasd:Description>
          <rasd:ElementName>%dMB of memory</rasd:ElementName>
          <rasd:InstanceID>2</rasd:InstanceID>
          <rasd:ResourceType>4</rasd:ResourceType>
          <rasd:VirtualQuantity>%d</rasd:VirtualQuantity>
        </Item>
`, memMB, memMB)
	// Network adapter — E1000 for vmware (universally supported), virtio for libvirt.
	nicName := "E1000"
	if targetStr == string(OVATargetLibvirt) {
		nicName = "virtio-net"
	}
	fmt.Fprintf(&hw, `        <Item>
          <rasd:AllocationUnits></rasd:AllocationUnits>
          <rasd:Connection>VM Network</rasd:Connection>
          <rasd:Description>%s network adapter</rasd:Description>
          <rasd:ElementName>%s 1</rasd:ElementName>
          <rasd:InstanceID>3</rasd:InstanceID>
          <rasd:ResourceType>10</rasd:ResourceType>
        </Item>
`, nicName, nicName)
	// Disk controller. VMware/VirtualBox targets use IDE (ResourceType 5)
	// for maximum portability; the libvirt target uses SCSI (ResourceType 6).
	var controllerType, controllerName string
	if targetStr == string(OVATargetLibvirt) {
		controllerType = "6"
		controllerName = "SCSI Controller"
	} else {
		controllerType = "5"
		controllerName = "IDE Controller"
	}
	fmt.Fprintf(&hw, `        <Item>
          <rasd:Description>%s</rasd:Description>
          <rasd:ElementName>%s</rasd:ElementName>
          <rasd:InstanceID>4</rasd:InstanceID>
          <rasd:ResourceType>%s</rasd:ResourceType>
        </Item>
`, controllerName, controllerName, controllerType)
	// One Item per disk, parented to the controller.
	for i := range entries {
		diskID := fmt.Sprintf("vmdisk%d", i+1)
		hostRes := fmt.Sprintf("ovf:/disk/%s", diskID)
		fmt.Fprintf(&hw, `        <Item>
          <rasd:AddressOnParent>%d</rasd:AddressOnParent>
          <rasd:ElementName>disk-%d</rasd:ElementName>
          <rasd:HostResource>%s</rasd:HostResource>
          <rasd:InstanceID>%d</rasd:InstanceID>
          <rasd:Parent>4</rasd:Parent>
          <rasd:ResourceType>17</rasd:ResourceType>
        </Item>
`, i, i+1, hostRes, 100+i)
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<Envelope xmlns="%s" xmlns:ovf="%s" xmlns:rasd="%s" xmlns:vssd="%s" xmlns:xsi="%s">
  <References>
%s  </References>
  <DiskSection>
    <Info>List of the virtual disks</Info>
%s  </DiskSection>
  <NetworkSection>
    <Info>The list of logical networks</Info>
    <Network ovf:name="VM Network">
      <Description>The VM Network network</Description>
    </Network>
  </NetworkSection>
  <VirtualSystem ovf:id="%s">
    <Info>A WebKVM-exported virtual machine</Info>
    <Name>%s</Name>
    <OperatingSystemSection ovf:id="100" ovf:version="6" ovf:osType="%s">
      <Info>The kind of installed guest operating system</Info>
      <Description>%s</Description>
    </OperatingSystemSection>
    <VirtualHardwareSection>
      <Info>Virtual hardware requirements</Info>
      <System>%s</System>
%s    </VirtualHardwareSection>
  </VirtualSystem>
</Envelope>
`, ns, ns, rasd, vssd, xsi,
		refs.String(),
		disks.String(),
		name, name, ovfOS, ovfOS,
		fmt.Sprintf("WebKVM export of %s", name),
		hw.String())
}

// buildManifest emits a DMTF OVF manifest with one line per tar
// entry: <path>(sha256)= <hex>. The OVF descriptor entry is listed
// first, then each disk. VirtualBox refuses to import an OVA whose
// manifest is present but malformed (e.g. missing the OVF line), so
// callers must always pass the OVF hash.
func buildManifest(ovfPath, ovfSum string, sums map[string]string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s(sha256)= %s\n", ovfPath, ovfSum)
	for p, s := range sums {
		fmt.Fprintf(&sb, "%s(sha256)= %s\n", p, s)
	}
	return sb.String()
}

// sha256Hex returns the hex-encoded SHA256 of b. Used to build the
// OVF manifest entry.
func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func ovfOSType(guestOS, target string) string {
	g := strings.ToLower(guestOS)
	switch {
	case strings.Contains(g, "win"):
		if target == string(OVATargetVMware) {
			return "windows9_64Guest"
		}
		return "windows"
	case strings.Contains(g, "linux"), strings.Contains(g, "unix"):
		if target == string(OVATargetVMware) {
			return "otherLinux64Guest"
		}
		return "linux"
	default:
		if target == string(OVATargetVMware) {
			return "otherLinux64Guest"
		}
		return "linux"
	}
}

// detectGuestOS does a best-effort lookup of the guest OS string from
// the libvirt XML <os> block and any <description> metadata.
func detectGuestOS(xmlDesc string) string {
	// Look for <os><type>...</type></os> first
	if t := extractField(xmlDesc, "<type arch="); t != "" {
		if strings.Contains(strings.ToLower(t), "win") {
			return "windows"
		}
		if strings.Contains(strings.ToLower(t), "linux") {
			return "linux"
		}
	}
	if t := extractField(xmlDesc, "<type>"); t != "" {
		tt := strings.ToLower(t)
		if strings.Contains(tt, "win") {
			return "windows"
		}
		if strings.Contains(tt, "linux") {
			return "linux"
		}
	}
	return "linux"
}

func countVCPUs(xmlDesc string) int64 {
	v := extractField(xmlDesc, "<vcpu")
	if v == "" {
		v = extractField(xmlDesc, "<vcpu>")
	}
	if v == "" {
		return 1
	}
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), "</vcpu>"))
	v = strings.TrimSpace(strings.TrimSuffix(v, "</vcpu"))
	// remove any attributes that may have been included
	if idx := strings.IndexAny(v, " \t\n/>"); idx >= 0 {
		v = v[:idx]
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	if n < 1 {
		return 1
	}
	return n
}

// qcow2VirtualSize returns the virtual size of a qcow2 image by
// asking qemu-img. Used for upper-bound size estimation.
func qcow2VirtualSize(path string) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", path).Output()
	if err != nil {
		return 0, err
	}
	var info struct {
		VirtualSize int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return 0, err
	}
	return info.VirtualSize, nil
}

// newCompressor wraps w in the configured compression layer. Returns
// the wrapped writer and a close function the caller must invoke to
// flush the compressed stream.
func newCompressor(w io.Writer, kind OVACompress, zstdLevel int) (io.Writer, func() error, error) {
	if kind == "" {
		kind = OVACompressZstd
	}
	if zstdLevel <= 0 {
		zstdLevel = 19
	}
	if zstdLevel > 22 {
		zstdLevel = 22
	}
	switch kind {
	case OVACompressZstd:
		zw, err := zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(zstdLevel)))
		if err != nil {
			return nil, nil, err
		}
		return zw, zw.Close, nil
	default:
		return nil, nil, fmt.Errorf("unknown compression: %s", kind)
	}
}

// extractField returns the content of the first XML element whose
// opening tag contains the given substring, or "" if not found.
// Example: extractField("<memory unit='KiB'>4194304</memory>", "<memory>")
// returns "4194304". Strips attributes from the opening tag.
func extractField(xml, openTag string) string {
	idx := strings.Index(xml, openTag)
	if idx < 0 {
		return ""
	}
	// Find the closing '>' of the opening tag.
	end := strings.Index(xml[idx:], ">")
	if end < 0 {
		return ""
	}
	open := idx + end + 1
	closeTag := "</" + openTag[1:]
	close := strings.Index(xml[open:], closeTag)
	if close < 0 {
		return ""
	}
	return strings.TrimSpace(xml[open : open+close])
}

// ImportOVA restores a VM from a .ova archive produced by
// ExportDomainOVA. The OVF descriptor is parsed to discover disk
// filenames; each disk is then extracted from the .ova tar and
// converted to qcow2 in the destination pool (VMDK streamOptimized
// is converted via qemu-img convert; qcow2 inside the OVA is moved
// as-is if it's already qcow2). Path references in the OVF are
// rewritten to point at the new disk locations before the domain is
// defined.
func (c *Connector) ImportOVA(ovaPath, newName, poolName string) (string, string, error) {
	if err := c.ensureConnected(); err != nil {
		return "", "", err
	}
	if poolName == "" {
		poolName = c.DiskPoolName()
	}
	poolPath, err := c.GetPoolPath(poolName)
	if err != nil {
		return "", "", fmt.Errorf("get pool path: %w", err)
	}

	// Detect & decompress.
	raw, cleanup, err := openOVAStream(ovaPath)
	if err != nil {
		return "", "", err
	}
	defer cleanup()

	tr := tar.NewReader(raw)
	var ovfData []byte
	diskFiles := map[string][]byte{} // arcName -> raw bytes
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(hdr.Name, ".ovf"):
			ovfData, err = io.ReadAll(tr)
			if err != nil {
				return "", "", err
			}
		case strings.HasPrefix(hdr.Name, "disks/") || strings.HasSuffix(hdr.Name, ".vmdk") || strings.HasSuffix(hdr.Name, ".qcow2"):
			diskFiles[hdr.Name] = nil // we'll read on demand below
		}
	}

	if len(ovfData) == 0 {
		return "", "", fmt.Errorf("OVA missing domain.ovf")
	}

	// Re-open the tar (the previous one has been consumed) and stream
	// each disk to a temp file we can hand to qemu-img for conversion.
	raw2, cleanup2, err := openOVAStream(ovaPath)
	if err != nil {
		return "", "", err
	}
	defer cleanup2()
	tr2 := tar.NewReader(raw2)

	// Keep the working directory inside the destination pool. This
	// avoids filling /tmp (often a small tmpfs) with disk images.
	workDir, err := os.MkdirTemp(poolPath, ".webkvm-ova-import-*")
	if err != nil {
		return "", "", fmt.Errorf("create work dir in pool: %w", err)
	}
	defer os.RemoveAll(workDir)

	for {
		hdr, err := tr2.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", "", err
		}
		if _, ok := diskFiles[hdr.Name]; !ok {
			continue
		}
		// Extract the disk to a temp file — prevent zipslip via Base + prefix check.
		base := filepath.Base(hdr.Name)
		if base == "." || base == "/" || strings.Contains(base, "..") {
			continue
		}
		tmpDisk := filepath.Join(workDir, base)
		if !strings.HasPrefix(filepath.Clean(tmpDisk), filepath.Clean(workDir)+string(os.PathSeparator)) {
			continue
		}
		f, err := os.Create(tmpDisk)
		if err != nil {
			return "", "", err
		}
		if _, err := io.Copy(f, tr2); err != nil {
			f.Close()
			return "", "", err
		}
		f.Close()
		diskFiles[hdr.Name] = []byte(tmpDisk) // reuse the map; the value is now a path
	}

	// Convert each disk to qcow2 in the destination pool.
	ovfStr := string(ovfData)

	// Determine the effective name: caller-supplied, else <Name> in the OVF.
	effectiveName := newName
	if effectiveName == "" {
		if m := regexp.MustCompile(`<Name>([^<]*)</Name>`).FindStringSubmatch(ovfStr); len(m) > 1 {
			effectiveName = m[1]
		}
	}
	if effectiveName == "" {
		return "", "", fmt.Errorf("OVA missing domain name")
	}
	resolvedName, err := c.resolveUniqueDomainName(effectiveName)
	if err != nil {
		return "", "", err
	}
	// Always rewrite the <Name> in the OVF to the resolved value.
	ovfStr = regexp.MustCompile(`<Name>[^<]*</Name>`).ReplaceAllString(ovfStr, "<Name>"+resolvedName+"</Name>")

	// For each disk referenced in the OVF, convert to qcow2 and
	// rewrite the OVF reference to point at the new path. The
	// disk files are named after resolvedName (the unique VM
	// name), with a counter suffix on collision — see
	// vmDiskPath for the full policy.
	ovfStr = rewriteOVFDisks(ovfStr, diskFiles, poolPath, resolvedName)

	// Re-register the qcow2 files we just wrote with libvirt. The
	// `qemu-img convert` inside rewriteOVFDisks writes directly to
	// the pool directory, bypassing the volume registry. Without
	// this refresh, the disks are "ghost" volumes — visible on
	// disk and referenced by the new domain XML, but invisible to
	// `virsh vol-list` and /api/storage/volumes. A failure here
	// is not fatal: libvirt doesn't require a volume entry to use
	// a file in a dir pool, and the domain definition below would
	// still succeed.
	if pool, err := c.conn.LookupStoragePoolByName(poolName); err == nil {
		if rerr := pool.Refresh(0); rerr != nil {
			slog.Warn("ova_pool_refresh_failed",
				"pool", poolName, "err", rerr.Error())
		}
		pool.Free()
	} else {
		slog.Warn("ova_pool_lookup_failed",
			"pool", poolName, "err", err.Error())
	}

	// The OVF is a VirtualBox/VMware-style descriptor, not a libvirt
	// XML. For libvirt import, we need to convert the OVF into a
	// libvirt domain XML. For now, a minimal path: if the OVF has a
	// <Name> tag, we use it to define a domain from a generated
	// libvirt XML. Full OVF->libvirt translation is left for a future
	// pass — for this round-trip to work between OVA exports, we use
	// the qcow2 disk path as the basis for a generated domain.
	xmlStr, err := ovfToLibvirtXML(ovfStr, poolPath)
	if err != nil {
		return "", "", err
	}

	dom, err := c.conn.DomainDefineXML(xmlStr)
	if err != nil {
		return "", "", fmt.Errorf("define domain: %w", err)
	}
	defer dom.Free()
	uuid, err := dom.GetUUIDString()
	if err != nil {
		return "", "", err
	}
	return uuid, resolvedName, nil
}

// openOVAStream opens ovaPath, auto-detects whether it's compressed
// with gzip or zstd, and returns a reader positioned at the start of
// the tar payload plus a cleanup function the caller must invoke.
func openOVAStream(ovaPath string) (io.Reader, func(), error) {
	f, err := os.Open(ovaPath)
	if err != nil {
		return nil, nil, err
	}
	br := bufio.NewReader(f)
	head, _ := br.Peek(4)
	if len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return gz, func() { gz.Close(); f.Close() }, nil
	}
	if len(head) >= 4 && head[0] == 0x28 && head[1] == 0xb5 && head[2] == 0x2f && head[3] == 0xfd {
		// Single-threaded, low-memory decode: a multi-GB backup
		// streamed through the decoder must not balloon the
		// process RSS (the OOM killer would reap the backend mid-
		// import). Concurrency 1 + Lowmem trades a little speed
		// for a much smaller decoder window.
		zr, err := zstd.NewReader(br,
			zstd.WithDecoderConcurrency(1),
			zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxMemory(512<<20),
		)
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return zr, func() { zr.Close(); f.Close() }, nil
	}
	// Plain tar
	return br, func() { f.Close() }, nil
}

// rewriteOVFDisks walks the OVF, finds File ovf:href="disks/..."
// references, and rewrites them to point at the qcow2 file we just
// converted into the destination pool. The disk is named after
// vmName (the resolved, unique VM name) using vmDiskPath — the
// first disk becomes <vmName>.qcow2, subsequent disks become
// <vmName>-2.qcow2, etc. Returns the rewritten OVF.
func rewriteOVFDisks(ovf string, diskFiles map[string][]byte, poolPath, vmName string) string {
	// Simple regex: ovf:href="disks/SOMETHING"
	re := regexp.MustCompile(`ovf:href="(disks/[^"]+)"`)
	// Track per-import disk counter so the first disk lands at
	// <vmName>.qcow2, the second at <vmName>-2.qcow2, etc. We
	// re-resolve the path each time so collisions with files
	// that already exist in the pool are handled by vmDiskPath.
	diskCounter := 0
	return re.ReplaceAllStringFunc(ovf, func(match string) string {
		sm := re.FindStringSubmatch(match)
		if len(sm) < 2 {
			return match
		}
		arcName := sm[1]
		tmpPathBytes, ok := diskFiles[arcName]
		if !ok {
			return match
		}
		tmpPath := string(tmpPathBytes)

		// Resolve the final destination name in the pool.
		// Use the resolvedName (not the request newName) so the
		// disk file matches the actual VM name libvirt will
		// register after collision resolution.
		probe := vmName
		if diskCounter > 0 {
			probe = fmt.Sprintf("%s-%d", vmName, diskCounter+1)
		}
		qcow2Path, perr := VMDiskPath(poolPath, probe, ".qcow2")
		if perr != nil {
			// Fall back to the basename if vmDiskPath
			// rejects the input (should not happen — the
			// caller has already validated vmName).
			qcow2Path = filepath.Join(poolPath, filepath.Base(arcName))
		}

		// Convert to qcow2 at the final destination — ensure qcow2Path is within poolPath.
		if !strings.HasPrefix(filepath.Clean(qcow2Path), filepath.Clean(poolPath)+string(os.PathSeparator)) {
			return match
		}
		convCtx, convCancel := context.WithTimeout(context.Background(), 2*time.Hour)
		defer convCancel()
		cmd := exec.CommandContext(convCtx, "qemu-img", "convert", "-O", "qcow2", "-q", tmpPath, qcow2Path)
		if out, err := cmd.CombinedOutput(); err != nil {
			// Fall back: try to copy as-is
			_ = out
			in, _ := os.Open(tmpPath)
			if in != nil {
				defer in.Close()
				out, _ := os.Create(qcow2Path)
				if out != nil {
					defer out.Close()
					_, _ = io.Copy(out, in)
				}
			}
		}
		diskCounter++
		// Return the same href; ovfToLibvirtXML will pick the disk
		// up from diskFiles and use the final pool path.
		return match
	})
}

// ovfToLibvirtXML translates the OVF (VirtualBox/VMware-style
// descriptor) into a minimal libvirt domain XML. The translation is
// intentionally conservative: only the fields we know how to
// populate are filled in. Anything else (custom networks, hostdev,
// etc.) is dropped, since the OVF doesn't carry that information
// anyway. The goal is a working round-trip, not full fidelity.
func ovfToLibvirtXML(ovf, poolPath string) (string, error) {
	// Parse the OVF with a tolerant struct.
	var doc struct {
		VirtualSystem struct {
			Name              string `xml:"Name"`
			OperatingSystem   struct {
				Description string `xml:"Description"`
			} `xml:"OperatingSystemSection"`
			VirtualHardware struct {
				System struct {
					ElementName string `xml:"ElementName"`
				} `xml:"System"`
				Items []struct {
					ResourceType  int    `xml:"ResourceType"`
					ElementName   string `xml:"ElementName"`
					VirtualQuantity int64 `xml:"VirtualQuantity"`
					HostResource  string `xml:"HostResource"`
				} `xml:"Item"`
			} `xml:"VirtualHardwareSection"`
		} `xml:"VirtualSystem"`
		References struct {
			Files []struct {
				ID   string `xml:"id,attr"`
				Href string `xml:"href,attr"`
			} `xml:"File"`
		} `xml:"References"`
	}
	if err := xmlDecode([]byte(ovf), &doc); err != nil {
		return "", fmt.Errorf("parse OVF: %w", err)
	}
	if doc.VirtualSystem.Name == "" {
		return "", fmt.Errorf("OVF missing VirtualSystem/Name")
	}

	vcpus := int64(1)
	memMB := int64(512)
	for _, it := range doc.VirtualSystem.VirtualHardware.Items {
		switch it.ResourceType {
		case 3: // CPU
			if it.VirtualQuantity > 0 {
				vcpus = it.VirtualQuantity
			}
		case 4: // Memory
			if it.VirtualQuantity > 0 {
				memMB = it.VirtualQuantity
			}
		}
	}

	// Build disk sections. We pick up the disk files we just wrote
	// to the pool (the .qcow2 files).
	var disks strings.Builder
	for _, f := range doc.References.Files {
		if !strings.HasPrefix(f.Href, "disks/") {
			continue
		}
		base := filepath.Base(f.Href)
		diskPath := filepath.Join(poolPath, base)
		fmt.Fprintf(&disks, `    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='%s'/>
      <target dev='vda' bus='virtio'/>
    </disk>
`, diskPath)
	}
	if disks.Len() == 0 {
		return "", fmt.Errorf("OVF has no disk references")
	}

	// Build a minimal libvirt domain XML.
	memKiB := memMB * 1024
	return fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='KiB'>%d</memory>
  <vcpu>%d</vcpu>
  <os>
    <type arch='x86_64'>hvm</type>
    <boot dev='hd'/>
  </os>
  <features>
    <acpi/>
    <apic/>
  </features>
  <devices>
%s    <interface type='network'>
      <source network='default'/>
      <model type='virtio'/>
    </interface>
    <graphics type='vnc' listen='0.0.0.0'/>
  </devices>
</domain>
`, doc.VirtualSystem.Name, memKiB, vcpus, disks.String()), nil
}

// xmlDecode is encoding/xml.Unmarshal; aliased here so callers in
// this file can use the short name without polluting the import
// list.
func xmlDecode(data []byte, v interface{}) error {
	return _xmlUnmarshal(data, v)
}
