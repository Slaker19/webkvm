package api

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"webkvm/internal/audit"
	"webkvm/internal/auth"
	"webkvm/internal/compute"
	"webkvm/internal/models"
	"webkvm/internal/safego"
)

// serialSessions tracks the currently active SerialProxy WebSocket
// per VM ID (vmID string -> *websocket.Conn), so a new connection can
// force out a stale one instead of the two perpetually fighting over
// the single libvirt console slot.
var serialSessions sync.Map

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

	// libvirt only allows one active console reader per domain. A
	// stale session (a second tab, or a browser that dropped without
	// a clean close handshake) would otherwise fight this one for the
	// console forever: each side's retry loop kicks the other out,
	// re-acquires, and logs a false "resumed after reboot" — visible
	// as that message repeating nonstop with no actual reboot behind
	// it. Newest viewer wins: force-close whatever session is
	// currently registered for this VM before proceeding.
	//
	// A plain Close() looks identical to a network drop to the evicted
	// browser tab, which auto-retries and immediately steals the console
	// back — two open tabs on the same VM then evict each other forever,
	// every ~2s. Send a distinguishing close code first so that tab can
	// tell "replaced by another tab" apart from "connection dropped" and
	// stand down instead of fighting back.
	if old, loaded := serialSessions.Swap(id, ws); loaded {
		oldWS := old.(*websocket.Conn)
		_ = oldWS.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(4409, "replaced by a newer session"),
			time.Now().Add(time.Second))
		_ = oldWS.Close()
	}
	defer serialSessions.CompareAndDelete(id, ws)

	grace := 30 * time.Second // covers a full guest reboot cycle
	deadline := time.Now().Add(grace)

	var stream compute.ConsoleStream
	var oerr error
	for {
		stream, oerr = h.compute.OpenSerialConsole(id)
		if oerr == nil {
			break
		}
		// A VM with no serial device can never produce a console. It used
		// to fall through to the generic branch, which reported the VM as
		// "powered off or still booting" — about a running VM — and the
		// client then reconnected on a loop, filling the log with a
		// warning nobody could act on. Say what is actually wrong, once.
		if errors.Is(oerr, compute.ErrNoSerialDevice) {
			slog.Info("serial_no_device", "vm_id", id)
			ws.WriteMessage(websocket.TextMessage,
				[]byte("\r\n[this VM has no serial console device. Add one in its hardware settings to use the text console.]\r\n"))
			return
		}
		retryable := errors.Is(oerr, compute.ErrDomainNotRunning) ||
			strings.Contains(oerr.Error(), "Active console session")
		if !retryable || time.Now().After(deadline) {
			slog.Warn("serial_grace_exhausted", "vm_id", id, "err", oerr)
			ws.WriteMessage(websocket.TextMessage,
				[]byte("\r\n[console unavailable: the VM is powered off or still booting]\r\n"))
			return
		}
		time.Sleep(700 * time.Millisecond)
	}
	defer func() {
		if stream != nil {
			stream.Free()
		}
	}()

	var wsDead atomic.Bool
	var serialWriteMu sync.Mutex
	writeWS := func(msgType int, data []byte) error {
		serialWriteMu.Lock()
		defer serialWriteMu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return ws.WriteMessage(msgType, data)
	}
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
				stream, oerr = h.compute.OpenSerialConsole(id)
				if oerr == nil {
					ok = true
					break
				}
				if !errors.Is(oerr, compute.ErrDomainNotRunning) &&
					!strings.Contains(oerr.Error(), "Active console session") {
					break
				}
				time.Sleep(750 * time.Millisecond)
			}
			if !ok {
				_ = writeWS(websocket.TextMessage,
					[]byte("\r\n[console unavailable: the VM is powered off]\r\n"))
				return
			}
			appendBoot := []byte("\r\n\x1b[90m[session resumed after VM reboot]\x1b[0m\r\n")
			_ = writeWS(websocket.TextMessage, appendBoot)
			slog.Info("serial_reacquired_after_reboot", "vm_id", id, "attempt", attempt)
		}

		errc := make(chan error, 3)

		// libvirt stream → websocket
		go func() {
			defer safego.Recover("serial_stream_to_ws")
			buf := make([]byte, 65536)
			defer func() {
				if stream != nil {
					_ = stream.Finish()
				}
			}()
			for {
				if stream == nil {
					return
				}
				n, err := stream.Recv(buf)
				if err != nil {
					slog.Warn("serial_stream_recv_end", "vm_id", id, "err", err)
					errc <- err
					return
				}
				if err := writeWS(websocket.TextMessage, buf[:n]); err != nil {
					errc <- err
					return
				}
			}
		}()

		// websocket → libvirt stream
		go func() {
			defer safego.Recover("serial_ws_to_stream")
			defer func() {
				if stream != nil {
					_ = stream.Finish()
				}
			}()
			for {
				_, msg, err := ws.ReadMessage()
				if err != nil {
					wsDead.Store(true) // client went away: end session
					errc <- err
					return
				}
				if stream == nil {
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
		if stream != nil {
			_ = stream.Finish()
		}
		if wsDead.Load() || reason == nil || errors.Is(reason, websocket.ErrCloseSent) {
			break
		}
		// If read on websocket failed, the client is disconnected.
		// Do not loop spawning new libvirt streams for a closed websocket.
		if websocket.IsCloseError(reason, websocket.CloseNormalClosure, websocket.CloseGoingAway, 4409) ||
			websocket.IsUnexpectedCloseError(reason) || strings.Contains(reason.Error(), "closed network connection") ||
			strings.Contains(reason.Error(), "use of closed network connection") {
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
	ws.SetReadLimit(4 << 20)

	user, role, ip := audit.FromRequest(r)
	if h.audit != nil {
		h.audit.Log(auditFor(r, "host.terminal.connect", "host", map[string]interface{}{
			"user": user,
			"ip":   ip,
		}))
	}

	// Initial PTY size: the browser sends its real grid via ?cols=&rows=
	// so btop/htop draw correctly from the very first frame.
	cols, rows := 120, 30
	if v, err := strconv.Atoi(r.URL.Query().Get("cols")); err == nil && v >= 20 && v <= 500 {
		cols = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("rows")); err == nil && v >= 5 && v <= 200 {
		rows = v
	}

	// Host terminal is an admin-authenticated route, but it must still
	// require the operator to authenticate against the host's own login
	// (there is no auto-root). If /bin/login is missing, refuse rather
	// than silently dropping into a root shell — the previous fallback
	// gave any admin session an unauthenticated root shell, which
	// contradicts this handler's documented contract.
	if _, err := os.Stat("/bin/login"); err != nil {
		slog.Error("host_terminal_login_missing", "err", err)
		_ = ws.WriteMessage(websocket.TextMessage, []byte("host login unavailable: /bin/login not found"))
		return
	}
	cmd := exec.Command("/bin/login", "-p")

	// Build enriched environment with UTF-8 locale and truecolor support
	// so modern TUIs (btop, mc, htop, yazi, lazygit) render box-drawing
	// characters and 24-bit colors correctly.
	env := os.Environ()
	hasLang := false
	hasLCAll := false
	for _, e := range env {
		if strings.HasPrefix(e, "LANG=") {
			hasLang = true
		}
		if strings.HasPrefix(e, "LC_ALL=") {
			hasLCAll = true
		}
	}
	if !hasLang {
		env = append(env, "LANG=C.UTF-8")
	}
	if !hasLCAll {
		env = append(env, "LC_ALL=C.UTF-8")
	}
	env = append(env, "TERM=xterm-256color", "COLORTERM=truecolor")
	cmd.Env = env

	ptmx, err := pty.StartWithAttrs(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}, &syscall.SysProcAttr{Setsid: true})
	if err != nil {
		slog.Error("host_terminal_pty_failed", "err", err, "user", user, "ip", ip)
		_ = ws.WriteMessage(websocket.TextMessage, []byte("failed to start login: "+err.Error()))
		return
	}

	const (
		pingPeriod = 20 * time.Second
		writeWait  = 10 * time.Second
	)

	idleTimeoutMin := 0
	if h.settings != nil {
		idleTimeoutMin = h.settings.GetInt("terminal.idle_timeout_min")
	}

	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().Unix())

	var wsMu sync.Mutex
	writeWS := func(msgType int, data []byte) error {
		wsMu.Lock()
		defer wsMu.Unlock()
		_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
		return ws.WriteMessage(msgType, data)
	}

	done := make(chan struct{})
	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() {
			close(done)
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			_ = ptmx.Close()
			_ = ws.Close()
		})
	}
	defer cleanup()

	// Optional idle timeout watcher (0 = disabled / never die)
	if idleTimeoutMin > 0 {
		go func() {
			defer safego.Recover("host_terminal_idle_checker")
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			maxIdle := time.Duration(idleTimeoutMin) * time.Minute
			for {
				select {
				case <-ticker.C:
					last := time.Unix(lastActivity.Load(), 0)
					if time.Since(last) > maxIdle {
						_ = writeWS(websocket.TextMessage, []byte("\r\n[Session closed due to inactivity timeout]\r\n"))
						cleanup()
						return
					}
				case <-done:
					return
				}
			}
		}()
	}

	// Ping ticker to keep WebSocket alive through reverse proxies and idle periods
	go func() {
		defer safego.Recover("host_terminal_ping")
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				wsMu.Lock()
				_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
				err := ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait))
				wsMu.Unlock()
				if err != nil {
					cleanup()
					return
				}
			case <-done:
				return
			}
		}
	}()

	errc := make(chan error, 3)
	var wsWrites int64

	// PTY → websocket
	go func() {
		defer safego.Recover("serial_pty_to_ws")
		buf := make([]byte, 65536)
		eioCount := 0
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				// On Linux, master PTY returns EIO during process execve / tty transitions.
				// If process is still running, retry reading up to a reasonable limit.
				if errors.Is(err, syscall.EIO) && eioCount < 10 {
					eioCount++
					time.Sleep(50 * time.Millisecond)
					continue
				}
				errc <- err
				return
			}
			eioCount = 0
			lastActivity.Store(time.Now().Unix())
			atomic.AddInt64(&wsWrites, 1)
			if err := writeWS(websocket.BinaryMessage, buf[:n]); err != nil {
				errc <- err
				return
			}
		}
	}()

	// websocket → PTY
	go func() {
		defer safego.Recover("serial_ws_to_pty")
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			lastActivity.Store(time.Now().Unix())

			// Check for resize messages: {"cols":N,"rows":N} or {"type":"resize","cols":N,"rows":N}
			if len(msg) > 0 && msg[0] == '{' {
				var resize struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
				}
				if err := json.Unmarshal(msg, &resize); err == nil && resize.Cols > 0 && resize.Rows > 0 {
					_ = pty.Setsize(ptmx, &pty.Winsize{
						Rows: uint16(resize.Rows),
						Cols: uint16(resize.Cols),
					})
					continue
				}
			}

			if _, err := ptmx.Write(msg); err != nil {
				errc <- err
				return
			}
		}
	}()

	slog.Info("host_terminal_connected", "user", user, "role", role, "ip", ip)
	<-errc
	if atomic.LoadInt64(&wsWrites) == 0 {
		_ = writeWS(websocket.TextMessage, []byte("\r\n[Session ended unexpectedly — click Reconnect]\r\n"))
	}
	slog.Info("host_terminal_disconnected", "user", user, "ip", ip)
	if h.audit != nil {
		h.audit.Log(auditFor(r, "host.terminal.disconnect", "host", map[string]interface{}{
			"user": user,
			"ip":   ip,
		}))
	}
}

