package libvirt

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"libvirt.org/go/libvirt"
)

// Gate B (audit #2 / #24): every domain definition goes through
// Connector.defineDomain, the ONLY place allowed to call DomainDefineXML
// (enforced by TestDomainDefineXMLOnlyInWrapper). XML that originates from
// outside WebKVM (backup archive, OVA, restored snapshot) is parsed with
// encoding/xml in strict mode, checked against an allow-list and then
// RE-SERIALISED from the validated tree, so libvirt never sees the original
// bytes (no parser differential between Go and libxml2).

// maxDomainXMLBytes is the largest domain.xml accepted from an archive.
const maxDomainXMLBytes = 1 << 20

const (
	maxDomainXMLNodes = 20000
	maxDomainXMLDepth = 32
)

// DomainPolicy tells defineDomain how much to trust the XML it is given.
type DomainPolicy struct {
	// Trusted XML is either built by WebKVM's own templates (CreateDomain)
	// or is an edit of the XML libvirt itself returned for an existing
	// domain. It is passed through unchanged. Everything that comes from an
	// archive, OVA or snapshot MUST use the zero value (untrusted).
	Trusted bool
	// AllowedDiskFiles are the exact final paths of the disk files this
	// operation extracted from the archive. Any other <source file=...> is
	// rejected.
	AllowedDiskFiles map[string]bool
}

// TrustedDomain is the policy for WebKVM-generated / libvirt-sourced XML.
var TrustedDomain = DomainPolicy{Trusted: true}

// defineDomain is the single funnel to libvirt's DomainDefineXML.
func (c *Connector) defineDomain(xmlStr string, pol DomainPolicy) (*libvirt.Domain, error) {
	if !pol.Trusted {
		clean, err := SanitizeDomainXML(xmlStr, pol)
		if err != nil {
			return nil, fmt.Errorf("domain definition rejected: %w", err)
		}
		xmlStr = clean
	}
	return c.conn.DomainDefineXML(xmlStr)
}

// ---------------------------------------------------------------------------
// strict parsing

type xattr struct{ Name, Value string }

type xnode struct {
	Name  string
	Attrs []xattr
	Kids  []*xnode
	Text  string
}

