package ha

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"

	"webkvm/internal/sysutil"
)

const (
	PrereqOK       = "ok"
	PrereqInactive = "inactive"
	PrereqMissing  = "missing"
)

// Prerequisite is the live state of one dependency the local node needs before
// HA can be offered. It carries no credentials and performs no changes.
type Prerequisite struct {
	ID    string `json:"id"`
	State string `json:"state"`
}

var prerequisiteUnits = []string{"pacemaker", "corosync", "sbd", "linstor-satellite", "drbd-reactor"}

// ProbePrerequisites checks the local node only. Remote members are inspected
// by their own WebKVM instance; this endpoint never shells into a peer.
func ProbePrerequisites(ctx context.Context, runner sysutil.CommandRunner) []Prerequisite {
	result := make([]Prerequisite, 0, len(prerequisiteUnits)+1)
	if runner == nil {
		return result
	}
	for _, unit := range prerequisiteUnits {
		result = append(result, Prerequisite{
			ID:    strings.ReplaceAll(unit, "-", "_"),
			State: unitState(ctx, runner, unit),
		})
	}
	result = append(result, Prerequisite{ID: "drbd", State: moduleState(ctx, runner)})
	return result
}

func unitState(ctx context.Context, runner sysutil.CommandRunner, unit string) string {
	commandCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := runner.Run(commandCtx, "systemctl", "is-active", unit)
	if err != nil {
		var notFound *exec.Error
		if errors.As(err, &notFound) {
			return PrereqMissing
		}
		return PrereqInactive
	}
	if strings.TrimSpace(string(out)) == "active" {
		return PrereqOK
	}
	return PrereqInactive
}

func moduleState(ctx context.Context, runner sysutil.CommandRunner) string {
	commandCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := runner.Run(commandCtx, "modinfo", "drbd"); err != nil {
		return PrereqMissing
	}
	return PrereqOK
}
