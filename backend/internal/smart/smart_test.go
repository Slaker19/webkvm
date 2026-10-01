package smart

import (
	"context"
	"testing"
	"time"
)

const sampleNVMeJSON = `{
  "model_name": "Samsung SSD 980 PRO 1TB",
  "serial_number": "S5GXNX0T123456",
  "firmware_version": "5B2QGXA7",
  "smart_status": {
    "passed": true
  },
  "temperature": {
    "current": 42
  },
  "power_on_time": {
    "hours": 5120
  },
  "power_cycle_count": 210,
  "nvme_smart_health_information_log": {
    "critical_warning": 0,
    "temperature": 42,
    "available_spare": 100,
    "percentage_used": 3,
    "data_units_written": 20480000,
    "power_cycles": 210,
    "power_on_hours": 5120
  }
}`

const sampleATAJSON = `{
  "model_name": "ST3000DM008-2DM166",
  "serial_number": "Z500XYZW",
  "firmware_version": "CC26",
  "smart_status": {
    "passed": true
  },
  "temperature": {
    "current": 34
  },
  "power_on_time": {
    "hours": 18450
  },
  "power_cycle_count": 85,
  "ata_smart_attributes": {
    "table": [
      {
        "id": 5,
        "name": "Reallocated_Sector_Ct",
        "value": 100,
        "worst": 100,
        "raw": { "value": 0, "string": "0" }
      },
      {
        "id": 197,
        "name": "Current_Pending_Sector",
        "value": 100,
        "worst": 100,
        "raw": { "value": 0, "string": "0" }
      },
      {
        "id": 194,
        "name": "Temperature_Celsius",
        "value": 34,
        "worst": 45,
        "raw": { "value": 34, "string": "34" }
      }
    ]
  }
}`

const sampleFailingATAJSON = `{
  "model_name": "Dying Disk 500GB",
  "serial_number": "BAD123",
  "smart_status": {
    "passed": false
  },
  "temperature": {
    "current": 55
  },
  "ata_smart_attributes": {
    "table": [
      {
        "id": 5,
        "name": "Reallocated_Sector_Ct",
        "raw": { "value": 48 }
      },
      {
        "id": 197,
        "name": "Current_Pending_Sector",
        "raw": { "value": 12 }
      }
    ]
  }
}`

func TestParseJSON_NVMe(t *testing.T) {
	info, err := ParseJSON("/dev/nvme0n1", []byte(sampleNVMeJSON))
	if err != nil {
		t.Fatalf("ParseJSON failed: %v", err)
	}

	if info.Device != "/dev/nvme0n1" {
		t.Errorf("expected device /dev/nvme0n1, got %s", info.Device)
	}
	if info.Model != "Samsung SSD 980 PRO 1TB" {
		t.Errorf("expected model Samsung SSD 980 PRO 1TB, got %s", info.Model)
	}
	if !info.Healthy || info.Status != "PASSED" {
		t.Errorf("expected healthy PASSED, got healthy=%v, status=%s", info.Healthy, info.Status)
	}
	if info.TemperatureC != 42 {
		t.Errorf("expected temperature 42, got %d", info.TemperatureC)
	}
	if info.WearPercentage != 3 {
		t.Errorf("expected wear percentage 3, got %d", info.WearPercentage)
	}
	if info.PowerOnHours != 5120 {
		t.Errorf("expected 5120 hours, got %d", info.PowerOnHours)
	}
	// 20480000 * 512000 = 10485760000000 bytes
	if info.DataWrittenBytes == 0 {
		t.Errorf("expected non-zero data written bytes")
	}
}

func TestParseJSON_ATA(t *testing.T) {
	info, err := ParseJSON("/dev/sdb", []byte(sampleATAJSON))
	if err != nil {
		t.Fatalf("ParseJSON failed: %v", err)
	}

	if info.Device != "/dev/sdb" {
		t.Errorf("expected device /dev/sdb, got %s", info.Device)
	}
	if !info.Healthy || info.Status != "PASSED" {
		t.Errorf("expected healthy PASSED, got %v / %s", info.Healthy, info.Status)
	}
	if info.TemperatureC != 34 {
		t.Errorf("expected 34C, got %d", info.TemperatureC)
	}
	if info.ReallocatedSectors != 0 {
		t.Errorf("expected 0 reallocated sectors, got %d", info.ReallocatedSectors)
	}
}

func TestParseJSON_FailingATA(t *testing.T) {
	info, err := ParseJSON("/dev/sdc", []byte(sampleFailingATAJSON))
	if err != nil {
		t.Fatalf("ParseJSON failed: %v", err)
	}

	if info.Healthy || info.Status != "FAILED" {
		t.Errorf("expected unhealthy FAILED, got healthy=%v, status=%s", info.Healthy, info.Status)
	}
	if info.ReallocatedSectors != 48 {
		t.Errorf("expected 48 reallocated sectors, got %d", info.ReallocatedSectors)
	}
	if info.PendingSectors != 12 {
		t.Errorf("expected 12 pending sectors, got %d", info.PendingSectors)
	}
}

func TestValidateDevice(t *testing.T) {
	valid := []string{"/dev/sda", "sdb", "/dev/nvme0n1", "nvme1n2"}
	for _, v := range valid {
		clean, err := ValidateDevice(v)
		if err != nil {
			t.Errorf("expected %q to be valid, got error: %v", v, err)
		}
		if clean == "" {
			t.Errorf("expected non-empty clean path for %q", v)
		}
	}

	invalid := []string{"/dev/sda; rm -rf", "/dev/../etc/passwd", "md0", "zram0", "random_dev"}
	for _, inv := range invalid {
		if _, err := ValidateDevice(inv); err == nil {
			t.Errorf("expected %q to be rejected, got nil error", inv)
		}
	}
}

func TestCacheInvalidation(t *testing.T) {
	cacheMu.Lock()
	cache["/dev/sda"] = cacheEntry{
		info:      Info{Device: "/dev/sda", Status: "PASSED"},
		expiresAt: time.Now().Add(10 * time.Minute),
	}
	cacheMu.Unlock()

	InvalidateCache("/dev/sda")

	cacheMu.RLock()
	_, found := cache["/dev/sda"]
	cacheMu.RUnlock()

	if found {
		t.Error("expected /dev/sda to be evicted from cache")
	}
}

func TestProbe_LiveOrFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Probing /dev/sdb on live system
	info, err := Probe(ctx, "/dev/sdb")
	if err != nil && !IsAvailable() {
		t.Skip("smartctl not available")
	}
	if err == nil {
		if info.Device != "/dev/sdb" {
			t.Errorf("expected /dev/sdb, got %s", info.Device)
		}
		if info.Status == "" {
			t.Error("expected non-empty status")
		}
	}
}
