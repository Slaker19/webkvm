package api

import (
	"sync"
	"testing"

	"webkvm/internal/models"
	"webkvm/internal/user"
)

// TestRecheckDeployQuota_Race simulates the V13-DATA-01 condition with a
// `-race` run: a user whose quota is nearly exhausted starts a deploy; the
// REAL on-disk size of the materialized volume pushes them over the limit.
// Many goroutines evaluate the same decision concurrently and must all
// agree (deterministic, no shared mutable state).
func TestRecheckDeployQuota_Race(t *testing.T) {
	// Scenario: user at 9/10 GB (global), deploying a disk whose real
	// footprint is 2 GB -> 11 GB > 10 -> quota exceeded (must roll back).
	exceedQuota := models.Quota{MaxDiskGB: 10}
	cur := map[string]int64{"webkvm-disks": 9}
	add := map[string]int64{"webkvm-disks": 2}

	// Boundary control: real footprint of exactly 1 GB -> 9+1 = 10, NOT
	// > 10 -> allowed (the estimate was accurate).
	okQuota := models.Quota{MaxDiskGB: 10}
	okCur := map[string]int64{"webkvm-disks": 9}
	okAdd := map[string]int64{"webkvm-disks": 1}

	const goroutines = 48
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	okErrs := make([]error, goroutines)
	wg.Add(goroutines * 2)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			errs[i] = enforceDiskQuota("t01", exceedQuota, cur, add)
		}(i)
		go func(i int) {
			defer wg.Done()
			okErrs[i] = enforceDiskQuota("t01", okQuota, okCur, okAdd)
		}(i)
	}
	wg.Wait()

	// Every goroutine must see the SAME verdict.
	for i := 0; i < goroutines; i++ {
		if errs[i] == nil {
			t.Fatalf("goroutine %d: over-quota deploy was allowed (TOCTOU!)", i)
		}
		if okErrs[i] != nil {
			t.Fatalf("goroutine %d: boundary case rejected: %v", i, okErrs[i])
		}
	}

	// Per-pool cap variant: same user, per-pool limit of 10 on the pool.
	poolQ := models.Quota{PoolQuotas: map[string]int{"webkvm-disks": 10}}
	if err := enforceDiskQuota("t01", poolQ, cur, add); err == nil {
		t.Error("per-pool over-quota deploy was allowed")
	}
}

// TestRecheckDiskQuota_FailClosedNoLibvirt: sin libvirt, el re-check debe
// fallar cerrado (error explícito), nunca conceder ni paniquear.
func TestRecheckDiskQuota_FailClosedNoLibvirt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")
	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := us.Create(models.CreateUserRequest{
		Username: "quota-user", Password: "Str0ng-Pass#2026", Role: models.RoleOperator,
		Quota: models.Quota{MaxDiskGB: 10},
	}); err != nil {
		t.Fatal(err)
	}

	h := &Handler{userStore: us} // lv == nil
	err = h.recheckDiskQuota("quota-user", "webkvm-disks", "vm1.qcow2")
	if err == nil {
		t.Fatal("recheck without libvirt must fail closed, got nil")
	}
}

// TestRecheckDiskQuota_AdminExempt: un admin nunca pasa por el re-check
// (exención por rol), incluso sin libvirt.
func TestRecheckDiskQuota_AdminExempt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")
	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{userStore: us} // admin seeded by NewStore, lv nil
	if err := h.recheckDiskQuota("admin", "webkvm-disks", "vm1.qcow2"); err != nil {
		t.Fatalf("admin must be exempt from re-check: %v", err)
	}
}
