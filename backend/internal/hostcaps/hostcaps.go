// Package hostcaps reports what the local QEMU/libvirt actually
// supports, so the UI never offers a device model that the engine
// will reject at define time.
//
// The motivating bug: WebKVM advertised "qxl" as a video model (and
// even picked it as the default for Windows presets), but QEMU 10.x
// no longer ships QXL — Debian/Ubuntu dropped the device from the
// main package, and `virsh domcapabilities` simply omits it. Users
// hit a hard "invalid video model" error with no way to know why.
// SPICE suffered the same fate: qemu-system-modules-spice is a
// separate, often-absent package.
//
// The source of truth is `<video><enum name='modelType'>` (and the
// sibling enums for graphics, disk bus, network, sound, CPU models)
// inside `virsh domcapabilities`. We parse those enumerations rather
// than hardcode a list, so a host that DOES have the extra modules
// (and thus advertises qxl/spice) keeps offering them.
package hostcaps

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Capabilities is the set of device models/enums the local engine
// accepts. Every slice is sorted and de-duplicated. A caller that
// finds a slice empty should treat the corresponding picker as
// "unknown" and fall back to offering its full curated list (better
// a rejected request than a blank dropdown on an exotic host).
type Capabilities struct {
	// VideoModels mirrors <video><enum name='modelType'>.
	// Typical modern value: vga, cirrus, vmvga, virtio, none, bochs, ramfb.
	// Missing qxl/parallels is expected on QEMU 10+.
	VideoModels []string
	// GraphicsTypes mirrors <graphics><enum name='type'>.
	// Typical: sdl, vnc, spice, egl-headless, dbus. SPICE is absent
	// unless qemu-system-modules-spice (or a distro build with it) is present.
	GraphicsTypes []string
	// DiskBuses mirrors <disk><enum name='bus'>: fdc, scsi, virtio, usb, sata, nvme.
	DiskBuses []string
	// DiskDevices mirrors <disk><enum name='device'>: disk, cdrom, floppy, lun.
	DiskDevices []string
	// SoundModels mirrors <sound><enum name='model'>.
	SoundModels []string
	// NetworkModels is derived from `qemu -device help`, not domcapabilities
	// (libvirt does not enumerate NIC models there).
	NetworkModels []string
	// CPUModels mirrors <cpu><enum name='model'> (host-agnostic libvirt list).
	CPUModels []string
	// CPUFlags is the set of CPUID feature names QEMU accepts in a
	// `+flag`/`-flag` policy, from `qemu -cpu help` ("Recognized CPUID
	// flags"). This is what the on/auto/off flag switches validate
	// against; absent means "unknown", which callers treat permissively.
	CPUFlags []string
	// CPUModes mirrors <cpu><mode> (host-passthrough, host-model, custom, ...).
	CPUModes []string
	// SPICESupported is derived from the graphics enum, split out because
	// SPICE drives several console code paths.
	SPICESupported bool
	// GuestFS reports whether the libguestfs inspection tools
	// (virt-inspector / guestfish) are installed on the host, which
	// enables deep disk inspection (OS/partition/filesystem detection on
	// a VM disk image). Absent means the UI offers only the basic
	// qemu-img based probe. Optional because libguestfs is a heavy
	// dependency (mini-VM per call) that many hosts do not have.
	GuestFS bool
	// GuestFSBin is the resolved path of virt-inspector when GuestFS is
	// true, for diagnostics.
	GuestFSBin string
	// QEMUVersion is the engine version, for diagnostics.
	QEMUVersion string
	// HasGPU reports whether the host exposes a DRM render node
	// (/dev/dri/renderD*), which is what a container needs for hardware
	// transcoding. It is deliberately about the *render* node and not
	// merely a card: a card with no render node cannot accelerate
	// anything a container would ask of it.
	HasGPU bool
	// GPURenderNodes lists those nodes, for diagnostics and for a UI
	// that wants to say which device it is offering.
	GPURenderNodes []string
	// Parsed is false when domcapabilities could not be read at all, in
	// which case slices fall back to conservative curated defaults.
	Parsed bool
}

// curatedVideoModels is the safe fallback when probing fails. It omits
// qxl deliberately: a host we cannot probe is far more likely to be a
// modern QEMU without QXL than an old one with it, and a missing option
// is a better failure mode than one that errors on save.
var curatedVideoModels = []string{"virtio", "vga", "vmvga", "cirrus", "bochs", "ramfb", "none"}

