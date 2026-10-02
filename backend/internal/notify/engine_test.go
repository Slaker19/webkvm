package notify

import (
	"strings"
	"testing"
	"time"
)

// newTestEngine builds an engine backed by a real Notifier writing to a
// temp dir, with delivery disabled (Enabled:false) so evaluate() only
// exercises the rule logic and the in-memory event ring.
func newTestEngine(t *testing.T, sources Sources) (*AlertEngine, *Notifier) {
	t.Helper()
	n, err := New(t.TempDir(), Config{Enabled: false, DiskFreePercent: 10}, nil)
	if err != nil {
		t.Fatalf("notifier: %v", err)
	}
	return NewEngine(n, sources, time.Minute, time.Hour, nil), n
}

func subjects(events []AlertEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Subject)
	}
	return out
}

// TestVMDowntimeAlertsOnlyForDownVMs is the core contract of the
// VMState rule: a VM reported as running must never alert, and one
// reported as down must.
//
// Note the map only ever contains VMs the caller considers relevant
// (main.go filters to autostart-enabled VMs) — a VM deliberately shut
// off is expected to be absent from the map entirely, not present with
// running=false, which would fire a warning on every tick.
func TestVMDowntimeAlertsOnlyForDownVMs(t *testing.T) {
	e, n := newTestEngine(t, Sources{
		VMState: func() (map[string]bool, error) {
			return map[string]bool{"web-01": true, "db-01": false}, nil
		},
	})
	e.Evaluate()

	got := subjects(n.Events())
	var sawDown, sawUp bool
	for _, s := range got {
		if strings.Contains(s, "db-01") {
			sawDown = true
		}
		if strings.Contains(s, "web-01") {
			sawUp = true
		}
	}
	if !sawDown {
		t.Errorf("expected an alert for the down VM db-01, got %v", got)
	}
	if sawUp {
		t.Errorf("running VM web-01 must not alert, got %v", got)
	}
}

// TestAlertsAreDedupedWithinQuietWindow guards the flap protection: a
// VM that stays down across several evaluation ticks must notify once,
// not once per tick.
func TestAlertsAreDedupedWithinQuietWindow(t *testing.T) {
	e, n := newTestEngine(t, Sources{
		VMState: func() (map[string]bool, error) {
			return map[string]bool{"db-01": false}, nil
		},
	})
	e.Evaluate()
	e.Evaluate()
	e.Evaluate()

	count := 0
	for _, s := range subjects(n.Events()) {
		if strings.Contains(s, "db-01") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected the repeated alert to be deduped to 1, got %d", count)
	}
}

// TestDiskAlertRespectsThreshold verifies the low-disk rule fires only
// below the configured percentage.
func TestDiskAlertRespectsThreshold(t *testing.T) {
	cases := []struct {
		name      string
		freePct   int
		wantAlert bool
	}{
		{"well above threshold", 50, false},
		{"exactly at threshold", 10, false},
		{"below threshold", 5, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pct := c.freePct
			e, n := newTestEngine(t, Sources{
				DiskFreePercent: func() (int, error) { return pct, nil },
			})
			e.Evaluate()

			var fired bool
			for _, s := range subjects(n.Events()) {
				if strings.Contains(strings.ToLower(s), "disk") {
					fired = true
				}
			}
			if fired != c.wantAlert {
				t.Errorf("free=%d%%: alert fired=%v, want %v", c.freePct, fired, c.wantAlert)
			}
		})
	}
}

// TestBackupFailureAlertsOnlyOnError makes sure a successful run is
// silent.
func TestBackupFailureAlertsOnlyOnError(t *testing.T) {
	e, n := newTestEngine(t, Sources{
		LastBackupResult: func() map[string]string {
			return map[string]string{"nas": "success", "offsite": "error"}
		},
	})
	e.Evaluate()

	got := subjects(n.Events())
	var sawFail, sawOK bool
	for _, s := range got {
		if strings.Contains(s, "offsite") {
			sawFail = true
		}
		if strings.Contains(s, "nas") {
			sawOK = true
		}
	}
	if !sawFail {
		t.Errorf("expected an alert for the failed backup target, got %v", got)
	}
	if sawOK {
		t.Errorf("successful backup target must not alert, got %v", got)
	}
}

// TestNilSourcesAreSkipped confirms an unwired source is simply
// inactive rather than panicking the evaluation loop.
func TestNilSourcesAreSkipped(t *testing.T) {
	e, n := newTestEngine(t, Sources{})
	e.Evaluate()
	if len(n.Events()) != 0 {
		t.Errorf("no sources wired should produce no alerts, got %v", subjects(n.Events()))
	}
}

// TestStorageHealthAlertsFired verifies that storage degradation and SMART alerts
// are detected, dispatched, and deduped properly.
func TestStorageHealthAlertsFired(t *testing.T) {
	e, n := newTestEngine(t, Sources{
		StorageHealth: func() []StorageAlert {
			return []StorageAlert{
				{
					Level:   "critical",
					Subject: "SMART Failure on /dev/sda",
					Message: "Disk /dev/sda reported SMART status FAILED.",
				},
				{
					Level:   "critical",
					Subject: "ZFS Pool tank is DEGRADED",
					Message: "ZFS Pool tank is in DEGRADED state!",
				},
				{
					Level:   "critical",
					Subject: "Software RAID Array Degraded",
					Message: "mdadm array /dev/md0 is running degraded.",
				},
			}
		},
	})

	e.Evaluate()

	got := subjects(n.Events())
	if len(got) != 3 {
		t.Fatalf("expected 3 storage alerts, got %d: %v", len(got), got)
	}

	var sawSMART, sawZFS, sawRAID bool
	for _, s := range got {
		if strings.Contains(s, "SMART") {
			sawSMART = true
		}
		if strings.Contains(s, "ZFS") {
			sawZFS = true
		}
		if strings.Contains(s, "RAID") {
			sawRAID = true
		}
	}

	if !sawSMART || !sawZFS || !sawRAID {
		t.Errorf("missing expected storage alert categories in %v", got)
	}

	// Verify deduplication on subsequent evaluation
	e.Evaluate()
	if len(n.Events()) != 3 {
		t.Errorf("expected storage alerts to be deduped to 3, got %d", len(n.Events()))
	}
}
