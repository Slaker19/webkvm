package netguard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestJail_Allowlist(t *testing.T) {
	dir := t.TempDir()
	j, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Never touch the real nftables table from a unit test.
	j.applyBan = func(string, time.Duration) {}

	// Localhost should never be banned
	for i := 0; i < 10; i++ {
		banned := j.RecordFailure("127.0.0.1", "bad password")
		if banned {
			t.Fatalf("127.0.0.1 should be exempt from bans")
		}
	}

	// RFC1918 is NOT exempt by default any more (SEG-1): container bridges
	// and Docker live there. The fifth failure must ban it.
	for i := 0; i < 4; i++ {
		if j.RecordFailure("192.168.1.100", "bad token") {
			t.Fatalf("192.168.1.100 banned before reaching the limit")
		}
	}
	if !j.RecordFailure("192.168.1.100", "bad token") {
		t.Fatalf("192.168.1.100 should be bannable by default")
	}

	// Add custom subnet to whitelist
	if err := j.AddWhitelist("203.0.113.0/24", "Test Office Subnet"); err != nil {
		t.Fatalf("AddWhitelist failed: %v", err)
	}

	// IP inside added subnet should now be exempt
	for i := 0; i < 10; i++ {
		if j.RecordFailure("203.0.113.50", "bad pass") {
			t.Fatalf("203.0.113.50 should be exempt after adding to whitelist")
		}
	}

	// Remove custom subnet from whitelist
	if err := j.RemoveWhitelist("203.0.113.0/24"); err != nil {
		t.Fatalf("RemoveWhitelist failed: %v", err)
	}

	// Verify persistence in jail.json
	cfgData, err := os.ReadFile(filepath.Join(dir, "jail.json"))
	if err != nil {
		t.Fatalf("jail.json missing: %v", err)
	}
	if len(cfgData) == 0 {
		t.Fatal("jail.json is empty")
	}
}

func TestJail_BanAndUnban(t *testing.T) {
	j := NewJail(3, 1*time.Minute, 5*time.Minute, nil)
	j.applyBan = func(string, time.Duration) {}

	attackerIP := "198.51.100.42"

	// 1st attempt
	if j.RecordFailure(attackerIP, "test") {
		t.Fatalf("should not ban on 1st failure")
	}

	// 2nd attempt
	if j.RecordFailure(attackerIP, "test") {
		t.Fatalf("should not ban on 2nd failure")
	}

	// 3rd attempt: should ban!
	if !j.RecordFailure(attackerIP, "test") {
		t.Fatalf("should ban on 3rd failure")
	}

	bannedList := j.ListBanned()
	if len(bannedList) != 1 {
		t.Fatalf("expected 1 banned IP, got %d", len(bannedList))
	}
	if bannedList[0].IP != attackerIP {
		t.Fatalf("expected IP %s to be banned, got %s", attackerIP, bannedList[0].IP)
	}
	if bannedList[0].Jail != "webkvm" {
		t.Fatalf("expected jail webkvm, got %s", bannedList[0].Jail)
	}

	// Unban
	if err := j.Unban(attackerIP); err != nil {
		t.Fatalf("unban failed: %v", err)
	}

	if len(j.ListBanned()) != 0 {
		t.Fatalf("expected 0 banned IPs after unban")
	}
}

func TestJail_ManualBan(t *testing.T) {
	dir := t.TempDir()
	j, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	j.applyBan = func(string, time.Duration) {}

	// Reject banning loopback, the only system-exempt network now.
	err = j.ManualBan("127.0.0.1", "manual test", "manual", time.Hour)
	if err == nil {
		t.Fatal("expected error banning loopback")
	}

	// Ban non-whitelisted IP
	ip := "198.51.100.99"
	err = j.ManualBan(ip, "manual test scan", "manual", 2*time.Hour)
	if err != nil {
		t.Fatalf("ManualBan failed: %v", err)
	}

	if !j.IsBanned(ip) {
		t.Fatalf("expected %s to be banned", ip)
	}

	bans := j.ListBanned()
	if len(bans) != 1 || bans[0].IP != ip || bans[0].Jail != "manual" {
		t.Fatalf("unexpected ban details: %+v", bans)
	}

	// Unban
	if err := j.Unban(ip); err != nil {
		t.Fatalf("Unban failed: %v", err)
	}
	if j.IsBanned(ip) {
		t.Fatalf("expected %s to be unbanned", ip)
	}
}