var curatedGraphicsTypes = []string{"vnc"}

var curatedDiskBuses = []string{"virtio", "sata", "scsi", "ide", "usb", "nvme"}

var curatedSoundModels = []string{"ich9", "ac97", "es1370", "usb"}

var curatedNetworkModels = []string{"virtio", "e1000e", "e1000", "rtl8139", "pcnet", "vmxnet3"}

// curatedCPUModes is fixed by libvirt's API and not host-dependent.
var curatedCPUModes = []string{"host-passthrough", "host-model", "max", "custom"}

var (
	cacheMu      sync.Mutex
	cachedCaps   *Capabilities
	cachedAt     time.Time
	cacheTTL     = 5 * time.Minute
	probeTimeout = 10 * time.Second
)

// Get returns the host's capabilities, caching the probe for cacheTTL.
// It never returns an error: an unprobeable host yields Parsed=false
// with curated defaults, because refusing to render the create form
// would be worse than offering a possibly-insufficient list.
func Get() *Capabilities {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cachedCaps != nil && time.Since(cachedAt) < cacheTTL {
		return cachedCaps
	}
	cachedCaps = probe()
	cachedAt = time.Now()
	return cachedCaps
}

// Invalidate drops the cache so the next Get re-probes. Call after a
// package install of qemu modules, or from a "refresh capabilities"
// button.
func Invalidate() {
	cacheMu.Lock()
	cachedCaps = nil
	cacheMu.Unlock()
}

// probe runs domcapabilities and qemu -device help. Any individual
// failure degrades only the corresponding field.
func probe() *Capabilities {
	c := &Capabilities{}

	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	// domcapabilities needs a machine/arch/virttype triple. q35 + x86_64
	// + kvm is what WebKVM targets; on a non-x86 host libvirt returns an
	// error and we fall back to curated defaults.
	out, err := exec.CommandContext(ctx, "virsh", "domcapabilities",
		"--machine", "q35", "--arch", "x86_64", "--virttype", "kvm").Output()
	if err == nil && len(out) > 0 {
		xml := string(out)
		c.VideoModels = enumValues(xml, "video", "modelType")
		c.GraphicsTypes = enumValues(xml, "graphics", "type")
		c.DiskBuses = enumValues(xml, "disk", "bus")
		// libvirt names this enum "diskDevice" (not "device").
		c.DiskDevices = enumValues(xml, "disk", "diskDevice")
		c.SoundModels = enumValues(xml, "sound", "model")
		c.Parsed = len(c.VideoModels) > 0 || len(c.GraphicsTypes) > 0
	}

	// CPU models are NOT in domcapabilities on this libvirt (there is no
	// <cpu> block); `virsh cpu-models <arch>` is the authoritative list.
	// CPU modes, by contrast, are a fixed libvirt concept and do not need
	// probing — see curatedCPUModes.
	if out, cerr := exec.CommandContext(ctx, "virsh", "cpu-models", "x86_64").Output(); cerr == nil {
		c.CPUModels = parseLines(string(out))
	}
	c.CPUModes = append([]string(nil), curatedCPUModes...)

	// QEMU version, NIC enumeration and CPUID flag names all come from the
	// binary directly, so a libvirt permission problem does not blind us
	// to them.
	if qr := probeQEMU(ctx); qr.err == nil {
		c.QEMUVersion = qr.version
		c.NetworkModels = nicModels(qr.devices)
		c.CPUFlags = cpuFlags(qr.cpuHelp)
	}

	// Normalize sound model names: libvirt reports the controller
	// ("ich9", "ac97", "es1370", "usb") which is what the XML wants.
	c.SoundModels = normalizeSound(c.SoundModels)

	c.GPURenderNodes = renderNodes()
	c.HasGPU = len(c.GPURenderNodes) > 0

	if !c.Parsed {
		c.VideoModels = append([]string(nil), curatedVideoModels...)
		c.GraphicsTypes = append([]string(nil), curatedGraphicsTypes...)
		c.DiskBuses = append([]string(nil), curatedDiskBuses...)
		c.DiskDevices = []string{"disk", "cdrom", "floppy", "lun"}
	}
	if len(c.CPUModels) == 0 {
		c.CPUModels = nil // unknown: callers treat an empty list as "any"
	}
	if len(c.SoundModels) == 0 {
		c.SoundModels = append([]string(nil), curatedSoundModels...)
	}
	if len(c.NetworkModels) == 0 {
		c.NetworkModels = append([]string(nil), curatedNetworkModels...)
	}

	c.SPICESupported = containsFold(c.GraphicsTypes, "spice")

	// libguestfs inspection tools are optional; their absence is normal
	// and never degrades anything else. Both virt-inspector and guestfish
	// are needed for the full deep-probe path.
	if p, lerr := exec.LookPath("virt-inspector"); lerr == nil {
		if _, gerr := exec.LookPath("guestfish"); gerr == nil {
			c.GuestFS = true
			c.GuestFSBin = p
		}
	}

	return c
}

