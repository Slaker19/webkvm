package libvirt

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RunCloudInitReprovision fires a best-effort guest-exec that clears
// cloud-init's "already ran" state and re-executes it, so a freshly
// re-seeded NoCloud ISO (new instance-id) actually takes effect on a
// VM that is already running instead of silently being ignored until
// the next reboot. Fire-and-forget: the guest agent may not be
// installed or running yet, and this must never block the HTTP
// response for the (slow, seconds-long) cloud-init modules.
//
// Windows guests don't run cloud-init; callers should skip this for
// non-Linux VMs.
func (c *Connector) RunCloudInitReprovision(id string) error {
	cmdStr := `cloud-init clean --logs >/tmp/webkvm-cireprovision.log 2>&1; ` +
		`cloud-init init >>/tmp/webkvm-cireprovision.log 2>&1; ` +
		`cloud-init modules --mode=config >>/tmp/webkvm-cireprovision.log 2>&1; ` +
		`cloud-init modules --mode=final >>/tmp/webkvm-cireprovision.log 2>&1`
	qcmd := fmt.Sprintf(`{"execute":"guest-exec","arguments":{"path":"/bin/bash","arg":["-c","%s"],"capture-output":false}}`, jsonEscape(cmdStr)) // lgtm[go/unsafe-quoting] - cmdStr is static, no user input
	return c.guestExec(id, qcmd)
}

func (c *Connector) GuestSetClipboard(id, text string) error {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return fmt.Errorf("lookup domain: %w", err)
	}
	dom.Free()

	if err := c.setClipboardLinux(id, text); err == nil {
		return nil
	}
	if err := c.setClipboardWindows(id, text); err == nil {
		return nil
	}
	return fmt.Errorf("clipboard not supported on this guest OS")
}

func (c *Connector) setClipboardLinux(id, text string) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	cmdStr := `U=$(loginctl list-sessions --no-legend 2>/dev/null|grep -v manager|head -1|awk '{print $2}');` +
		`export XDG_RUNTIME_DIR=/run/user/$(id -u "$U");` +
		`export WAYLAND_DISPLAY=$(ls /run/user/$(id -u "$U")/wayland-* 2>/dev/null|head -1|xargs -r basename 2>/dev/null);` +
		`if [ -n "$WAYLAND_DISPLAY" ]; then` +
		` echo ` + b64 + `|base64 -d|timeout 2 wl-copy 2>/dev/null && exit 0;` +
		`fi;` +
		`export DISPLAY=:$(ls /tmp/.X11-unix/ 2>/dev/null|head -1|sed 's/X//';echo 0|head -1);` +
		`echo ` + b64 + `|base64 -d|timeout 2 xclip -selection clipboard 2>/dev/null && exit 0;` +
		`exit 1`

	qcmd := fmt.Sprintf(`{"execute":"guest-exec","arguments":{"path":"/bin/bash","arg":["-c","%s"],"capture-output":false}}`, jsonEscape(cmdStr)) // lgtm[go/unsafe-quoting] - cmdStr is base64-encoded and json-escaped
	return c.guestExec(id, qcmd)
}

func (c *Connector) setClipboardWindows(id, text string) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	psCmd := fmt.Sprintf(`[System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String("%s")) | Set-Clipboard`, b64)                                                                 // lgtm[go/unsafe-quoting] - b64 is base64, safe
	qcmd := fmt.Sprintf(`{"execute":"guest-exec","arguments":{"path":"powershell.exe","arg":["-NoProfile","-NonInteractive","-Command","%s"],"capture-output":false}}`, jsonEscape(psCmd)) // lgtm[go/unsafe-quoting]
	return c.guestExec(id, qcmd)
}

func (c *Connector) GuestGetClipboard(id string) (string, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return "", fmt.Errorf("lookup domain: %w", err)
	}
	dom.Free()

	text, err := c.getClipboardLinux(id)
	if err == nil && text != "" {
		return text, nil
	}
	text, err = c.getClipboardWindows(id)
	if err == nil && text != "" {
		return text, nil
	}
	return "", nil
}

