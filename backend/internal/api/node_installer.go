package api

import (
	"context"
	_ "embed"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"webkvm/internal/sysutil"
)

//go:embed node_dependencies.sh
var nodeDependenciesScript string

var nodeInstallUserRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)

func validNodeInstallHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) == 0 || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

func nodeInstallSSHArgs(uri string, allowSystemUpgrade bool) ([]string, string, error) {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || u.Scheme != "qemu+ssh" || u.Path != "/system" || u.User == nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, "", errors.New("automatic installation requires qemu+ssh://user@host/system without URI options")
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return nil, "", errors.New("passwords in libvirt URIs are not supported; use SSH keys")
	}
	user, host := u.User.Username(), u.Hostname()
	if !nodeInstallUserRE.MatchString(user) || !validNodeInstallHost(host) {
		return nil, "", errors.New("invalid SSH username or host in URI")
	}
	port := 22
	if portText := u.Port(); portText != "" {
		port, err = strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, "", errors.New("invalid SSH port in URI")
		}
	}
	allowUpgrade := "0"
	if allowSystemUpgrade {
		allowUpgrade = "1"
	}
	args := []string{
		"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=yes",
	}
	if port != 22 {
		args = append(args, "-p", strconv.Itoa(port))
	}
	remoteCommand := "env WEBKVM_SSH_USER=" + user + " WEBKVM_ALLOW_SYSTEM_UPGRADE=" + allowUpgrade + " /bin/sh -s"
	if user != "root" {
		remoteCommand = "sudo -n " + remoteCommand
	}
	args = append(args, user+"@"+host, remoteCommand)
	return args, host, nil
}

// nodeInstallMaxDuration caps one remote install regardless of the client.
const nodeInstallMaxDuration = 10 * time.Minute

// nodeInstallBusy tracks hosts with an install in flight, so two requests
// never run apt/dnf concurrently on the same machine.
var (
	nodeInstallMu   sync.Mutex
	nodeInstallBusy = map[string]bool{}
)

func acquireNodeInstall(host string) bool {
	nodeInstallMu.Lock()
	defer nodeInstallMu.Unlock()
	if nodeInstallBusy[host] {
		return false
	}
	nodeInstallBusy[host] = true
	return true
}

func releaseNodeInstall(host string) {
	nodeInstallMu.Lock()
	delete(nodeInstallBusy, host)
	nodeInstallMu.Unlock()
}

func runNodeDependencyInstaller(ctx context.Context, runner sysutil.InputCommandRunner, uri string, allowSystemUpgrade bool) ([]byte, string, error) {
	args, host, err := nodeInstallSSHArgs(uri, allowSystemUpgrade)
	if err != nil {
		return nil, "", err
	}
	out, err := runner.RunInput(ctx, "ssh", []byte(nodeDependenciesScript), args...)
	return out, host, err
}

func nodeInstallerOutput(output []byte) string {
	const maxOutput = 6000
	if len(output) > maxOutput {
		return "[earlier output truncated]\n" + string(output[len(output)-maxOutput:])
	}
	return string(output)
}

// GetNodeDependencyInstaller returns the exact script used by one-click setup.
func (h *Handler) GetNodeDependencyInstaller(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, map[string]string{"script": nodeDependenciesScript})
}

// InstallNodeDependencies installs libvirt prerequisites through the URI's
// SSH user. The operation requires non-interactive sudo on the remote host.
func (h *Handler) InstallNodeDependencies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URI                string `json:"uri"`
		AllowSystemUpgrade bool   `json:"allow_system_upgrade"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	runner, ok := sysutil.DefaultRunner.(sysutil.InputCommandRunner)
	if !ok {
		jsonErr(w, http.StatusNotImplemented, "SSH installer is unavailable with the configured command runner")
		return
	}
	// Validate before touching the network: a bad URI is the caller's
	// mistake (400), not a failed install.
	if _, host, err := nodeInstallSSHArgs(req.URI, req.AllowSystemUpgrade); err != nil {
		jsonErrCode(w, http.StatusBadRequest, "invalid_uri", err.Error())
		return
	} else if !acquireNodeInstall(host) {
		jsonErrCode(w, http.StatusConflict, "install_in_progress", "an installation is already running on this host")
		return
	} else {
		defer releaseNodeInstall(host)
	}
	// The install must not be killed halfway by a closed browser tab (apt
	// could be left mid-transaction), so it does not inherit the request
	// context; it has its own cap instead.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), nodeInstallMaxDuration)
	defer cancel()
	out, host, err := runNodeDependencyInstaller(ctx, runner, req.URI, req.AllowSystemUpgrade)
	if err != nil {
		message := strings.TrimSpace(nodeInstallerOutput(out))
		if h.audit != nil {
			h.audit.Log(auditFor(r, "node.install_dependencies_failed", host, map[string]any{
				"allow_system_upgrade": req.AllowSystemUpgrade,
			}))
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			jsonErr(w, http.StatusGatewayTimeout, "remote install timed out; some packages may already have changed")
			return
		}
		if message == "" {
			message = "Confirm SSH key access, the known host key, and passwordless sudo (-n)."
		}
		jsonErr(w, http.StatusBadGateway, "remote install failed; changes may be partial: "+message)
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "node.install_dependencies", host, map[string]any{
			"allow_system_upgrade": req.AllowSystemUpgrade,
		}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"ok": true, "output": nodeInstallerOutput(out)})
}
