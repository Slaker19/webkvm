package ha

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"webkvm/internal/sysutil"
)

// Released VMs (8.19/8.20): turning HA off with mode stop/forget removes the
// Pacemaker stack of a KVM workload. VirtualDomain ran the VM as a transient
// domain whose definition lives on the replicated filesystem, and the DRBD
// resource agent brings the resource down, so the VM vanishes from libvirt.
// Before the stack is removed WebKVM records what it needs to bring the VM
// back (an inventory entry "released") and offers an explicit Restore.

// Typed errors of the release/restore flow.
const (
	CodeReleaseCaptureFailed = "release_capture_failed"
	CodeReleaseStoreFailed   = "release_store_failed"
	CodeReleaseNotFound      = "release_not_found"
	CodeReleaseUnrestorable  = "release_unrestorable"
	CodeReleaseXMLInvalid    = "release_xml_invalid"
	CodeRestoreNodeNotLocal  = "restore_node_not_local"
	CodeWorkloadManaged      = "workload_managed"
	CodeDRBDManaged          = "drbd_managed"
	CodeDomainConflict       = "domain_conflict"
	CodeMountConflict        = "mount_conflict"
	CodeDRBDPrimaryElsewhere = "drbd_primary_elsewhere"
	CodeDRBDNotUpToDate      = "drbd_not_uptodate"
	CodeDRBDUpFailed         = "drbd_up_failed"
	CodeDRBDPromoteFailed    = "drbd_promote_failed"
	CodeMountFailed          = "mount_failed"
	CodeDefineFailed         = "define_failed"
	CodeStartFailed          = "start_failed"
	CodeDRBDPeersDown        = "drbd_peers_down"
	CodeMountNotVisible      = "mount_not_visible"
	CodeXMLLinkFailed        = "xml_link_failed"
	ReleaseStatusReleased    = "released"
	ReleaseStatusRestored    = "restored"
	maxReleaseXMLBytes       = 1 << 20
	releasedStoreFile        = "ha-released-vms.json"
	releasedStoreSchema      = 1
)

// Sources of a captured domain XML.
const (
	ReleaseSourceConfigFile   = "config_file"
	ReleaseSourceLibvirtLocal = "libvirt_local"
	ReleaseSourceLibvirtPeer  = "libvirt_peer"
	ReleaseSourcePrevious     = "previous_capture"
	ReleaseSourceSaved        = "saved"
)

// KVMStack is the storage side of a KVM workload as configured in the CIB.
type KVMStack struct {
	DRBDResource string `json:"drbd_resource,omitempty"`
	Device       string `json:"device,omitempty"`
	MountDir     string `json:"mount_dir,omitempty"`
	FSType       string `json:"fstype,omitempty"`
	ConfigPath   string `json:"config_path,omitempty"`
}

// kvmStackOf extracts the KVM stack of the workload named name from the CIB.
func kvmStackOf(root xmlElement, name string) (KVMStack, bool) {
	prims := cibPrimitives(root)
	for _, p := range prims {
		if !p.isVirtualDomain() {
			continue
		}
		if p.Meta[MetaKind] != KindKVM || p.Meta[MetaName] != name {
			if p.Meta[MetaKind] != "" || p.Params["config"] == "" || domainNameReader(p.Params["config"]) != name {
				continue
			}
		}
		st := KVMStack{ConfigPath: p.Params["config"]}
		for _, o := range prims {
			switch {
			case p.Group != "" && o.Group == p.Group && o.Type == "Filesystem":
				st.Device, st.MountDir, st.FSType = o.Params["device"], o.Params["directory"], o.Params["fstype"]
			case o.ID == p.ID+"-data" && o.Type == "drbd":
				st.DRBDResource = o.Params["drbd_resource"]
			}
		}
		return st, true
	}
	return KVMStack{}, false
}

