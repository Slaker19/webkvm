package netguard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// WhitelistEntry represents an allowed network or IP with a description.
type WhitelistEntry struct {
	CIDR        string    `json:"cidr"`
	Description string    `json:"description,omitempty"`
	AddedAt     time.Time `json:"added_at"`
	System      bool      `json:"system,omitempty"` // true for built-in subnets like loopback
}

// JailDefinition defines the parameters for a jail instance.
type JailDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	MaxAttempts int    `json:"max_attempts"`
	WindowSec   int    `json:"window_sec"`
	BanDuration int    `json:"ban_duration_sec"`
	Port        int    `json:"port,omitempty"` // 0 for all ports / application level
	Description string `json:"description,omitempty"`
	Custom      bool   `json:"custom"`
}

// JailEntry tracks a banned IP address and its expiration.
type JailEntry struct {
	IP        string    `json:"ip"`
	Reason    string    `json:"reason"`
	Jail      string    `json:"jail"` // "webkvm", "ssh", "manual", or custom jail id
	FailCount int       `json:"fail_count"`
	BannedAt  time.Time `json:"banned_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// JailConfig holds all persistent jail settings.
type JailConfig struct {
	Enabled     bool             `json:"enabled"`
	SSHJail     JailDefinition   `json:"ssh_jail"`
	WebKVMJail  JailDefinition   `json:"webkvm_jail"`
	CustomJails []JailDefinition `json:"custom_jails"`
	Whitelist   []WhitelistEntry `json:"whitelist"`
}

// AlertFunc callback for notifying external services when bans occur.
type AlertFunc func(level, subject, msg string)

// Jail manages active IP bans, multiple jail definitions, whitelist networks,
// and enforces kernel space drops via nftables.
type Jail struct {
	mu sync.RWMutex

	dataDir    string
	configPath string
	bansPath   string
	cfg        JailConfig

	// Compiled in-memory allowlist for quick checking
	compiledAllowlist []*net.IPNet

	// In-memory state
	attempts map[string][]time.Time // key is jailID + ":" + ip
	banned   map[string]JailEntry   // key is ip

	alertFn AlertFunc
	logger  *slog.Logger
	// applyBan installs the kernel-space ban. It is a field so tests can
	// assert that X-Forwarded-For addresses never reach nftables.
	applyBan func(ip string, duration time.Duration)
}

// DefaultWhitelist only exempts loopback. RFC1918 ranges used to be exempt
// too, but they cover incusbr0 (10.68.x), Docker (172.17/172.18) and the LAN,
// so a compromised container could brute-force the login forever (SEG-1).
var DefaultWhitelist = []WhitelistEntry{
	{CIDR: "127.0.0.0/8", Description: "IPv4 Loopback", System: true},
	{CIDR: "::1/128", Description: "IPv6 Loopback", System: true},
}

// legacySystemWhitelist lists the RFC1918 defaults shipped before SEG-1 was
// fixed. They are dropped on load when they are still System entries; an
// operator who added the same range by hand (System=false) keeps it.
var legacySystemWhitelist = map[string]bool{
	"10.0.0.0/8":     true,
	"172.16.0.0/12":  true,
	"192.168.0.0/16": true,
	"100.64.0.0/10":  true,
}

// DefaultConfig returns the out-of-the-box configuration.
func DefaultConfig() JailConfig {
	return JailConfig{
		Enabled: true,
		WebKVMJail: JailDefinition{
			ID:          "webkvm",
			Name:        "WebKVM Console & API",
			Enabled:     true,
			MaxAttempts: 5,
			WindowSec:   300,
			BanDuration: 900, // 15m
			Description: "Protects WebKVM login and API against brute-force attacks",
			Custom:      false,
		},
		SSHJail: JailDefinition{
			ID:          "ssh",
			Name:        "Host SSH Service",
			Enabled:     true,
			MaxAttempts: 5,
			WindowSec:   300,
			BanDuration: 3600, // 1h
			Port:        22,
			Description: "Monitors host SSH authentication logs and blocks brute-force attackers",
			Custom:      false,
		},
		CustomJails: []JailDefinition{},
		Whitelist:   append([]WhitelistEntry{}, DefaultWhitelist...),
	}
}

// New initializes the persistent brute-force protection jail.
func New(dataDir string, logger *slog.Logger) (*Jail, error) {
	if logger == nil {
		logger = slog.Default()
	}

	j := &Jail{
		dataDir:  dataDir,
		attempts: make(map[string][]time.Time),
		banned:   make(map[string]JailEntry),
		cfg:      DefaultConfig(),
		logger:   logger,
	}
	j.applyBan = j.applyNftBan

	if dataDir != "" {
		j.configPath = filepath.Join(dataDir, "jail.json")
		j.bansPath = filepath.Join(dataDir, "jail-bans.json")
		if err := j.loadConfig(); err != nil {
			logger.Warn("jail_config_load_failed_using_defaults", "err", err)
		}
		// loadBans filters against the allowlist, so compile it first.
		j.compileAllowlist()
		// Bans live on the shared DRBD volume so a failover does not
		// hand an attacker a fresh set of attempts. Kernel rules are
		// re-applied separately by ReapplyBans (main calls it at boot).
		if err := j.loadBans(); err != nil {
			logger.Warn("jail_bans_load_failed", "err", err)
		}
	}

	j.compileAllowlist()
	return j, nil
}

// NewJail provides backwards compatibility for legacy constructors.
func NewJail(maxAttempts int, window, banDuration time.Duration, logger *slog.Logger) *Jail {
	j, _ := New("", logger)
	if maxAttempts > 0 {
		j.cfg.WebKVMJail.MaxAttempts = maxAttempts
	}
	if window > 0 {
		j.cfg.WebKVMJail.WindowSec = int(window.Seconds())
	}
	if banDuration > 0 {
		j.cfg.WebKVMJail.BanDuration = int(banDuration.Seconds())
	}
	return j
}

// SetAlertFunc registers a callback to receive ban alerts.
func (j *Jail) SetAlertFunc(fn AlertFunc) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.alertFn = fn
}

func (j *Jail) loadConfig() error {
	if j.configPath == "" {
		return nil
	}
	data, err := os.ReadFile(j.configPath)
	if errors.Is(err, os.ErrNotExist) {
		return j.saveConfig()
	}
	if err != nil {
		return err
	}
	var cfg JailConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	// Ensure system whitelist defaults if empty
	if len(cfg.Whitelist) == 0 {
		cfg.Whitelist = append([]WhitelistEntry{}, DefaultWhitelist...)
	}
	// SEG-1 migration: remove the old RFC1918 system defaults. Entries the
	// operator added by hand are System=false and are preserved.
	kept := cfg.Whitelist[:0]
	for _, entry := range cfg.Whitelist {
		if entry.System && legacySystemWhitelist[entry.CIDR] {
			continue
		}
		kept = append(kept, entry)
	}
	cfg.Whitelist = kept
	if cfg.WebKVMJail.ID == "" {
		cfg.WebKVMJail = DefaultConfig().WebKVMJail
	}
	if cfg.SSHJail.ID == "" {
		cfg.SSHJail = DefaultConfig().SSHJail
	}

	j.cfg = cfg
	return nil
}

func (j *Jail) saveConfig() error {
	if j.configPath == "" {
		return nil
	}
	data, err := json.MarshalIndent(j.cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := j.configPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, j.configPath)
}

// jailBansFile is the on-disk shape of the ban list. Versioned so a future
// change can migrate instead of guessing.
type jailBansFile struct {
	Version int         `json:"version"`
	Bans    []JailEntry `json:"bans"`
}

func (j *Jail) loadBans() error {
	if j.bansPath == "" {
		return nil
	}
	data, err := os.ReadFile(j.bansPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var f jailBansFile
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}
	now := time.Now()
	for _, e := range f.Bans {
		// Never restore a ban that the kernel could not enforce (expired),
		// was later whitelisted, or names a non-IP.
		if !now.Before(e.ExpiresAt) || e.IP == "" || net.ParseIP(e.IP) == nil {
			continue
		}
		if j.isAllowed(e.IP) {
			continue
		}
		j.banned[e.IP] = e
	}
	return nil
}

// saveBansLocked writes the active bans atomically. Must hold j.mu: it
// prunes the map in place. Bans are rare (one write per ban), so a small
// synchronous write is cheaper than a background writer.
func (j *Jail) saveBansLocked() error {
	if j.bansPath == "" {
		return nil
	}
	now := time.Now()
	bans := make([]JailEntry, 0, len(j.banned))
	for ip, e := range j.banned {
		if !now.Before(e.ExpiresAt) || j.isAllowed(ip) {
			delete(j.banned, ip)
			continue
		}
		bans = append(bans, e)
	}
	data, err := json.MarshalIndent(jailBansFile{Version: 1, Bans: bans}, "", "  ")
	if err != nil {
		return err
	}
	tmp := j.bansPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, j.bansPath)
}

// ReapplyBans installs the restored bans into nftables. Call it once at
// startup, after the process is allowed to touch the host firewall: the
// nft set is node-local, so a new active node must rebuild it even though
// the ban list itself came from the shared volume.
func (j *Jail) ReapplyBans() int {
	j.mu.RLock()
	active := make([]JailEntry, 0, len(j.banned))
	now := time.Now()
	for _, e := range j.banned {
		if now.Before(e.ExpiresAt) {
			active = append(active, e)
		}
	}
	j.mu.RUnlock()

	for _, e := range active {
		j.applyBan(e.IP, time.Until(e.ExpiresAt))
	}
	return len(active)
}

func (j *Jail) compileAllowlist() {
	var list []*net.IPNet
	for _, entry := range j.cfg.Whitelist {
		cidr := strings.TrimSpace(entry.CIDR)
		if !strings.Contains(cidr, "/") {
			if strings.Contains(cidr, ":") {
				cidr += "/128"
			} else {
				cidr += "/32"
			}
		}
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network != nil {
			list = append(list, network)
		}
	}
	j.compiledAllowlist = list
}

// AddAllowCIDR adds a network to the unbannable allowlist (compat helper).
func (j *Jail) AddAllowCIDR(cidr string) {
	_ = j.AddWhitelist(cidr, "Legacy Whitelist Entry")
}

// isAllowed checks if an IP is protected by allowlist. Must be called with lock or read lock.
func (j *Jail) isAllowed(ipStr string) bool {
	parsed := net.ParseIP(ipStr)
	if parsed == nil {
		return false
	}
	for _, n := range j.compiledAllowlist {
		if n.Contains(parsed) {
			return true
		}
	}
	return false
}

// IsAllowedPublic checks whether an IP is currently in the allowlist.
func (j *Jail) IsAllowedPublic(ipStr string) bool {
	j.mu.RLock()
	defer j.mu.RUnlock()
	cleanIP := strings.TrimSpace(ipStr)
	if host, _, err := net.SplitHostPort(cleanIP); err == nil {
		cleanIP = host
	}
	return j.isAllowed(cleanIP)
}

// findJail looks up a jail definition by ID. Must be called with lock.
func (j *Jail) findJail(id string) *JailDefinition {
	if id == "" || id == "webkvm" {
		return &j.cfg.WebKVMJail
	}
	if id == "ssh" {
		return &j.cfg.SSHJail
	}
	for i := range j.cfg.CustomJails {
		if j.cfg.CustomJails[i].ID == id {
			return &j.cfg.CustomJails[i]
		}
	}
	return nil
}

// RecordFailure notes an authentication failure from WebKVM web console/API.
func (j *Jail) RecordFailure(ipStr, reason string) bool {
	return j.recordFailure("webkvm", ipStr, false, reason)
}

// RecordFailureClient records a failure for the resolved client IP.
// fromHeader=true means the address came from X-Forwarded-For: the
// in-memory ban still applies, but nftables is skipped because the packet
// source is the proxy, not the attacker (SEG-2).
func (j *Jail) RecordFailureClient(ipStr string, fromHeader bool, reason string) bool {
	return j.recordFailure("webkvm", ipStr, fromHeader, reason)
}

// RecordSSHFailure notes an authentication failure from SSH.
func (j *Jail) RecordSSHFailure(ipStr, reason string) bool {
	return j.recordFailure("ssh", ipStr, false, reason)
}

// RecordFailureWithJail logs a failed authentication attempt against a specific jail.
// If the failure count crosses maxAttempts, the IP is jailed in nftables.
func (j *Jail) RecordFailureWithJail(jailID, ipStr, reason string) bool {
	return j.recordFailure(jailID, ipStr, false, reason)
}

func (j *Jail) recordFailure(jailID, ipStr string, fromHeader bool, reason string) bool {
	cleanIP := strings.TrimSpace(ipStr)
	if host, _, err := net.SplitHostPort(cleanIP); err == nil {
		cleanIP = host
	}
	if parsed := net.ParseIP(cleanIP); parsed == nil {
		return false
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	if !j.cfg.Enabled {
		return false
	}

	if j.isAllowed(cleanIP) {
		return false
	}

	jailDef := j.findJail(jailID)
	if jailDef == nil || !jailDef.Enabled {
		return false
	}

	maxAttempts := jailDef.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	window := time.Duration(jailDef.WindowSec) * time.Second
	if window <= 0 {
		window = 5 * time.Minute
	}
	banDuration := time.Duration(jailDef.BanDuration) * time.Second
	if banDuration <= 0 {
		banDuration = 15 * time.Minute
	}

	now := time.Now()

	// Check if already banned
	if entry, ok := j.banned[cleanIP]; ok && now.Before(entry.ExpiresAt) {
		return true
	}

	key := jailID + ":" + cleanIP
	times := j.attempts[key]
	cutoff := now.Add(-window)
	valid := make([]time.Time, 0, len(times)+1)
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	valid = append(valid, now)
	j.attempts[key] = valid

	if len(valid) >= maxAttempts {
		expires := now.Add(banDuration)
		entry := JailEntry{
			IP:        cleanIP,
			Reason:    reason,
			Jail:      jailID,
			FailCount: len(valid),
			BannedAt:  now,
			ExpiresAt: expires,
		}
		j.banned[cleanIP] = entry
		delete(j.attempts, key)
		if err := j.saveBansLocked(); err != nil {
			j.logger.Warn("jail_bans_persist_failed", "err", err)
		}

		if !fromHeader {
			j.applyBan(cleanIP, banDuration)
		}
		j.logger.Warn("ip_banned_bruteforce", "ip", cleanIP, "jail", jailID, "attempts", len(valid), "duration", banDuration)

		if j.alertFn != nil {
			go j.alertFn("warning", fmt.Sprintf("IP %s Banned (%s)", cleanIP, jailDef.Name),
				fmt.Sprintf("IP %s has been banned for %v after %d failed attempts. Reason: %s", cleanIP, banDuration, len(valid), reason))
		}

		return true
	}

	return false
}

// ManualBan manually bans an IP address with a custom reason and duration.
func (j *Jail) ManualBan(ipStr, reason, jailID string, duration time.Duration) error {
	cleanIP := strings.TrimSpace(ipStr)
	if host, _, err := net.SplitHostPort(cleanIP); err == nil {
		cleanIP = host
	}
	parsed := net.ParseIP(cleanIP)
	if parsed == nil {
		return errors.New("invalid IP address format")
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	if j.isAllowed(cleanIP) {
		return fmt.Errorf("cannot ban IP %s: it is protected by whitelist", cleanIP)
	}

	if duration <= 0 {
		duration = 24 * time.Hour
	}
	if reason == "" {
		reason = "Manual ban by administrator"
	}
	if jailID == "" {
		jailID = "manual"
	}

	now := time.Now()
	entry := JailEntry{
		IP:        cleanIP,
		Reason:    reason,
		Jail:      jailID,
		FailCount: 1,
		BannedAt:  now,
		ExpiresAt: now.Add(duration),
	}
	j.banned[cleanIP] = entry
	if err := j.saveBansLocked(); err != nil {
		j.logger.Warn("jail_bans_persist_failed", "err", err)
	}

	j.applyBan(cleanIP, duration)
	j.logger.Warn("ip_banned_manually", "ip", cleanIP, "jail", jailID, "duration", duration, "reason", reason)

	if j.alertFn != nil {
		go j.alertFn("warning", fmt.Sprintf("IP %s Banned Manually", cleanIP),
			fmt.Sprintf("IP %s was manually banned for %v. Reason: %s", cleanIP, duration, reason))
	}

	return nil
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
	// Clear any active attempt counts for this IP
	for k := range j.attempts {
		if strings.HasSuffix(k, ":"+cleanIP) {
			delete(j.attempts, k)
		}
	}
	if err := j.saveBansLocked(); err != nil {
		j.logger.Warn("jail_bans_persist_failed", "err", err)
	}
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

// GetWhitelist returns the list of whitelisted networks.
func (j *Jail) GetWhitelist() []WhitelistEntry {
	j.mu.RLock()
	defer j.mu.RUnlock()
	res := make([]WhitelistEntry, len(j.cfg.Whitelist))
	copy(res, j.cfg.Whitelist)
	return res
}

// AddWhitelist adds a new CIDR or IP to the allowlist.
func (j *Jail) AddWhitelist(cidrStr, desc string) error {
	clean := strings.TrimSpace(cidrStr)
	if clean == "" {
		return errors.New("CIDR or IP cannot be empty")
	}
	if !strings.Contains(clean, "/") {
		if strings.Contains(clean, ":") {
			clean += "/128"
		} else {
			clean += "/32"
		}
	}
	_, parsedNet, err := net.ParseCIDR(clean)
	if err != nil {
		return fmt.Errorf("invalid CIDR network format: %w", err)
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	canon := parsedNet.String()
	for _, w := range j.cfg.Whitelist {
		if w.CIDR == canon {
			return errors.New("network already exists in whitelist")
		}
	}

	entry := WhitelistEntry{
		CIDR:        canon,
		Description: desc,
		AddedAt:     time.Now(),
		System:      false,
	}
	j.cfg.Whitelist = append(j.cfg.Whitelist, entry)
	j.compileAllowlist()

	// If any currently banned IP falls within this new whitelist, automatically unban it!
	var toUnban []string
	for ip := range j.banned {
		if j.isAllowed(ip) {
			toUnban = append(toUnban, ip)
		}
	}
	for _, ip := range toUnban {
		delete(j.banned, ip)
		_ = j.removeNftBan(ip)
	}
	if len(toUnban) > 0 {
		if err := j.saveBansLocked(); err != nil {
			j.logger.Warn("jail_bans_persist_failed", "err", err)
		}
	}

	return j.saveConfig()
}

// RemoveWhitelist removes a CIDR from the allowlist.
func (j *Jail) RemoveWhitelist(cidrStr string) error {
	clean := strings.TrimSpace(cidrStr)
	if !strings.Contains(clean, "/") {
		if strings.Contains(clean, ":") {
			clean += "/128"
		} else {
			clean += "/32"
		}
	}
	_, parsedNet, err := net.ParseCIDR(clean)
	if err != nil {
		return fmt.Errorf("invalid CIDR network format: %w", err)
	}
	canon := parsedNet.String()

	j.mu.Lock()
	defer j.mu.Unlock()

	found := false
	filtered := make([]WhitelistEntry, 0, len(j.cfg.Whitelist))
	for _, w := range j.cfg.Whitelist {
		if w.CIDR == canon {
			found = true
			continue
		}
		filtered = append(filtered, w)
	}

	if !found {
		return errors.New("network not found in whitelist")
	}

	j.cfg.Whitelist = filtered
	j.compileAllowlist()
	return j.saveConfig()
}

// GetConfig returns the current jail configuration.
func (j *Jail) GetConfig() JailConfig {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return j.cfg
}

// UpdateConfig replaces overall jail configuration and persists it.
func (j *Jail) UpdateConfig(newCfg JailConfig) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	j.cfg.Enabled = newCfg.Enabled
	if newCfg.WebKVMJail.Name != "" {
		j.cfg.WebKVMJail = newCfg.WebKVMJail
	}
	if newCfg.SSHJail.Name != "" {
		j.cfg.SSHJail = newCfg.SSHJail
	}
	if newCfg.CustomJails != nil {
		j.cfg.CustomJails = newCfg.CustomJails
	}

	return j.saveConfig()
}

// AddCustomJail registers a new custom jail definition.
func (j *Jail) AddCustomJail(jd JailDefinition) error {
	name := strings.TrimSpace(jd.Name)
	if name == "" {
		return errors.New("jail name cannot be empty")
	}

	j.mu.Lock()
	defer j.mu.Unlock()

	id := strings.ToLower(strings.TrimSpace(jd.ID))
	if id == "" {
		id = "jail_" + fmt.Sprintf("%d", time.Now().Unix())
	}
	if id == "ssh" || id == "webkvm" || id == "manual" {
		return errors.New("cannot use reserved jail ID")
	}

	for _, existing := range j.cfg.CustomJails {
		if existing.ID == id {
			return errors.New("a jail with this ID already exists")
		}
	}

	if jd.MaxAttempts <= 0 {
		jd.MaxAttempts = 5
	}
	if jd.WindowSec <= 0 {
		jd.WindowSec = 300
	}
	if jd.BanDuration <= 0 {
		jd.BanDuration = 1800
	}

	jd.ID = id
	jd.Name = name
	jd.Custom = true
	j.cfg.CustomJails = append(j.cfg.CustomJails, jd)

	return j.saveConfig()
}

// DeleteCustomJail removes a custom jail definition.
func (j *Jail) DeleteCustomJail(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()

	found := false
	filtered := make([]JailDefinition, 0, len(j.cfg.CustomJails))
	for _, jd := range j.cfg.CustomJails {
		if jd.ID == id {
			found = true
			continue
		}
		filtered = append(filtered, jd)
	}

	if !found {
		return errors.New("jail not found")
	}

	j.cfg.CustomJails = filtered
	return j.saveConfig()
}

// Janitor cleans up expired bans in memory periodically.
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
			removed := false
			for ip, entry := range j.banned {
				if now.After(entry.ExpiresAt) {
					delete(j.banned, ip)
					removed = true
				}
			}
			// Persist the prune so a failover does not resurrect an
			// already expired ban (loadBans would drop it anyway).
			if removed {
				if err := j.saveBansLocked(); err != nil {
					j.logger.Warn("jail_bans_persist_failed", "err", err)
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
	// The table and set may not exist yet (the firewall can be applied after
	// the first failed login). Create them best-effort; both commands are
	// no-ops/ignored when they already exist.
	_ = exec.Command("nft", "add", "table", "ip", "webkvm").Run()
	_ = exec.Command("nft", "add", "set", "ip", "webkvm", "jail_blacklist",
		"{ type ipv4_addr; flags timeout; }").Run()
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
