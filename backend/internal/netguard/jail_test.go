package netguard

import (
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

	// Localhost should never be banned
	for i := 0; i < 10; i++ {
		banned := j.RecordFailure("127.0.0.1", "bad password")
		if banned {
			t.Fatalf("127.0.0.1 should be exempt from bans")
		}
	}

	// 192.168.1.100 should also be exempt by default
	for i := 0; i < 10; i++ {
		banned := j.RecordFailure("192.168.1.100", "bad token")
		if banned {
			t.Fatalf("192.168.x.x should be exempt from bans by default")
		}
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

	// Reject banning whitelisted IP
	err = j.ManualBan("192.168.1.50", "manual test", "manual", time.Hour)
	if err == nil {
		t.Fatal("expected error banning whitelisted IP")
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
