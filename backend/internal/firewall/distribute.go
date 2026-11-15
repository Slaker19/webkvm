package firewall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// PeerSyncResult is what the active node knows about a peer after the last
// push. It is persisted locally (in DATA_DIR) and shown in the UI; a peer
// failure is reported per node and never rolls back the others.
type PeerSyncResult struct {
	Peer      string    `json:"peer"`
	Version   int64     `json:"version,omitempty"`
	SHA256    string    `json:"sha256,omitempty"`
	AppliedAt time.Time `json:"applied_at,omitempty"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// SSHConfig is the restricted transport used to push the profile. The remote
// user's authorized_keys entry forces `webkvm-cli firewall cluster receive`,
// so the private key can only upload a profile, never run a shell.
type SSHConfig struct {
	User        string
	KeyPath     string
	KnownHosts  string
	ClusterCIDR string
	Timeout     time.Duration
}

func (c SSHConfig) withDefaults() SSHConfig {
	if c.User == "" {
		c.User = "webkvm-fw"
	}
	if c.KeyPath == "" {
		c.KeyPath = "/etc/webkvm/ssh/cluster-fw"
	}
	if c.KnownHosts == "" {
		c.KnownHosts = "/etc/webkvm/ssh/known_hosts"
	}
	if c.Timeout <= 0 {
		c.Timeout = 8 * time.Second
	}
	return c
}

// ValidPeerIP restricts distribution to the cluster network. Accepting an
// arbitrary address from settings would turn the active node into an SSH
// client for anything the admin (or a compromised session) types.
func ValidPeerIP(raw, clusterCIDR string) bool {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil || ip.To4() == nil {
		return false
	}
	if strings.TrimSpace(clusterCIDR) == "" {
		return false
	}
	_, cluster, err := net.ParseCIDR(strings.TrimSpace(clusterCIDR))
	return err == nil && cluster.Contains(ip)
}

// SSHRunner builds the real transport: strict host key checking against a
// dedicated known_hosts, short timeouts, no PTY, and the profile on stdin.
func SSHRunner(cfg SSHConfig) func(context.Context, string, []byte) ([]byte, error) {
	cfg = cfg.withDefaults()
	return func(ctx context.Context, peer string, payload []byte) ([]byte, error) {
		if !ValidPeerIP(peer, cfg.ClusterCIDR) {
			return nil, fmt.Errorf("peer %q is not in the configured cluster network %q", peer, cfg.ClusterCIDR)
		}
		peerCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
		args := []string{
			"-i", cfg.KeyPath,
			"-o", "StrictHostKeyChecking=yes",
			"-o", "UserKnownHostsFile=" + cfg.KnownHosts,
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=5",
			"-o", "ServerAliveInterval=5",
			"-o", "ServerAliveCountMax=2",
			cfg.User + "@" + peer,
			"webkvm-cli", "firewall", "cluster", "receive", "--json",
		}
		cmd := exec.CommandContext(peerCtx, "ssh", args...)
		cmd.Stdin = bytes.NewReader(payload)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		if err != nil {
			msg := strings.TrimSpace(stderr.String())
			if len(msg) > 500 {
				msg = msg[:500] + "…"
			}
			if msg == "" {
				msg = err.Error()
			}
			return stdout.Bytes(), errors.New(msg)
		}
		return stdout.Bytes(), nil
	}
}

// DistributeProfile sends the same payload to every peer. A failure is
// recorded per peer and does not stop the loop: the others keep the new
// profile and the UI shows exactly which node is behind.
func DistributeProfile(ctx context.Context, payload []byte, peers []string, clusterCIDR string, run func(context.Context, string, []byte) ([]byte, error)) []PeerSyncResult {
	now := time.Now().UTC()
	results := make([]PeerSyncResult, 0, len(peers))
	for _, peer := range peers {
		peer = strings.TrimSpace(peer)
		if peer == "" {
			continue
		}
		result := PeerSyncResult{Peer: peer, UpdatedAt: now}
		if !ValidPeerIP(peer, clusterCIDR) {
			result.Error = fmt.Sprintf("peer is not in the configured cluster network %q; refusing to push", clusterCIDR)
			results = append(results, result)
			continue
		}
		out, err := run(ctx, peer, payload)
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		var state SyncState
		if jerr := json.Unmarshal(out, &state); jerr != nil {
			result.Error = "peer returned an invalid sync state: " + jerr.Error()
			results = append(results, result)
			continue
		}
		result.Version = state.Version
		result.SHA256 = state.SHA256
		result.AppliedAt = state.AppliedAt
		result.Error = state.LastError
		results = append(results, result)
	}
	return results
}

// PeerStatesPath is the per-active-node record of the last push. It sits in
// DATA_DIR so a failover keeps the history.
func PeerStatesPath(dataDir string) string {
	return dataDir + string(os.PathSeparator) + "cluster-firewall-peers.json"
}

// LoadPeerStates reads the last push results. Missing/corrupt file = none.
func LoadPeerStates(path string) []PeerSyncResult {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var results []PeerSyncResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil
	}
	return results
}

// SavePeerStates persists the last push results atomically (0600).
func SavePeerStates(path string, results []PeerSyncResult) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o600)
}

// DivergentHashes reports whether any reachable peer applied a different
// profile than this node. Peers that failed the last push are reported
// separately by the per-node errors, not here.
func DivergentHashes(local SyncState, peers []PeerSyncResult) bool {
	for _, peer := range peers {
		if peer.Error != "" || peer.SHA256 == "" {
			continue
		}
		if local.SHA256 == "" || peer.SHA256 != local.SHA256 {
			return true
		}
	}
	return false
}
