package backupstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestChainStore(t *testing.T) *ChainStore {
	t.Helper()
	dir := t.TempDir()
	return NewChainStore(dir, "test-target")
}

func TestChainStoreLoadMissing(t *testing.T) {
	cs := newTestChainStore(t)
	chains, err := cs.Load()
	if err != nil {
		t.Fatalf("Load on missing file: %v", err)
	}
	if chains != nil {
		t.Fatalf("expected nil chains, got %v", chains)
	}
}

func TestChainStoreAppendAndGet(t *testing.T) {
	cs := newTestChainStore(t)
	now := time.Now().UTC()

	_, err := cs.AppendEntry("vm-1", "test-target", "TestVM", CheckpointEntry{
		Name:       "chk-0",
		JobID:      "job-1",
		BackupFile: "webkvm-host-20260101T000000.000000000Z-abc123-TestVM.qcow2",
		Parent:     "",
		Mode:       "full",
		SizeBytes:  1024,
		CreatedAt:  now,
	})
	if err != nil {
		t.Fatalf("AppendEntry: %v", err)
	}

	chain, err := cs.GetChain("vm-1")
	if err != nil {
		t.Fatalf("GetChain: %v", err)
	}
	if chain == nil {
		t.Fatal("expected chain, got nil")
	}
	if chain.VMID != "vm-1" {
		t.Errorf("VMID = %q, want vm-1", chain.VMID)
	}
	if chain.BaseFile == "" {
		t.Error("BaseFile should be set after first entry")
	}
	if chain.Checkpoint != "chk-0" {
		t.Errorf("Checkpoint = %q, want chk-0", chain.Checkpoint)
	}
	if len(chain.Entries) != 1 {
		t.Fatalf("len(Entries) = %d, want 1", len(chain.Entries))
	}
}

