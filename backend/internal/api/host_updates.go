package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
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
	updatesMu          sync.Mutex
	cachedUpdates      *HostUpdatesSummary
	lastCheckedTime    time.Time
	isUpgradingHost    bool
	isRefreshingHost   bool
	upgradableLineRE   = regexp.MustCompile(`^([a-zA-Z0-9.+~-]+)/([^\s/]+)\s+(\S+)\s+(\S+)\s+\[(?:upgradable from|actualizable desde):\s+([^\]]+)\]`)
	unpackingPkgRE     = regexp.MustCompile(`(?:Unpacking|Preparando para desempaquetar|Desempaquetando)\s+([a-zA-Z0-9.+~-]+)`)
	settingUpPkgRE     = regexp.MustCompile(`(?:Setting up|Configurando)\s+([a-zA-Z0-9.+~-]+)`)
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
		cmd = exec.CommandContext(ctx, "apt-get", "update")
		cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
	case PkgManagerDNF:
		cmd = exec.CommandContext(ctx, "dnf", "makecache", "--refresh")
		cmd.Env = append(os.Environ(), "LC_ALL=C")
	case PkgManagerPacman:
		cmd = exec.CommandContext(ctx, "pacman", "-Sy")
		cmd.Env = append(os.Environ(), "LC_ALL=C")
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

// UpgradeHostUpdates starts an async background job executing the host system upgrade.
func (h *Handler) UpgradeHostUpdates(w http.ResponseWriter, r *http.Request) {
	pm := detectPackageManager()
	if pm == PkgManagerNone {
		jsonErr(w, http.StatusNotImplemented, "no supported package manager (apt, dnf, pacman) found on this host")
		return
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
	job := submitJobWithPool(owner, jobTitle, "", func(progress func(pct float64, stage string)) (any, error) {
		defer func() {
			updatesMu.Lock()
			isUpgradingHost = false
			cachedUpdates = nil
			updatesMu.Unlock()
		}()

		progress(5, fmt.Sprintf("Inicializando gestor de paquetes %s...", strings.ToUpper(string(pm))))

		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
		defer cancel()

		var cmd *exec.Cmd
		switch pm {
		case PkgManagerAPT:
			cmd = exec.CommandContext(ctx, "apt-get", "-y",
				"-o", "Dpkg::Options::=--force-confdef",
				"-o", "Dpkg::Options::=--force-confold",
				"dist-upgrade")
			cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
		case PkgManagerDNF:
			cmd = exec.CommandContext(ctx, "dnf", "-y", "upgrade")
			cmd.Env = append(os.Environ(), "LC_ALL=C")
		case PkgManagerPacman:
			cmd = exec.CommandContext(ctx, "pacman", "-Syu", "--noconfirm")
			cmd.Env = append(os.Environ(), "LC_ALL=C")
		}

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
				return nil, fmt.Errorf("apt-get upgrade timed out after 30 minutes")
			}
			detail := strings.TrimSpace(strings.Join(tailLines, "\n"))
			if detail != "" {
				return nil, fmt.Errorf("apt-get upgrade exited with error: %w\n%s", err, detail)
			}
			return nil, fmt.Errorf("apt-get upgrade exited with error: %w", err)
		}

		progress(100, "Actualización del sistema completada con éxito")
		return map[string]any{
			"status":     "completed",
			"unpacked":   len(unpackedPkgs),
			"configured": len(configuredPkgs),
			"timestamp":  time.Now().Format(time.RFC3339),
		}, nil
	})

	h.audit.Log(auditFor(r, "host.updates_upgrade_started", "host", map[string]any{
		"job_id": job.ID,
	}))
	jsonResp(w, http.StatusAccepted, job)
}
