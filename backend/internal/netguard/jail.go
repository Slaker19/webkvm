package netguard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// JailEntry tracks a banned IP address and its expiration.
type JailEntry struct {
	IP        string    `json:"ip"`
	Reason    string    `json:"reason"`
	FailCount int       `json:"fail_count"`
	BannedAt  time.Time `json:"banned_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Jail manages active IP bans and enforces them in kernel space via nftables.
type Jail struct {
	mu sync.RWMutex

	// Config
	maxAttempts int
	window      time.Duration
	banDuration time.Duration
	allowlist   []*net.IPNet

	// In-memory state
	attempts map[string][]time.Time
	banned   map[string]JailEntry

	logger *slog.Logger
}

// NewJail initializes the brute-force protection jail.
func NewJail(maxAttempts int, window, banDuration time.Duration, logger *slog.Logger) *Jail {
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	if banDuration <= 0 {
		banDuration = 15 * time.Minute
	}
	if logger == nil {
		logger = slog.Default()
	}

	j := &Jail{
		maxAttempts: maxAttempts,
		window:      window,
		banDuration: banDuration,
		attempts:    make(map[string][]time.Time),
		banned:      make(map[string]JailEntry),
		logger:      logger,
	}

	// Always allow loopback and standard private networks
	j.AddAllowCIDR("127.0.0.0/8")
	j.AddAllowCIDR("::1/128")
	j.AddAllowCIDR("10.0.0.0/8")
	j.AddAllowCIDR("172.16.0.0/12")
	j.AddAllowCIDR("192.168.0.0/16")
	j.AddAllowCIDR("100.64.0.0/10") // Carrier-grade NAT / Tailscale

	return j
}

// AddAllowCIDR adds a network to the unbannable allowlist.
func (j *Jail) AddAllowCIDR(cidr string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	_, network, err := net.ParseCIDR(strings.TrimSpace(cidr))
	if err == nil {
		j.allowlist = append(j.allowlist, network)
	}
}

// isAllowed checks if an IP is protected by allowlist.
func (j *Jail) isAllowed(ipStr string) bool {
	parsed := net.ParseIP(ipStr)
	if parsed == nil {
		return false
	}
	for _, n := range j.allowlist {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// RecordFailure notes an authentication failure from an IP.
// If the failure count crosses maxAttempts, the IP is jailed in nftables.
func (j *Jail) RecordFailure(ipStr, reason string) bool {
	cleanIP := strings.TrimSpace(ipStr)
	// Strip port if present
	if host, _, err := net.SplitHostPort(cleanIP); err == nil {
		cleanIP = host
	}

	if j.isAllowed(cleanIP) {
		return false
	}

	now := time.Now()

	j.mu.Lock()
	defer j.mu.Unlock()

	// Check if already banned
	if entry, ok := j.banned[cleanIP]; ok && now.Before(entry.ExpiresAt) {
		return true
	}

	// Clean out-of-window attempts
	times := j.attempts[cleanIP]
	cutoff := now.Add(-j.window)
	valid := make([]time.Time, 0, len(times)+1)
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	valid = append(valid, now)
	j.attempts[cleanIP] = valid

	if len(valid) >= j.maxAttempts {
		expires := now.Add(j.banDuration)
		entry := JailEntry{
			IP:        cleanIP,
			Reason:    reason,
			FailCount: len(valid),
			BannedAt:  now,
			ExpiresAt: expires,
		}
		j.banned[cleanIP] = entry
		delete(j.attempts, cleanIP)

		j.applyNftBan(cleanIP, j.banDuration)
		j.logger.Warn("ip_banned_bruteforce", "ip", cleanIP, "attempts", len(valid), "duration", j.banDuration)
		return true
	}

	return false
}

// IsBanned checks if an IP is currently banned in the jail.
func (j *Jail) IsBanned(ipStr string) bool {
	cleanIP := strings.TrimSpace(ipStr)
	if host, _, err := net.SplitHostPort(cleanIP); err == nil {
		cleanIP = host
	}
	j.mu.RLock()
	defer j.mu.RUnlock()
	entry, ok := j.banned[cleanIP]
	if !ok {
		return false
	}
	return time.Now().Before(entry.ExpiresAt)
}

// Unban manually removes an IP from the jail.
func (j *Jail) Unban(ipStr string) error {
	cleanIP := strings.TrimSpace(ipStr)
	if host, _, err := net.SplitHostPort(cleanIP); err == nil {
		cleanIP = host
	}
	parsed := net.ParseIP(cleanIP)
	if parsed == nil {
		return errors.New("invalid IP address")
	}

	j.mu.Lock()
	delete(j.banned, cleanIP)
	delete(j.attempts, cleanIP)
	j.mu.Unlock()

	return j.removeNftBan(cleanIP)
}

// ListBanned returns all currently active bans.
func (j *Jail) ListBanned() []JailEntry {
	j.mu.RLock()
	defer j.mu.RUnlock()

	now := time.Now()
	res := make([]JailEntry, 0, len(j.banned))
	for _, e := range j.banned {
		if now.Before(e.ExpiresAt) {
			res = append(res, e)
		}
	}
	return res
}

// Janitor cleans up expired bans in memory.
func (j *Jail) Janitor(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			j.mu.Lock()
			now := time.Now()
			for ip, entry := range j.banned {
				if now.After(entry.ExpiresAt) {
					delete(j.banned, ip)
				}
			}
			j.mu.Unlock()
		}
	}
}

func (j *Jail) applyNftBan(ip string, duration time.Duration) {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return // table ip webkvm is IPv4 only
	}
	secs := int(duration.Seconds())
	if secs <= 0 {
		secs = 900
	}
	// Best-effort insertion into nftables set if nft is present
	_ = exec.Command("nft", "add", "element", "ip", "webkvm", "jail_blacklist", fmt.Sprintf("{ %s timeout %ds }", parsed.String(), secs)).Run()
}

func (j *Jail) removeNftBan(ip string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return errors.New("invalid or unsupported IP address (must be IPv4)")
	}
	// Best-effort removal from nftables set; set element may have already expired in kernel
	_ = exec.Command("nft", "delete", "element", "ip", "webkvm", "jail_blacklist", fmt.Sprintf("{ %s }", parsed.String())).Run()
	return nil
}
