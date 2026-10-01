package smart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var safeDiskDeviceRE = regexp.MustCompile(`^/dev/(sd[a-z]+|nvme[0-9]+n[0-9]+|vd[a-z]+|hd[a-z]+|xvd[a-z]+|mmcblk[0-9]+)$`)

// Info holds parsed S.M.A.R.T. health and telemetry data for a disk.
type Info struct {
	Device           string `json:"device"`
	Model            string `json:"model,omitempty"`
	Serial           string `json:"serial,omitempty"`
	Firmware         string `json:"firmware,omitempty"`
	Protocol         string `json:"protocol,omitempty"` // "ATA", "NVMe", etc.
	Available        bool   `json:"available"`
	Healthy          bool   `json:"healthy"`
	Status           string `json:"status"` // "PASSED", "FAILED", "UNKNOWN"
	TemperatureC     int    `json:"temperature_c"`
	PowerOnHours     int64  `json:"power_on_hours"`
	PowerCycles      int64  `json:"power_cycles"`
	WearPercentage   int    `json:"wear_percentage"`          // 0-100%, -1 if not supported
	DataWrittenBytes uint64 `json:"data_written_bytes"`       // in bytes (TBW)
	ReallocatedSectors int64 `json:"reallocated_sectors"`     // -1 if not applicable
	PendingSectors     int64 `json:"pending_sectors"`         // -1 if not applicable
	CriticalWarning    int   `json:"critical_warning"`        // NVMe critical warning bitmask
	SelfTestStatus     string `json:"self_test_status,omitempty"`
	FetchedAt          int64  `json:"fetched_at"`
}

type cacheEntry struct {
	info      Info
	expiresAt time.Time
}

var (
	cacheMu sync.RWMutex
	cache   = make(map[string]cacheEntry)
)

const defaultCacheTTL = 60 * time.Second

// IsAvailable reports whether the `smartctl` binary exists on the system.
func IsAvailable() bool {
	_, err := exec.LookPath("smartctl")
	return err == nil
}

// ListPhysicalDisks discovers physical disks present on the system.
func ListPhysicalDisks() []string {
	entries, err := filepath.Glob("/sys/block/*/device")
	if err != nil || len(entries) == 0 {
		return []string{"/dev/sda", "/dev/sdb", "/dev/nvme0n1"}
	}
	var res []string
	for _, e := range entries {
		parts := strings.Split(filepath.Clean(e), string(filepath.Separator))
		if len(parts) >= 2 {
			devName := parts[len(parts)-2]
			devPath := "/dev/" + devName
			if _, valErr := ValidateDevice(devPath); valErr == nil {
				res = append(res, devPath)
			}
		}
	}
	if len(res) == 0 {
		return []string{"/dev/sda", "/dev/sdb", "/dev/nvme0n1"}
	}
	return res
}

// ValidateDevice checks that the device path is a valid physical block device.
func ValidateDevice(devPath string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(devPath))
	if !strings.HasPrefix(clean, "/dev/") {
		clean = "/dev/" + clean
	}
	if !safeDiskDeviceRE.MatchString(clean) || strings.Contains(clean, "..") {
		return "", fmt.Errorf("invalid or untrusted disk device path %q: must match /dev/sd[a-z], /dev/nvmeXnY, etc", devPath)
	}
	return clean, nil
}

