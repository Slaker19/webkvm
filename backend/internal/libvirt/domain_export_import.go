package libvirt

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	libvirt "libvirt.org/go/libvirt"
	"webkvm/internal/backupstore"
	"webkvm/internal/imgsafe"
	"webkvm/internal/models"
)

// CloneDomain duplicates a domain into a new one named req.Name. The
// source VM must be shut off — a running qcow2 file can't be safely
// read by qemu-img while QEMU holds the write lock, and a copy taken
// mid-write would be corrupt.
//
// Every <disk device='disk'> is cloned (not just the first one, the
// historical behavior): each gets its own new file in req.Pool (or
// the default disk pool), named "<newName>[-N].qcow2".
//
//   - req.Linked=true: the new disk is a qcow2 overlay with the
//     source disk as backing file (qemu-img create -b/-F). Near
//     instant, minimal extra space, but the clone depends on the
//     source disk continuing to exist — deleting/moving/resizing the
//     source breaks it. Only valid for qcow2 sources.
//   - req.Linked=false (default): a fully independent copy via
//     qemu-img convert. Slower, uses full disk space, no dependency.
func (c *Connector) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return models.VM{}, err
	}
	defer dom.Free()

	if state, _, serr := dom.GetState(); serr == nil && state != libvirt.DOMAIN_SHUTOFF {
		return models.VM{}, ErrDomainMustBeStoppedToClone
	}

	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		xmlDesc, _ = dom.GetXMLDesc(0)
	}
	if xmlDesc == "" {
		return models.VM{}, fmt.Errorf("cannot get domain XML")
	}

	newUUID := uuid.New().String()
	newName := req.Name
	if newName == "" || strings.Contains(newName, "/") || strings.Contains(newName, "\\") || strings.Contains(newName, "..") {
		return models.VM{}, fmt.Errorf("invalid VM name %q", newName)
	}

	xmlDesc = regexp.MustCompile(`<name>[^<]+</name>`).ReplaceAllString(xmlDesc, "<name>"+xmlEscape(newName)+"</name>")
	xmlDesc = regexp.MustCompile(`<uuid>[^<]+</uuid>`).ReplaceAllString(xmlDesc, "<uuid>"+newUUID+"</uuid>")
	xmlDesc = regexp.MustCompile(`<title>[^<]*</title>`).ReplaceAllString(xmlDesc, "")
	// Remove MAC addresses so libvirt generates new ones
	xmlDesc = regexp.MustCompile(`<mac\b[^/]*/>\s*`).ReplaceAllString(xmlDesc, "")
	// Remove NVRAM path — libvirt regenerates it from the template on
	// first define of a domain that has none.
	xmlDesc = regexp.MustCompile(`<nvram>[^<]*</nvram>`).ReplaceAllString(xmlDesc, "")

	if req.Network != "" {
		xmlDesc = regexp.MustCompile(`(<source\s+network=)['"][^'"]+['"]`).
			ReplaceAllString(xmlDesc, "${1}'"+xmlEscape(req.Network)+"'")
	}

	poolName := req.Pool
	if poolName == "" {
		poolName = c.DiskPoolName()
	}
	poolPath, err := c.GetPoolPath(poolName)
	if err != nil {
		return models.VM{}, fmt.Errorf("get pool path: %w", err)
	}

	// Clone every <disk device='disk'> block, not just the first —
	// VMs with a secondary data disk used to silently lose it on
	// clone (the new domain referenced the *original*'s second disk
	// file, sharing writable state between two running domains).
	diskRe := regexp.MustCompile(`<disk\b[^>]+device='disk'[^>]*>[\s\S]*?<source\b[^>]*file='([^']+)'[^>]*/>[\s\S]*?</disk>`)
	matches := diskRe.FindAllStringSubmatch(xmlDesc, -1)

	clonedPaths := make([]string, 0, len(matches))
	cleanupAll := func() {
		for _, p := range clonedPaths {
			_ = os.Remove(p)
		}
	}

	for i, dm := range matches {
		fullMatch := dm[0]
		oldDiskPath := dm[1]

		suffix := ""
		if i > 0 {
			suffix = fmt.Sprintf("-%d", i+1)
		}
		newDiskName := newName + suffix + ".qcow2"
		newDiskPath := filepath.Join(poolPath, newDiskName)
		if rel, rerr := filepath.Rel(poolPath, newDiskPath); rerr != nil || strings.HasPrefix(rel, "..") || rel == ".." {
			cleanupAll()
			return models.VM{}, fmt.Errorf("invalid disk path")
		}

		originalFormat := "qcow2"
		if fm := regexp.MustCompile(`<driver[^>]*type='([^']+)'`).FindStringSubmatch(fullMatch); len(fm) > 1 {
			originalFormat = fm[1]
		}

		if err := cloneDiskFile(oldDiskPath, newDiskPath, originalFormat, req.Linked); err != nil {
			cleanupAll()
			return models.VM{}, fmt.Errorf("clone disk %s: %w", filepath.Base(oldDiskPath), err)
		}
		clonedPaths = append(clonedPaths, newDiskPath)

		// Only the first occurrence of oldDiskPath is replaced so
		// disks that happen to share a source file (rare, but not
		// impossible with hand-edited XML) don't get double-replaced.
		xmlDesc = strings.Replace(xmlDesc, oldDiskPath, newDiskPath, 1)
	}

	if len(clonedPaths) > 0 {
		if pool, perr := c.conn.LookupStoragePoolByName(poolName); perr == nil {
			if rerr := pool.Refresh(0); rerr != nil {
				slog.Warn("clone_pool_refresh_failed", "pool", poolName, "err", rerr.Error())
			}
			pool.Free()
		}
	}

	newDom, err := c.defineDomain(xmlDesc, TrustedDomain)
	if err != nil {
		cleanupAll()
		return models.VM{}, fmt.Errorf("define cloned domain: %w", err)
	}
	defer newDom.Free()

	return c.domainToVM(newDom)
}

