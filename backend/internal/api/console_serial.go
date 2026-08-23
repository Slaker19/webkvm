package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/go-chi/chi/v5"
	lv "github.com/libvirt/libvirt-go"
	"github.com/gorilla/websocket"

	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
)

// SerialProxy upgrades the HTTP connection to a WebSocket and pipes
// it to the VM's serial console via virDomainOpenConsole.
func (h *Handler) SerialProxy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	// WebSocket upgrades FIRST; the serial stream is acquired (and
	// re-acquired) with retries so a guest REBOOT never kills the web
	// session — the proxy silently reopens the console when the domain
	// is back. The client just sees output pause and resume.
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("serial_ws_upgrade_failed", "err", err)
		return
	}
	defer ws.Close()
	ws.SetReadLimit(1 << 20)

	grace := 30 * time.Second // covers a full guest reboot cycle
	deadline := time.Now().Add(grace)

	var stream *lv.Stream
	for {
		dom, st, oerr := h.lv.OpenSerialConsole(id)
		if dom != nil {
			_ = dom.Free() // domain handle isn't needed past OpenConsole
		}
		if oerr == nil {
			stream = st
			break
		}
		retryable := errors.Is(oerr, libvirt.ErrDomainNotRunning) ||
			strings.Contains(oerr.Error(), "Active console session")
		if !retryable || time.Now().After(deadline) {
			slog.Warn("serial_grace_exhausted", "vm_id", id, "err", oerr)
			ws.WriteMessage(websocket.TextMessage,
				[]byte("\r\n[console unavailable: the VM is powered off or still booting]\r\n"))
			return
		}
		time.Sleep(700 * time.Millisecond)
	}
	defer stream.Free()

	var wsDead atomic.Bool
	slog.Info("serial_proxy_connected", "vm_id", id)

	// Session loop: the web session OUTLIVES guest reboots. When the
	// serial stream dies (domain shutdown/reboot), we re-open it as soon
	// as the domain runs again — the browser never notices.
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			time.Sleep(800 * time.Millisecond)
		}
		if attempt > 0 {
			var oerr error
			ok := false
			for i := 0; i < 40; i++ { // ~30s grace per reacquire
				var d *lv.Domain
				var st *lv.Stream
				d, st, oerr = h.lv.OpenSerialConsole(id)
				if d != nil {
					_ = d.Free()
				}
				if oerr == nil {
					stream = st
					ok = true
					break
				}
				if !errors.Is(oerr, libvirt.ErrDomainNotRunning) &&
					!strings.Contains(oerr.Error(), "Active console session") {
					break
				}
				time.Sleep(750 * time.Millisecond)
			}
			if !ok {
				ws.WriteMessage(websocket.TextMessage,
					[]byte("\r\n[console unavailable: the VM is powered off]\r\n"))
				return
			}
			appendBoot := []byte("\r\n\x1b[90m[session resumed after VM reboot]\x1b[0m\r\n")
			_ = ws.WriteMessage(websocket.TextMessage, appendBoot)
			slog.Info("serial_reacquired_after_reboot", "vm_id", id, "attempt", attempt)
		}

		errc := make(chan error, 3)

		// libvirt stream → websocket
		go func() {
			buf := make([]byte, 65536)
			defer func() { _ = stream.Finish() }()
			for {
				n, err := stream.Recv(buf)
				if err != nil {
					slog.Warn("serial_stream_recv_end", "vm_id", id, "err", err)
					errc <- err
					return
				}
				if err := ws.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
					errc <- err
					return
				}
			}
		}()

		// websocket → libvirt stream
		go func() {
			defer func() { _ = stream.Finish() }()
			for {
				_, msg, err := ws.ReadMessage()
				if err != nil {
					wsDead.Store(true) // client went away: end session
					errc <- err
					return
				}
				if _, err := stream.Send(msg); err != nil {
					errc <- err
					return
				}
			}
		}()

		reason := <-errc
		// Unblock the sibling parked in Recv/Send so its defer Free() runs.
		select {
		case errc <- nil:
		default:
		}
		_ = stream.Finish()
		if wsDead.Load() || reason == nil {
			break
		}
		// Stream-side failure (guest rebooting): loop and reattach.
	}
	slog.Info("serial_proxy_disconnected", "vm_id", id)
}