// ResetVMPassword generates a new random password for a VM user.
// It stores the new password and returns it once. The caller is
// responsible for applying it to the guest (via cloud-init re-provision
// or guest agent).
func (h *Handler) ResetVMPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.compute.GetDomain(id)
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
	meta, _ := h.compute.GetVMMeta(id)
	username := meta.CiUser
	if username == "" {
		jsonErr(w, http.StatusConflict, "this VM was not created with a WebKVM cloud-init user, so WebKVM does not know which user to reset. Log in with the serial console and change the password there, or re-create the VM with cloud-init provisioning.")
		return
	}

	newPassword := generatePasswordString(8)
	if err := h.compute.SetUserPassword(id, username, newPassword); err != nil {
		slog.Error("password_reset_failed", "vm_id", id, "user", username, "err", err)
		h.vmActionErr(w, err, nil)
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
	// Unbiased, fail-closed: crypto/rand errors propagate (we panic rather
	// than fall back to a weak/known password), and rejection sampling
	// removes the modulo bias the old byte%len(charset) scheme had.
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const maxUnbiased = uint64(4294967296) / uint64(len(charset)) * uint64(len(charset))
	b := make([]byte, 4)
	out := make([]byte, 0, length)
	for len(out) < length {
		if _, err := rand.Read(b); err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}
		x := binary.BigEndian.Uint32(b)
		if uint64(x) >= maxUnbiased {
			continue
		}
		out = append(out, charset[x%uint32(len(charset))])
	}
	return string(out)
}