// ReadKVMStack reads the CIB and returns the storage stack of a KVM workload.
func ReadKVMStack(ctx context.Context, runner sysutil.CommandRunner, name string) (KVMStack, error) {
	out, err := runReadOnly(ctx, runner, "cluster", "cib")
	if err != nil {
		return KVMStack{}, fmt.Errorf("%w: %v", ErrPacemakerUnavailable, err)
	}
	var root xmlElement
	if err := xml.Unmarshal(out, &root); err != nil {
		return KVMStack{}, fmt.Errorf("parse CIB: %w", err)
	}
	st, ok := kvmStackOf(root, name)
	if !ok {
		return KVMStack{}, opErr(CodeNotFound, "kvm:%s is not in the CIB", name)
	}
	return st, nil
}

// RestoreConflicts reads the CIB and refuses a restore while Pacemaker still
// manages the workload or any resource of the cluster uses its DRBD
// resource: promoting or mounting it outside Pacemaker would race the agent.
func RestoreConflicts(ctx context.Context, runner sysutil.CommandRunner, name, drbdResource, cpGroup string) error {
	cib, status, err := readCIBAndStatus(ctx, runner)
	if err != nil {
		return opErr(CodePCSFailed, "cannot read the CIB: %v", err)
	}
	list, err := ParseWorkloads(cib, status, cpGroup)
	if err != nil {
		return opErr(CodePCSFailed, "%v", err)
	}
	if len(FindWorkload(list, WorkloadKey{Kind: KindKVM, Name: name})) > 0 {
		return opErr(CodeWorkloadManaged, "kvm:%s is managed by Pacemaker again; nothing was changed", name)
	}
	if drbdResource == "" {
		return nil
	}
	var root xmlElement
	if err := xml.Unmarshal(cib, &root); err != nil {
		return opErr(CodePCSFailed, "parse CIB: %v", err)
	}
	for _, p := range cibPrimitives(root) {
		if p.Params["drbd_resource"] == drbdResource {
			return opErr(CodeDRBDManaged, "DRBD resource %s is used by Pacemaker resource %s; nothing was changed", drbdResource, p.ID)
		}
	}
	return nil
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)

// ParseDomainIdentity validates a libvirt domain XML: a single <domain> root
// with a valid <name> and, when present, a valid <uuid>.
func ParseDomainIdentity(raw []byte) (name, uuid string, err error) {
	if len(raw) == 0 {
		return "", "", errors.New("empty domain XML")
	}
	if len(raw) > maxReleaseXMLBytes {
		return "", "", errors.New("domain XML too large")
	}
	var d struct {
		XMLName xml.Name
		Name    string `xml:"name"`
		UUID    string `xml:"uuid"`
	}
	dec := xml.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&d); err != nil {
		return "", "", fmt.Errorf("parse domain XML: %w", err)
	}
	if d.XMLName.Local != "domain" {
		return "", "", errors.New("root element is not <domain>")
	}
	name, uuid = strings.TrimSpace(d.Name), strings.ToLower(strings.TrimSpace(d.UUID))
	if !WorkloadNameRE.MatchString(name) {
		return "", "", errors.New("invalid domain name")
	}
	if uuid != "" && !uuidRE.MatchString(uuid) {
		return "", "", errors.New("invalid domain uuid")
	}
	return name, uuid, nil
}

// CheckDomainXML validates raw against the expected identity. An empty
// wantUUID accepts any (valid) uuid.
func CheckDomainXML(raw []byte, wantName, wantUUID string) (uuid string, err error) {
	name, uuid, err := ParseDomainIdentity(raw)
	if err != nil {
		return "", opErr(CodeReleaseXMLInvalid, "%v", err)
	}
	if name != wantName {
		return "", opErr(CodeReleaseXMLInvalid, "domain XML names %q, expected %q", name, wantName)
	}
	if wantUUID != "" && !strings.EqualFold(normUUID(uuid), normUUID(wantUUID)) {
		return "", opErr(CodeReleaseXMLInvalid, "domain XML uuid does not match the saved uuid")
	}
	return uuid, nil
}

