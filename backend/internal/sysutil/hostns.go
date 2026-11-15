package sysutil

import (
	"context"
	"math"
	"os"
	"os/exec"
	"strconv"
	"time"
)

// HostNamespaceRunner runs commands in the HOST mount namespace.
//
// webkvm.service runs with ProtectSystem=full and PrivateTmp=true, so systemd
// gives the backend its own mount namespace: a `mount` started as a direct
// child of the service is invisible to the host, to libvirt/qemu and to
// Pacemaker (STABLE-REPORT 8.21, defect 1). Commands whose effect must be
// seen by the host (mount, umount, findmnt, files under /etc) go through
// `systemd-run --pipe --wait --collect`: PID 1 starts them as a transient
// service in its own namespace, the same escape the host package manager
// uses (api/host_updates.go). `nsenter -t 1 -m` is NOT used: entering a mount
// namespace needs CAP_SYS_CHROOT, which the hardened unit's
// CapabilityBoundingSet does not grant.
//
// Without systemd (containers, dev hosts) there is no private namespace to
// escape and the command runs directly.
type HostNamespaceRunner struct {
	Inner CommandRunner
	// Available reports whether systemd-run can be used (nil: SystemdRunAvailable).
	Available func() bool
}

// SystemdRunAvailable reports whether systemd is PID 1 and systemd-run exists.
func SystemdRunAvailable() bool {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

// HostCommand returns the command line that runs name/args in the host mount
// namespace (systemd-run wrapper), honouring the deadline of ctx.
func HostCommand(ctx context.Context, name string, args ...string) (string, []string) {
	run := []string{"--pipe", "--wait", "--collect", "--quiet", "--setenv=LC_ALL=C"}
	if dl, ok := ctx.Deadline(); ok {
		if secs := int(math.Ceil(time.Until(dl).Seconds())); secs > 0 {
			// The transient unit outlives a killed systemd-run: bound it too.
			run = append(run, "--property=RuntimeMaxSec="+strconv.Itoa(secs))
		}
	}
	run = append(run, "--", name)
	return "systemd-run", append(run, args...)
}

func (h HostNamespaceRunner) wrap() bool {
	if h.Available != nil {
		return h.Available()
	}
	return SystemdRunAvailable()
}

// Run runs name/args in the host mount namespace.
func (h HostNamespaceRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if !h.wrap() {
		return h.Inner.Run(ctx, name, args...)
	}
	n, a := HostCommand(ctx, name, args...)
	return h.Inner.Run(ctx, n, a...)
}

// RunWithTimeout is Run with its own deadline.
func (h HostNamespaceRunner) RunWithTimeout(name string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return h.Run(ctx, name, args...)
}
