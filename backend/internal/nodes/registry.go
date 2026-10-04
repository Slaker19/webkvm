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
	"os"
	"path/filepath"
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

// Node is a registered libvirt or WebKVM host.
type Node struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URI       string    `json:"uri"`
	Type      NodeType  `json:"type"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

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
}

// IsLocal reports whether this node is the local libvirt instance.
func (n *Node) IsLocal() bool { return n.Type == NodeTypeLocal }

// ClusterSummary provides aggregated statistics across all fleet nodes.
type ClusterSummary struct {
	TotalNodes     int   `json:"total_nodes"`
	OnlineNodes    int   `json:"online_nodes"`
	OfflineNodes   int   `json:"offline_nodes"`
	AvgLatencyMs   int64 `json:"avg_latency_ms"`
	TotalDiskFree  int64 `json:"total_disk_free"`
	TotalDiskTotal int64 `json:"total_disk_total"`
}

// LocalProberFunc returns dynamic health telemetry for the local node.
type LocalProberFunc func() (status, version string, uptime, diskFree, diskTotal int64, libvirtStatus string, err error)

// Registry is the in-memory + on-disk store of nodes. Safe for concurrent use.
type Registry struct {
	mu          sync.RWMutex
	path        string
	nodes       map[string]*Node
	localProber LocalProberFunc
}

// New loads (or creates) the registry at {dataDir}/nodes.json.
// The local node is auto-created from localURI if missing.
func New(dataDir, localURI string) (*Registry, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	r := &Registry{
		path:  filepath.Join(dataDir, "nodes.json"),
		nodes: map[string]*Node{},
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
		if n.Status == "" {
			n.Status = "unknown"
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
	for _, n := range r.nodes {
		if n.IsLocal() {
			if n.URI != uri {
				n.URI = uri
				n.UpdatedAt = time.Now().UTC()
			}
			if n.Status == "" {
				n.Status = "online"
			}
			return
		}
	}
	r.nodes["local"] = &Node{
		ID:        "local",
		Name:      "local",
		URI:       uri,
		Type:      NodeTypeLocal,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Status:    "online",
	}
}

// List returns every node, sorted by created_at.
func (r *Registry) List() []Node {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Node, 0, len(r.nodes))
	for _, n := range r.nodes {
		out = append(out, *n)
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
	return *n, true
}

// Create adds a new remote node.
func (r *Registry) Create(name, uri string) (Node, error) {
	name = trimAll(name)
	if name == "" {
		return Node{}, errors.New("name is required")
	}
	if uri == "" {
		return Node{}, errors.New("uri is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range r.nodes {
		if n.Name == name {
			return Node{}, fmt.Errorf("a node named %q already exists", name)
		}
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	id := "n_" + hex.EncodeToString(b[:])
	now := time.Now().UTC()
	node := &Node{
		ID:        id,
		Name:      name,
		URI:       uri,
		Type:      NodeTypeRemote,
		Enabled:   true,
		Status:    "unknown",
		CreatedAt: now,
		UpdatedAt: now,
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
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[id]
	if !ok {
		return Node{}, errors.New("node not found")
	}
	if n.IsLocal() && uri != "" && uri != n.URI {
		return Node{}, errors.New("cannot change the URI of the local node; set LIBVIRT_URI and restart")
	}
	if name != "" {
		n.Name = trimAll(name)
	}
	if uri != "" && !n.IsLocal() {
		n.URI = uri
	}
	if enabled != nil {
		n.Enabled = *enabled
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
		return nil
	}
	if n.IsLocal() {
		return errors.New("cannot delete the local node")
	}
	delete(r.nodes, id)
	return r.save()
}

// ProbeNode pings a single node, calculates latency and updates dynamic telemetry.
func (r *Registry) ProbeNode(id string) (Node, error) {
	r.mu.RLock()
	n, ok := r.nodes[id]
	r.mu.RUnlock()
	if !ok {
		return Node{}, errors.New("node not found")
	}

	start := time.Now()
	now := time.Now().UTC()

	if n.IsLocal() {
		r.mu.Lock()
		defer r.mu.Unlock()
		n.Status = "online"
		n.LatencyMs = 0
		n.LastSeen = &now
		n.LastError = ""
		if r.localProber != nil {
			status, version, uptime, diskFree, diskTotal, libvirtStatus, err := r.localProber()
			if err != nil {
				n.Status = "degraded"
				n.LastError = err.Error()
			} else {
				if status != "" {
					n.Status = status
				}
				n.Version = version
				n.UptimeSec = uptime
				n.DiskFree = diskFree
				n.DiskTotal = diskTotal
				n.LibvirtStatus = libvirtStatus
			}
		}
		return *n, nil
	}

	if !n.Enabled {
		r.mu.Lock()
		defer r.mu.Unlock()
		n.Status = "offline"
		n.LastError = "node is disabled"
		return *n, nil
	}

	// Remote node probe
	uriStr := strings.TrimSpace(n.URI)
	targetURL := ""
	tcpHostPort := ""

	if strings.HasPrefix(uriStr, "http://") || strings.HasPrefix(uriStr, "https://") {
		targetURL = strings.TrimRight(uriStr, "/") + "/api/health"
	} else if strings.HasPrefix(uriStr, "qemu+ssh://") || strings.HasPrefix(uriStr, "qemu+tcp://") {
		// e.g. qemu+ssh://alvin@192.168.1.215/system
		clean := strings.TrimPrefix(uriStr, "qemu+ssh://")
		clean = strings.TrimPrefix(clean, "qemu+tcp://")
		parts := strings.SplitN(clean, "/", 2)
		hostPart := parts[0]
		if atIdx := strings.LastIndex(hostPart, "@"); atIdx != -1 {
			hostPart = hostPart[atIdx+1:]
		}
		host := hostPart
		port := "22"
		if strings.HasPrefix(uriStr, "qemu+tcp://") {
			port = "16509"
		}
		if h, p, err := net.SplitHostPort(hostPart); err == nil {
			host = h
			port = p
		}
		tcpHostPort = net.JoinHostPort(host, port)
		// Check HTTPS WebKVM first on standard ports
		targetURL = fmt.Sprintf("https://%s:8080/api/health", host)
	} else if host, port, err := net.SplitHostPort(uriStr); err == nil {
		targetURL = fmt.Sprintf("https://%s:%s/api/health", host, port)
		tcpHostPort = net.JoinHostPort(host, port)
	} else {
		// Bare IP or hostname
		targetURL = fmt.Sprintf("https://%s:8080/api/health", uriStr)
		tcpHostPort = net.JoinHostPort(uriStr, "22")
	}

	// 1. Try WebKVM HTTP/HTTPS API probe if applicable
	if targetURL != "" {
		client := &http.Client{
			Timeout: 4 * time.Second,
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

				r.mu.Lock()
				defer r.mu.Unlock()
				n.Status = "online"
				n.LatencyMs = time.Since(start).Milliseconds()
				n.LastSeen = &now
				n.LastError = ""
				n.Version = health.Version
				n.UptimeSec = health.Uptime
				n.DiskFree = health.DiskFree
				n.DiskTotal = health.DiskTotal
				n.LibvirtStatus = health.Libvirt
				return *n, nil
			}
		}
	}

	// 2. Fallback to TCP handshake if HTTP API is not active
	if tcpHostPort != "" {
		conn, err := net.DialTimeout("tcp", tcpHostPort, 3*time.Second)
		if err == nil {
			_ = conn.Close()
			r.mu.Lock()
			defer r.mu.Unlock()
			n.Status = "online"
			n.LatencyMs = time.Since(start).Milliseconds()
			n.LastSeen = &now
			n.LastError = ""
			n.LibvirtStatus = "reachable"
			return *n, nil
		}
	}

	// Unreachable
	r.mu.Lock()
	defer r.mu.Unlock()
	n.Status = "offline"
	n.LatencyMs = 0
	n.LastError = "connection timed out or refused"
	return *n, fmt.Errorf("probe failed for %s: %s", n.Name, n.LastError)
}

// ProbeAll concurrently pings all nodes and updates their states.
func (r *Registry) ProbeAll() []Node {
	r.mu.RLock()
	ids := make([]string, 0, len(r.nodes))
	for id := range r.nodes {
		ids = append(ids, id)
	}
	r.mu.RUnlock()

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			_, _ = r.ProbeNode(nodeID)
		}(id)
	}
	wg.Wait()
	return r.List()
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

// ClusterSummary aggregates total nodes, status and capacity across all fleet members.
func (r *Registry) ClusterSummary() ClusterSummary {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var summary ClusterSummary
	summary.TotalNodes = len(r.nodes)

	var latencySum int64
	var onlineCount int64

	for _, n := range r.nodes {
		if n.Status == "online" {
			summary.OnlineNodes++
			onlineCount++
			latencySum += n.LatencyMs
		} else {
			summary.OfflineNodes++
		}
		summary.TotalDiskFree += n.DiskFree
		summary.TotalDiskTotal += n.DiskTotal
	}

	if onlineCount > 0 {
		summary.AvgLatencyMs = latencySum / onlineCount
	}

	return summary
}

func trimAll(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