func normUUID(u string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(u), "-", ""))
}

// ValidReleasePath reports whether p is an absolute, clean path without
// traversal (paths come from the CIB and are passed to mount/read).
func ValidReleasePath(p string) bool {
	return p != "" && absPathRE.MatchString(p) && !strings.Contains(p, "..") && filepath.Clean(p) == p
}

// ReleasedVM is one inventory entry. XML may carry graphics passwords: the
// store is 0600 and the API never returns it.
type ReleasedVM struct {
	Name          string   `json:"name"`
	UUID          string   `json:"uuid,omitempty"`
	Status        string   `json:"status"`
	Mode          string   `json:"mode"`
	Stack         KVMStack `json:"stack"`
	LastNode      string   `json:"last_node,omitempty"`
	PreferredNode string   `json:"preferred_node,omitempty"`
	AllowedNodes  []string `json:"allowed_nodes,omitempty"`
	// DRBDPeers are the other nodes holding a replica of the resource, as
	// seen at capture time (read-only `drbdsetup status`). Restore needs
	// them up (`drbdadm up <res>`) when the resource uses quorum.
	DRBDPeers    []string   `json:"drbd_peers,omitempty"`
	XML          string     `json:"xml,omitempty"`
	XMLSource    string     `json:"xml_source,omitempty"`
	XMLSHA256    string     `json:"xml_sha256,omitempty"`
	Unrestorable bool       `json:"unrestorable,omitempty"`
	ReleasedAt   time.Time  `json:"released_at"`
	ReleasedBy   string     `json:"released_by,omitempty"`
	RestoredAt   *time.Time `json:"restored_at,omitempty"`
	RestoredNode string     `json:"restored_node,omitempty"`
	RestoredBy   string     `json:"restored_by,omitempty"`
}

// ReleasedVMView is the API shape (no XML).
type ReleasedVMView struct {
	Key           string     `json:"key"`
	Name          string     `json:"name"`
	UUID          string     `json:"uuid,omitempty"`
	Status        string     `json:"status"`
	Mode          string     `json:"mode"`
	DRBDResource  string     `json:"drbd_resource,omitempty"`
	MountDir      string     `json:"mount_dir,omitempty"`
	ConfigPath    string     `json:"config_path,omitempty"`
	LastNode      string     `json:"last_node,omitempty"`
	PreferredNode string     `json:"preferred_node,omitempty"`
	AllowedNodes  []string   `json:"allowed_nodes"`
	DRBDPeers     []string   `json:"drbd_peers"`
	HasXML        bool       `json:"has_xml"`
	XMLSource     string     `json:"xml_source,omitempty"`
	XMLSHA256     string     `json:"xml_sha256,omitempty"`
	Restorable    bool       `json:"restorable"`
	ReleasedAt    time.Time  `json:"released_at"`
	ReleasedBy    string     `json:"released_by,omitempty"`
	RestoredAt    *time.Time `json:"restored_at,omitempty"`
	RestoredNode  string     `json:"restored_node,omitempty"`
}

// View strips the XML.
func (e ReleasedVM) View() ReleasedVMView {
	allowed := e.AllowedNodes
	if allowed == nil {
		allowed = []string{}
	}
	peers := e.DRBDPeers
	if peers == nil {
		peers = []string{}
	}
	return ReleasedVMView{
		Key: WorkloadKey{Kind: KindKVM, Name: e.Name}.String(), Name: e.Name, UUID: e.UUID, Status: e.Status, Mode: e.Mode,
		DRBDResource: e.Stack.DRBDResource, MountDir: e.Stack.MountDir, ConfigPath: e.Stack.ConfigPath,
		LastNode: e.LastNode, PreferredNode: e.PreferredNode, AllowedNodes: allowed, DRBDPeers: peers,
		HasXML: e.XML != "", XMLSource: e.XMLSource, XMLSHA256: e.XMLSHA256,
		Restorable: e.XML != "" && !e.Unrestorable,
		ReleasedAt: e.ReleasedAt, ReleasedBy: e.ReleasedBy, RestoredAt: e.RestoredAt, RestoredNode: e.RestoredNode,
	}
}

