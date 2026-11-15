package ha

import (
	"context"
	"strings"
	"syscall"
	"time"

	"webkvm/internal/sysutil"
)

// HealthWarnings returns conditions that need attention but do not by
// themselves block HA: failed resource actions, a disconnected DRBD link and
// a nearly full control volume. Read-only.
func HealthWarnings(ctx context.Context, runner sysutil.CommandRunner, dataDir string) []string {
	warnings := make([]string, 0, 3)
	if out, err := runReadOnly(ctx, runner, "resource", "failcount", "show"); err == nil {
		if pacemakerFailures(out) {
			warnings = append(warnings, "pacemaker_failed_actions")
		}
	}
	if out, err := runTool(ctx, runner, "drbdsetup", "status"); err == nil {
		if drbdProblems(out) {
			warnings = append(warnings, "drbd_link_degraded")
		}
	}
	if dataDir != "" {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(dataDir, &stat); err == nil && lowSpace(stat.Bavail, stat.Blocks) {
			warnings = append(warnings, "control_volume_low_space")
		}
	}
	return warnings
}

func runTool(ctx context.Context, runner sysutil.CommandRunner, name string, args ...string) ([]byte, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return runner.Run(commandCtx, name, args...)
}

// pacemakerFailures reports whether any resource has a non-zero fail count.
func pacemakerFailures(out []byte) bool {
	text := strings.ToLower(string(out))
	if strings.Contains(text, "no failcounts") || strings.TrimSpace(text) == "" {
		return false
	}
	return strings.Contains(text, "failcount")
}

// drbdProblems reports split-brain or a peer that is not connected. A
// diskless peer is normal on a node excluded from a group and is ignored.
func drbdProblems(out []byte) bool {
	text := strings.ToLower(string(out))
	for _, marker := range []string{"split-brain", "splitbrain", "standalone", "wfconnection"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func lowSpace(available, total uint64) bool {
	if total == 0 {
		return false
	}
	return available*100/total < 10
}
