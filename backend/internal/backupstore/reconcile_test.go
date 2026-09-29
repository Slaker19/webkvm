package backupstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReconcile_DryRunAndApply(t *testing.T) {
	dataDir := t.TempDir()
	store, err := New(dataDir)
	if err != nil {
		t.Fatalf("New store: %v", err)
	}

	backupDir := t.TempDir()
	tgt, err := store.CreateTarget("Local Test", backupDir, TargetLocal, "", nil)
	if err != nil {
		t.Fatalf("CreateTarget: %v", err)
	}

	// 1. Create a dummy orphan backup file on target directory
	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	orphanFile := "webkvm-host-" + ts + "-a1b2c3d4e5f6-qa1.tar.zst"
	filePath := filepath.Join(backupDir, orphanFile)
	if err := os.WriteFile(filePath, []byte("dummy-backup-content"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 2. Run Reconcile in dry-run mode
	report, err := store.Reconcile(tgt, false)
	if err != nil {
		t.Fatalf("Reconcile dry-run: %v", err)
	}
	if report.ScannedFiles != 1 {
		t.Errorf("Expected 1 scanned file, got %d", report.ScannedFiles)
	}
	if len(report.OrphanFiles) != 1 || report.OrphanFiles[0].Filename != orphanFile {
		t.Errorf("Expected orphan file %s, got %+v", orphanFile, report.OrphanFiles)
	}
	if report.AdoptedJobs != 0 {
		t.Errorf("Expected 0 adopted jobs in dry-run, got %d", report.AdoptedJobs)
	}

	// Verify no jobs were recorded during dry-run
	jobs := store.ListJobs(0)
	if len(jobs) != 0 {
		t.Fatalf("Expected 0 jobs in store after dry-run, got %d", len(jobs))
	}

	// 3. Run Reconcile in apply mode
	reportApply, err := store.Reconcile(tgt, true)
	if err != nil {
		t.Fatalf("Reconcile apply: %v", err)
	}
	if reportApply.AdoptedJobs != 1 {
		t.Errorf("Expected 1 adopted job in apply mode, got %d", reportApply.AdoptedJobs)
	}

	// Verify job was adopted into store
	jobs = store.ListJobs(0)
	if len(jobs) != 1 {
		t.Fatalf("Expected 1 job in store after apply, got %d", len(jobs))
	}
	if jobs[0].TargetID != tgt.ID {
		t.Errorf("Expected job target ID %s, got %s", tgt.ID, jobs[0].TargetID)
	}
	if len(jobs[0].Files) != 1 || jobs[0].Files[0].Filename != orphanFile {
		t.Errorf("Expected adopted job to contain file %s, got %+v", orphanFile, jobs[0].Files)
	}

	// 4. Reconcile again: now there should be 0 orphans and 0 ghosts
	reportClean, err := store.Reconcile(tgt, false)
	if err != nil {
		t.Fatalf("Reconcile clean: %v", err)
	}
	if len(reportClean.OrphanFiles) != 0 {
		t.Errorf("Expected 0 orphans after sync, got %d", len(reportClean.OrphanFiles))
	}
	if len(reportClean.GhostFiles) != 0 {
		t.Errorf("Expected 0 ghosts after sync, got %d", len(reportClean.GhostFiles))
	}
}
