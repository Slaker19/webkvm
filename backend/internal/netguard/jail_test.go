package netguard

import (
	"testing"
	"time"
)

func TestJail_Allowlist(t *testing.T) {
	j := NewJail(3, 1*time.Minute, 5*time.Minute, nil)

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
}

func TestJail_BanAndUnban(t *testing.T) {
	j := NewJail(3, 1*time.Minute, 5*time.Minute, nil)

	attackerIP := "203.0.113.42"

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

	// Unban
	if err := j.Unban(attackerIP); err != nil {
		t.Fatalf("unban failed: %v", err)
	}

	if len(j.ListBanned()) != 0 {
		t.Fatalf("expected 0 banned IPs after unban")
	}
}
