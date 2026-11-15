package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"webkvm/internal/hainstall"
	"webkvm/internal/sysutil"
)

// Exit codes of `webkvm-cli ha deps`, shared with scripts/install-ha-stack.sh.
const (
	exitHAOK           = 0
	exitHAMissing      = 1  // check found missing components
	exitHAUsage        = 2  // usage or privileges
	exitHANotInstall   = 10 // distro/tier/repo not installable as-is
	exitHAPackageFail  = 11 // package manager or preflight failure
	exitHAUnitsActive  = 12 // units need operator action
	exitHAManualNeeded = 13 // WebKVM part done; the operator still has manual steps
)

type haDepsEnv struct {
	Out, Err    io.Writer
	In          io.Reader
	Runner      sysutil.CommandRunner
	DataDir     string
	EUID        int
	Interactive bool
	Poll        time.Duration
}

func defaultHADepsEnv() haDepsEnv {
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "/opt/webkvm"
	}
	info, err := os.Stdin.Stat()
	return haDepsEnv{Out: os.Stdout, Err: os.Stderr, In: os.Stdin, Runner: sysutil.DefaultRunner, DataDir: dataDir,
		EUID: os.Geteuid(), Interactive: err == nil && info.Mode()&os.ModeCharDevice != 0, Poll: 500 * time.Millisecond}
}

const haUsage = `Usage: webkvm-cli ha deps <check|install> [flags]

  check      Read-only: what is installed, what would be installed, what is
             manual. Needs no root.
  install    Install the Cluster/HA packages on THIS node (root). Shows the
             plan and asks for confirmation unless --yes. Never forms a cluster.

Flags:
  --storage               Include the storage opt-in (DRBD/LINSTOR where packaged)
  --accept-experimental   Acknowledge an experimental/partial distribution
  --yes, -y               Do not ask for confirmation
  --json                  Machine-readable output

Exit codes: 0 ok | 1 check found missing | 2 usage/privileges
  10 distro/repo not installable as-is | 11 package failure (cleanup ran)
  12 units need action | 13 WebKVM part done, manual steps remain
`

// runHA implements `webkvm-cli ha ...` and returns the process exit code.
func runHA(args []string, env haDepsEnv, jsonOut bool) int {
	if len(args) < 2 || args[0] != "deps" || (args[1] != "check" && args[1] != "install") {
		fmt.Fprint(env.Err, haUsage)
		return exitHAUsage
	}
	opts := hainstall.Options{}
	yes := false
	for _, arg := range args[2:] {
		switch arg {
		case "--storage":
			opts.Linstor = true
		case "--accept-experimental":
			opts.AcceptExperimental = true
		case "--yes", "-y":
			yes = true
		case "--json":
			jsonOut = true
		default:
			fmt.Fprintf(env.Err, "unknown flag: %s\n\n%s", arg, haUsage)
			return exitHAUsage
		}
	}
	if args[1] == "check" {
		return haDepsCheck(env, opts, jsonOut)
	}
	return haDepsInstall(env, opts, yes, jsonOut)
}

func haDepsCheck(env haDepsEnv, opts hainstall.Options, jsonOut bool) int {
	ctx := context.Background()
	m := hainstall.NewEphemeral(env.Runner)
	plan := m.Plan(ctx, opts)
	status := m.ReconcilePlan(ctx, plan)
	if jsonOut {
		writeJSON(env.Out, map[string]any{"plan": plan, "status": status})
	} else {
		printPlan(env.Out, plan, status.State)
	}
	if status.State == hainstall.StateInstalled {
		return exitHAOK
	}
	return exitHAMissing
}

