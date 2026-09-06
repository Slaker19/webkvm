package audit

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRotate_MaxBackupsEnforced: tras rotar más veces que maxBackups,
// el fichero más antiguo se descarta y nunca hay más de maxBackups
// backups en disco (V12-OPS-05).
func TestRotate_MaxBackupsEnforced(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.Close() }()

	// Rotate more times than maxBackups (8 > 7). Each rotation shifts the
	// chain and drops the oldest.
	for i := 0; i < maxBackups+1; i++ {
		logger.Log(Entry{User: "u", Action: "rotate"})
		if err := logger.rotateLocked(); err != nil {
			t.Fatalf("rotate %d: %v", i, err)
		}
	}

	// Backups .1..maxBackups must exist; .(maxBackups+1) must not.
	for i := 1; i <= maxBackups; i++ {
		if _, err := os.Stat(path + "." + strconv.Itoa(i)); err != nil {
			t.Errorf("backup .%d missing: %v", i, err)
		}
	}
	if _, err := os.Stat(path + "." + strconv.Itoa(maxBackups+1)); !os.IsNotExist(err) {
		t.Errorf("backup .%d should have been dropped", maxBackups+1)
	}

	// The current file must be open and writable again after the chain.
	logger.Log(Entry{User: "u", Action: "after"})
	if err := logger.w.Flush(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Error("current audit file is empty after rotation chain")
	}
}

// TestRotate_EntryOrderPreserved: las rotaciones preservan todos los
// registros (nada se pierde por buffers) y el orden se mantiene desde el
// backup más antiguo hasta el fichero actual (V12-OPS-05 durability).
func TestRotate_EntryOrderPreserved(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}

	const batches = 5
	const perBatch = 3
	// Escribir varias tandas con rotación manual entre medias para que
	// los registros queden repartidos por los backups y el fichero actual.
	for b := 0; b < batches; b++ {
		for e := 0; e < perBatch; e++ {
			logger.Log(Entry{User: "u", Action: "test", Detail: map[string]any{"batch": b, "entry": e}})
		}
		if err := logger.rotateLocked(); err != nil {
			t.Fatalf("rotate %d: %v", b, err)
		}
	}

	entries, total, err := logger.List(ListOptions{}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != batches*perBatch {
		t.Errorf("total = %d, want %d (entries lost across rotation)", total, batches*perBatch)
	}
	// Newest first: last entry is batch=batches-1, entry=perBatch-1.
	if len(entries) > 0 {
		last := entries[0]
		batch := int(last.Detail["batch"].(float64))
		entry := int(last.Detail["entry"].(float64))
		if batch != batches-1 || entry != perBatch-1 {
			t.Errorf("newest entry = batch:%d entry:%d, want batch=%d entry=%d", batch, entry, batches-1, perBatch-1)
		}
	}
	_ = logger.Close()
}

// TestClose_IsIdempotent: Close puede llamarse varias veces sin error y
// libera el descriptor (V12-OPS-05).
func TestClose_IsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	logger.Log(Entry{User: "u", Action: "x"})
	if err := logger.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// El contenido debe estar en disco (flushed + fsynced).
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "x") {
		t.Error("audit entry missing after Close")
	}
}

// TestList_ReadsAllBackups: List reconstruye los registros a través de
// todos los backups, no solo .1 (V12-OPS-05).
func TestList_ReadsAllBackups(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "audit.log")
	logger, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		logger.Log(Entry{User: "u", Action: "fill"})
		if err := logger.rotateLocked(); err != nil {
			t.Fatal(err)
		}
	}
	logger.Log(Entry{User: "u", Action: "current"})

	_, total, err := logger.List(ListOptions{}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Errorf("total = %d, want 4 (List must read every backup + current)", total)
	}
	_ = logger.Close()
}
