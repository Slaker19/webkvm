package api

import (
	"testing"

	"webkvm/internal/models"
)

func TestEnforceDiskQuota_Global(t *testing.T) {
	q := models.Quota{MaxDiskGB: 10}
	if err := enforceDiskQuota("u", q, map[string]int64{"default": 8}, map[string]int64{"default": 1}); err != nil {
		t.Fatalf("8+1=9 should fit in 10: %v", err)
	}
	if err := enforceDiskQuota("u", q, map[string]int64{"default": 8}, map[string]int64{"default": 4}); err == nil {
		t.Fatal("8+4=12 should exceed 10")
	}
}

func TestEnforceDiskQuota_PerPool(t *testing.T) {
	q := models.Quota{PoolQuotas: map[string]int{"fast": 10}}
	// Adding to the capped pool violates only that pool.
	if err := enforceDiskQuota("u", q, map[string]int64{"fast": 5}, map[string]int64{"fast": 6}); err == nil {
		t.Fatal("fast 5+6=11 should exceed pool cap 10")
	}
	// Adding to a different, uncapped pool is fine even if global would
	// be huge (no global cap set).
	if err := enforceDiskQuota("u", q, map[string]int64{"fast": 5}, map[string]int64{"slow": 100}); err != nil {
		t.Fatalf("uncapped pool should be allowed: %v", err)
	}
	// Boundary: exactly at the cap is allowed.
	if err := enforceDiskQuota("u", q, map[string]int64{"fast": 5}, map[string]int64{"fast": 5}); err != nil {
		t.Fatalf("5+5=10 equals cap, should be allowed: %v", err)
	}
}

func TestEnforceDiskQuota_MultiPoolVM(t *testing.T) {
	// User owns 5GB on "fast" and 5GB on "slow"; only "slow" is capped
	// at 10. Powering on a VM whose disk lives on "slow" must be blocked
	// when it would push "slow" over, even though "fast" is untouched.
	q := models.Quota{PoolQuotas: map[string]int{"fast": 10, "slow": 10}}
	if err := enforceDiskQuota("u", q,
		map[string]int64{"fast": 5, "slow": 5},
		map[string]int64{"slow": 6}); err == nil {
		t.Fatal("slow 5+6=11 should exceed its pool cap")
	}
}

func TestEnforceDiskQuota_ShrinkAllowed(t *testing.T) {
	q := models.Quota{MaxDiskGB: 10}
	if err := enforceDiskQuota("u", q, map[string]int64{"default": 8}, map[string]int64{"default": -2}); err != nil {
		t.Fatalf("shrinking must be allowed: %v", err)
	}
}

func TestEnforceDiskQuota_PoolQuotaOnlyEnablesQuota(t *testing.T) {
	// A user with only per-pool limits (MaxDiskGB==0) must still be
	// subject to quota — Enabled() must report true.
	q := models.Quota{PoolQuotas: map[string]int{"fast": 5}}
	if !q.Enabled() {
		t.Fatal("quota with only PoolQuotas should be Enabled()")
	}
	if err := enforceDiskQuota("u", q, map[string]int64{"fast": 5}, map[string]int64{"fast": 1}); err == nil {
		t.Fatal("per-pool limit should be enforced even without global cap")
	}
}

func TestEnforceStartQuota_RunningFootprint(t *testing.T) {
	q := models.Quota{MaxVCPUs: 4, MaxRAMMB: 8192}
	// Running 2 vCPU + starting a 2-vCPU VM == 4 (allowed, not over).
	if err := enforceStartQuota("u", q, 2, 4096, 2, 4096); err != nil {
		t.Fatalf("exactly at limit should be allowed: %v", err)
	}
	// Running 2 + starting 4 == 6 > 4 -> blocked.
	if err := enforceStartQuota("u", q, 2, 4096, 4, 4096); err == nil {
		t.Fatal("vCPU 2+4=6 should exceed 4")
	}
	// RAM path.
	if err := enforceStartQuota("u", q, 2, 4096, 2, 8192); err == nil {
		t.Fatal("RAM 4096+8192=12288 should exceed 8192")
	}
}

func TestVmTotalDiskByPool_MultiDisk(t *testing.T) {
	vm := models.VM{
		Disks: []models.DiskInfo{
			{Pool: "fast", SizeGB: 20},
			{Pool: "slow", SizeGB: 35},
		},
	}
	byPool := vmTotalDiskByPool(vm)
	if byPool["fast"] != 20 || byPool["slow"] != 35 {
		t.Fatalf("per-pool totals wrong: %v", byPool)
	}
	if got := vmTotalDiskGB(vm); got != 55 {
		t.Fatalf("vmTotalDiskGB = %d, want 55", got)
	}
	// Fallback when the per-disk list is empty.
	empty := models.VM{DiskGB: 12}
	if got := vmTotalDiskGB(empty); got != 12 {
		t.Fatalf("fallback vmTotalDiskGB = %d, want 12", got)
	}
}