func haDepsInstall(env haDepsEnv, opts hainstall.Options, yes, jsonOut bool) int {
	if env.EUID != 0 {
		fmt.Fprintln(env.Err, "run 'webkvm-cli ha deps install' as root (sudo)")
		return exitHAUsage
	}
	ctx := context.Background()
	// Read-only until the operator confirms: an install that does nothing
	// (missing repo, already installed, aborted) must not create state.
	planM := hainstall.NewEphemeral(env.Runner)
	plan := planM.Plan(ctx, opts)
	// The header must show what dpkg/rpm report now, not the stale record.
	status := planM.ReconcilePlan(ctx, plan)
	if !jsonOut {
		printPlan(env.Out, plan, status.State)
	}
	// Nothing is missing: exit before preflight checks that no longer matter
	// (disk space, time sync...). Strict preflight stays for real installs.
	if nothingMissing(plan) {
		if jsonOut {
			writeJSON(env.Out, map[string]any{"plan": plan, "status": status})
		} else {
			fmt.Fprintln(env.Out, "\nNothing for WebKVM to install: all packages are already installed.")
		}
		return exitHAOK
	}
	if !plan.Preflight.OK {
		return exitHAForCause(preflightCause(plan), jsonOut, env, planM, opts)
	}
	if !plan.CanInstall {
		if jsonOut {
			writeJSON(env.Out, map[string]any{"plan": plan, "status": planM.ReconcilePlan(ctx, plan)})
		} else {
			fmt.Fprintln(env.Out, "\nNothing for WebKVM to install here; follow the manual steps above, then run 'webkvm-cli ha deps check'.")
		}
		if plan.Guided {
			return exitHAManualNeeded
		}
		return exitHAOK
	}
	if !yes {
		if !env.Interactive {
			fmt.Fprintln(env.Err, "refusing to install without a terminal: pass --yes")
			return exitHAUsage
		}
		fmt.Fprint(env.Out, "\nInstall the packages marked 'WebKVM installs' now? [y/N] ")
		answer, _ := bufio.NewReader(env.In).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Fprintln(env.Err, "aborted")
			return exitHAUsage
		}
	}
	// An interrupt must never leave the units a postinst enabled behind: the
	// package manager is not killed half way (that would corrupt its
	// transaction) and the restore step must run. So INT/TERM/HUP are held
	// until the install and the restore have finished. A repeated signal
	// still cannot kill the CLI; SIGKILL is the operator's last resort.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	installM := hainstall.New(env.DataDir, env.Runner)
	if err := installM.Start(opts); err != nil {
		fmt.Fprintln(env.Err, "error:", err)
		return exitHAPackageFail
	}
	printed := 0
	warned := false
	for {
		select {
		case <-sigs:
			if !warned {
				warned = true
				fmt.Fprintln(env.Err, "interrupt received: waiting for the package manager and restoring the units; do not kill this process")
			}
		default:
		}
		time.Sleep(env.Poll)
		snap := installM.Snapshot()
		if !jsonOut {
			for ; printed < len(snap.Log); printed++ {
				fmt.Fprintln(env.Out, snap.Log[printed])
			}
		}
		if !installM.Busy() {
			break
		}
	}
	status = installM.Snapshot()
	if jsonOut {
		writeJSON(env.Out, status)
	} else {
		printStatus(env.Out, status)
	}
	return exitHAFromStatus(status)
}

// nothingMissing is true on a supported node where every package of the plan
// is already installed. An unknown or unsupported distribution never counts.
func nothingMissing(plan hainstall.Plan) bool {
	switch plan.Family {
	case hainstall.FamilyApt, hainstall.FamilyDnf, hainstall.FamilyPacman:
	default:
		return false
	}
	if plan.Tier == hainstall.TierUnsupported || len(plan.Packages) == 0 {
		return false
	}
	for _, pkg := range plan.Packages {
		if !pkg.Installed {
			return false
		}
	}
	return true
}

// preflightCause names why the preflight failed, for the exit code.
func preflightCause(plan hainstall.Plan) string {
	cause := "preflight"
	for _, check := range plan.Preflight.Checks {
		if check.OK {
			continue
		}
		switch check.ID {
		case "distro":
			return "unsupported_distro"
		case "experimental":
			cause = "experimental_not_accepted"
		case "ha_repo":
			cause = "repo_missing"
		case "sync_db":
			cause = "sync_db_missing"
		case "repo_scope":
			cause = "repo_scope"
		}
	}
	return cause
}