// XMLDigest is the sha256 of a domain XML (for display, never the XML).
func XMLDigest(raw string) string {
	if raw == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// ReleasedStore persists the inventory as one JSON file under DATA_DIR,
// written atomically (temp file, fsync, rename, fsync dir) with mode 0600.
// Entries are only removed by an explicit Discard.
type ReleasedStore struct {
	Dir string
}

var releasedStoreMu sync.Mutex

type releasedFile struct {
	Schema  int                   `json:"schema"`
	Entries map[string]ReleasedVM `json:"entries"`
}

func (s ReleasedStore) path() (string, error) {
	if strings.TrimSpace(s.Dir) == "" || !filepath.IsAbs(s.Dir) {
		return "", errors.New("data dir is not configured")
	}
	return filepath.Join(s.Dir, releasedStoreFile), nil
}

func (s ReleasedStore) load() (releasedFile, error) {
	f := releasedFile{Schema: releasedStoreSchema, Entries: map[string]ReleasedVM{}}
	p, err := s.path()
	if err != nil {
		return f, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return f, fmt.Errorf("parse %s: %w", releasedStoreFile, err)
	}
	if f.Entries == nil {
		f.Entries = map[string]ReleasedVM{}
	}
	return f, nil
}

func (s ReleasedStore) save(f releasedFile) error {
	p, err := s.path()
	if err != nil {
		return err
	}
	f.Schema = releasedStoreSchema
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".ha-released-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, p); err != nil {
		return err
	}
	ok = true
	if d, err := os.Open(s.Dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// List returns every entry sorted by name.
func (s ReleasedStore) List() ([]ReleasedVM, error) {
	releasedStoreMu.Lock()
	defer releasedStoreMu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]ReleasedVM, 0, len(f.Entries))
	for _, e := range f.Entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns the entry of a VM name.
func (s ReleasedStore) Get(name string) (ReleasedVM, bool, error) {
	releasedStoreMu.Lock()
	defer releasedStoreMu.Unlock()
	f, err := s.load()
	if err != nil {
		return ReleasedVM{}, false, err
	}
	e, ok := f.Entries[name]
	return e, ok, nil
}

// Put stores (replaces) the entry of e.Name and returns the previous one.
func (s ReleasedStore) Put(e ReleasedVM) (prev *ReleasedVM, err error) {
	if !WorkloadNameRE.MatchString(e.Name) {
		return nil, errors.New("invalid VM name")
	}
	releasedStoreMu.Lock()
	defer releasedStoreMu.Unlock()
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	if old, ok := f.Entries[e.Name]; ok {
		prev = &old
	}
	f.Entries[e.Name] = e
	return prev, s.save(f)
}

// Revert puts prev back for name (nil removes the entry). It only undoes a
// Put of the same request whose operation changed nothing.
func (s ReleasedStore) Revert(name string, prev *ReleasedVM) error {
	releasedStoreMu.Lock()
	defer releasedStoreMu.Unlock()
	f, err := s.load()
	if err != nil {
		return err
	}
	if prev == nil {
		delete(f.Entries, name)
	} else {
		f.Entries[name] = *prev
	}
	return s.save(f)
}

// Delete removes an entry (explicit Discard only). found is false when there
// was nothing to remove.
func (s ReleasedStore) Delete(name string) (found bool, err error) {
	releasedStoreMu.Lock()
	defer releasedStoreMu.Unlock()
	f, err := s.load()
	if err != nil {
		return false, err
	}
	if _, ok := f.Entries[name]; !ok {
		return false, nil
	}
	delete(f.Entries, name)
	return true, s.save(f)
}