// cloneDiskFile creates dstPath as a copy of srcPath. When linked is
// true and format is qcow2, it creates a copy-on-write overlay
// backed by srcPath (near-instant); otherwise it makes a fully
// independent copy with qemu-img convert (linked is silently ignored
// for non-qcow2 sources, since raw disks have no backing-file
// support).
func cloneDiskFile(srcPath, dstPath, format string, linked bool) error {
	if strings.Contains(srcPath, "..") || strings.Contains(dstPath, "..") {
		return fmt.Errorf("invalid disk path: traversal not allowed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	if linked && format == "qcow2" {
		cmd := exec.CommandContext(ctx, "qemu-img", "create",
			"-f", "qcow2", "-F", "qcow2", "-b", srcPath, dstPath)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("qemu-img create (linked): %w (%s)", err, strings.TrimSpace(string(out)))
		}
		return nil
	}

	cmd := exec.CommandContext(ctx, "qemu-img", "convert", "-O", format, "-q", srcPath, dstPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(dstPath)
		return fmt.Errorf("qemu-img convert: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
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
	// Every successful lookup returns a virDomainPtr that holds a
	// reference in libvirtd until it is freed. Discarding it with `_`
	// leaked one object per probe — and this loop probes up to 1000
	// times, so importing repeatedly into a namespace with many
	// same-prefixed VMs grew the backend's RSS and libvirtd's
	// reference table without bound.
	taken := func(n string) bool {
		dom, err := c.conn.LookupDomainByName(n)
		if err != nil {
			return false
		}
		dom.Free()
		return true
	}
	if !taken(name) {
		// Not found: original is fine.
		return name, nil
	}
	for i := 1; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !taken(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find a free name after 1000 attempts starting at %s", name)
}

// DomainExists reports whether a domain with exactly this name is already
// defined, regardless of power state. Same lookup semantics as
// resolveUniqueDomainName: a failed LookupDomainByName means the name is
// free. Used by the appliance deploy pre-flight so a deployment is
// rejected BEFORE any image I/O can overwrite an existing disk.
func (c *Connector) DomainExists(name string) (bool, error) {
	if err := c.ensureConnected(); err != nil {
		return false, err
	}
	if name == "" {
		return false, fmt.Errorf("empty name")
	}
	dom, err := c.conn.LookupDomainByName(name)
	if err == nil {
		// Free the reference: the caller only wants the boolean, and
		// leaking a virDomainPtr per call kept the domain object (and
		// its XML) pinned in libvirtd for the life of the backend.
		dom.Free()
		return true, nil
	}
	return false, nil
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
	// Admin is true when the caller is an administrator. Only then are the
	// RBAC-relevant metadata fields (tags, groups) of the archive carried over.
	Admin bool
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
			xmlBytes, err = readAllLimited(tr, maxDomainXMLBytes)
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
	if newName != "" {
		xmlStr = regexp.MustCompile(`<mac\s+address=['"][^'"]*['"]\s*/>`).ReplaceAllString(xmlStr, "")
	}

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
	// The name may come from the uploaded archive's <name> element, not
	// just from the (already validated) caller-supplied newName. It is
	// later used in filesystem paths (the NVRAM "/<name>_VARS.fd"
	// rewrite below), so reject anything that could escape the pool
	// directory or inject XML before it is used.
	if !nameRE.MatchString(effectiveName) {
		return "", "", nil, fmt.Errorf("invalid domain name in archive %q (allowed: A-Z a-z 0-9 . _ -, max 63 chars)", effectiveName)
	}
	resolvedName, err := c.resolveUniqueDomainName(effectiveName)
	if err != nil {
		return "", "", nil, err
	}
	// resolveUniqueDomainName may append "-N"; re-validate the final value
	// so nothing that skips nameRE (e.g. a caller-supplied name) reaches
	// the NVRAM path rewrite.
	if !nameRE.MatchString(resolvedName) {
		return "", "", nil, fmt.Errorf("invalid resolved domain name %q", resolvedName)
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
	budget := newDiskBudget(poolPath)

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
		if err := budget.take(hdr.Size); err != nil {
			return "", "", nil, err
		}
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			return "", "", nil, fmt.Errorf("create disk %s: %w", dst, err)
		}
		if _, err := copyLimited(f, tr2, hdr.Size); err != nil {
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
	// The XML comes from the archive: it is untrusted for EVERY role. It is
	// validated, re-serialised and only disks extracted above may be
	// referenced (audit #2).
	allowedDisks := make(map[string]bool, len(diskWritten))
	for _, dst := range diskWritten {
		allowedDisks[dst] = true
	}
	dom, err := c.defineDomain(xmlStr, DomainPolicy{AllowedDiskFiles: allowedDisks})
	if err != nil {
		// Do not leave extracted disks behind for a definition we refused.
		for _, dst := range diskWritten {
			_ = os.Remove(dst)
		}
		return "", "", nil, fmt.Errorf("define domain: %w", err)
	}
	defer dom.Free()
	uuid, err := dom.GetUUIDString()
	if err != nil {
		return "", "", nil, err
	}

	// The sanitiser dropped <metadata>; carry over only the descriptive
	// fields (alias, notes...). Ownership and template/shared flags are never
	// taken from an archive. Best effort.
	if meta, ok := archiveMeta(xmlBytes, opts.Admin); ok {
		if merr := c.SetVMMeta(uuid, meta); merr != nil {
			slog.Warn("import_meta_restore_failed", "uuid", uuid, "err", merr)
		}
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
	// Gate C (audit #6): the extracted disks, and the snapshot chain just
	// rebuilt behind them, must not reference files outside the pool
	// (qcow2 backing file / data-file pointing at host files).
	if cerr := verifyImportedDisks(dom, poolPath, diskWritten); cerr != nil {
		_ = dom.Undefine()
		for _, dst := range diskWritten {
			_ = os.Remove(dst)
		}
		return "", "", nil, fmt.Errorf("imported disk rejected: %w", cerr)
	}
	opts.report(100, "done", nil)

	return uuid, resolvedName, warnings, nil
}

// verifyImportedDisks checks every disk this import wrote: qcow2 images must
// have a backing chain that stays inside poolPath and no external data-file;
// other formats are inspected for extents pointing outside the image.
func verifyImportedDisks(dom *libvirt.Domain, poolPath string, written map[string]string) error {
	if len(written) == 0 || !imgsafe.Available() {
		return nil
	}
	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return fmt.Errorf("read back definition: %w", err)
	}
	root, err := parseStrict([]byte(xmlDesc))
	if err != nil {
		// libvirt's own output may carry namespaced metadata; fall back to
		// checking every written file as qcow2-or-probed.
		root = nil
	}
	formats := map[string]string{}
	if root != nil {
		if devs := root.child("devices"); devs != nil {
			for _, d := range devs.Kids {
				if d.Name != "disk" {
					continue
				}
				src, drv := d.child("source"), d.child("driver")
				if src == nil || drv == nil {
					continue
				}
				f, _ := src.attr("file")
				formats[f] = mustAttr(drv, "type")
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, dst := range written {
		switch f := formats[dst]; f {
		case "raw":
			continue
		case "", "qcow2":
			if err := imgsafe.CheckChainInside(ctx, dst, f, poolPath); err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(dst), err)
			}
		default:
			if _, err := imgsafe.Inspect(ctx, dst, f); err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(dst), err)
			}
		}
	}
	return nil
}

// writeArchiveBody streams an archive entry (r) straight to path
// without buffering it in memory. Snapshot overlays / base disks can
// be gigabytes; io.ReadAll on them would OOM the backend.
func writeArchiveBody(r io.Reader, path string, max int64) error {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || strings.Contains(cleaned, "..") {
		return fmt.Errorf("invalid archive path %q", path)
	}
	f, err := os.Create(cleaned)
	if err != nil {
		return err
	}
	if _, cerr := copyLimited(f, r, max); cerr != nil {
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
	snapBudget := newDiskBudget(poolPath)

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
		if snapName != "_base" && !nameRE.MatchString(strings.TrimSuffix(snapName, ".xml")) {
			continue // never let an archive path component shape a pool filename
		}

		if snapName == "_base" {
			// Root base disk of the backing chain.
			if len(parts) == 2 {
				basePath := filepath.Join(poolPath, resolvedName+".base")
				if werr := snapBudget.take(hdr.Size); werr != nil {
					return fmt.Sprintf("restore snapshots: %v", werr)
				}
				if werr := writeArchiveBody(tr3, basePath, hdr.Size); werr != nil {
					return fmt.Sprintf("restore snapshots: write _base: %v", werr)
				}
				baseVolumeArc = basePath
			}
			continue
		}

		if len(parts) == 1 && strings.HasSuffix(parts[0], ".xml") {
			// Snapshot metadata XML.
			body, rerr := readAllLimited(tr3, maxDomainXMLBytes)
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
			if werr := snapBudget.take(hdr.Size); werr != nil {
				return fmt.Sprintf("restore snapshots: %v", werr)
			}
			if werr := writeArchiveBody(tr3, volPath, hdr.Size); werr != nil {
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
		overlay := ""
		if sr.volumeArcName != "" {
			overlay = filepath.Join(poolPath, resolvedName+"."+name)
		}
		// Untrusted metadata: validated and re-serialised; the embedded
		// <domain> is dropped so a later revert cannot install a
		// definition chosen by the archive (audit #2).
		cleanXML, verr := SanitizeSnapshotXML(string(sr.xmlBytes), overlay)
		if verr != nil {
			return fmt.Sprintf("restore snapshots: snapshot %s rejected: %v", name, verr)
		}
		_, serr := dom.CreateSnapshotXML(cleanXML, 1 /* REDEFINE */)
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