func exitHAForCause(cause string, jsonOut bool, env haDepsEnv, m *hainstall.Manager, opts hainstall.Options) int {
	if jsonOut {
		writeJSON(env.Out, map[string]any{"plan": m.Plan(context.Background(), opts), "cause": cause})
	}
	switch cause {
	case "unsupported_distro", "experimental_not_accepted", "repo_missing", "sync_db_missing", "repo_scope":
		return exitHANotInstall
	}
	return exitHAPackageFail
}

func exitHAFromStatus(s hainstall.Status) int {
	switch {
	case len(s.UnitsActive) > 0 || s.Cause == "services_active":
		return exitHAUnitsActive
	case s.State == hainstall.StateInstalled:
		return exitHAOK
	case s.State == hainstall.StatePartialGuided:
		return exitHAManualNeeded
	case s.Cause == "unsupported_distro" || s.Cause == "experimental_not_accepted" || s.Cause == "repo_missing" || s.Cause == "sync_db_missing":
		return exitHANotInstall
	}
	return exitHAPackageFail
}

func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func printPlan(w io.Writer, plan hainstall.Plan, state string) {
	pf := plan.Preflight
	fmt.Fprintf(w, "Node: %s %s (%s, tier %s)  state: %s\n\n", pf.Distro, pf.Version, plan.Family, plan.Tier, state)
	fmt.Fprintln(w, "Checks:")
	for _, c := range pf.Checks {
		mark := "ok  "
		if !c.OK {
			mark = "WARN"
			if c.Severity == "blocker" {
				mark = "FAIL"
			}
		}
		fmt.Fprintf(w, "  [%s] %-12s %s\n", mark, c.ID, c.Detail)
		if !c.OK && c.Hint != "" {
			fmt.Fprintf(w, "         -> %s\n", c.Hint)
		}
	}
	fmt.Fprintf(w, "\nPackages:\n")
	for _, p := range plan.Packages {
		who := "WebKVM installs"
		if p.Guided {
			who = "YOU install (" + p.Origin + ")"
		} else if !p.Installed && p.Origin != hainstall.OriginManual && p.Candidate == "" {
			who = "no candidate (repo missing)"
		}
		st := "missing"
		if p.Installed {
			st, who = "installed", "-"
		}
		fmt.Fprintf(w, "  %-18s %-9s %-9s %s\n", p.Name, p.Layer, st, who)
	}
	for _, step := range plan.GuidedSteps {
		fmt.Fprintf(w, "\nManual step %q (run as %s):\n", step.ID, step.RunAs)
		for _, c := range step.Commands {
			fmt.Fprintf(w, "  %s\n", c)
		}
	}
	fmt.Fprintf(w, "\nFencing: watchdog %s, virtualization %s", pf.Watchdog.Type, pf.Virt)
	if pf.Watchdog.Type == "softdog" || pf.Watchdog.Type == "none" {
		fmt.Fprint(w, "  (no real watchdog: automatic failover needs one; see the hints above)")
	}
	fmt.Fprintln(w)
}

func printStatus(w io.Writer, s hainstall.Status) {
	fmt.Fprintf(w, "\nResult: %s", s.State)
	if s.Cause != "" {
		fmt.Fprintf(w, " (%s)", s.Cause)
	}
	fmt.Fprintln(w)
	if s.Error != "" {
		fmt.Fprintln(w, s.Error)
	}
	if len(s.UnitsActive) > 0 {
		fmt.Fprintf(w, "Units that need your action: %s\n", strings.Join(s.UnitsActive, ", "))
	}
	if len(s.Commands) > 0 {
		fmt.Fprintln(w, "Run by hand:")
		for _, c := range s.Commands {
			fmt.Fprintf(w, "  %s\n", c)
		}
	}
	fmt.Fprintln(w, "Not done on purpose: cluster join, SBD/fencing config, Pacemaker start.")
}
