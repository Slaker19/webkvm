package api

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// One RAM rule for every place that sets a VM's memory: the create wizard,
// Edit hardware and these handlers. KEEP IN SYNC with
// frontend/src/lib/utils/ramValidation.js.
const (
	// MinVMRAMMB is the smallest RAM accepted without the explicit
	// allow_low_ram override: below it most guests do not boot.
	MinVMRAMMB int64 = 512
	// LowRAMFloorMB is the absolute floor, even with the override.
	LowRAMFloorMB int64 = 128
)

// ramRuleError is a typed 400 for the RAM rule.
type ramRuleError struct {
	code string
	msg  string
}

// checkRAMRule validates a requested RAM (nil = not part of the request) and
// ballooning floor (minRAM > 0 only). currentRAM is used for the floor check
// when ram is not part of the request (0 = unknown, skip).
func checkRAMRule(ramp *int64, minRAM, currentRAM int64, allowLow bool) *ramRuleError {
	var ram int64
	if ramp != nil {
		ram = *ramp
		if ram < MinVMRAMMB && !allowLow {
			return &ramRuleError{"ram_below_minimum", fmt.Sprintf("ram_mb must be at least %d MB (set allow_low_ram to go down to %d MB)", MinVMRAMMB, LowRAMFloorMB)}
		}
		if ram < LowRAMFloorMB {
			return &ramRuleError{"ram_below_floor", fmt.Sprintf("ram_mb must be at least %d MB", LowRAMFloorMB)}
		}
	}
	effRAM := ram
	if effRAM <= 0 {
		effRAM = currentRAM
	}
	if minRAM > 0 && effRAM > 0 && minRAM > effRAM {
		return &ramRuleError{"min_ram_above_ram", "min_ram_mb cannot be greater than ram_mb"}
	}
	return nil
}

func writeRAMRuleError(w http.ResponseWriter, e *ramRuleError) {
	jsonResp(w, http.StatusBadRequest, map[string]any{
		"error":      e.msg,
		"code":       e.code,
		"min_ram_mb": MinVMRAMMB,
		"floor_mb":   LowRAMFloorMB,
	})
}

// hostMemoryMB is the host's total RAM in MB, or 0 when it cannot be read.
func hostMemoryMB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || kb <= 0 {
				return 0
			}
			return kb / 1024
		}
	}
	return 0
}
