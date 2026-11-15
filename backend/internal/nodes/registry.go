// Package nodes manages a registry of libvirt "nodes" the backend
// can talk to. Each node has a libvirt URI; the local node is the
// one created from the LIBVIRT_URI env var at startup. Remote nodes
// are added via the API (or a future UI) and may use any libvirt
// URI the host can reach (qemu:///system, qemu+ssh://user@host/system,
// qemu+tcp://host:16509/system, https://host:8080, etc.).
package nodes

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// NodeType categorizes a node. Only "local" and "ssh" are first-class;
// "tcp" and others fall under "remote".
type NodeType string

const (
	NodeTypeLocal  NodeType = "local"
	NodeTypeRemote NodeType = "remote"
)

// Backend identifies the API used to manage instances on a node.
type Backend string

const (
	BackendLibvirt Backend = "libvirt"
	BackendIncus   Backend = "incus"
)

var nodeProjectRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,63}$`)
var nodeNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,63}$`)
var nodeGeneratedIDRE = regexp.MustCompile(`^n_[0-9a-f]{16}$`)
var nodeCredentialIDRE = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

// Sentinel errors so API handlers can map failures to HTTP statuses.
var (
	ErrNotFound = errors.New("node not found")
	ErrConflict = errors.New("node conflict")
	ErrInvalid  = errors.New("invalid node")
)

// Error codes carried by ValidationError.
const (
	CodeInvalidName  = "invalid_name"
	CodeReservedName = "reserved_name"
	CodeInvalidURI   = "invalid_uri"
	CodeInvalidUIURL = "invalid_ui_url"
)

// ValidationError is an input error with a stable machine-readable code.
// It matches ErrInvalid with errors.Is.
type ValidationError struct {
	Code string
	Msg  string
}

func (e *ValidationError) Error() string { return e.Msg }
func (e *ValidationError) Unwrap() error { return ErrInvalid }

type conflictError struct{ msg string }

func (e *conflictError) Error() string { return e.msg }
func (e *conflictError) Unwrap() error { return ErrConflict }

func invalidf(code, format string, a ...any) error {
	return &ValidationError{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// ValidateNodeName enforces the naming rules for user-supplied node names:
// 1..63 characters of letters, digits, '.', '_' and '-', and never "local",
// the empty name or an auto-generated id (n_ + 16 hex digits), because names
// and ids are interchangeable selectors in the API. Names already stored in
// nodes.json are not re-validated on load.
func ValidateNodeName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return invalidf(CodeInvalidName, "name is required")
	}
	if strings.EqualFold(name, "local") || nodeGeneratedIDRE.MatchString(name) {
		return invalidf(CodeReservedName, "node name %q is reserved", name)
	}
	if !nodeNameRE.MatchString(name) {
		return invalidf(CodeInvalidName, "node name must be 1-63 characters: letters, digits, '.', '_' or '-'")
	}
	return nil
}

// Default bounds for slow probes. Variables so tests can shorten them.
const (
	defaultProberTimeout  = 8 * time.Second
	defaultProbeDeadline  = 10 * time.Second
	probeTimeoutLastError = "probe timed out"
)

