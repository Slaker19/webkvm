// Package nodes manages a registry of libvirt "nodes" the backend
// can talk to. Each node has a libvirt URI; the local node is the
// one created from the LIBVIRT_URI env var at startup. Remote nodes
// are added via the API (or a future UI) and may use any libvirt
// URI the host can reach (qemu:///system, qemu+ssh://user@host/system,
// qemu+tcp://host:16509/system, etc.).
//
// This package only stores the configuration; it does NOT open
// libvirt connections. The libvirt.Connector is the long-lived
// connection holder, and a future refactor will key its connection
// map by node ID. For Phase 1.7 we ship the registry + API + UI;
// "real" multi-host comes in Phase 1.11 (cluster).
package nodes

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// Node is a registered libvirt host.
type Node struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URI       string    `json:"uri"`
	Type      NodeType  `json:"type"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Note: there's no "last_seen" yet because we don't open the
	// connection in this package. The /api/system/status endpoint
	// exposes libvirt connectivity for the *local* node, and the
	// multi-host implementation will populate per-node status.
}

// IsLocal reports whether this node is the local libvirt instance.
func (n *Node) IsLocal() bool { return n.Type == NodeTypeLocal }

// Registry is the in-memory + on-disk store of nodes. Safe for
// concurrent use.
type Registry struct {
	mu    sync.RWMutex
	path  string
	nodes map[string]*Node
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

// Create adds a new remote node. Returns the new node. The ID is
// auto-generated. localURI is the URI of the local node — added
// nodes must not collide with it.
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

// Update modifies a remote node. The local node's URI follows the
// LIBVIRT_URI env var; attempts to update it are rejected.
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

func trimAll(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