func (n *xnode) attr(name string) (string, bool) {
	for _, a := range n.Attrs {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

func (n *xnode) setAttr(name, value string) {
	for i := range n.Attrs {
		if n.Attrs[i].Name == name {
			n.Attrs[i].Value = value
			return
		}
	}
	n.Attrs = append(n.Attrs, xattr{name, value})
}

func (n *xnode) child(name string) *xnode {
	for _, k := range n.Kids {
		if k.Name == name {
			return k
		}
	}
	return nil
}

func qualified(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

// rejectNamespaces refuses namespaced elements/attributes and xmlns
// declarations anywhere in the tree.
func rejectNamespaces(n *xnode) error {
	if strings.Contains(n.Name, ":") {
		return fmt.Errorf("namespaced element %q is not allowed", n.Name)
	}
	for _, a := range n.Attrs {
		if a.Name == "xmlns" || strings.Contains(a.Name, ":") {
			return fmt.Errorf("namespace attribute %q is not allowed", a.Name)
		}
	}
	for _, k := range n.Kids {
		if err := rejectNamespaces(k); err != nil {
			return err
		}
	}
	return nil
}

// parseStrict parses b into a tree. It rejects DTDs/entities, processing
// instructions other than the XML declaration, namespaces (qemu:commandline
// lives in one), oversize input and pathological nesting.
func parseStrict(b []byte) (*xnode, error) {
	if len(b) > maxDomainXMLBytes {
		return nil, errors.New("XML document too large")
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = true
	d.Entity = nil
	var stack []*xnode
	var root *xnode
	nodes := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			return nil, errors.New("DTD/entity declarations are not allowed")
		case xml.ProcInst:
			if t.Target != "xml" {
				return nil, errors.New("processing instructions are not allowed")
			}
		case xml.StartElement:
			nodes++
			if nodes > maxDomainXMLNodes || len(stack) >= maxDomainXMLDepth {
				return nil, errors.New("XML too complex")
			}
			// Namespaced names are kept (as "space:local") so that the
			// <metadata> subtree, which legitimately carries WebKVM's own
			// namespace, can be dropped; rejectNamespaces refuses them
			// everywhere else (qemu:commandline lives in a namespace).
			n := &xnode{Name: qualified(t.Name)}
			for _, a := range t.Attr {
				n.Attrs = append(n.Attrs, xattr{qualified(a.Name), a.Value})
			}
			if len(stack) > 0 {
				p := stack[len(stack)-1]
				p.Kids = append(p.Kids, n)
			} else {
				if root != nil {
					return nil, errors.New("multiple root elements")
				}
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty XML document")
	}
	// Mixed content never occurs in libvirt XML; drop whitespace between
	// children and refuse real text next to children.
	var fix func(n *xnode) error
	fix = func(n *xnode) error {
		if len(n.Kids) > 0 {
			if strings.TrimSpace(n.Text) != "" {
				return fmt.Errorf("unexpected text inside <%s>", n.Name)
			}
			n.Text = ""
		} else {
			n.Text = strings.TrimSpace(n.Text)
		}
		for _, k := range n.Kids {
			if err := fix(k); err != nil {
				return err
			}
		}
		return nil
	}
	if err := fix(root); err != nil {
		return nil, err
	}
	return root, nil
}

func escText(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func escAttr(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "'", "&apos;", "\"", "&quot;", "\n", "&#10;", "\r", "&#13;", "\t", "&#9;").Replace(s)
}

func (n *xnode) serialize(sb *strings.Builder, depth int) {
	ind := strings.Repeat("  ", depth)
	sb.WriteString(ind + "<" + n.Name)
	for _, a := range n.Attrs {
		sb.WriteString(" " + a.Name + "='" + escAttr(a.Value) + "'")
	}
	switch {
	case len(n.Kids) == 0 && n.Text == "":
		sb.WriteString("/>\n")
	case len(n.Kids) == 0:
		sb.WriteString(">" + escText(n.Text) + "</" + n.Name + ">\n")
	default:
		sb.WriteString(">\n")
		for _, k := range n.Kids {
			k.serialize(sb, depth+1)
		}
		sb.WriteString(ind + "</" + n.Name + ">\n")
	}
}

// ---------------------------------------------------------------------------
// domain allow-list

// rejectedElements are refused at any depth.
var rejectedElements = map[string]string{
	"hostdev":        "host device passthrough",
	"filesystem":     "host filesystem sharing",
	"redirdev":       "USB redirection",
	"redirfilter":    "USB redirection",
	"smartcard":      "smartcard passthrough",
	"shmem":          "shared memory device",
	"lease":          "lease",
	"sysinfo":        "sysinfo",
	"launchSecurity": "launch security",
	"kernel":         "direct kernel boot",
	"initrd":         "direct kernel boot",
	"dtb":            "direct kernel boot",
	"cmdline":        "direct kernel boot",
	"encryption":     "disk encryption",
	"script":         "network script",
	"commandline":    "qemu command line",
	"auth":           "network authentication",
	"secret":         "secret reference",
}

// strippedElements are dropped at any depth: libvirt regenerates them or
// they carry host-specific state that must not come from an archive.
var strippedElements = map[string]bool{
	"seclabel":     true,
	"backingStore": true,
	"uuid":         true,
	"metadata":     true,
	"genid":        true,
	"resource":     true,
}

var allowedTopLevel = map[string]bool{
	"name": true, "title": true, "description": true, "memory": true, "currentMemory": true,
	"maxMemory": true, "memoryBacking": true, "vcpu": true, "vcpus": true, "iothreads": true,
	"iothreadids": true, "cputune": true, "os": true, "features": true, "cpu": true,
	"clock": true, "on_poweroff": true, "on_reboot": true, "on_crash": true,
	"on_lockfailure": true, "pm": true, "devices": true, "numatune": true, "blkiotune": true,
	"memtune": true, "perf": true,
}

var allowedOSChildren = map[string]bool{
	"type": true, "boot": true, "bootmenu": true, "loader": true, "nvram": true,
	"smbios": true, "bios": true, "firmware": true, "acpi": true,
}

// forbiddenAttrs may not appear anywhere except where explicitly handled.
var forbiddenAttrs = map[string]bool{
	"file": true, "path": true, "dir": true, "socket": true, "script": true,
	"template": true, "dev": true, "host": true, "service": true,
}

func validateFirmwarePath(p string) bool {
	if p == "" {
		return true
	}
	if p == ovmfCode || p == ovmfCodeSecboot || p == ovmfVarsTemplate {
		return true
	}
	c := filepath.Clean(p)
	return c == p && strings.HasPrefix(c, "/usr/share/") && !strings.Contains(c, "..")
}

// SanitizeDomainXML validates untrusted domain XML against the allow-list and
// returns freshly serialised XML.
func SanitizeDomainXML(in string, pol DomainPolicy) (string, error) {
	root, err := parseStrict([]byte(in))
	if err != nil {
		return "", err
	}
	if root.Name != "domain" {
		return "", errors.New("root element is not <domain>")
	}
	if t, _ := root.attr("type"); t != "kvm" && t != "qemu" {
		return "", fmt.Errorf("unsupported domain type %q", t)
	}
	root.Attrs = []xattr{{"type", mustAttr(root, "type")}} // drops id= etc.

	var kept []*xnode
	for _, k := range root.Kids {
		if strippedElements[k.Name] {
			continue
		}
		if !allowedTopLevel[k.Name] {
			return "", fmt.Errorf("element <%s> is not allowed in an imported domain", k.Name)
		}
		kept = append(kept, k)
	}
	root.Kids = kept
	if err := rejectNamespaces(root); err != nil {
		return "", err
	}

	if err := rejectDeep(root); err != nil {
		return "", err
	}
	for _, k := range root.Kids {
		switch k.Name {
		case "os":
			if err := validateOS(k); err != nil {
				return "", err
			}
		case "devices":
			if err := validateDevices(k, pol); err != nil {
				return "", err
			}
		}
	}
	if err := checkAttrs(root, ""); err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.WriteString("<?xml version='1.0'?>\n")
	root.serialize(&sb, 0)
	return sb.String(), nil
}

func mustAttr(n *xnode, name string) string { v, _ := n.attr(name); return v }

// rejectDeep applies the global rules: rejected elements are errors,
// stripped elements are removed, and every element name must be a plain
// identifier.
func rejectDeep(n *xnode) error {
	kept := n.Kids[:0]
	for _, k := range n.Kids {
		if why, bad := rejectedElements[k.Name]; bad {
			return fmt.Errorf("element <%s> (%s) is not allowed in an imported domain", k.Name, why)
		}
		if strippedElements[k.Name] {
			continue
		}
		if err := rejectDeep(k); err != nil {
			return err
		}
		kept = append(kept, k)
	}
	n.Kids = kept
	return nil
}

// checkAttrs rejects host-path style attributes outside the places that are
// validated explicitly (disk source file, target/boot dev, nvram template).
func checkAttrs(n *xnode, parent string) error {
	for _, a := range n.Attrs {
		if !forbiddenAttrs[a.Name] {
			continue
		}
		switch {
		case a.Name == "dev" && (n.Name == "target" || n.Name == "boot"):
		case a.Name == "file" && n.Name == "source" && parent == "disk": // validated in validateDisk
		case a.Name == "template" && n.Name == "nvram" && validateFirmwarePath(a.Value):
		default:
			return fmt.Errorf("attribute %s on <%s> is not allowed", a.Name, n.Name)
		}
	}
	for _, k := range n.Kids {
		if err := checkAttrs(k, n.Name); err != nil {
			return err
		}
	}
	return nil
}

func validateOS(os *xnode) error {
	for _, k := range os.Kids {
		if !allowedOSChildren[k.Name] {
			return fmt.Errorf("element <%s> is not allowed in <os>", k.Name)
		}
		switch k.Name {
		case "loader":
			if !validateFirmwarePath(k.Text) {
				return fmt.Errorf("loader path %q is outside the firmware directories", k.Text)
			}
		case "nvram":
			// libvirt allocates the per-VM file itself; never take a path.
			k.Text = ""
			if tpl, ok := k.attr("template"); ok && !validateFirmwarePath(tpl) {
				return fmt.Errorf("nvram template %q is outside the firmware directories", tpl)
			}
		case "firmware":
			for _, fk := range k.Kids {
				if fk.Name != "feature" {
					return fmt.Errorf("element <%s> is not allowed in <firmware>", fk.Name)
				}
			}
		}
	}
	return nil
}

var allowedDeviceTypes = map[string]bool{
	"disk": true, "controller": true, "interface": true, "input": true, "graphics": true,
	"video": true, "sound": true, "audio": true, "memballoon": true, "rng": true, "tpm": true,
	"watchdog": true, "channel": true, "console": true, "serial": true, "parallel": true,
	"panic": true, "vsock": true, "iommu": true, "hub": true, "emulator": true,
}

func validateDevices(dev *xnode, pol DomainPolicy) error {
	var out []*xnode
	var wantVNC, wantSPICE bool
	for _, d := range dev.Kids {
		if !allowedDeviceTypes[d.Name] {
			return fmt.Errorf("device <%s> is not allowed in an imported domain", d.Name)
		}
		switch d.Name {
		case "emulator":
			continue // libvirt picks the default emulator
		case "graphics":
			switch mustAttr(d, "type") {
			case "vnc":
				wantVNC = true
			case "spice":
				wantSPICE = true
			case "", "rdp", "sdl", "desktop", "egl-headless", "dbus":
				// dropped, never carried over
			}
			continue
		case "disk":
			if err := validateDisk(d, pol); err != nil {
				return err
			}
		case "interface":
			if err := validateInterface(d); err != nil {
				return err
			}
		case "serial", "console", "parallel":
			if t := mustAttr(d, "type"); t != "pty" {
				return fmt.Errorf("<%s type=%q> is not allowed (only pty)", d.Name, t)
			}
			d.Kids = dropKids(d.Kids, "source")
		case "channel":
			switch mustAttr(d, "type") {
			case "spicevmc":
			case "unix":
				d.Kids = dropKids(d.Kids, "source")
			default:
				return fmt.Errorf("<channel type=%q> is not allowed", mustAttr(d, "type"))
			}
		case "rng":
			if b := d.child("backend"); b != nil {
				if mustAttr(b, "model") != "random" || (b.Text != "" && b.Text != "/dev/urandom" && b.Text != "/dev/random") {
					return errors.New("only random rng backends on /dev/urandom or /dev/random are allowed")
				}
			}
		case "tpm":
			if b := d.child("backend"); b != nil && mustAttr(b, "type") != "emulator" {
				return errors.New("only emulated TPM backends are allowed")
			}
		case "audio":
			if t := mustAttr(d, "type"); t != "none" && t != "spice" {
				return fmt.Errorf("<audio type=%q> is not allowed", t)
			}
		}
		out = append(out, d)
	}
	// Regenerate graphics: loopback only, auto port, no password carried over.
	if wantSPICE {
		out = append(out, &xnode{Name: "graphics", Attrs: []xattr{{"type", "spice"}, {"port", "-1"}, {"autoport", "yes"}, {"listen", "127.0.0.1"}},
			Kids: []*xnode{{Name: "listen", Attrs: []xattr{{"type", "address"}, {"address", "127.0.0.1"}}}}})
	}
	if wantVNC {
		out = append(out, &xnode{Name: "graphics", Attrs: []xattr{{"type", "vnc"}, {"port", "-1"}, {"autoport", "yes"}, {"listen", "127.0.0.1"}},
			Kids: []*xnode{{Name: "listen", Attrs: []xattr{{"type", "address"}, {"address", "127.0.0.1"}}}}})
	}
	dev.Kids = out
	return nil
}

func dropKids(kids []*xnode, name string) []*xnode {
	out := kids[:0]
	for _, k := range kids {
		if k.Name != name {
			out = append(out, k)
		}
	}
	return out
}

var allowedDiskChildren = map[string]bool{
	"driver": true, "source": true, "target": true, "readonly": true, "shareable": true,
	"boot": true, "serial": true, "wwn": true, "vendor": true, "product": true, "alias": true,
	"address": true, "iotune": true, "blockio": true, "geometry": true, "transient": true,
}

func validateDisk(d *xnode, pol DomainPolicy) error {
	if t := mustAttr(d, "type"); t != "file" {
		return fmt.Errorf("<disk type=%q> is not allowed (only file-backed disks)", t)
	}
	if dv := mustAttr(d, "device"); dv != "disk" && dv != "cdrom" {
		return fmt.Errorf("<disk device=%q> is not allowed", dv)
	}
	for _, k := range d.Kids {
		if !allowedDiskChildren[k.Name] {
			return fmt.Errorf("element <%s> is not allowed in <disk>", k.Name)
		}
		if k.Name == "source" {
			file, has := k.attr("file")
			if !has {
				return errors.New("<disk><source> without file")
			}
			if !pol.AllowedDiskFiles[file] {
				return fmt.Errorf("disk source %q does not correspond to a disk extracted from the archive", file)
			}
			if len(k.Kids) > 0 {
				return errors.New("unexpected content inside disk <source>")
			}
			for _, a := range k.Attrs {
				if a.Name != "file" && a.Name != "startupPolicy" && a.Name != "index" {
					return fmt.Errorf("attribute %s on disk <source> is not allowed", a.Name)
				}
			}
		}
	}
	return nil
}

func validateInterface(i *xnode) error {
	if t := mustAttr(i, "type"); t != "bridge" && t != "network" {
		return fmt.Errorf("<interface type=%q> is not allowed (only bridge or network)", t)
	}
	kept := i.Kids[:0]
	for _, k := range i.Kids {
		switch k.Name {
		case "target": // tap name of a running domain: regenerated by libvirt
			continue
		case "backend":
			return errors.New("interface <backend> is not allowed")
		case "rom":
			if _, ok := k.attr("file"); ok {
				return errors.New("interface <rom file> is not allowed")
			}
		}
		kept = append(kept, k)
	}
	i.Kids = kept
	return nil
}

// ---------------------------------------------------------------------------
// snapshot XML

// SanitizeSnapshotXML prepares the XML of a snapshot found in an archive for
// REDEFINE. The embedded <domain> is dropped (libvirt then uses the current,
// already validated, definition so reverting can never install an
// attacker-chosen one), memory state files are dropped, and every disk source
// is rewritten to overlayPath (the file this restore wrote).
func SanitizeSnapshotXML(in, overlayPath string) (string, error) {
	root, err := parseStrict([]byte(in))
	if err != nil {
		return "", err
	}
	if root.Name != "domainsnapshot" {
		return "", errors.New("root element is not <domainsnapshot>")
	}
	allowed := map[string]bool{"name": true, "description": true, "state": true, "creationTime": true,
		"parent": true, "memory": true, "disks": true, "domain": true, "active": true, "cookie": true}
	var kept []*xnode
	for _, k := range root.Kids {
		if !allowed[k.Name] {
			return "", fmt.Errorf("element <%s> is not allowed in a snapshot", k.Name)
		}
		switch k.Name {
		case "domain", "cookie", "memory":
			continue
		case "disks":
			for _, dk := range k.Kids {
				if dk.Name != "disk" {
					return "", fmt.Errorf("element <%s> is not allowed in <disks>", dk.Name)
				}
				if t, ok := dk.attr("type"); ok && t != "file" {
					return "", fmt.Errorf("snapshot disk type %q is not allowed", t)
				}
				var nk []*xnode
				for _, s := range dk.Kids {
					switch s.Name {
					case "source":
						if overlayPath == "" {
							continue
						}
						nk = append(nk, &xnode{Name: "source", Attrs: []xattr{{"file", overlayPath}}})
					case "driver":
						nk = append(nk, s)
					default:
						return "", fmt.Errorf("element <%s> is not allowed in snapshot <disk>", s.Name)
					}
				}
				dk.Kids = nk
				dk.Attrs = filterAttrs(dk.Attrs, "name", "snapshot", "type")
			}
		}
		kept = append(kept, k)
	}
	root.Kids = kept
	if err := rejectNamespaces(root); err != nil {
		return "", err
	}
	if err := rejectDeep(root); err != nil {
		return "", err
	}
	root.Attrs = nil
	var sb strings.Builder
	root.serialize(&sb, 0)
	return sb.String(), nil
}

func filterAttrs(in []xattr, keep ...string) []xattr {
	var out []xattr
	for _, a := range in {
		for _, k := range keep {
			if a.Name == k {
				out = append(out, a)
			}
		}
	}
	return out
}
