package libvirt

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

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
	psCmd := fmt.Sprintf(`[System.Text.Encoding]::UTF8.GetString([Convert]::FromBase64String("%s")) | Set-Clipboard`, b64) // lgtm[go/unsafe-quoting] - b64 is base64, safe
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