func TestJail_SSHAndCustomJails(t *testing.T) {
	dir := t.TempDir()
	j, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	j.applyBan = func(string, time.Duration) {}

	// 1. SSH Jail
	sshAttacker := "198.51.100.77"
	for i := 0; i < 4; i++ {
		if j.RecordSSHFailure(sshAttacker, "ssh fail") {
			t.Fatalf("should not ban before 5th attempt")
		}
	}
	if !j.RecordSSHFailure(sshAttacker, "ssh fail") {
		t.Fatalf("expected SSH jail to ban on 5th attempt")
	}
	if !j.IsBanned(sshAttacker) {
		t.Fatalf("expected %s to be banned", sshAttacker)
	}

	// 2. Custom Jail
	customJail := JailDefinition{
		ID:          "openvpn",
		Name:        "OpenVPN Service",
		Enabled:     true,
		MaxAttempts: 2,
		WindowSec:   60,
		BanDuration: 1800,
		Port:        1194,
		Description: "Protects OpenVPN gateway",
	}
	if err := j.AddCustomJail(customJail); err != nil {
		t.Fatalf("AddCustomJail failed: %v", err)
	}

	vpnAttacker := "198.51.100.88"
	if j.RecordFailureWithJail("openvpn", vpnAttacker, "vpn auth error") {
		t.Fatalf("should not ban on 1st attempt")
	}
	if !j.RecordFailureWithJail("openvpn", vpnAttacker, "vpn auth error") {
		t.Fatalf("expected openvpn jail to ban on 2nd attempt")
	}

	bans := j.ListBanned()
	if len(bans) != 2 {
		t.Fatalf("expected 2 active bans, got %d", len(bans))
	}

	// Delete custom jail
	if err := j.DeleteCustomJail("openvpn"); err != nil {
		t.Fatalf("DeleteCustomJail failed: %v", err)
	}
}

func TestDefaultWhitelistOnlyLoopback(t *testing.T) {
	for _, entry := range DefaultConfig().Whitelist {
		if entry.CIDR != "127.0.0.0/8" && entry.CIDR != "::1/128" {
			t.Fatalf("unexpected default whitelist entry %q (SEG-1)", entry.CIDR)
		}
	}
}