// qemuProbeResult bundles the raw text of the QEMU probes we need. A
// single struct return (rather than three-plus named returns) keeps
// probeQEMU's signature stable as more probes get added.
type qemuProbeResult struct {
	devices string
	cpuHelp string
	version string
	err     error
}

// probeQEMU runs `-device help`, `-cpu help` and `--version` against the
// local QEMU binary. The binary name is resolved via PATH; arch-prefixed
// names are tried after the plain one so cross-arch hosts still work.
// renderNodes lists the host's DRM render nodes (/dev/dri/renderD*).
//
// The render node is the right thing to look for. /dev/dri/card* is the
// privileged display device, while renderD* is the one that grants
// offscreen acceleration — the interface a container uses to transcode,
// and the one an Incus "gpu" device makes usable. A host with a card but
// no render node (some server VGA chips) cannot accelerate anything, so
// advertising a GPU there would be a promise that fails at runtime.
func renderNodes() []string {
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return nil
	}
	var nodes []string
	for _, e := range entries {
		if name := e.Name(); strings.HasPrefix(name, "renderD") {
			nodes = append(nodes, "/dev/dri/"+name)
		}
	}
	sort.Strings(nodes)
	return nodes
}

func probeQEMU(ctx context.Context) qemuProbeResult {
	bin, err := exec.LookPath("qemu-system-x86_64")
	if err != nil {
		bin, err = exec.LookPath("qemu-kvm")
		if err != nil {
			return qemuProbeResult{err: err}
		}
	}
	var r qemuProbeResult
	if out, verr := exec.CommandContext(ctx, bin, "--version").Output(); verr == nil {
		r.version = parseQEMUVersion(string(out))
	}
	devOut, derr := exec.CommandContext(ctx, bin, "-device", "help").Output()
	if derr != nil {
		r.err = derr
		return r
	}
	r.devices = string(devOut)
	// -cpu help is best-effort: a failure here should not blind us to
	// devices/NICs, which is why it does not set r.err.
	if cpuOut, cerr := exec.CommandContext(ctx, bin, "-cpu", "help").Output(); cerr == nil {
		r.cpuHelp = string(cpuOut)
	}
	return r
}

var qemuVersionRE = regexp.MustCompile(`version ([0-9][0-9.]*)`)

func parseQEMUVersion(s string) string {
	if m := qemuVersionRE.FindStringSubmatch(s); len(m) == 2 {
		return m[1]
	}
	return ""
}

// nicModels extracts NIC device names from `qemu -device help` output.
// It only keeps the families WebKVM exposes, and normalizes the
// virtio-net variants to the bare "virtio" the UI uses.
func nicModels(devices string) []string {
	known := map[string]string{
		"virtio-net-pci": "virtio",
		"e1000":          "e1000",
		"e1000e":         "e1000e",
		"rtl8139":        "rtl8139",
		"pcnet":          "pcnet",
		"vmxnet3":        "vmxnet3",
	}
	found := map[string]bool{}
	for _, line := range strings.Split(devices, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `name "`) {
			continue
		}
		end := strings.Index(line[6:], `"`)
		if end < 0 {
			continue
		}
		name := line[6 : 6+end]
		if norm, ok := known[name]; ok {
			found[norm] = true
		}
	}
	return sortedKeys(found)
}