func (c *Connector) getClipboardLinux(id string) (string, error) {
	cmdStr := `U=$(loginctl list-sessions --no-legend 2>/dev/null|grep -v manager|head -1|awk '{print $2}');` +
		`export XDG_RUNTIME_DIR=/run/user/$(id -u "$U");` +
		`export WAYLAND_DISPLAY=$(ls /run/user/$(id -u "$U")/wayland-* 2>/dev/null|head -1|xargs -r basename 2>/dev/null);` +
		`wl-paste 2>/dev/null||export DISPLAY=:$(ls /tmp/.X11-unix/ 2>/dev/null|head -1|sed 's/X//';echo 0|head -1);xclip -selection clipboard -o 2>/dev/null||true`

	qcmd := fmt.Sprintf(`{"execute":"guest-exec","arguments":{"path":"/bin/bash","arg":["-c","%s"],"capture-output":true}}`, jsonEscape(cmdStr)) // lgtm[go/unsafe-quoting] - cmdStr is static, no user input
	return c.guestExecCapture(id, qcmd)
}

func (c *Connector) getClipboardWindows(id string) (string, error) {
	qcmd := `{"execute":"guest-exec","arguments":{"path":"powershell.exe","arg":["-NoProfile","-NonInteractive","-Command","Get-Clipboard"],"capture-output":true}}`
	return c.guestExecCapture(id, qcmd)
}