// Node is a registered libvirt or WebKVM host.
type Node struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URI  string `json:"uri"`
	// UIURL is the operator-facing address of the node's own WebKVM on the
	// real (reachable) network, as scheme://host[:port]. Optional: the URI
	// host is often a private cluster address the operator cannot reach.
	UIURL             string    `json:"ui_url,omitempty"`
	Backend           Backend   `json:"backend,omitempty"`
	Project           string    `json:"project,omitempty"`
	CredentialID      string    `json:"credential_id,omitempty"`
	ServerFingerprint string    `json:"server_fingerprint,omitempty"`
	Type              NodeType  `json:"type"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`

	// Dynamic health & telemetry fields (kept in memory, updated by prober)
	Status        string     `json:"status"` // "online", "offline", "degraded", "unknown"
	LatencyMs     int64      `json:"latency_ms"`
	LastSeen      *time.Time `json:"last_seen,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	Version       string     `json:"version,omitempty"`
	UptimeSec     int64      `json:"uptime_sec,omitempty"`
	DiskFree      int64      `json:"disk_free,omitempty"`
	DiskTotal     int64      `json:"disk_total,omitempty"`
	LibvirtStatus string     `json:"libvirt_status,omitempty"`
	// Access is the state of WebKVM's managed SSH access to a remote
	// libvirt node: AccessOK, AccessNotSetUp (the host may answer, but
	// WebKVM cannot log in yet; AccessCode says why) or "" (unknown).
	Access     string `json:"access,omitempty"`
	AccessCode string `json:"access_code,omitempty"`

	// IsSelf marks a remote entry that points at THIS machine (registered
	// before the control plane moved here). Computed on every read from the
	// local hostname/aliases/addresses, never trusted from disk. The API
	// resolves such an entry to the local node; it is never auto-deleted.
	IsSelf bool `json:"is_self,omitempty"`

	// Host capacity, read from libvirt's host API for placement decisions
	// (F3.5). Zero when unknown (probe skipped or hypervisor unreachable).
	CPUs     int   `json:"cpus,omitempty"`
	RAMFree  int64 `json:"ram_free,omitempty"`
	RAMTotal int64 `json:"ram_total,omitempty"`
}

// LibvirtStatusTCPReachable is libvirt_status after a bare TCP handshake to
// the SSH port: the port is open, nothing more (no login, no libvirt). It
// replaces the misleading "reachable" of older versions; the frontend still
// accepts "reachable" for data written by them.
const LibvirtStatusTCPReachable = "tcp_reachable"

// IsLocal reports whether this node is the local libvirt instance.
func (n *Node) IsLocal() bool { return n.Type == NodeTypeLocal }

// Driver preserves compatibility with registries written before typed backends.
func (n Node) Driver() Backend {
	if n.Backend == "" {
		return BackendLibvirt
	}
	return n.Backend
}

// ClusterSummary provides aggregated statistics across all fleet nodes.
type ClusterSummary struct {
	TotalNodes   int `json:"total_nodes"`
	OnlineNodes  int `json:"online_nodes"`
	OfflineNodes int `json:"offline_nodes"`
	// NoAccessNodes: enabled remote nodes that answer but whose SSH access
	// is not set up. They are in neither online nor offline.
	NoAccessNodes  int   `json:"no_access_nodes"`
	AvgLatencyMs   int64 `json:"avg_latency_ms"`
	TotalDiskFree  int64 `json:"total_disk_free"`
	TotalDiskTotal int64 `json:"total_disk_total"`
}

// LocalProberFunc returns dynamic health telemetry for the local node.
type LocalProberFunc func() (status, version string, uptime, diskFree, diskTotal int64, libvirtStatus string, err error)

// RemoteProberFunc returns host capacity (CPUs, free/total RAM in bytes) for
// a remote libvirt node, used for placement. May be nil (feature disabled).
type RemoteProberFunc func(uri string) (cpus int, ramFree, ramTotal int64, err error)
type IncusProberFunc func(node Node) (version string, err error)

// Registry is the in-memory + on-disk store of nodes. Safe for concurrent use.
type Registry struct {
	mu           sync.RWMutex
	path         string
	nodes        map[string]*Node
	localProber  LocalProberFunc
	remoteProber RemoteProberFunc
	incusProber  IncusProberFunc
	// localCapacity reads this host's CPUs/RAM (cheap local read). The
	// remote prober cannot serve the local node: it goes through the SSH
	// pool, which has no URI for qemu:///system.
	localCapacity LocalCapacityFunc
	credentials   *IncusCredentialStore
	// lastProbe throttles ProbeAllStale so a busy UI does not open one
	// SSH/libvirt connection per request.
	lastProbe map[string]time.Time
	// proberTimeout bounds each prober callback (libvirt has no connect
	// timeout); probeDeadline caps a whole ProbeAll/ProbeAllStale round.
	proberTimeout time.Duration
	probeDeadline time.Duration
	httpTimeout   time.Duration
}

// New loads (or creates) the registry at {dataDir}/nodes.json.
// The local node is auto-created from localURI if missing.
func New(dataDir, localURI string) (*Registry, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	r := &Registry{
		path:        filepath.Join(dataDir, "nodes.json"),
		nodes:       map[string]*Node{},
		credentials: NewIncusCredentialStore(dataDir),
		lastProbe:   map[string]time.Time{},

		proberTimeout: defaultProberTimeout,
		probeDeadline: defaultProbeDeadline,
		httpTimeout:   4 * time.Second,
	}
	if err := r.load(); err != nil {
		return nil, err
	}
	r.ensureLocal(localURI)
	if err := r.save(); err != nil {
		return nil, err
	}
	return r, nil
}

// SetLocalProber configures the callback that fetches local system status.
func (r *Registry) SetLocalProber(fn LocalProberFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.localProber = fn
}

// SetRemoteProber configures the callback that fetches remote host capacity.
func (r *Registry) SetRemoteProber(fn RemoteProberFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.remoteProber = fn
}

// SetIncusProber configures authenticated health checks for remote Incus nodes.
func (r *Registry) SetIncusProber(fn IncusProberFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.incusProber = fn
}

// IncusCredentials returns the protected credential store owned by this registry.
func (r *Registry) IncusCredentials() *IncusCredentialStore { return r.credentials }

// Path returns the on-disk location. Useful for logs.
func (r *Registry) Path() string { return r.path }

func (r *Registry) load() error {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*Node
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("parse %s: %w", r.path, err)
	}
	for _, n := range list {
		if n.Backend == "" {
			n.Backend = BackendLibvirt
		}
		if n.Status == "" {
			n.Status = "unknown"
		}
		n.IsSelf = false
		if n.IsLocal() {
			// The shared DATA_DIR follows the control plane: persisted
			// capacity of the local entry belongs to whichever host wrote
			// it last. It is re-read from this host instead.
			n.CPUs, n.RAMFree, n.RAMTotal = 0, 0, 0
		}
		r.nodes[n.ID] = n
	}
	return nil
}

func (r *Registry) save() error {
	list := make([]*Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func (r *Registry) ensureLocal(uri string) {
	name := localHostname()
	for _, n := range r.nodes {
		if n.IsLocal() {
			changed := false
			if n.URI != uri {
				n.URI = uri
				changed = true
			}
			// The local identity is the machine we are running on, never
			// the name persisted in the shared DATA_DIR: after a Pacemaker
			// failover nodes.json still carries the old node's hostname.
			if n.Name != name {
				n.Name = name
				changed = true
			}
			if n.Status == "" {
				n.Status = "online"
				changed = true
			}
			if !n.Enabled {
				n.Enabled = true
				changed = true
			}
			if changed {
				n.UpdatedAt = time.Now().UTC()
			}
			return
		}
	}
	r.nodes["local"] = &Node{
		ID:        "local",
		Name:      name,
		URI:       uri,
		Backend:   BackendLibvirt,
		Type:      NodeTypeLocal,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Status:    "online",
	}
}

// LocalHostname returns the machine's hostname so the local node is
// identifiable in a fleet instead of the generic "local". The registry and
// the VM list must use this, not the name persisted in nodes.json.
func LocalHostname() string { return localHostname() }

// localHostname returns the machine's hostname so the local node is
// identifiable in a fleet instead of the generic "local".
func localHostname() string {
	if h, err := os.Hostname(); err == nil {
		if h = strings.TrimSpace(h); h != "" {
			return h
		}
	}
	return "local"
}

// List returns every node, sorted by created_at.
func (r *Registry) List() []Node {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Node, 0, len(r.nodes))
	self := r.selfIdentityLocked()
	for _, n := range r.nodes {
		c := *n
		c.IsSelf = self.selfCandidate(n)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Get returns a copy of the node with the given ID.
func (r *Registry) Get(id string) (Node, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.nodes[id]
	if !ok {
		return Node{}, false
	}
	c := *n
	c.IsSelf = r.selfIdentityLocked().selfCandidate(n)
	return c, true
}

// CheckNewName validates a candidate name and reports ErrConflict when it is
// taken. Callers with side effects (one-time tokens) use it before acting.
func (r *Registry) CheckNewName(name string) error {
	name = strings.TrimSpace(name)
	if err := ValidateNodeName(name); err != nil {
		return err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, n := range r.nodes {
		if n.Name == name {
			return &conflictError{fmt.Sprintf("a node named %q already exists", name)}
		}
	}
	return nil
}

// Create adds a new remote node.
func (r *Registry) Create(name, uri string) (Node, error) {
	return r.CreateTyped(name, uri, BackendLibvirt, "", "", "")
}

// CreateTyped registers a remote libvirt or Incus endpoint.
func (r *Registry) CreateTyped(name, uri string, backend Backend, project, credentialID, serverFingerprint string) (Node, error) {
	return r.CreateWithUI(name, uri, backend, project, credentialID, serverFingerprint, "")
}

// CreateWithUI is CreateTyped with the optional operator-network address
// (validated and normalized by ValidateUIURL).
func (r *Registry) CreateWithUI(name, uri string, backend Backend, project, credentialID, serverFingerprint, uiURL string) (Node, error) {
	uiURL, err := ValidateUIURL(uiURL)
	if err != nil {
		return Node{}, err
	}
	name = strings.TrimSpace(name)
	if err := ValidateNodeName(name); err != nil {
		return Node{}, err
	}
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return Node{}, invalidf(CodeInvalidURI, "uri is required")
	}
	if backend == "" {
		backend = BackendLibvirt
	}
	if backend != BackendLibvirt && backend != BackendIncus {
		return Node{}, fmt.Errorf("unsupported node backend %q", backend)
	}
	if backend == BackendIncus {
		u, err := url.Parse(strings.TrimSpace(uri))
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return Node{}, errors.New("an Incus node requires an https endpoint without credentials or a path")
		}
		if project == "" {
			project = "default"
		}
		if !nodeProjectRE.MatchString(project) {
			return Node{}, errors.New("invalid Incus project name")
		}
		if !nodeCredentialIDRE.MatchString(credentialID) || !nodeCredentialIDRE.MatchString(serverFingerprint) {
			return Node{}, errors.New("an Incus node requires client credentials and a pinned server fingerprint")
		}
	} else if credentialID != "" || serverFingerprint != "" || project != "" {
		return Node{}, errors.New("incus connection fields are not valid for a libvirt node")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.nodes {
		if n.Name == name {
			return Node{}, &conflictError{fmt.Sprintf("a node named %q already exists", name)}
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	id := "n_" + hex.EncodeToString(b[:])
	now := time.Now().UTC()
	node := &Node{
		ID:                id,
		Name:              name,
		URI:               uri,
		UIURL:             uiURL,
		Backend:           backend,
		Project:           project,
		CredentialID:      credentialID,
		ServerFingerprint: serverFingerprint,
		Type:              NodeTypeRemote,
		Enabled:           true,
		Status:            "unknown",
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	r.nodes[id] = node
	if err := r.save(); err != nil {
		delete(r.nodes, id)
		return Node{}, err
	}
	return *node, nil
}

// Update modifies a remote node.
func (r *Registry) Update(id, name, uri string, enabled *bool) (Node, error) {
	return r.UpdateWithUI(id, name, uri, nil, enabled)
}

// UpdateWithUI is Update plus the operator-network address: nil keeps the
// current value, a pointer to "" clears it, anything else is validated.
func (r *Registry) UpdateWithUI(id, name, uri string, uiURL *string, enabled *bool) (Node, error) {
	newUI := ""
	if uiURL != nil {
		v, err := ValidateUIURL(*uiURL)
		if err != nil {
			return Node{}, err
		}
		newUI = v
	}
	blankName := name != "" && strings.TrimSpace(name) == ""
	name = strings.TrimSpace(name)
	uri = strings.TrimSpace(uri)
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return Node{}, ErrNotFound
	}
	if n.IsLocal() && enabled != nil && !*enabled {
		// Migrations resolve the local node by URI; a disabled local node
		// would break them (and the node cannot be removed either).
		return Node{}, invalidf("local_node", "the local node cannot be disabled")
	}
	if blankName {
		return Node{}, invalidf(CodeInvalidName, "name is required")
	}
	if n.IsLocal() && uri != "" && uri != n.URI {
		return Node{}, invalidf(CodeInvalidURI, "cannot change the URI of the local node; set LIBVIRT_URI and restart")
	}
	if !n.IsLocal() && n.Driver() == BackendIncus && uri != "" && uri != n.URI {
		return Node{}, invalidf(CodeInvalidURI, "cannot change an Incus endpoint without re-enrolling its TLS certificate")
	}
	if name != "" && name != n.Name {
		// An unchanged (possibly legacy) name is never re-validated, so an
		// old node can still be enabled/disabled.
		if err := ValidateNodeName(name); err != nil {
			return Node{}, err
		}
		for otherID, other := range r.nodes {
			if otherID != id && other.Name == name {
				return Node{}, &conflictError{fmt.Sprintf("a node named %q already exists", name)}
			}
		}
		n.Name = name
	}
	if uri != "" && !n.IsLocal() {
		n.URI = uri
	}
	if enabled != nil {
		n.Enabled = *enabled
	}
	if uiURL != nil {
		n.UIURL = newUI
	}
	n.UpdatedAt = time.Now().UTC()
	if err := r.save(); err != nil {
		return Node{}, err
	}
	return *n, nil
}

// Delete removes a node. The local node cannot be deleted.
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return ErrNotFound
	}
	if n.IsLocal() {
		return invalidf("local_node", "cannot delete the local node")
	}
	delete(r.nodes, id)
	return r.save()
}

// runBounded runs fn in its own goroutine and gives up after d. libvirt's
// NewConnect has no timeout, so a hung libvirtd or SSH peer would otherwise
// hold the caller (and any lock it owns) forever. The abandoned goroutine
// finishes on its own; its result is discarded.
func runBounded[T any](d time.Duration, fn func() T) (T, bool) {
	if d <= 0 {
		d = defaultProberTimeout
	}
	done := make(chan T, 1)
	go func() { done <- fn() }()
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case v := <-done:
		return v, true
	case <-timer.C:
		var zero T
		return zero, false
	}
}

type capacity struct {
	cpus              int
	ramFree, ramTotal int64
	err               error
}

// hostCapacity calls the remote prober with a timeout. ok is false when
// there is no prober, the URI is not probeable or the call timed out.
func (r *Registry) hostCapacity(prober RemoteProberFunc, timeout time.Duration, uri string) (capacity, bool) {
	c, ok := r.hostCapacityErr(prober, timeout, uri)
	if !ok || c.err != nil {
		return capacity{}, false
	}
	return c, true
}

// hostCapacityErr is hostCapacity keeping the prober error (c.err) so the
// caller can tell "SSH access not set up" from other failures. ok is false
// only when no call was made or it timed out.
func (r *Registry) hostCapacityErr(prober RemoteProberFunc, timeout time.Duration, uri string) (capacity, bool) {
	if prober == nil || !probeableLibvirtURI(uri) {
		return capacity{}, false
	}
	return runBounded(timeout, func() capacity {
		cpus, free, total, err := prober(uri)
		return capacity{cpus, free, total, err}
	})
}

// probeableLibvirtURI reports whether a libvirt URI may be handed to the
// libvirt client library: only the known schemes and never URIs carrying
// query options (command=, netcat=, keyfile=...), which can run local
// binaries. Nodes saved before URI validation existed keep loading but are
// not connected to.
func probeableLibvirtURI(uri string) bool {
	uri = strings.TrimSpace(uri)
	if uri == "qemu:///system" || uri == "qemu:///session" {
		return true
	}
	if !strings.HasPrefix(uri, "qemu+ssh://") && !strings.HasPrefix(uri, "qemu+tcp://") {
		return false
	}
	u, err := url.Parse(uri)
	return err == nil && u.RawQuery == "" && u.Fragment == ""
}

// LegacyURIWarning returns a short warning for a stored libvirt URI that
// would not be accepted today (plain qemu+tcp, URI options), or "".
func LegacyURIWarning(uri string) string {
	uri = strings.TrimSpace(uri)
	if strings.HasPrefix(uri, "qemu+tcp://") {
		return "insecure qemu+tcp URI: migrate this node to qemu+ssh or Incus mTLS"
	}
	if strings.HasPrefix(uri, "qemu+") || strings.HasPrefix(uri, "qemu:") {
		if !probeableLibvirtURI(uri) && uri != "" {
			return "unsafe libvirt URI options are ignored: re-create this node with a plain qemu+ssh URI"
		}
	}
	return ""
}

// probeTargets derives the WebKVM health URL and the TCP fallback address
// from a node URI. IPv6 literals work with or without brackets/port.
func probeTargets(uriStr string) (targetURL, tcpHostPort string) {
	uriStr = strings.TrimSpace(uriStr)
	switch {
	case strings.HasPrefix(uriStr, "http://") || strings.HasPrefix(uriStr, "https://"):
		return strings.TrimRight(uriStr, "/") + "/api/health", ""
	case strings.HasPrefix(uriStr, "qemu+ssh://") || strings.HasPrefix(uriStr, "qemu+tcp://"):
		clean := strings.TrimPrefix(uriStr, "qemu+ssh://")
		clean = strings.TrimPrefix(clean, "qemu+tcp://")
		hostPart := strings.SplitN(clean, "/", 2)[0]
		if atIdx := strings.LastIndex(hostPart, "@"); atIdx != -1 {
			hostPart = hostPart[atIdx+1:]
		}
		port := "22"
		if strings.HasPrefix(uriStr, "qemu+tcp://") {
			port = "16509"
		}
		host, p := splitHostPortDefault(hostPart, port)
		return "https://" + net.JoinHostPort(host, "8080") + "/api/health", net.JoinHostPort(host, p)
	}
	if host, port, err := net.SplitHostPort(uriStr); err == nil {
		return "https://" + net.JoinHostPort(host, port) + "/api/health", net.JoinHostPort(host, port)
	}
	host := strings.Trim(uriStr, "[]")
	return "https://" + net.JoinHostPort(host, "8080") + "/api/health", net.JoinHostPort(host, "22")
}

func splitHostPortDefault(hostPart, defPort string) (string, string) {
	if h, p, err := net.SplitHostPort(hostPart); err == nil {
		return h, p
	}
	return strings.Trim(hostPart, "[]"), defPort
}

// probeResult is the dynamic telemetry computed by a probe. It is applied
// to the live registry entry only (see commitProbe), never swapped in as a
// whole node, so a concurrent Update/Delete is never overwritten.
type probeResult struct {
	status        string
	latencyMs     int64
	lastSeen      *time.Time
	lastError     string
	version       string
	uptimeSec     int64
	diskFree      int64
	diskTotal     int64
	libvirtStatus string
	// setAccess writes access/accessCode (remote libvirt probes only).
	setAccess  bool
	access     string
	accessCode string
	cpus       int
	ramFree    int64
	ramTotal   int64
	// keep* mean "leave the previous value": used where the probe did not
	// measure that field.
	keepHealth bool
	keepCap    bool
	setName    string
}

// commitProbe writes res into the current registry entry for id. It is a
// no-op returning ErrNotFound when the node was deleted meanwhile, and skips
// the write when the node's URI or enabled flag changed during the probe
// (the result describes a different target).
func (r *Registry) commitProbe(orig Node, res probeResult) (Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.nodes[orig.ID]
	if !ok {
		return Node{}, ErrNotFound
	}
	if !orig.IsLocal() && (cur.URI != orig.URI || cur.Enabled != orig.Enabled) {
		return *cur, nil
	}
	if res.setName != "" {
		cur.Name = res.setName
	}
	cur.Status = res.status
	cur.LatencyMs = res.latencyMs
	cur.LastError = res.lastError
	if res.lastSeen != nil {
		cur.LastSeen = res.lastSeen
	}
	if !res.keepHealth {
		cur.Version = res.version
		cur.UptimeSec = res.uptimeSec
		cur.DiskFree = res.diskFree
		cur.DiskTotal = res.diskTotal
		cur.LibvirtStatus = res.libvirtStatus
	}
	if res.setAccess {
		cur.Access = res.access
		cur.AccessCode = res.accessCode
	}
	if !res.keepCap {
		cur.CPUs = res.cpus
		cur.RAMFree = res.ramFree
		cur.RAMTotal = res.ramTotal
	}
	return *cur, nil
}

// ProbeNode pings a single node, calculates latency and updates dynamic telemetry.
// The probe runs on a copy without any registry lock; only telemetry fields
// are written back, and only if the node still exists unchanged.
func (r *Registry) ProbeNode(id string) (Node, error) {
	r.mu.RLock()
	cur, ok := r.nodes[id]
	var n Node
	if ok {
		n = *cur
	}
	incusProber := r.incusProber
	localProber := r.localProber
	remoteProber := r.remoteProber
	localCapacity := r.localCapacity
	timeout := r.proberTimeout
	httpTimeout := r.httpTimeout
	r.mu.RUnlock()
	if httpTimeout <= 0 {
		httpTimeout = 4 * time.Second
	}
	if !ok {
		return Node{}, ErrNotFound
	}

	start := time.Now()
	now := time.Now().UTC()

	if n.IsLocal() {
		res := probeResult{status: "online", lastSeen: &now, setName: localHostname(), keepHealth: true, keepCap: true}
		if localProber != nil {
			type localOut struct {
				status, version             string
				uptime, diskFree, diskTotal int64
				libvirtStatus               string
				err                         error
			}
			out, finished := runBounded(timeout, func() localOut {
				st, ver, up, df, dt, lv, err := localProber()
				return localOut{st, ver, up, df, dt, lv, err}
			})
			switch {
			case !finished:
				res.status = "degraded"
				res.lastError = "local probe timed out"
			case out.err != nil:
				res.status = "degraded"
				res.lastError = out.err.Error()
			default:
				if out.status != "" {
					res.status = out.status
				}
				res.keepHealth = false
				res.version, res.uptimeSec = out.version, out.uptime
				res.diskFree, res.diskTotal, res.libvirtStatus = out.diskFree, out.diskTotal, out.libvirtStatus
			}
		}
		// Local host capacity too, so the placement panel covers every node.
		if localCapacity != nil {
			c, ok := runBounded(timeout, func() capacity {
				cpus, free, total, err := localCapacity()
				return capacity{cpus, free, total, err}
			})
			if ok && c.err == nil && c.ramTotal > 0 {
				res.keepCap = false
				res.cpus, res.ramFree, res.ramTotal = c.cpus, c.ramFree, c.ramTotal
			}
		} else if c, ok := r.hostCapacity(remoteProber, timeout, n.URI); ok {
			res.keepCap = false
			res.cpus, res.ramFree, res.ramTotal = c.cpus, c.ramFree, c.ramTotal
		}
		return r.commitProbe(n, res)
	}

	if !n.Enabled {
		return r.commitProbe(n, probeResult{status: "offline", lastError: "node is disabled", keepHealth: true, keepCap: true})
	}
	if n.Driver() == BackendIncus {
		if incusProber == nil {
			return r.recordIncusProbe(n.ID, start, now, "", errors.New("the Incus health probe is not configured"))
		}
		type incusOut struct {
			version string
			err     error
		}
		out, finished := runBounded(timeout, func() incusOut {
			v, err := incusProber(n)
			return incusOut{v, err}
		})
		if !finished {
			return r.recordIncusProbe(n.ID, start, now, "", errors.New(probeTimeoutLastError))
		}
		return r.recordIncusProbe(n.ID, start, now, out.version, out.err)
	}

	// Remote libvirt/WebKVM node probe.
	uriStr := strings.TrimSpace(n.URI)
	targetURL, tcpHostPort := probeTargets(uriStr)
	warning := LegacyURIWarning(uriStr)
	finish := func(res probeResult) (Node, error) {
		res.status = "online"
		res.latencyMs = time.Since(start).Milliseconds()
		res.lastSeen = &now
		res.lastError = ""
		if warning != "" {
			res.status = "degraded"
			res.lastError = warning
		}
		// Host capacity (CPUs/RAM) for placement. Best-effort and only for
		// reachable nodes, so a down node never waits on libvirt. It is
		// also the SSH login check: a reachable host whose login is not set
		// up is reported as such, never as plainly online.
		if c, ok := r.hostCapacityErr(remoteProber, timeout, uriStr); ok {
			res.setAccess = true
			switch code, pending := AccessSetupCode(c.err); {
			case c.err == nil:
				res.access = AccessOK
				res.cpus, res.ramFree, res.ramTotal = c.cpus, c.ramFree, c.ramTotal
			case pending:
				res.access, res.accessCode = AccessNotSetUp, code
			}
		}
		return r.commitProbe(n, res)
	}

	// 1. Try WebKVM HTTP/HTTPS API probe if applicable
	if targetURL != "" {
		client := &http.Client{
			Timeout: httpTimeout,
			Transport: &http.Transport{
				TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
				DisableKeepAlives: true,
			},
		}
		resp, err := client.Get(targetURL)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				var health struct {
					Status    string `json:"status"`
					Version   string `json:"version"`
					Uptime    int64  `json:"uptime"`
					DiskFree  int64  `json:"disk_free"`
					DiskTotal int64  `json:"disk_total"`
					Libvirt   string `json:"libvirt"`
				}
				_ = json.Unmarshal(body, &health)
				return finish(probeResult{
					version: health.Version, uptimeSec: health.Uptime,
					diskFree: health.DiskFree, diskTotal: health.DiskTotal, libvirtStatus: health.Libvirt,
				})
			}
		}
	}

	// 2. Fallback to TCP handshake if HTTP API is not active
	if tcpHostPort != "" {
		conn, err := net.DialTimeout("tcp", tcpHostPort, 3*time.Second)
		if err == nil {
			_ = conn.Close()
			return finish(probeResult{libvirtStatus: LibvirtStatusTCPReachable})
		}
	}

	// Unreachable
	res := probeResult{status: "offline", lastError: "connection timed out or refused", keepHealth: true, keepCap: true}
	committed, cerr := r.commitProbe(n, res)
	if errors.Is(cerr, ErrNotFound) {
		return Node{}, cerr
	}
	return committed, fmt.Errorf("probe failed for %s: %s", n.Name, res.lastError)
}

func (r *Registry) recordIncusProbe(id string, start time.Time, now time.Time, version string, probeErr error) (Node, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return Node{}, ErrNotFound
	}
	if probeErr != nil {
		n.Status = "offline"
		n.LastError = probeErr.Error()
		n.LatencyMs = 0
		return *n, fmt.Errorf("probe failed for %s: %w", n.Name, probeErr)
	}
	n.Status = "online"
	n.LatencyMs = time.Since(start).Milliseconds()
	n.LastSeen = &now
	n.LastError = ""
	n.Version = version
	return *n, nil
}

// probeMany probes ids in parallel and returns when all finished or the
// round deadline passed. Nodes still probing at the deadline are marked
// unreachable (stale) instead of blocking the caller; their goroutines
// finish in the background and commit normally.
func (r *Registry) probeMany(ids []string) {
	if len(ids) == 0 {
		return
	}
	r.mu.RLock()
	deadline := r.probeDeadline
	r.mu.RUnlock()
	if deadline <= 0 {
		deadline = defaultProbeDeadline
	}
	var mu sync.Mutex
	finished := map[string]bool{}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			_, _ = r.ProbeNode(nodeID)
			mu.Lock()
			finished[nodeID] = true
			mu.Unlock()
		}(id)
	}
	all := make(chan struct{})
	go func() { wg.Wait(); close(all) }()
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	select {
	case <-all:
		return
	case <-timer.C:
	}
	mu.Lock()
	var late []string
	for _, id := range ids {
		if !finished[id] {
			late = append(late, id)
		}
	}
	mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range late {
		n, ok := r.nodes[id]
		if !ok {
			continue
		}
		if n.IsLocal() {
			n.Status = "degraded"
		} else {
			n.Status = "offline"
			n.LatencyMs = 0
		}
		n.LastError = probeTimeoutLastError
	}
}

// ProbeAll concurrently pings all nodes and updates their states, bounded
// by the round deadline.
func (r *Registry) ProbeAll() []Node {
	r.mu.RLock()
	ids := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		ids = append(ids, id)
	}
	r.mu.RUnlock()
	r.probeMany(ids)
	return r.List()
}

// ProbeAllStale probes only the nodes whose last probe is older than ttl.
// Recent results are reused, so a UI polling /api/nodes does not hammer SSH
// or libvirt. A zero ttl falls back to 15s. Probes run in parallel under a
// global deadline; slow nodes are reported offline/stale, never awaited.
func (r *Registry) ProbeAllStale(ttl time.Duration) []Node {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	now := time.Now()
	r.mu.Lock()
	if r.lastProbe == nil {
		r.lastProbe = map[string]time.Time{}
	}
	ids := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		if now.Sub(r.lastProbe[id]) >= ttl {
			ids = append(ids, id)
			// Mark before probing so two concurrent requests do not both
			// dial the same node.
			r.lastProbe[id] = now
		}
	}
	r.mu.Unlock()
	r.probeMany(ids)
	return r.List()
}

// MarkUnreachable records that a remote node could not be reached by a caller
// other than the prober (e.g. VM federation). It only touches the live entry,
// is a no-op for the local or an unknown node, and is overwritten by the next
// successful probe.
func (r *Registry) MarkUnreachable(id, reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok || n.IsLocal() || !n.Enabled {
		return
	}
	n.Status = "offline"
	n.LatencyMs = 0
	n.LastError = reason
}

// StartHealthMonitor periodically probes all fleet nodes in the background.
func (r *Registry) StartHealthMonitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	// Initial probe on startup
	go r.ProbeAll()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.ProbeAll()
		}
	}
}

// ClusterSummary aggregates fleet status and capacity.
//
// Semantics: total_nodes counts every registered node except remote entries
// that are this machine again (is_self). online_nodes counts enabled nodes
// whose last probe was "online" and whose SSH access is not known to be
// missing (those are no_access_nodes); offline_nodes counts enabled
// nodes that are "offline" or "degraded" (not fully healthy). Disabled nodes
// and nodes never probed ("unknown") are in the total but in neither bucket,
// so online + no_access + offline <= total. Capacity and latency only use enabled nodes
// that answered (online/degraded).
func (r *Registry) ClusterSummary() ClusterSummary {
	r.RefreshLocalCapacity()
	r.mu.RLock()
	defer r.mu.RUnlock()

	var summary ClusterSummary
	self := r.selfIdentityLocked()
	var latencySum, onlineCount int64
	for _, n := range r.nodes {
		// A remote entry that is this machine again is not another node.
		if self.selfCandidate(n) {
			continue
		}
		summary.TotalNodes++
		if !n.Enabled {
			continue
		}
		switch n.Status {
		case "online":
			if n.Access == AccessNotSetUp {
				summary.NoAccessNodes++
				break
			}
			summary.OnlineNodes++
			onlineCount++
			latencySum += n.LatencyMs
		case "degraded":
			summary.OfflineNodes++
		case "offline":
			summary.OfflineNodes++
			continue
		default:
			continue
		}
		summary.TotalDiskFree += n.DiskFree
		summary.TotalDiskTotal += n.DiskTotal
	}
	if onlineCount > 0 {
		summary.AvgLatencyMs = latencySum / onlineCount
	}
	return summary
}
