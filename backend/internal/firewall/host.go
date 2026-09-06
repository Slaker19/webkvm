package firewall

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Host-level firewall (V13-C-01 / V13-C-02).
//
// The per-VM firewall (store.go) manages rules scoped to a single VM.
// The HOST firewall is a global ruleset the administrator edits from
// the Firewall page:
//
//   - Input rules  → traffic destined to the WebKVM host itself
//     (filter/input chain). The management ports (SSH, the web UI, the
//     noVNC range) are ALWAYS accepted by implicit, non-deletable
//     safety rails emitted before any operator rule, and the input
//     chain policy is `accept` — so it is structurally impossible to
//     lock yourself out by misconfiguring this page. As a second line
//     of defence, the backend REJECTS any drop rule that targets a
//     protected management port.
//   - Forward rules → DNAT port forwards from a host port to a guest
//     IP:port (VM traffic). These are explicit and global (they do not
//     depend on a VM being registered in webkvm).

// HostInputRule is a single host-input rule (traffic to the host).
type HostInputRule struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	Proto  string `json:"proto"`         // tcp | udp | both
	Port   int    `json:"port"`          // 1-65535
	Src    string `json:"src,omitempty"` // optional IPv4 or CIDR source filter
	Action string `json:"action"`        // allow | drop
}

// HostForwardRule is a DNAT port forward to a guest IP (VM traffic).
type HostForwardRule struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Proto     string `json:"proto"` // tcp | udp | both
	HostPort  int    `json:"host_port"`
	GuestIP   string `json:"guest_ip"` // required IPv4
	GuestPort int    `json:"guest_port"`
}

// HostFirewall is the full host-level ruleset.
type HostFirewall struct {
	Input    []HostInputRule   `json:"input"`
	Forwards []HostForwardRule `json:"forwards"`
}

// IsEmpty reports whether the host ruleset has no rules at all.
func (h HostFirewall) IsEmpty() bool {
	return len(h.Input) == 0 && len(h.Forwards) == 0
}

// HostStore persists the CONFIRMED host firewall to
// {dataDir}/firewall-host.json. Staged-but-unconfirmed applies never
// touch this file (see Manager.StageHostApply / ConfirmHostApply).
type HostStore struct {
	mu   sync.Mutex
	path string
	fw   HostFirewall
}

// NewHostStore creates the host store (does not touch disk).
func NewHostStore(dataDir string) *HostStore {
	return &HostStore{path: filepath.Join(dataDir, "firewall-host.json")}
}

// Load reads the confirmed host rules. A missing file is fine.
func (s *HostStore) Load() error {
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
		Version  int          `json:"version"`
		Firewall HostFirewall `json:"firewall"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return err
	}
	s.fw = file.Firewall
	return nil
}

// Save persists the confirmed rules atomically (0600 — no secrets, but
// this file describes the host's network posture).
func (s *HostStore) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

func (s *HostStore) saveLocked() error {
	data, err := json.MarshalIndent(map[string]any{
		"version":  1,
		"firewall": s.fw,
	}, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the confirmed host firewall.
func (s *HostStore) Get() HostFirewall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fw
}

// Set replaces the confirmed host firewall AND persists it.
func (s *HostStore) Set(fw HostFirewall) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fw = fw
	return s.saveLocked()
}

// IsEmpty reports whether the confirmed host ruleset is empty.
func (s *HostStore) IsEmpty() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fw.IsEmpty()
}

// ProtectedPorts returns the management ports that can never be
// blocked: SSH (22), the web UI/API port, and the noVNC range
// (5900-5903). The UI renders them as a locked, non-deletable section
// and the backend rejects drop rules that target them.
func ProtectedPorts(webPort int) []int {
	return HostPorts(webPort)
}

// ErrAntiLockout is returned when an operator tries to add a drop rule
// on a protected management port (V13-C-01).
var ErrAntiLockout = errors.New("cannot drop traffic on a protected WebKVM management port (SSH/UI/VNC); WebKVM keeps these open so you can never lock yourself out")

// ErrNoPendingApply is returned by Confirm/Rollback when there is no
// staged apply in flight.
var ErrNoPendingApply = errors.New("no pending firewall apply to confirm or roll back")

// ValidateHostFirewall validates an incoming host ruleset and enforces
// the anti-lockout policy. webPort is the web UI/API port.
func ValidateHostFirewall(fw HostFirewall, webPort int) error {
	prot := map[int]bool{}
	for _, p := range ProtectedPorts(webPort) {
		prot[p] = true
	}
	for _, r := range fw.Input {
		if r.Proto != "tcp" && r.Proto != "udp" && r.Proto != "both" {
			return fmt.Errorf("input rule %q: protocol must be tcp, udp or both", r.ID)
		}
		if r.Port < 1 || r.Port > 65535 {
			return fmt.Errorf("input rule %q: port must be 1-65535", r.ID)
		}
		if r.Action != "allow" && r.Action != "drop" {
			return fmt.Errorf("input rule %q: action must be allow or drop", r.ID)
		}
		if r.Src != "" {
			if net.ParseIP(r.Src) == nil {
				if _, _, err := net.ParseCIDR(r.Src); err != nil {
					return fmt.Errorf("input rule %q: invalid source %q (want IPv4 or CIDR)", r.ID, r.Src)
				}
			}
			if strings.Contains(r.Src, ":") {
				return fmt.Errorf("input rule %q: only IPv4 sources are supported", r.ID)
			}
		}
		// Anti-lockout: a drop on a protected management port is
		// rejected outright (even though the implicit ACCEPT rails make
		// it inert, refusing it avoids confusion and documents intent).
		if r.Action == "drop" && prot[r.Port] {
			return fmt.Errorf("%w (port %d)", ErrAntiLockout, r.Port)
		}
	}
	for _, f := range fw.Forwards {
		if f.Proto != "tcp" && f.Proto != "udp" && f.Proto != "both" {
			return fmt.Errorf("forward rule %q: protocol must be tcp, udp or both", f.ID)
		}
		if f.HostPort < 1 || f.HostPort > 65535 || f.GuestPort < 1 || f.GuestPort > 65535 {
			return fmt.Errorf("forward rule %q: host and guest ports must be 1-65535", f.ID)
		}
		if net.ParseIP(f.GuestIP) == nil || strings.Contains(f.GuestIP, ":") {
			return fmt.Errorf("forward rule %q: a valid IPv4 guest address is required", f.ID)
		}
	}
	return nil
}
