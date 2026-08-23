// Package firewall manages per-VM firewall rules and port forwards on
// the host using nftables. It is designed to be safe on a shared host:
//
//   - A dedicated table `ip webkvm` is used; existing tables (libvirt's
//     `filter`/`nat`, docker, firewalld) are never touched.
//   - Rules are applied atomically via `nft -f` (a syntax error aborts
//     the whole transaction, leaving the previous ruleset intact), and
//     a `nft -c` check pass runs before applying.
//   - The input chain keeps policy `accept` and ALWAYS re-inserts the
//     host's essential ports (SSH, the web UI, the VNC range) before
//     the operator rules, so it is impossible to lock yourself out by
//     blocking SSH.
//   - Port forwards resolve the guest IP via libvirt; a forward whose
//     VM is off or has no lease is skipped (and reported as pending),
//     never pointed at a stale address.
//   - The ruleset is rebuilt from the store on every change and on
//     startup, so it survives service restarts and never drifts.
package firewall

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Rule is an inbound rule on the host for a VM.
type Rule struct {
	ID     string `json:"id"`
	Proto  string `json:"proto"`  // tcp | udp | both
	Port   int    `json:"port"`   // host destination port
	Action string `json:"action"` // allow | drop
}

// Forward publishes a host port to a guest port on the VM's IP.
type Forward struct {
	ID        string `json:"id"`
	Proto     string `json:"proto"` // tcp | udp | both
	HostPort  int    `json:"host_port"`
	GuestPort int    `json:"guest_port"`
	// TargetIP is resolved by the backend via libvirt. An operator
	// override is respected only if it is a valid IPv4.
	TargetIP string `json:"target_ip,omitempty"`
	// Applied/Pending reflect the last apply attempt.
	Applied bool `json:"applied,omitempty"`
}

// VMFirewall is the set of rules for one VM.
type VMFirewall struct {
	VMID     string    `json:"vm_id"`
	Rules    []Rule    `json:"rules"`
	Forwards []Forward `json:"forwards"`
}

// Store persists per-VM firewall rules to {dataDir}/firewall.json.
type Store struct {
	mu   sync.Mutex
	path string
	byVM map[string]*VMFirewall
}

// NewStore creates the store (does not touch disk).
func NewStore(dataDir string) *Store {
	return &Store{
		path: filepath.Join(dataDir, "firewall.json"),
		byVM: map[string]*VMFirewall{},
	}
}

// Load reads the rules file. A missing file is fine (empty rules).
func (s *Store) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var file struct {
		Version int                     `json:"version"`
		VMs     map[string]*VMFirewall `json:"vms"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	if file.VMs != nil {
		s.byVM = file.VMs
	}
	return nil
}

// Save persists the rules atomically.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"vms":     s.byVM,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the rules for a VM (empty if none).
func (s *Store) Get(vmID string) VMFirewall {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.byVM[vmID]; ok {
		return *v
	}
	return VMFirewall{VMID: vmID}
}

// Set replaces the rules for a VM and saves.
func (s *Store) Set(fw VMFirewall) error {
	s.mu.Lock()
	if len(fw.Rules) == 0 && len(fw.Forwards) == 0 {
		delete(s.byVM, fw.VMID)
	} else {
		s.byVM[fw.VMID] = &fw
	}
	s.mu.Unlock()
	return s.Save()
}

// All returns every VM's rules.
func (s *Store) All() []VMFirewall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]VMFirewall, 0, len(s.byVM))
	for _, v := range s.byVM {
		out = append(out, *v)
	}
	return out
}

// SetApplied marks the applied/pending state of a VM's forwards.
func (s *Store) SetApplied(vmID string, applied []string, pending []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.byVM[vmID]
	if !ok {
		return
	}
	ap := map[string]bool{}
	for _, id := range applied {
		ap[id] = true
	}
	for i := range v.Forwards {
		v.Forwards[i].Applied = ap[v.Forwards[i].ID]
	}
}