func TestLegacyWhitelistMigration(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Whitelist = []WhitelistEntry{
		{CIDR: "127.0.0.0/8", System: true},
		{CIDR: "10.0.0.0/8", System: true},
		{CIDR: "172.16.0.0/12", System: true},
		{CIDR: "192.168.5.0/24", Description: "operator LAN", System: false},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jail.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if j.IsAllowedPublic("10.1.1.1") {
		t.Fatal("legacy 10/8 system entry survived migration")
	}
	if j.IsAllowedPublic("172.16.1.1") {
		t.Fatal("legacy 172.16/12 system entry survived migration")
	}
	if !j.IsAllowedPublic("192.168.5.10") {
		t.Fatal("operator-added whitelist entry was removed")
	}
}

func TestRecordFailureFromContainerIPCounts(t *testing.T) {
	j, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	j.applyBan = func(string, time.Duration) {}
	j.cfg.WebKVMJail.MaxAttempts = 3
	for i := 0; i < 2; i++ {
		if j.RecordFailure("10.68.108.5", "test") {
			t.Fatalf("banned before reaching the limit")
		}
	}
	if !j.RecordFailure("10.68.108.5", "test") {
		t.Fatal("container IP 10.68.108.5 must be bannable (SEG-1)")
	}
	if !j.IsBanned("10.68.108.5") {
		t.Fatal("expected in-memory ban")
	}
}

func TestRecordFailureFromHeaderSkipsNft(t *testing.T) {
	j, err := New("", nil)
	if err != nil {
		t.Fatal(err)
	}
	j.cfg.WebKVMJail.MaxAttempts = 1
	called := false
	j.applyBan = func(string, time.Duration) { called = true }
	if !j.RecordFailureClient("203.0.113.9", true, "test") {
		t.Fatal("expected the in-memory ban")
	}
	if called {
		t.Fatal("nft ban must be skipped for X-Forwarded-For addresses (SEG-2)")
	}
	if !j.IsBanned("203.0.113.9") {
		t.Fatal("expected in-memory ban for the XFF client")
	}
}

func TestJailBansPersistAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	j, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	j.applyBan = func(string, time.Duration) {}
	if err := j.ManualBan("198.51.100.7", "persist test", "manual", time.Hour); err != nil {
		t.Fatal(err)
	}

	j2, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !j2.IsBanned("198.51.100.7") {
		t.Fatal("ban did not survive a restart")
	}
	banned := j2.ListBanned()
	if len(banned) != 1 || banned[0].Jail != "manual" || banned[0].Reason != "persist test" {
		t.Fatalf("restored ban = %+v", banned)
	}
	info, err := os.Stat(filepath.Join(dir, "jail-bans.json"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("jail-bans.json mode = %o, want 600", perm)
	}
}

func TestJailDropsExpiredBansOnLoad(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	f := jailBansFile{Version: 1, Bans: []JailEntry{
		{IP: "198.51.100.1", Jail: "manual", BannedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)},
		{IP: "198.51.100.2", Jail: "manual", BannedAt: now, ExpiresAt: now.Add(time.Hour)},
	}}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jail-bans.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if j.IsBanned("198.51.100.1") {
		t.Fatal("expired ban must not be restored")
	}
	if !j.IsBanned("198.51.100.2") {
		t.Fatal("active ban must be restored")
	}
}

func TestJailUnbanClearsPersistedBan(t *testing.T) {
	dir := t.TempDir()
	j, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	j.applyBan = func(string, time.Duration) {}
	if err := j.ManualBan("198.51.100.9", "unban test", "manual", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := j.Unban("198.51.100.9"); err != nil {
		t.Fatal(err)
	}
	j2, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if j2.IsBanned("198.51.100.9") {
		t.Fatal("unbanned IP came back after a restart")
	}
}

func TestJailReapplyBans(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	f := jailBansFile{Version: 1, Bans: []JailEntry{
		{IP: "10.20.30.10", Jail: "manual", BannedAt: now, ExpiresAt: now.Add(time.Hour)},
		{IP: "10.20.30.11", Jail: "manual", BannedAt: now, ExpiresAt: now.Add(time.Hour)},
	}}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "jail-bans.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	j, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	applied := make([]string, 0, 2)
	j.applyBan = func(ip string, d time.Duration) {
		if d <= 0 {
			t.Fatalf("reapplied ban for %s has non-positive duration %v", ip, d)
		}
		applied = append(applied, ip)
	}
	if n := j.ReapplyBans(); n != 2 {
		t.Fatalf("ReapplyBans = %d, want 2", n)
	}
	if len(applied) != 2 {
		t.Fatalf("applyBan calls = %v, want both bans", applied)
	}
}

func TestJailBanRecordIsPersisted(t *testing.T) {
	dir := t.TempDir()
	j, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	j.applyBan = func(string, time.Duration) {}
	j.cfg.WebKVMJail.MaxAttempts = 2
	j.cfg.WebKVMJail.WindowSec = 300
	j.cfg.WebKVMJail.BanDuration = 600
	r := "203.0.113.44"
	if j.RecordFailure(r, "bad") {
		t.Fatal("banned before the limit")
	}
	if !j.RecordFailure(r, "bad") {
		t.Fatal("expected a ban at the limit")
	}
	j2, err := New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !j2.IsBanned(r) {
		t.Fatal("brute-force ban did not survive a restart")
	}
}