// Probe returns the SMART health report for the given disk.
// Results are cached for 60 seconds to avoid spinning up idle disks.
func Probe(ctx context.Context, devPath string) (Info, error) {
	cleanPath, err := ValidateDevice(devPath)
	if err != nil {
		return Info{}, err
	}

	cacheMu.RLock()
	entry, ok := cache[cleanPath]
	cacheMu.RUnlock()
	if ok && time.Now().Before(entry.expiresAt) {
		return entry.info, nil
	}

	if !IsAvailable() {
		return Info{
			Device:    cleanPath,
			Available: false,
			Status:    "UNKNOWN",
		}, errors.New("smartctl command not found on host")
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, "smartctl", "-j", "-i", "-H", "-A", cleanPath)
	var out bytes.Buffer
	cmd.Stdout = &out
	// smartctl returns non-zero exit codes for warnings (e.g. past errors),
	// so we inspect stdout as long as output is produced.
	_ = cmd.Run()

	if out.Len() == 0 {
		return Info{
			Device:    cleanPath,
			Available: false,
			Status:    "UNKNOWN",
		}, fmt.Errorf("smartctl returned no output for %s", cleanPath)
	}

	info, err := ParseJSON(cleanPath, out.Bytes())
	if err != nil {
		return Info{
			Device:    cleanPath,
			Available: false,
			Status:    "UNKNOWN",
		}, err
	}

	cacheMu.Lock()
	cache[cleanPath] = cacheEntry{
		info:      info,
		expiresAt: time.Now().Add(defaultCacheTTL),
	}
	cacheMu.Unlock()

	return info, nil
}

// InvalidateCache clears cached SMART data for a disk or all disks if devPath is empty.
func InvalidateCache(devPath string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if devPath == "" {
		cache = make(map[string]cacheEntry)
		return
	}
	clean := filepath.Clean(strings.TrimSpace(devPath))
	delete(cache, clean)
}

// RunSelfTest initiates a SMART self-test ("short", "long", "abort").
func RunSelfTest(ctx context.Context, devPath, testType string) error {
	cleanPath, err := ValidateDevice(devPath)
	if err != nil {
		return err
	}

	testType = strings.ToLower(strings.TrimSpace(testType))
	var arg string
	switch testType {
	case "short":
		arg = "--test=short"
	case "long", "extended":
		arg = "--test=long"
	case "abort", "stop", "-x":
		arg = "-X"
	default:
		return fmt.Errorf("unsupported self-test type %q (use 'short', 'long', or 'abort')", testType)
	}

	if !IsAvailable() {
		return errors.New("smartctl command not found on host")
	}

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	// If a test is requested (short or long), pass -t force or abort the current running one
	// first to prevent "Can't start self-test without aborting current test" error.
	if arg == "--test=short" || arg == "--test=long" {
		_ = exec.CommandContext(cctx, "smartctl", "-X", cleanPath).Run()
	}

	out, err := exec.CommandContext(cctx, "smartctl", arg, cleanPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("smartctl %s failed: %s (%w)", arg, strings.TrimSpace(string(out)), err)
	}

	InvalidateCache(cleanPath)
	return nil
}

// rawSmartJSON models the subset of smartctl JSON output we care about.
type rawSmartJSON struct {
	ModelName    string `json:"model_name"`
	SerialNumber string `json:"serial_number"`
	FirmwareVer  string `json:"firmware_version"`
	Device       struct {
		Name     string `json:"name"`
		Protocol string `json:"protocol"`
	} `json:"device"`
	SmartStatus struct {
		Passed bool `json:"passed"`
	} `json:"smart_status"`
	Temperature struct {
		Current int `json:"current"`
	} `json:"temperature"`
	PowerOnTime struct {
		Hours int64 `json:"hours"`
	} `json:"power_on_time"`
	PowerCycleCount int64 `json:"power_cycle_count"`
	// NVMe specific
	NVMeLog *struct {
		CriticalWarning  int    `json:"critical_warning"`
		Temperature      int    `json:"temperature"`
		AvailableSpare   int    `json:"available_spare"`
		PercentageUsed   int    `json:"percentage_used"`
		DataUnitsWritten uint64 `json:"data_units_written"`
		PowerCycles      int64  `json:"power_cycles"`
		PowerOnHours     int64  `json:"power_on_hours"`
	} `json:"nvme_smart_health_information_log"`
	// ATA specific attributes
	ATASmartAttributes *struct {
		Table []struct {
			ID    int    `json:"id"`
			Name  string `json:"name"`
			Value int    `json:"value"`
			Worst int    `json:"worst"`
			Raw   struct {
				Value int64  `json:"value"`
				Str   string `json:"string"`
			} `json:"raw"`
		} `json:"table"`
	} `json:"ata_smart_attributes"`
	AtaSelfTest struct {
		Status struct {
			Passed bool   `json:"passed"`
			String string `json:"string"`
		} `json:"status"`
	} `json:"ata_smart_data"`
}