// HostTerminal proxies a WebSocket to a login prompt (getty-style) on
// the host via PTY. The user must authenticate with real system
// credentials — no auto-root. Must be called from an admin-only route
// group.
func (h *Handler) HostTerminal(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("host_terminal_ws_upgrade_failed", "err", err)
		return
	}
	defer ws.Close()
	ws.SetReadLimit(1 << 20)

	// /bin/login shows the "host login:" / "Password:" prompts and hands
	// the session to the authenticated user's shell via PAM. Falls back
	// to a root bash only if /bin/login is somehow unavailable.
	// Initial PTY size: the browser sends its real grid via ?cols=&rows=
	// so btop/htop draw correctly from the very first frame (a 0x0
	// terminal makes /bin/login exit instantly under some races; live
	// resizes keep arriving via JSON control messages below).
	cols, rows := 120, 30
	if v, err := strconv.Atoi(r.URL.Query().Get("cols")); err == nil && v >= 20 && v <= 500 {
		cols = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("rows")); err == nil && v >= 5 && v <= 200 {
		rows = v
	}
	cmd := exec.Command("/bin/login")
	if _, err := os.Stat("/bin/login"); err != nil {
		slog.Warn("host_terminal_login_missing_fallback_root")
		cmd = exec.Command("/bin/bash", "-i")
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	// NOTE: Setsid WITHOUT Setctty — empirically, with Setctty the spawned
	// /bin/login intermittently produces no output at all on this stack;
	// without it the login prompt appears instantly. login/bash re-attach
	// the controlling terminal themselves once they start.
	ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}, &syscall.SysProcAttr{Setsid: true})
	if err != nil {
		slog.Error("host_terminal_pty_failed", "err", err)
		ws.WriteMessage(websocket.TextMessage, []byte("failed to start login: "+err.Error()))
		return
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = ptmx.Close()
	}()

	errc := make(chan error, 3)
	var wsWrites int64

	// PTY → websocket
	go func() {
		buf := make([]byte, 65536)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				errc <- err
				return
			}
			atomic.AddInt64(&wsWrites, 1)
			if err := ws.WriteMessage(websocket.TextMessage, buf[:n]); err != nil {
				errc <- err
				return
			}
		}
	}()

	// websocket → PTY
	go func() {
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			// Handle resize messages: {"cols":N,"rows":N}
			var resize struct {
				Cols int `json:"cols"`
				Rows int `json:"rows"`
			}
			if err := json.Unmarshal(msg, &resize); err == nil && resize.Cols > 0 && resize.Rows > 0 {
				_ = pty.Setsize(ptmx, &pty.Winsize{
					Rows: uint16(resize.Rows),
					Cols: uint16(resize.Cols),
				})
				continue
			}
			if _, err := ptmx.Write(msg); err != nil {
				errc <- err
				return
			}
		}
	}()

	slog.Info("host_terminal_connected")
	<-errc
	// If the PTY died before producing ANY output, it's the transient
	// instant-exit race — tell the user to reconnect instead of leaving
	// a silent dead terminal.
	if atomic.LoadInt64(&wsWrites) == 0 {
		ws.WriteMessage(websocket.TextMessage, []byte("\r\n[Sesión terminada inesperadamente — pulsa Reconectar]\r\n"))
	}
	slog.Info("host_terminal_disconnected")
}

// ResetVMPassword generates a new random password for a VM user.
// It stores the new password and returns it once. The caller is
// responsible for applying it to the guest (via cloud-init re-provision
// or guest agent).
func (h *Handler) ResetVMPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.lv.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}

	// The password can only be changed in a running VM via the QEMU
	// guest agent. A shut-off VM has no agent, so a real reset is not
	// possible there.
	if vm.State != "running" {
		jsonErr(w, http.StatusConflict, "the VM must be running with qemu-guest-agent installed to reset its password")
		return
	}

	// The cloud-init username provisioned at creation (stored in meta).
	// If it is missing (e.g. the VM was imported, not created through
	// WebKVM), we cannot guess it — guessing "admin" is wrong now that
	// system-group names are rejected, so fail with a clear instruction.
	meta, _ := h.lv.GetVMMeta(id)
	username := meta.CiUser
	if username == "" {
		jsonErr(w, http.StatusConflict, "this VM was not created with a WebKVM cloud-init user, so WebKVM does not know which user to reset. Log in with the serial console and change the password there, or re-create the VM with cloud-init provisioning.")
		return
	}

	newPassword := generatePasswordString(8)
	if err := h.lv.SetUserPassword(id, username, newPassword); err != nil {
		slog.Error("password_reset_failed", "vm_id", id, "user", username, "err", err)
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}

	slog.Info("password_reset", "vm_id", id, "vm_name", vm.Name, "user", username)
	jsonResp(w, http.StatusOK, map[string]any{
		"id":       id,
		"username": username,
		"password": newPassword,
		"warning":  "Save this password! It won't be shown again. The new password is active now.",
	})
}

func generatePasswordString(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, length)
	// Read from /dev/urandom for better randomness
	f, err := os.Open("/dev/urandom")
	if err == nil {
		defer f.Close()
		if _, err := f.Read(b); err != nil {
			slog.Warn("generatePasswordString_urandom_read_failed", "err", err)
		}
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}

// VMConsoleTicket issues a short-lived single-use ticket authorizing one
// WebSocket connection to the VM serial proxy. Any authenticated user
// may obtain one — same policy as the serial endpoint itself. The SPA
// embeds the terminal and connects with this ticket, so no long-lived
// credentials ever travel in URLs.
func (h *Handler) VMConsoleTicket(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := h.lv.GetDomain(id); err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	user, role, _ := audit.FromRequest(r)
	tk, err := auth.IssueTicket(user, role)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "issue ticket: "+err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"ticket": tk, "expires_in": 30})
}

// HostTerminalTicket issues a single-use ticket for the embedded host
// terminal. Admin-only: enforced here AND re-checked by the middleware
// when the ticket is consumed against /api/host/terminal.
func (h *Handler) HostTerminalTicket(w http.ResponseWriter, r *http.Request) {
	user, _, _ := audit.FromRequest(r)
	tk, err := auth.IssueTicket(user, string(models.RoleAdmin))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "issue ticket: "+err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"ticket": tk, "expires_in": 30})
}
