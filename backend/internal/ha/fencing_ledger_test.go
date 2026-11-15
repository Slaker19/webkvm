package ha

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func ledgerSt(cluster string, evs ...FenceEvent) PacemakerStatus {
	return PacemakerStatus{ClusterName: cluster, NodeNames: []string{"n1", "n2"}, FenceEvents: evs}
}

func okEv(target, completed string) FenceEvent {
	return FenceEvent{Target: target, Action: "reboot", Status: "success", Completed: completed, Delegate: "n1", Origin: "n1"}
}

func TestLedgerMergeDedupeAndRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	ev := okEv("n2", "2026-11-12 10:00:00 +00:00")
	got := MergeFencingLedger(dir, ledgerSt("c", ev, FenceEvent{Target: "n2", Action: "reboot", Status: "failed", Completed: "2026-11-12 09:00:00 +00:00"}), now)
	if len(got) != 1 || got[0].FirstSeen == "" || got[0].Completed != "2026-11-12T10:00:00Z" {
		t.Fatalf("%+v", got)
	}
	// Same event again (other layout) is not duplicated, first_seen preserved.
	got = MergeFencingLedger(dir, ledgerSt("c", okEv("n2", "2026-11-12 10:00:00 +00:00")), now.Add(time.Hour))
	if len(got) != 1 || got[0].FirstSeen != now.Format(time.RFC3339) {
		t.Fatalf("%+v", got)
	}
	// Restart: empty history, the old event survives.
	st := ledgerSt("c")
	got = MergeFencingLedger(dir, st, now.Add(2*time.Hour))
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	e := BuildFencingEvidenceWithLedger(st, nil, got)
	if !e.Tested || e.RecordedBy != "ledger" || e.TestedAt != "2026-11-12T10:00:00Z" || len(e.Events) != 1 || e.Events[0].Source != "ledger" {
		t.Fatalf("%+v", e)
	}
	// Live history for the same event is labelled history, not duplicated.
	e = BuildFencingEvidenceWithLedger(ledgerSt("c", ev), nil, got)
	if len(e.Events) != 1 || e.Events[0].Source != "history" || e.RecordedBy != "event" {
		t.Fatalf("%+v", e)
	}
	if fi, err := os.Stat(FencingLedgerPath(dir)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
}

func TestLedgerClusterChangeAndForeignNodes(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	MergeFencingLedger(dir, ledgerSt("c", okEv("n2", "2026-11-12 10:00:00 +00:00")), now)
	if got := MergeFencingLedger(dir, ledgerSt("other"), now); len(got) != 0 {
		t.Fatalf("different cluster must start fresh: %+v", got)
	}
	// Going back does not resurrect the archived ledger.
	if got := MergeFencingLedger(dir, ledgerSt("c"), now); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	d2 := t.TempDir()
	if got := MergeFencingLedger(d2, ledgerSt("c", okEv("ghost", "2026-11-12 10:00:00 +00:00")), now); len(got) != 0 {
		t.Fatalf("foreign node recorded: %+v", got)
	}
	if got := MergeFencingLedger(d2, PacemakerStatus{FenceEvents: []FenceEvent{okEv("n1", "2026-11-12 10:00:00 +00:00")}}, now); got != nil {
		t.Fatalf("unknown cluster must not record: %+v", got)
	}
	if _, err := os.Stat(FencingLedgerPath(d2)); err == nil {
		t.Fatal("nothing should be written")
	}
}

func TestLedgerCorruptOrOpenFileIgnored(t *testing.T) {
	dir := t.TempDir()
	p := FencingLedgerPath(dir)
	now := time.Now()
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := ledgerSt("c")
	if got := MergeFencingLedger(dir, st, now); len(got) != 0 || BuildFencingEvidenceWithLedger(st, nil, got).Tested {
		t.Fatalf("corrupt file became tested: %+v", got)
	}
	// A 0644 file with a valid event is not trusted.
	valid := `{"cluster_name":"c","events":[{"target":"n2","action":"reboot","status":"success","completed":"2026-11-12T10:00:00Z"}]}`
	_ = os.Remove(p)
	if err := os.WriteFile(p, []byte(valid), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MergeFencingLedger(dir, st, now); len(got) != 0 {
		t.Fatalf("0644 file trusted: %+v", got)
	}
	// A symlink is not trusted either.
	_ = os.Remove(p)
	target := filepath.Join(dir, "real.json")
	_ = os.WriteFile(target, []byte(valid), 0o600)
	if err := os.Symlink(target, p); err != nil {
		t.Fatal(err)
	}
	if got := MergeFencingLedger(dir, st, now); len(got) != 0 {
		t.Fatalf("symlink trusted: %+v", got)
	}
	// And a valid 0600 file is.
	_ = os.Remove(p)
	if err := os.WriteFile(p, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := MergeFencingLedger(dir, st, now); len(got) != 1 {
		t.Fatalf("%+v", got)
	}
}

func TestLedgerCap(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i := 0; i < 60; i++ {
		MergeFencingLedger(dir, ledgerSt("c", okEv("n2", fmt.Sprintf("2026-10-01 10:%02d:00 +00:00", i))), now)
	}
	got := MergeFencingLedger(dir, ledgerSt("c"), now)
	if len(got) != MaxLedgerEvents || got[0].Completed != "2026-10-01T10:59:00Z" {
		t.Fatalf("%d %+v", len(got), got[0])
	}
}

func TestLedgerDoesNotFeedDeviceRecordOrSkipUntested(t *testing.T) {
	st := PacemakerStatus{FenceDevices: 1, ClusterName: "c", NodeNames: []string{"n1"}}
	if e := BuildFencingEvidenceWithLedger(st, nil, nil); e.Tested {
		t.Fatal("no evidence must stay untested")
	}
}