// ParseJSON parses raw smartctl JSON bytes into Info.
func ParseJSON(devPath string, data []byte) (Info, error) {
	var raw rawSmartJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return Info{}, fmt.Errorf("unmarshal smartctl json: %w", err)
	}

	info := Info{
		Device:             devPath,
		Model:              raw.ModelName,
		Serial:             raw.SerialNumber,
		Firmware:           raw.FirmwareVer,
		Protocol:           raw.Device.Protocol,
		Available:          true,
		Healthy:            raw.SmartStatus.Passed,
		TemperatureC:       raw.Temperature.Current,
		PowerOnHours:       raw.PowerOnTime.Hours,
		PowerCycles:        raw.PowerCycleCount,
		WearPercentage:     -1,
		ReallocatedSectors: -1,
		PendingSectors:     -1,
		FetchedAt:          time.Now().Unix(),
	}

	if raw.SmartStatus.Passed {
		info.Status = "PASSED"
	} else {
		info.Status = "FAILED"
	}

	// Handle NVMe telemetry
	if raw.NVMeLog != nil {
		if info.Protocol == "" {
			info.Protocol = "NVMe"
		}
		info.CriticalWarning = raw.NVMeLog.CriticalWarning
		info.WearPercentage = raw.NVMeLog.PercentageUsed
		// Each data unit written represents 512,000 bytes (1000 * 512B blocks)
		info.DataWrittenBytes = raw.NVMeLog.DataUnitsWritten * 512000
		if raw.NVMeLog.Temperature > 0 && info.TemperatureC == 0 {
			info.TemperatureC = raw.NVMeLog.Temperature
		}
		if raw.NVMeLog.PowerOnHours > 0 && info.PowerOnHours == 0 {
			info.PowerOnHours = raw.NVMeLog.PowerOnHours
		}
		if raw.NVMeLog.PowerCycles > 0 && info.PowerCycles == 0 {
			info.PowerCycles = raw.NVMeLog.PowerCycles
		}
		if raw.NVMeLog.CriticalWarning != 0 {
			info.Healthy = false
			info.Status = "WARNING"
		}
		return info, nil
	}

	// Handle ATA / SATA telemetry
	if raw.ATASmartAttributes != nil {
		if info.Protocol == "" {
			info.Protocol = "ATA"
		}
		for _, attr := range raw.ATASmartAttributes.Table {
			switch attr.ID {
			case 5: // Reallocated_Sector_Ct
				info.ReallocatedSectors = attr.Raw.Value
				if attr.Raw.Value > 0 && info.Status == "PASSED" {
					info.Status = "WARNING"
				}
			case 197: // Current_Pending_Sector
				info.PendingSectors = attr.Raw.Value
				if attr.Raw.Value > 0 && info.Status == "PASSED" {
					info.Status = "WARNING"
				}
			case 194: // Temperature_Celsius
				if info.TemperatureC == 0 && attr.Raw.Value > 0 {
					info.TemperatureC = int(attr.Raw.Value & 0xFF)
				}
			case 9: // Power_On_Hours
				if info.PowerOnHours == 0 {
					info.PowerOnHours = attr.Raw.Value
				}
			case 12: // Power_Cycle_Count
				if info.PowerCycles == 0 {
					info.PowerCycles = attr.Raw.Value
				}
			case 177, 231, 233: // SSD Wear Range Delta / SSD Life Left
				if info.WearPercentage == -1 && attr.Value > 0 {
					// Usually 100 = 100% life left, so wear = 100 - value
					wear := 100 - attr.Value
					if wear >= 0 && wear <= 100 {
						info.WearPercentage = wear
					}
				}
			case 241: // Total_LBAs_Written (512 bytes per LBA)
				if info.DataWrittenBytes == 0 && attr.Raw.Value > 0 {
					info.DataWrittenBytes = uint64(attr.Raw.Value) * 512
				}
			}
		}
	}

	return info, nil
}
