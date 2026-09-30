package netstore

import (
	"path/filepath"
	"testing"
	"time"

	"webkvm/internal/models"
)

func TestNetstore_SaveGetDeleteAll(t *testing.T) {
	dir := t.TempDir()
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	// 1. Initial store should be empty
	if all := store.All(); len(all) != 0 {
		t.Fatalf("expected 0 records, got %d", len(all))
	}

	// 2. Save a NAT bridge
	rec1 := Record{
		Name:      "vmbr10",
		Kind:      "nat",
		CIDR:      "192.168.100.1/24",
		DHCPStart: "192.168.100.10",
		DHCPEnd:   "192.168.100.50",
		DNS:       []string{"1.1.1.1", "8.8.8.8"},
		MTU:       1500,
		Reservations: []models.DHCPReservation{
			{MAC: "52:54:00:12:34:56", IP: "192.168.100.20", Name: "box1"},
		},
	}
	if err := store.Save(rec1); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 3. Get record
	got, ok := store.Get("vmbr10")
	if !ok {
		t.Fatalf("expected vmbr10 to exist")
	}
	if got.Name != rec1.Name || got.Kind != rec1.Kind || got.CIDR != rec1.CIDR || got.MTU != 1500 {
		t.Fatalf("mismatched record fields: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt should have been auto-populated")
	}
	if len(got.Reservations) != 1 || got.Reservations[0].Name != "box1" {
		t.Fatalf("mismatched reservations: %+v", got.Reservations)
	}

	// 4. Persistence across reopen
	store2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen failed: %v", err)
	}
	got2, ok := store2.Get("vmbr10")
	if !ok {
		t.Fatalf("expected vmbr10 in reopened store")
	}
	if got2.Name != rec1.Name || len(got2.Reservations) != 1 {
		t.Fatalf("mismatched data after reopen: %+v", got2)
	}

	// 5. All records
	rec2 := Record{
		Name:      "vmbr20",
		Kind:      "direct",
		Interface: "eth1",
		CIDR:      "192.168.1.100/24",
		Gateway:   "192.168.1.1",
		DNS:       []string{"1.1.1.1", "1.0.0.1"},
		MovedIPv4: "192.168.1.50/24",
		CreatedAt: time.Now(),
	}
	if err := store.Save(rec2); err != nil {
		t.Fatalf("Save rec2 failed: %v", err)
	}

	gotDirect, ok := store.Get("vmbr20")
	if !ok || gotDirect.Gateway != "192.168.1.1" || len(gotDirect.DNS) != 2 || gotDirect.CIDR != "192.168.1.100/24" {
		t.Fatalf("direct record mismatch: %+v", gotDirect)
	}

	all := store.All()
	if len(all) != 2 {
		t.Fatalf("expected 2 records, got %d", len(all))
	}

	// 6. Delete
	if err := store.Delete("vmbr10"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}
	if _, ok := store.Get("vmbr10"); ok {
		t.Fatalf("vmbr10 should have been deleted")
	}
	if len(store.All()) != 1 {
		t.Fatalf("expected 1 record remaining, got %d", len(store.All()))
	}

	// Deleting nonexistent record should succeed (idempotent)
	if err := store.Delete("nonexistent"); err != nil {
		t.Fatalf("idempotent delete failed: %v", err)
	}
}

func TestNetstore_SaveLockedEnsuresDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "sub", "dir")
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}

	rec := Record{Name: "testbr", Kind: "isolated"}
	if err := store.Save(rec); err != nil {
		t.Fatalf("Save in nested dir failed: %v", err)
	}

	got, ok := store.Get("testbr")
	if !ok || got.Kind != "isolated" {
		t.Fatalf("failed to retrieve record from nested dir: %+v", got)
	}
}