// cpuFlags extracts CPUID feature names from `qemu -cpu help` output.
// The relevant section looks like:
//
//	Recognized CPUID flags:
//	  3dnow 3dnowext 3dnowprefetch abm ace2 ace2-en acpi adx aes ...
//	  ...
//
// i.e. a header line followed by whitespace-wrapped flag names running
// to the end of output. Everything before the header (the CPU model
// list) is ignored.
func cpuFlags(help string) []string {
	const header = "Recognized CPUID flags:"
	i := strings.Index(help, header)
	if i < 0 {
		return nil
	}
	body := help[i+len(header):]
	fields := strings.Fields(body)
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// normalizeSound maps libvirt's sound controller names to the values
// the domain XML expects. libvirt lists "ich9" but the qemu argument
// family also includes usb-audio, which we surface as "usb".
func normalizeSound(in []string) []string {
	found := map[string]bool{}
	for _, s := range in {
		switch strings.ToLower(s) {
		case "ich9", "ich6":
			found["ich9"] = true
		case "ac97":
			found["ac97"] = true
		case "es1370":
			found["es1370"] = true
		case "usb":
			found["usb"] = true
		}
	}
	return sortedKeys(found)
}

var enumCache sync.Map // map[string][]string: "parent\x00enum" -> values

// enumValues extracts the <value> children of the first
// <enum name='enumName'> that appears inside a <parent ...> block.
// Matching by parent keeps us from picking up the wrong enum when the
// same name (e.g. "type") repeats across devices.
func enumValues(doc, parent, enumName string) []string {
	key := parent + "\x00" + enumName
	if v, ok := enumCache.Load(key); ok {
		// Cache is per-process and keyed by content-independent name, so
		// only serve it when it came from the same document. To keep this
		// simple and correct we skip the cache when the doc differs in
		// length; probe() is itself cached, so this path is cold anyway.
		if vs, ok := v.(enumValuesEntry); ok && vs.docLen == len(doc) {
			return vs.values
		}
	}

	block := parentBlock(doc, parent)
	if block == "" {
		return nil
	}
	// (?s): the <value> children are newline-separated in real output.
	re := regexp.MustCompile(`(?s)<enum name='` + regexp.QuoteMeta(enumName) + `'>(.*?)</enum>`)
	m := re.FindStringSubmatch(block)
	if len(m) < 2 {
		return nil
	}
	vals := valueRE.FindAllStringSubmatch(m[1], -1)
	out := make([]string, 0, len(vals))
	seen := map[string]bool{}
	for _, v := range vals {
		val := strings.TrimSpace(v[1])
		if val == "" || seen[val] {
			continue
		}
		seen[val] = true
		out = append(out, val)
	}
	sort.Strings(out)
	enumCache.Store(key, enumValuesEntry{docLen: len(doc), values: out})
	return out
}

type enumValuesEntry struct {
	docLen int
	values []string
}

var valueRE = regexp.MustCompile(`<value>([^<]*)</value>`)

// parentBlock returns the text of the first <parent ...>...</parent>
// element. domcapabilities nests <disk>, <video>, etc. directly under
// <devices>; the non-greedy match stops at the first closing tag of the
// same name, which is correct because these are non-recursive.
//
// The opening tag is validated so that a longer sibling such as
// <videoX> does not satisfy a search for <video>: on a bad prefix the
// scan resumes past it rather than giving up.
func parentBlock(doc, parent string) string {
	open := "<" + parent
	closeTag := "</" + parent + ">"
	searchFrom := 0
	for {
		rel := strings.Index(doc[searchFrom:], open)
		if rel < 0 {
			return ""
		}
		i := searchFrom + rel
		// Ensure the match is the element itself, not e.g. <videoX>.
		if j := i + len(open); j < len(doc) {
			ch := doc[j]
			if ch != ' ' && ch != '>' && ch != '\t' && ch != '\n' && ch != '/' {
				searchFrom = i + len(open)
				continue
			}
		}
		j := strings.Index(doc[i:], closeTag)
		if j < 0 {
			return ""
		}
		return doc[i : i+j+len(closeTag)]
	}
}

func containsFold(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.EqualFold(h, needle) {
			return true
		}
	}
	return false
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// parseLines splits command output into trimmed, non-empty, de-duplicated
// lines, preserving order. Used for `virsh cpu-models`, which prints one
// model name per line.
func parseLines(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}