func TestChainStoreIncrementalAppend(t *testing.T) {
	cs := newTestChainStore(t)
	now := time.Now().UTC()

	_, err := cs.AppendEntry("vm-1", "test-target", "TestVM", CheckpointEntry{
		Name: "chk-0", JobID: "job-1", BackupFile: "base.qcow2",
		Parent: "", Mode: "full", SizeBytes: 100, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("AppendEntry base: %v", err)
	}

	_, err = cs.AppendEntry("vm-1", "test-target", "TestVM", CheckpointEntry{
		Name: "chk-1", JobID: "job-2", BackupFile: "inc1.qcow2",
		Parent: "chk-0", Mode: "incremental", SizeBytes: 50, CreatedAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("AppendEntry inc: %v", err)
	}

	latest, err := cs.LatestCheckpoint("vm-1")
	if err != nil {
		t.Fatalf("LatestCheckpoint: %v", err)
	}
	if latest != "chk-1" {
		t.Errorf("LatestCheckpoint = %q, want chk-1", latest)
	}

	chain, _ := cs.GetChain("vm-1")
	if len(chain.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(chain.Entries))
	}
	if chain.TotalSize() != 150 {
		t.Errorf("TotalSize = %d, want 150", chain.TotalSize())
	}
}

func TestChainFlattenOrder(t *testing.T) {
	chain := &Chain{
		Entries: []CheckpointEntry{
			{Name: "chk-0", BackupFile: "base.qcow2", Parent: ""},
			{Name: "chk-1", BackupFile: "inc1.qcow2", Parent: "chk-0"},
			{Name: "chk-2", BackupFile: "inc2.qcow2", Parent: "chk-1"},
		},
		Checkpoint: "chk-2",
	}

	tests := []struct {
		name       string
		checkpoint string
		want       []string
	}{
		{"latest", "", []string{"base.qcow2", "inc1.qcow2", "inc2.qcow2"}},
		{"middle", "chk-1", []string{"base.qcow2", "inc1.qcow2"}},
		{"base", "chk-0", []string{"base.qcow2"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chain.FlattenOrder(tt.checkpoint)
			if len(got) != len(tt.want) {
				t.Fatalf("FlattenOrder(%q) = %v, want %v", tt.checkpoint, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("FlattenOrder(%q)[%d] = %q, want %q", tt.checkpoint, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestChainFlattenOrderBrokenParent(t *testing.T) {
	// A chain with a missing parent should stop at the break.
	chain := &Chain{
		Entries: []CheckpointEntry{
			{Name: "chk-1", BackupFile: "inc1.qcow2", Parent: "chk-missing"},
		},
		Checkpoint: "chk-1",
	}
	got := chain.FlattenOrder("")
	if len(got) != 1 || got[0] != "inc1.qcow2" {
		t.Errorf("FlattenOrder with broken parent = %v, want [inc1.qcow2]", got)
	}
}

func TestChainDelete(t *testing.T) {
	cs := newTestChainStore(t)
	now := time.Now().UTC()

	_, _ = cs.AppendEntry("vm-1", "test-target", "TestVM", CheckpointEntry{
		Name: "chk-0", BackupFile: "base.qcow2", Mode: "full", CreatedAt: now,
	})
	_, _ = cs.AppendEntry("vm-2", "test-target", "OtherVM", CheckpointEntry{
		Name: "chk-0", BackupFile: "other.qcow2", Mode: "full", CreatedAt: now,
	})

	deleted, err := cs.DeleteChain("vm-1")
	if err != nil {
		t.Fatalf("DeleteChain: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true for existing chain")
	}

	chain, _ := cs.GetChain("vm-1")
	if chain != nil {
		t.Error("expected nil chain after delete")
	}

	// vm-2 should still exist.
	chain2, _ := cs.GetChain("vm-2")
	if chain2 == nil {
		t.Error("vm-2 chain should survive vm-1 deletion")
	}

	// Deleting again returns false.
	deleted, _ = cs.DeleteChain("vm-1")
	if deleted {
		t.Error("expected deleted=false for already-deleted chain")
	}
}

func TestChainPersistenceAcrossReload(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()

	cs1 := NewChainStore(dir, "target-x")
	_, err := cs1.AppendEntry("vm-9", "target-x", "PersistVM", CheckpointEntry{
		Name: "chk-0", BackupFile: "p.qcow2", Mode: "full", CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("first AppendEntry: %v", err)
	}

	// Simulate a backend restart: new ChainStore on the same dir.
	cs2 := NewChainStore(dir, "target-x")
	chain, err := cs2.GetChain("vm-9")
	if err != nil {
		t.Fatalf("GetChain after reload: %v", err)
	}
	if chain == nil {
		t.Fatal("chain lost across reload")
	}
	if chain.VMName != "PersistVM" {
		t.Errorf("VMName = %q, want PersistVM", chain.VMName)
	}
}

func TestChainCorruptFileDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backup", "chains-bad.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	cs := &ChainStore{path: path}
	_, err := cs.Load()
	if err == nil {
		t.Error("expected error on corrupt JSON, got nil")
	}
}

func TestChainAllFiles(t *testing.T) {
	chain := &Chain{
		Entries: []CheckpointEntry{
			{Name: "chk-0", BackupFile: "a.qcow2"},
			{Name: "chk-1", BackupFile: "b.qcow2"},
		},
	}
	files := chain.AllFiles()
	if len(files) != 2 || files[0] != "a.qcow2" || files[1] != "b.qcow2" {
		t.Errorf("AllFiles = %v, want [a.qcow2 b.qcow2]", files)
	}
}

func TestSortChainsByUpdatedAt(t *testing.T) {
	now := time.Now().UTC()
	chains := []*Chain{
		{VMID: "vm-old", UpdatedAt: now.Add(-time.Hour)},
		{VMID: "vm-new", UpdatedAt: now},
		{VMID: "vm-mid", UpdatedAt: now.Add(-time.Minute)},
	}
	SortChainsByUpdatedAt(chains)
	if chains[0].VMID != "vm-new" || chains[1].VMID != "vm-mid" || chains[2].VMID != "vm-old" {
		t.Errorf("sort order = [%s %s %s], want [vm-new vm-mid vm-old]",
			chains[0].VMID, chains[1].VMID, chains[2].VMID)
	}
}