// guestExec fires a guest-exec command and returns the PID. Does NOT poll for completion.
func (c *Connector) guestExec(id, qcmd string) error {
	raw := exec.Command("virsh", "qemu-agent-command", id, "--cmd", qcmd)
	out, err := raw.CombinedOutput()
	if err != nil {
		return fmt.Errorf("virsh: %w (out: %s)", err, string(out))
	}
	var resp struct {
		Return struct {
			PID int `json:"pid"`
		} `json:"return"`
		Error struct {
			Message string `json:"desc"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("parse: %w (out: %s)", err, string(out))
	}
	if resp.Error.Message != "" {
		return fmt.Errorf("guest agent: %s", resp.Error.Message)
	}
	if resp.Return.PID == 0 {
		return fmt.Errorf("no PID returned")
	}
	return nil
}

// guestExecCapture fires a guest-exec with capture-output and polls for the result.
func (c *Connector) guestExecCapture(id, qcmd string) (string, error) {
	raw := exec.Command("virsh", "qemu-agent-command", id, "--cmd", qcmd)
	out, err := raw.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("virsh: %w (out: %s)", err, string(out))
	}

	var resp struct {
		Return struct {
			PID int `json:"pid"`
		} `json:"return"`
		Error struct {
			Message string `json:"desc"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return "", fmt.Errorf("parse: %w (out: %s)", err, string(out))
	}
	if resp.Error.Message != "" {
		return "", fmt.Errorf("guest agent: %s", resp.Error.Message)
	}
	if resp.Return.PID == 0 {
		return "", fmt.Errorf("no PID returned")
	}

	pid := resp.Return.PID
	for i := 0; i < 50; i++ {
		statusCmd := fmt.Sprintf(`{"execute":"guest-exec-status","arguments":{"pid":%d}}`, pid)
		raw2 := exec.Command("virsh", "qemu-agent-command", id, "--cmd", statusCmd)
		out2, err2 := raw2.CombinedOutput()
		if err2 != nil {
			return "", fmt.Errorf("virsh status: %w (out: %s)", err2, string(out2))
		}
		var sr struct {
			Return struct {
				Exited   bool   `json:"exited"`
				ExitCode int    `json:"exitcode"`
				OutData  string `json:"out-data"`
				ErrData  string `json:"err-data"`
			} `json:"return"`
			Error struct {
				Message string `json:"desc"`
			} `json:"error,omitempty"`
		}
		if err := json.Unmarshal(out2, &sr); err != nil {
			return "", fmt.Errorf("parse status: %w (out: %s)", err, string(out2))
		}
		if sr.Error.Message != "" {
			return "", fmt.Errorf("guest agent status: %s", sr.Error.Message)
		}
		if sr.Return.Exited {
			if sr.Return.ExitCode != 0 || sr.Return.OutData == "" {
				return "", nil
			}
			data, err := base64.StdEncoding.DecodeString(sr.Return.OutData)
			if err != nil {
				return "", nil
			}
			return strings.TrimRight(string(data), "\n\r\t "), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", nil
}

func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}

// agentQuery runs a plain qemu-agent-command (no guest-exec involved)
// and unmarshals its "return" payload into out.
//
// The guest-exec helpers above spawn a process INSIDE the guest and
// poll for its output; the introspection commands used below
// (guest-get-fsinfo, guest-network-get-interfaces, guest-get-osinfo)
// are answered by the agent itself, so they're a single round-trip and
// work even on guests where exec is disabled via the agent's
// allow/deny list — which is a common hardening step.
func (c *Connector) agentQuery(id, command string, out any) error {
	qcmd := fmt.Sprintf(`{"execute":%q}`, command)
	raw := exec.Command("virsh", "qemu-agent-command", id, "--cmd", qcmd)
	stdout, err := raw.CombinedOutput()
	if err != nil {
		return fmt.Errorf("guest agent unreachable (is qemu-guest-agent running?): %w", err)
	}
	var resp struct {
		Return json.RawMessage `json:"return"`
		Error  struct {
			Message string `json:"desc"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(stdout, &resp); err != nil {
		return fmt.Errorf("parse agent reply: %w", err)
	}
	if resp.Error.Message != "" {
		return fmt.Errorf("guest agent: %s", resp.Error.Message)
	}
	if out == nil || len(resp.Return) == 0 {
		return nil
	}
	return json.Unmarshal(resp.Return, out)
}

// GuestFilesystem is one mounted filesystem as reported from inside
// the guest. Unlike the host-side disk sizes WebKVM already shows
// (which are the qcow2 allocation), these are the real numbers the
// guest OS sees — the only way to answer "is /var actually full?".
type GuestFilesystem struct {
	Name       string `json:"name"`        // e.g. "sda1"
	Mountpoint string `json:"mountpoint"`  // e.g. "/"
	Type       string `json:"type"`        // e.g. "ext4"
	TotalBytes int64  `json:"total_bytes"` // 0 when the agent doesn't report it
	UsedBytes  int64  `json:"used_bytes"`  // 0 when unknown
	UsedPct    int    `json:"used_pct"`    // 0-100, derived; 0 when unknown
}

// GuestNetworkInterface is one NIC as seen from inside the guest,
// including every address assigned to it (DHCP or static) — the host
// only ever sees the lease, and nothing at all for statically
// configured guests.
type GuestNetworkInterface struct {
	Name string   `json:"name"`
	MAC  string   `json:"mac,omitempty"`
	IPv4 []string `json:"ipv4,omitempty"`
	IPv6 []string `json:"ipv6,omitempty"`
}

// GuestOSInfo identifies the operating system running in the guest.
type GuestOSInfo struct {
	ID            string `json:"id,omitempty"`             // "ubuntu"
	Name          string `json:"name,omitempty"`           // "Ubuntu"
	PrettyName    string `json:"pretty_name,omitempty"`    // "Ubuntu 24.04.1 LTS"
	Version       string `json:"version,omitempty"`        // "24.04.1 LTS"
	VersionID     string `json:"version_id,omitempty"`     // "24.04"
	KernelRelease string `json:"kernel_release,omitempty"` // "6.8.0-45-generic"
	KernelVersion string `json:"kernel_version,omitempty"`
	Machine       string `json:"machine,omitempty"` // "x86_64"
}

// GuestInfo bundles everything the agent can tell us about a running
// guest. Each section is independent: a guest may answer osinfo but
// not fsinfo (older agents), so partial results are returned rather
// than failing the whole request.
type GuestInfo struct {
	Available   bool                    `json:"available"`
	Error       string                  `json:"error,omitempty"`
	OS          *GuestOSInfo            `json:"os,omitempty"`
	Filesystems []GuestFilesystem       `json:"filesystems,omitempty"`
	Interfaces  []GuestNetworkInterface `json:"interfaces,omitempty"`
	Hostname    string                  `json:"hostname,omitempty"`
}

// GetGuestInfo queries the QEMU guest agent for OS, filesystem and
// network details. It never returns an error for "agent not
// installed/running" — that's an expected state for many guests, and
// the UI renders it as a hint rather than a failure. A non-nil error
// means the domain itself couldn't be reached.
func (c *Connector) GetGuestInfo(id string) (GuestInfo, error) {
	dom, err := c.lookupDomain(id)
	if err != nil {
		return GuestInfo{}, fmt.Errorf("lookup domain: %w", err)
	}
	dom.Free()

	info := GuestInfo{}

	// Ping first: it's the cheapest way to distinguish "no agent" from
	// "agent present but this particular command is unsupported".
	if err := c.agentQuery(id, "guest-ping", nil); err != nil {
		info.Error = err.Error()
		return info, nil
	}
	info.Available = true

	var osRaw struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		PrettyName    string `json:"pretty-name"`
		Version       string `json:"version"`
		VersionID     string `json:"version-id"`
		KernelRelease string `json:"kernel-release"`
		KernelVersion string `json:"kernel-version"`
		Machine       string `json:"machine"`
	}
	if err := c.agentQuery(id, "guest-get-osinfo", &osRaw); err == nil && osRaw.Name != "" {
		info.OS = &GuestOSInfo{
			ID: osRaw.ID, Name: osRaw.Name, PrettyName: osRaw.PrettyName,
			Version: osRaw.Version, VersionID: osRaw.VersionID,
			KernelRelease: osRaw.KernelRelease, KernelVersion: osRaw.KernelVersion,
			Machine: osRaw.Machine,
		}
	}

	var hostRaw struct {
		HostName string `json:"host-name"`
	}
	if err := c.agentQuery(id, "guest-get-host-name", &hostRaw); err == nil {
		info.Hostname = hostRaw.HostName
	}

	var fsRaw []struct {
		Name       string `json:"name"`
		Mountpoint string `json:"mountpoint"`
		Type       string `json:"type"`
		TotalBytes int64  `json:"total-bytes"`
		UsedBytes  int64  `json:"used-bytes"`
	}
	if err := c.agentQuery(id, "guest-get-fsinfo", &fsRaw); err == nil {
		for _, f := range fsRaw {
			// Pseudo-filesystems carry no useful capacity and would
			// just be noise in the UI.
			switch f.Type {
			case "tmpfs", "devtmpfs", "squashfs", "overlay", "proc", "sysfs", "cgroup", "cgroup2":
				continue
			}
			item := GuestFilesystem{
				Name: f.Name, Mountpoint: f.Mountpoint, Type: f.Type,
				TotalBytes: f.TotalBytes, UsedBytes: f.UsedBytes,
			}
			if f.TotalBytes > 0 {
				item.UsedPct = int(f.UsedBytes * 100 / f.TotalBytes)
			}
			info.Filesystems = append(info.Filesystems, item)
		}
	}

	var netRaw []struct {
		Name         string `json:"name"`
		HardwareAddr string `json:"hardware-address"`
		IPAddresses  []struct {
			IPAddress     string `json:"ip-address"`
			IPAddressType string `json:"ip-address-type"`
		} `json:"ip-addresses"`
	}
	if err := c.agentQuery(id, "guest-network-get-interfaces", &netRaw); err == nil {
		for _, n := range netRaw {
			if n.Name == "lo" {
				continue
			}
			iface := GuestNetworkInterface{Name: n.Name, MAC: n.HardwareAddr}
			for _, a := range n.IPAddresses {
				if a.IPAddressType == "ipv6" {
					iface.IPv6 = append(iface.IPv6, a.IPAddress)
				} else {
					iface.IPv4 = append(iface.IPv4, a.IPAddress)
				}
			}
			info.Interfaces = append(info.Interfaces, iface)
		}
	}

	return info, nil
}
