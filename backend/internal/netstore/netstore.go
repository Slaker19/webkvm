// Package netstore persists which "kind" (nat/isolated/direct) each
// WebKVM-created Linux bridge was created as, plus the extra state each
// kind needs to tear itself down symmetrically:
//
//   - direct: the physical interface enslaved, and the IPv4 CIDR (if any)
//     moved off it, so DeleteNetwork can hand it back.
//   - nat: the CIDR the bridge NATs, so the firewall package can render a
//     masquerade rule for it without guessing from kernel state (a bare
//     bridge with no masquerade rule is genuinely ambiguous — it could be
//     "isolated" or "nat with the rule removed by hand").
//
// A bridge WebKVM did not create (the installer's own vmbr0/vmbr1, or one
// made by hand) simply has no record here; ListNetworks falls back to
// best-effort inference for those (see libvirt.Connector.ListNetworks).
package netstore

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"webkvm/internal/models"
)

// Record is the persisted state for one bridge WebKVM created via
// POST /api/networks.
type Record struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // "nat" | "isolated" | "direct"
	CIDR      string   `json:"cidr,omitempty"`
	Interface string   `json:"interface,omitempty"`  // direct only
	MovedIPv4 string   `json:"moved_ipv4,omitempty"` // direct only: CIDR moved off Interface at creation, to restore on delete
	DHCPStart string   `json:"dhcp_start,omitempty"` // nat/isolated only, when DHCP is on
	DHCPEnd   string   `json:"dhcp_end,omitempty"`   // nat/isolated only, when DHCP is on
	DNS       []string `json:"dns,omitempty"`        // nat/isolated only, when DHCP is on
	MTU       int      `json:"mtu,omitempty"`        // bridge link MTU (0 = default)
	// Reservations are fixed MAC→IP DHCP leases served by the bridge's
	// dnsmasq (nat/isolated only, requires DHCP on).
	Reservations []models.DHCPReservation `json:"reservations,omitempty"`
	CreatedAt    time.Time                `json:"created_at"`
}

// Store is a small JSON-backed map, keyed by bridge name.
type Store struct {
	mu      sync.Mutex
	path    string
	records map[string]Record
}

// Open loads (or initializes) the store at <dataDir>/networks.json.
func Open(dataDir string) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, "networks.json"), records: map[string]Record{}}
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	var records map[string]Record
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records != nil {
		s.records = records
	}
	return s, nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Save persists (or replaces) a bridge's record.
func (s *Store) Save(r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	s.records[r.Name] = r
	return s.saveLocked()
}

// Get returns a bridge's record, if WebKVM created it.
func (s *Store) Get(name string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[name]
	return r, ok
}

// Delete removes a bridge's record (called after it's torn down).
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[name]; !ok {
		return nil
	}
	delete(s.records, name)
	return s.saveLocked()
}

// All returns every persisted record. Used by the firewall package to
// render a masquerade rule per NAT-kind bridge.
func (s *Store) All() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	return out
}
