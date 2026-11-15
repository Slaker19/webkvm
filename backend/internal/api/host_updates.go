package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"webkvm/internal/hostsched"
	"webkvm/internal/safego"
)

type HostPackageUpdate struct {
	Package        string `json:"package"`
	CurrentVersion string `json:"current_version"`
	NewVersion     string `json:"new_version"`
	Arch           string `json:"arch"`
	Suite          string `json:"suite"`
	IsSecurity     bool   `json:"is_security"`
}

type HostUpdatesSummary struct {
	Packages       []HostPackageUpdate `json:"packages"`
	Total          int                 `json:"total"`
	SecurityCount  int                 `json:"security_count"`
	LastChecked    string              `json:"last_checked"`
	UpgradeRunning bool                `json:"upgrade_running"`
	RebootRequired bool                `json:"reboot_required"`
	RebootPackages []string            `json:"reboot_packages"`
}

var (
	updatesMu        sync.Mutex
	cachedUpdates    *HostUpdatesSummary
	lastCheckedTime  time.Time
	isUpgradingHost  bool
	isRefreshingHost bool
	upgradableLineRE = regexp.MustCompile(`^([a-zA-Z0-9.+~-]+)/([^\s/]+)\s+(\S+)\s+(\S+)\s+\[(?:upgradable from|actualizable desde):\s+([^\]]+)\]`)
	unpackingPkgRE   = regexp.MustCompile(`(?:Unpacking|Preparando para desempaquetar|Desempaquetando)\s+([a-zA-Z0-9.+~-]+)`)
	settingUpPkgRE   = regexp.MustCompile(`(?:Setting up|Configurando)\s+([a-zA-Z0-9.+~-]+)`)
	// safePkgNameRE accepts the package-name shape the UI surfaces and blocks
	// a leading dash so a hostile name cannot be parsed as a flag (--force-yes).
	safePkgNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+~-]*$`)
)

type PkgManager string

const (
	PkgManagerAPT    PkgManager = "apt"
	PkgManagerDNF    PkgManager = "dnf"
	PkgManagerPacman PkgManager = "pacman"
	PkgManagerNone   PkgManager = ""
)

func detectPackageManager() PkgManager {
	if _, err := exec.LookPath("apt-get"); err == nil {
		return PkgManagerAPT
	}
	if _, err := exec.LookPath("dnf"); err == nil {
		return PkgManagerDNF
	}
	if _, err := exec.LookPath("pacman"); err == nil {
		return PkgManagerPacman
	}
	return PkgManagerNone
}

// checkRebootRequired inspects standard Debian/Ubuntu reboot notification files
// as well as Arch/generic Linux kernel version divergence.
func checkRebootRequired() (bool, []string) {
	required := false
	var pkgs []string
	if _, err := os.Stat("/var/run/reboot-required"); err == nil {
		required = true
	}
	if data, err := os.ReadFile("/var/run/reboot-required.pkgs"); err == nil {
		lines := strings.Split(string(data), "\n")
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if l != "" {
				pkgs = append(pkgs, l)
			}
		}
	}
	if !required {
		if out, err := exec.Command("uname", "-r").Output(); err == nil {
			kver := strings.TrimSpace(string(out))
			if kver != "" {
				if _, err := os.Stat("/usr/lib/modules/" + kver); os.IsNotExist(err) {
					required = true
					pkgs = append(pkgs, "linux (running kernel "+kver+" replaced)")
				}
			}
		}
	}
	return required, pkgs
}

// fetchUpgradablePackages executes the host package manager and parses upgradable packages.
func fetchUpgradablePackages(ctx context.Context) (*HostUpdatesSummary, error) {
	rebootReq, rebootPkgs := checkRebootRequired()
	pm := detectPackageManager()

	var packages []HostPackageUpdate
	secCount := 0

	switch pm {
	case PkgManagerAPT:
		cmd := exec.CommandContext(ctx, "apt", "list", "--upgradable")
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("list upgradable packages: %w", err)
		}

		scanner := bufio.NewScanner(strings.NewReader(string(out)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "Listing") || strings.HasPrefix(line, "Listando") {
				continue
			}
			m := upgradableLineRE.FindStringSubmatch(line)
			if len(m) >= 6 {
				pkgName := m[1]
				suite := m[2]
				newVer := m[3]
				arch := m[4]
				curVer := m[5]
				isSec := strings.Contains(strings.ToLower(suite), "security") || strings.Contains(strings.ToLower(suite), "-sec")
				if isSec {
					secCount++
				}
				packages = append(packages, HostPackageUpdate{
					Package:        pkgName,
					CurrentVersion: curVer,
					NewVersion:     newVer,
					Arch:           arch,
					Suite:          suite,
					IsSecurity:     isSec,
				})
			}
		}

	case PkgManagerDNF:
		cmd := exec.CommandContext(ctx, "dnf", "check-update", "-q")
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && exitErr.ExitCode() == 100 {
				// Exit code 100 means updates available
			} else {
				slog.Warn("dnf check-update returned error", "err", err)
			}
		}
		scanner := bufio.NewScanner(strings.NewReader(string(out)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "Obsoleting") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				pkgArch := fields[0]
				newVer := fields[1]
				repo := fields[2]
				pkgName := pkgArch
				arch := ""
				if idx := strings.LastIndex(pkgArch, "."); idx > 0 {
					pkgName = pkgArch[:idx]
					arch = pkgArch[idx+1:]
				}
				isSec := strings.Contains(strings.ToLower(repo), "security") || strings.Contains(strings.ToLower(repo), "sec")
				if isSec {
					secCount++
				}
				packages = append(packages, HostPackageUpdate{
					Package:        pkgName,
					CurrentVersion: "",
					NewVersion:     newVer,
					Arch:           arch,
					Suite:          repo,
					IsSecurity:     isSec,
				})
			}
		}

	case PkgManagerPacman:
		tool := "checkupdates"
		var args []string
		if _, err := exec.LookPath("checkupdates"); err != nil {
			tool = "pacman"
			args = []string{"-Qu"}
		}
		cmd := exec.CommandContext(ctx, tool, args...)
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		out, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && (exitErr.ExitCode() == 1 || exitErr.ExitCode() == 2) {
				// exit 1 or 2 = no updates
			} else {
				slog.Warn("pacman checkupdates returned error", "err", err)
			}
		}
		scanner := bufio.NewScanner(strings.NewReader(string(out)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 4 && fields[2] == "->" {
				packages = append(packages, HostPackageUpdate{
					Package:        fields[0],
					CurrentVersion: fields[1],
					NewVersion:     fields[3],
					Arch:           "",
					Suite:          "core/extra",
					IsSecurity:     false,
				})
			}
		}
	}

	summary := &HostUpdatesSummary{
		Packages:       packages,
		Total:          len(packages),
		SecurityCount:  secCount,
		LastChecked:    time.Now().Format(time.RFC3339),
		UpgradeRunning: isUpgradingHost,
		RebootRequired: rebootReq,
		RebootPackages: rebootPkgs,
	}
	return summary, nil
}

// GetHostUpdates returns available package updates on the host.
func (h *Handler) GetHostUpdates(w http.ResponseWriter, r *http.Request) {
	updatesMu.Lock()
	defer updatesMu.Unlock()

	// Return cached if younger than 60 seconds
	if cachedUpdates != nil && time.Since(lastCheckedTime) < 60*time.Second {
		cachedUpdates.UpgradeRunning = isUpgradingHost
		cachedUpdates.RebootRequired, cachedUpdates.RebootPackages = checkRebootRequired()
		jsonResp(w, http.StatusOK, cachedUpdates)
		return
	}

	summary, err := fetchUpgradablePackages(r.Context())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	cachedUpdates = summary
	lastCheckedTime = time.Now()
	jsonResp(w, http.StatusOK, summary)
}

// RefreshHostUpdates triggers repository refresh and returns fresh updates.
func (h *Handler) RefreshHostUpdates(w http.ResponseWriter, r *http.Request) {
	pm := detectPackageManager()
	if pm == PkgManagerNone {
		jsonErr(w, http.StatusNotImplemented, "no supported package manager (apt, dnf, pacman) found on this host")
		return
	}

	updatesMu.Lock()
	if isUpgradingHost {
		updatesMu.Unlock()
		jsonErr(w, http.StatusConflict, "cannot refresh repositories while host upgrade is running")
		return
	}
	if isRefreshingHost {
		updatesMu.Unlock()
		jsonErr(w, http.StatusConflict, "repository refresh is already in progress")
		return
	}
	isRefreshingHost = true
	updatesMu.Unlock()

	defer func() {
		updatesMu.Lock()
		isRefreshingHost = false
		updatesMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	switch pm {
	case PkgManagerAPT:
		cmd = pkgManagerCmd(ctx, "apt-get", []string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"}, "update")
	case PkgManagerDNF:
		cmd = pkgManagerCmd(ctx, "dnf", []string{"LC_ALL=C"}, "makecache", "--refresh")
	case PkgManagerPacman:
		cmd = pkgManagerCmd(ctx, "pacman", []string{"LC_ALL=C"}, "-Sy")
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		jsonErr(w, http.StatusInternalServerError, fmt.Sprintf("%s refresh failed: %v (%s)", pm, err, strings.TrimSpace(string(out))))
		return
	}

	summary, err := fetchUpgradablePackages(r.Context())
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	updatesMu.Lock()
	cachedUpdates = summary
	lastCheckedTime = time.Now()
	updatesMu.Unlock()

	h.audit.Log(auditFor(r, "host.updates_refreshed", "host", map[string]any{
		"package_manager": string(pm),
		"total":           summary.Total,
		"security_count":  summary.SecurityCount,
	}))
	jsonResp(w, http.StatusOK, summary)
}

// systemdRunAvailable reports whether systemd-run can launch the package
// manager outside this service's hardened mount namespace. Package var so
// tests do not depend on the host having systemd.
var systemdRunAvailable = func() bool {
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

// pkgManagerCmd builds the command that runs the host package manager. When
// systemd-run is available it launches a transient service: systemd forks that
// process from PID 1's namespace, escaping ProtectSystem=full (which would
// otherwise mount /etc read-only and make dpkg/rpm fail with "Read-only file
// system"). --pipe forwards stdout/stderr for live progress and implies --wait,
// and --collect discards the unit afterwards. Without systemd-run (Docker,
// non-systemd hosts) it falls back to a direct child, which is correct there
// because those deployments never apply ProtectSystem.
func pkgManagerCmd(ctx context.Context, name string, env []string, args ...string) *exec.Cmd {
	if systemdRunAvailable() {
		run := []string{"--pipe", "--collect", "--quiet"}
		for _, e := range env {
			run = append(run, "--setenv="+e)
		}
		run = append(run, "--", name)
		run = append(run, args...)
		return exec.CommandContext(ctx, "systemd-run", run...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd
}

// upgradeCmd builds the package-manager invocation for a full or selective
// upgrade. An empty pkgs means "upgrade everything"; otherwise only those
// packages are upgraded (apt: install --only-upgrade, dnf: upgrade <pkg>,
// pacman: -S <pkg>).
func upgradeCmd(ctx context.Context, pm PkgManager, pkgs []string) *exec.Cmd {
	switch pm {
	case PkgManagerAPT:
		env := []string{"DEBIAN_FRONTEND=noninteractive", "LC_ALL=C"}
		args := []string{"-y", "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
		if len(pkgs) > 0 {
			args = append(args, "install", "--only-upgrade")
			args = append(args, pkgs...)
		} else {
			args = append(args, "dist-upgrade")
		}
		return pkgManagerCmd(ctx, "apt-get", env, args...)
	case PkgManagerDNF:
		args := append([]string{"-y", "upgrade"}, pkgs...)
		return pkgManagerCmd(ctx, "dnf", []string{"LC_ALL=C"}, args...)
	case PkgManagerPacman:
		if len(pkgs) > 0 {
			args := append([]string{"-S", "--noconfirm"}, pkgs...)
			return pkgManagerCmd(ctx, "pacman", []string{"LC_ALL=C"}, args...)
		}
		return pkgManagerCmd(ctx, "pacman", []string{"LC_ALL=C"}, "-Syu", "--noconfirm")
	}
	return nil
}

// Host-upgrade error codes, surfaced as the job's error_code so clients and
// operators can tell failure classes apart without parsing prose.
const (
	// errHostUpgradeROFS is the hardened-namespace failure: the package
	// manager ran inside this service's ProtectSystem=full mount namespace
	// and hit a read-only /etc. The fix is to run it via systemd-run; seeing
	// this code again means that escape failed (or was unavailable).
	errHostUpgradeROFS    = "host_upgrade_readonly_filesystem"
	errHostUpgradeTimeout = "host_upgrade_timeout"
	errHostUpgradeFailed  = "host_upgrade_failed"
)

// hostUpgradeErr is a coded error returned by the upgrade worker. The generic
// submitJob plumbing keys off Code() to populate the job's error_code.
type hostUpgradeErr struct {
	code string
	msg  string
}

func (e *hostUpgradeErr) Error() string { return e.msg }
func (e *hostUpgradeErr) Code() string  { return e.code }

// hostUpgradeCode classifies a package-manager failure output into a stable
// error code. Kept pure so it is unit-testable without invoking a real PM.
func hostUpgradeCode(detail string) string {
	if strings.Contains(strings.ToLower(detail), "read-only file system") {
		return errHostUpgradeROFS
	}
	return errHostUpgradeFailed
}

// notifyUpgrade emits a completion notification through the configured
// channels. Best-effort: a nil notifier (unit tests) or a delivery error is
// ignored — the job result already carries the outcome.
func (h *Handler) notifyUpgrade(pm PkgManager, err error) {
	if h.notifier == nil {
		return
	}
	label := strings.ToUpper(string(pm))
	if err != nil {
		h.notifier.Record("error", "Host upgrade failed ("+label+")", err.Error())
		return
	}
	h.notifier.Record("info", "Host upgrade completed", "The system upgrade via "+label+" finished successfully.")
}

// upgradeJob returns the background job body that performs a host upgrade via
// the detected package manager. Shared by the HTTP handler and the scheduled
// host-update runner. The job owns the isUpgradingHost flag and clears it on
// completion; completion is notified through the configured channels.
func (h *Handler) upgradeJob(pm PkgManager, pkgs []string) func(progress func(pct float64, stage string)) (any, error) {
	return func(progress func(pct float64, stage string)) (res any, err error) {
		defer func() {
			updatesMu.Lock()
			isUpgradingHost = false
			cachedUpdates = nil
			updatesMu.Unlock()
		}()
		// Best-effort completion notification (success or failure).
		defer func() {
			h.notifyUpgrade(pm, err)
		}()

		progress(5, fmt.Sprintf("Inicializando gestor de paquetes %s...", strings.ToUpper(string(pm))))

		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()

		cmd := upgradeCmd(ctx, pm, pkgs)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return nil, fmt.Errorf("stdout pipe: %w", err)
		}
		cmd.Stderr = cmd.Stdout // merge stderr into stdout pipe

		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("start apt-get upgrade: %w", err)
		}

		scanner := bufio.NewScanner(stdout)
		unpackedPkgs := make(map[string]bool)
		configuredPkgs := make(map[string]bool)
		totalEstimate := 25
		updatesMu.Lock()
		if cachedUpdates != nil && cachedUpdates.Total > 0 {
			totalEstimate = cachedUpdates.Total
		}
		updatesMu.Unlock()
		totalSteps := totalEstimate * 2
		stepCount := 0
		tailLines := make([]string, 0, 15)

		for scanner.Scan() {
			line := scanner.Text()
			slog.Debug("apt_upgrade_log", "line", line)

			if len(tailLines) >= 15 {
				tailLines = tailLines[1:]
			}
			tailLines = append(tailLines, line)

			if m := unpackingPkgRE.FindStringSubmatch(line); len(m) > 1 {
				pkg := m[1]
				if !unpackedPkgs[pkg] {
					unpackedPkgs[pkg] = true
					stepCount++
					pct := 10.0 + (float64(stepCount)/float64(totalSteps))*85.0
					if pct > 95.0 {
						pct = 95.0
					}
					progress(pct, fmt.Sprintf("Desempaquetando %s (%d/%d)", pkg, len(unpackedPkgs), totalEstimate))
				}
			} else if m := settingUpPkgRE.FindStringSubmatch(line); len(m) > 1 {
				pkg := m[1]
				if !configuredPkgs[pkg] {
					configuredPkgs[pkg] = true
					stepCount++
					pct := 10.0 + (float64(stepCount)/float64(totalSteps))*85.0
					if pct > 95.0 {
						pct = 95.0
					}
					progress(pct, fmt.Sprintf("Configurando %s (%d/%d)", pkg, len(configuredPkgs), totalEstimate))
				}
			} else if strings.Contains(line, "Reading database") {
				progress(10, "Leyendo base de datos de paquetes...")
			} else if strings.Contains(line, "Building dependency tree") {
				progress(15, "Resolviendo dependencias...") //nolint:misspell
			}
		}

		if err := cmd.Wait(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return nil, &hostUpgradeErr{code: errHostUpgradeTimeout, msg: "host upgrade timed out after 30 minutes"}
			}
			detail := strings.TrimSpace(strings.Join(tailLines, "\n"))
			msg := fmt.Sprintf("%s upgrade exited with error: %v", pm, err)
			if detail != "" {
				msg += "\n" + detail
			}
			return nil, &hostUpgradeErr{code: hostUpgradeCode(detail), msg: msg}
		}

		progress(100, "Actualización del sistema completada con éxito")
		return map[string]any{
			"status":     "completed",
			"unpacked":   len(unpackedPkgs),
			"configured": len(configuredPkgs),
			"timestamp":  time.Now().Format(time.RFC3339),
		}, nil
	}
}

// UpgradeHostUpdates starts an async background job executing the host system upgrade.
func (h *Handler) UpgradeHostUpdates(w http.ResponseWriter, r *http.Request) {
	pm := detectPackageManager()
	if pm == PkgManagerNone {
		jsonErr(w, http.StatusNotImplemented, "no supported package manager (apt, dnf, pacman) found on this host")
		return
	}

	// Optional body: {"packages": [...]} for a selective upgrade. Empty body
	// (or no "packages") means "upgrade everything".
	var req struct {
		Packages []string `json:"packages"`
	}
	if err := decodeBody(r, &req); err != nil && !errors.Is(err, io.EOF) {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	for _, p := range req.Packages {
		if !safePkgNameRE.MatchString(p) {
			jsonErr(w, http.StatusBadRequest, "invalid package name: "+p)
			return
		}
	}

	updatesMu.Lock()
	if isUpgradingHost {
		updatesMu.Unlock()
		jsonErr(w, http.StatusConflict, "a host system upgrade is already in progress")
		return
	}
	if isRefreshingHost {
		updatesMu.Unlock()
		jsonErr(w, http.StatusConflict, "cannot start upgrade while repository refresh is in progress")
		return
	}
	isUpgradingHost = true
	updatesMu.Unlock()

	owner := jobOwner(r)
	jobTitle := fmt.Sprintf("Host System Upgrade (%s)", strings.ToUpper(string(pm)))
	if len(req.Packages) > 0 {
		jobTitle = fmt.Sprintf("Host Package Upgrade (%s, %d pkg)", strings.ToUpper(string(pm)), len(req.Packages))
	}
	job := submitJobWithPool(owner, jobTitle, "", h.upgradeJob(pm, req.Packages))

	h.audit.Log(auditFor(r, "host.updates_upgrade_started", "host", map[string]any{
		"job_id": job.ID,
	}))
	jsonResp(w, http.StatusAccepted, job)
}

// RebootHost initiates a full host reboot. It is the action behind the
// "restart required" prompt that appears after a host upgrade. The reboot is
// scheduled in the background so the 202 response reaches the client before
// systemd tears the machine down.
func (h *Handler) RebootHost(w http.ResponseWriter, r *http.Request) {
	if !isRoot() {
		jsonErr(w, http.StatusForbidden, "reboot requires the backend to run as root")
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "host.reboot", "host", nil))
	}
	go func() {
		defer safego.Recover("host_reboot")
		time.Sleep(500 * time.Millisecond)
		_ = exec.Command("systemctl", "reboot").Run()
	}()
	jsonResp(w, http.StatusAccepted, map[string]string{"status": "rebooting"})
}

// runScheduledUpgrade is the callback fired by the hostsched scheduler. It
// submits a full host upgrade as a "system" job, guarded exactly like the
// HTTP path so a manual upgrade and a scheduled one cannot overlap.
func (h *Handler) runScheduledUpgrade() {
	pm := detectPackageManager()
	if pm == PkgManagerNone {
		return
	}
	updatesMu.Lock()
	if isUpgradingHost || isRefreshingHost {
		updatesMu.Unlock()
		slog.Info("hostsched_upgrade_skipped", "reason", "already running")
		return
	}
	isUpgradingHost = true
	updatesMu.Unlock()

	title := fmt.Sprintf("Scheduled Host Upgrade (%s)", strings.ToUpper(string(pm)))
	submitJobWithPool("system", title, "", h.upgradeJob(pm, nil))
}

// GetHostUpdateSchedule returns the current host-update schedule.
func (h *Handler) GetHostUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if h.hostSched == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host update scheduler not initialized")
		return
	}
	jsonResp(w, http.StatusOK, h.hostSched.Get())
}

// SetHostUpdateSchedule updates the host-update schedule (admin-only route).
func (h *Handler) SetHostUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	if h.hostSched == nil {
		jsonErr(w, http.StatusServiceUnavailable, "host update scheduler not initialized")
		return
	}
	var req hostsched.Config
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.hostSched.Set(req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid schedule: "+err.Error())
		return
	}
	if h.hostScheduler != nil {
		h.hostScheduler.Rebuild()
	}
	h.audit.Log(auditFor(r, "host.updates_schedule_set", "host", map[string]any{
		"enabled": req.Enabled,
		"cron":    req.UpgradeCron,
	}))
	jsonResp(w, http.StatusOK, h.hostSched.Get())
}