// VMConsoleTicket issues a short-lived single-use ticket authorizing one
// WebSocket connection to the VM serial proxy. Any authenticated user
// may obtain one — same policy as the serial endpoint itself. The SPA
// embeds the terminal and connects with this ticket, so no long-lived
// credentials ever travel in URLs.
func (h *Handler) VMConsoleTicket(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := h.compute.GetDomain(id); err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	user, role, _ := audit.FromRequest(r)
	tk, err := auth.IssueTicket(user, role, auth.TokenEpoch(r))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "issue ticket: "+err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"ticket": tk, "expires_in": 30})
}

// VNCTicket issues a reusable, VM-scoped ticket for the noVNC console
// (see auth.IssueVNCTicket for why it differs from VMConsoleTicket's
// single-use ticket). Same ownership gate as the /vnc route itself.
func (h *Handler) VNCTicket(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := h.compute.GetDomain(id); err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	user, role, _ := audit.FromRequest(r)
	tk, err := auth.IssueVNCTicket(user, role, id, auth.TokenEpoch(r))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "issue ticket: "+err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"vnc_ticket": tk, "expires_in": int(auth.VNCTicketTTL.Seconds())})
}

// HostTerminalTicket issues a single-use ticket for the embedded host
// terminal. Admin-only: enforced here AND re-checked by the middleware
// when the ticket is consumed against /api/host/terminal.
func (h *Handler) HostTerminalTicket(w http.ResponseWriter, r *http.Request) {
	user, _, _ := audit.FromRequest(r)
	tk, err := auth.IssueTicket(user, string(models.RoleAdmin), auth.TokenEpoch(r))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "issue ticket: "+err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"ticket": tk, "expires_in": 30})
}
