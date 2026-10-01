package api

import (
	"fmt"
	"html"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"webkvm/internal/safego"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// serverIP returns the IP address that should be baked into
// console-related files the user downloads (.rdp, .vv) and that
// the noVNC WebSocket should use as a default. In a normal
// systemd install, the first non-loopback IPv4 of the running
// process is the right answer. Override via the PUBLIC_HOST env
// var when the host has multiple interfaces or is behind NAT.
func (h *Handler) serverIP() string {
	if h.cfg != nil && h.cfg.PublicHost != "" {
		return h.cfg.PublicHost
	}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		host, err := os.Hostname()
		if err != nil {
			host = "unknown"
		}
		return host
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return host
}

// allowedOrigins returns the list of origins permitted to open WebSocket
// connections. Reuses CORSOrigin from config. A wildcard "*" disables
// origin checking entirely. Set CORS_ORIGIN to a specific scheme+host
// (e.g. https://webkvm.local) to restrict.
var allowedOrigins = func() []string {
	return []string{"*"}
}

// SetAllowedOrigins lets the router inject the config value at startup.
func SetAllowedOrigins(origins []string) {
	allowedOrigins = func() []string { return origins }
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			// Non-browser client (no Origin header) — allow.
			return true
		}
		origins := allowedOrigins()
		// If no explicit allowlist is configured (default "*"), require
		// same-origin: the console page and its WebSocket are served by
		// this backend, so a malicious cross-site page must not be able
		// to open a VNC WebSocket against it.
		if len(origins) == 1 && (origins[0] == "*" || origins[0] == "") {
			o, err := url.Parse(origin)
			return err == nil && o.Host == r.Host
		}
		for _, a := range origins {
			if a != "" && origin == a {
				return true
			}
		}
		slog.Warn("websocket_origin_rejected", "origin", origin, "host", r.Host)
		return false
	},
}

// ListJailedIPs returns all currently banned IPs.
func (h *Handler) ListJailedIPs(w http.ResponseWriter, r *http.Request) {
	if h.jail == nil {
		jsonResp(w, http.StatusOK, []any{})
		return
	}
	jsonResp(w, http.StatusOK, h.jail.ListBanned())
}

// UnbanJailedIP unbans an IP manually.
func (h *Handler) UnbanJailedIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IP string `json:"ip"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if h.jail == nil {
		jsonErr(w, http.StatusServiceUnavailable, "jail not initialized")
		return
	}
	if err := h.jail.Unban(req.IP); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "unbanned", "ip": req.IP})
}

func (h *Handler) GetGraphics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	info, err := h.compute.GetVNCInfo(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, info)
}

// SPICEProxy proxies SPICE client connections over WebSocket to the local QEMU SPICE port.
func (h *Handler) SPICEProxy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	info, err := h.compute.GetSPICEInfo(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}

	spiceHost := h.cfg.VNCProxyHost
	if spiceHost == "" {
		spiceHost = "127.0.0.1"
	}
	spiceAddr := net.JoinHostPort(spiceHost, strconv.Itoa(info.Port))
	slog.Info("spice_proxy_dialing", "vm_id", id, "spice_port", info.Port, "spice_addr", spiceAddr)

	responseHeader := http.Header{}
	if len(r.Header.Values("Sec-WebSocket-Protocol")) > 0 {
		responseHeader.Set("Sec-WebSocket-Protocol", "binary")
	}

	ws, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		slog.Error("spice_proxy_upgrade_failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "websocket upgrade failed")
		return
	}

	tcpConn, err := net.DialTimeout("tcp", spiceAddr, 10*time.Second)
	if err != nil {
		slog.Error("spice_proxy_dial_failed", "addr", spiceAddr, "err", err)
		_ = ws.Close()
		return
	}

	if tc, ok := tcpConn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	const (
		pongWait   = 60 * time.Second
		pingPeriod = 20 * time.Second
		writeWait  = 10 * time.Second
	)

	_ = ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	done := make(chan struct{})
	var closeOnce sync.Once
	var writeMu sync.Mutex
	closeAll := func() {
		closeOnce.Do(func() {
			close(done)
			_ = ws.Close()
			_ = tcpConn.Close()
		})
	}

	ws.SetCloseHandler(func(code int, text string) error {
		closeAll()
		return nil
	})

	go func() {
		defer safego.Recover("spice_ping_ticker")
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeMu.Lock()
				_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
				err := ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait))
				writeMu.Unlock()
				if err != nil {
					closeAll()
					return
				}
			case <-done:
				return
			}
		}
	}()

	go func() {
		defer safego.Recover("spice_ws_to_tcp")
		defer closeAll()
		for {
			msgType, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if msgType == websocket.BinaryMessage || msgType == websocket.TextMessage {
				_ = tcpConn.SetWriteDeadline(time.Now().Add(writeWait))
				if _, err := tcpConn.Write(data); err != nil {
					return
				}
			}
		}
	}()

	go func() {
		defer safego.Recover("spice_tcp_to_ws")
		defer closeAll()
		buf := make([]byte, 32768)
		for {
			n, err := tcpConn.Read(buf)
			if n > 0 {
				writeMu.Lock()
				_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
				werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n])
				writeMu.Unlock()
				if werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	<-done
}

func (h *Handler) VNCProxy(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	info, err := h.compute.GetVNCInfo(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}

	// The VNC port is bound on the host that runs libvirtd. In a
	// normal systemd install that's the same host as this backend,
	// so 127.0.0.1 works. Override VNC_PROXY_HOST only for a custom
	// libvirtd bind address.
	vncHost := h.cfg.VNCProxyHost
	if vncHost == "" {
		vncHost = "127.0.0.1"
	}
	vncAddr := net.JoinHostPort(vncHost, strconv.Itoa(info.Port))
	slog.Info("vnc_proxy_dialing", "vm_id", id, "libvirt_host", info.Host, "libvirt_port", info.Port, "vnc_addr", vncAddr)

	// Echo the noVNC "binary" subprotocol back to the client. The console
	// page opens the socket with wsProtocols: ['binary'], and RFC 6455 §4.1
	// requires the server to echo a requested subprotocol — if it does not,
	// the browser fails the connection, which surfaced as the VNC console
	// connecting for ~1ms and dropping (bytes=0).
	responseHeader := http.Header{}
	if len(r.Header.Values("Sec-WebSocket-Protocol")) > 0 {
		responseHeader.Set("Sec-WebSocket-Protocol", "binary")
	}

	ws, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		slog.Error("vnc_proxy_upgrade_failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "websocket upgrade failed")
		return
	}

	tcpConn, err := net.DialTimeout("tcp", vncAddr, 10*time.Second)
	if err != nil {
		slog.Error("vnc_proxy_dial_failed", "addr", vncAddr, "err", err)
		_ = ws.Close()
		return
	}

	if tc, ok := tcpConn.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}

	const (
		pongWait   = 60 * time.Second
		pingPeriod = 20 * time.Second
		writeWait  = 10 * time.Second
	)

	ws.SetReadLimit(4 << 20) // 4MB
	_ = ws.SetReadDeadline(time.Now().Add(pongWait))
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	var writeMu sync.Mutex
	var closeOnce sync.Once
	cleanup := func() {
		closeOnce.Do(func() {
			_ = tcpConn.Close()
			_ = ws.Close()
		})
	}
	defer cleanup()

	ws.SetCloseHandler(func(code int, text string) error {
		cleanup()
		return nil
	})

	done := make(chan struct{})

	// Ping ticker to keep WebSocket alive through reverse proxies, firewalls and idle periods
	go func() {
		defer safego.Recover("vnc_ping_ticker")
		ticker := time.NewTicker(pingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				writeMu.Lock()
				_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
				err := ws.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait))
				writeMu.Unlock()
				if err != nil {
					cleanup()
					return
				}
			case <-done:
				return
			}
		}
	}()

	errc := make(chan error, 2)

	go func() {
		defer safego.Recover("vnc_ws_to_tcp")
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			_ = ws.SetReadDeadline(time.Now().Add(pongWait))
			if _, err := tcpConn.Write(msg); err != nil {
				errc <- err
				return
			}
		}
	}()

	go func() {
		defer safego.Recover("vnc_tcp_to_ws")
		buf := make([]byte, 65536)
		for {
			n, err := tcpConn.Read(buf)
			if err != nil {
				errc <- err
				return
			}
			writeMu.Lock()
			_ = ws.SetWriteDeadline(time.Now().Add(writeWait))
			wErr := ws.WriteMessage(websocket.BinaryMessage, buf[:n])
			writeMu.Unlock()
			if wErr != nil {
				errc <- wErr
				return
			}
		}
	}()

	<-errc
	close(done)
}

func (h *Handler) ConsolePage(w http.ResponseWriter, r *http.Request) {
	id := html.EscapeString(chi.URLParam(r, "id"))

	html := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Console - %s</title>
<style>
:root{--indigo-400:#818cf8;--indigo-500:#6366f1;--indigo-600:#4f46e5;--indigo-700:#4338ca;--slate-800:#1e293b;--slate-850:#0f1729;--slate-900:#020617;--slate-950:#010101}
*{margin:0;padding:0;box-sizing:border-box}
html,body{background:var(--slate-900);overflow:hidden;height:100%%;width:100%%;font-family:system-ui,-apple-system,sans-serif}
#screen, #spice-area, #spice-screen{width:100%%;height:100%%;display:flex;align-items:center;justify-content:center;overflow:hidden}
canvas{display:block;margin:auto;max-width:100%%;max-height:100%%;object-fit:contain}
/* ---- sidebar ---- */
#sidebar{position:fixed;left:0;top:50%%;transform:translateY(-50%%);display:flex;flex-direction:column;gap:2px;padding:6px;border-radius:0 12px 12px 0;background:rgba(2,6,23,.75);backdrop-filter:blur(16px);border:1px solid rgba(99,102,241,.1);border-left:none;box-shadow:4px 0 24px rgba(0,0,0,.3);z-index:20;opacity:0;transition:opacity .3s}
#sidebar.visible{opacity:1}
#sidebar button{display:flex;align-items:center;justify-content:center;width:36px;height:36px;border:none;border-radius:8px;background:transparent;color:#64748b;cursor:pointer;transition:all .15s;position:relative}
#sidebar button:hover{background:rgba(99,102,241,.12);color:#c7d2fe}
#sidebar button.active{background:rgba(99,102,241,.18);color:var(--indigo-400)}
#sidebar button svg{width:18px;height:18px}
#sidebar button .badge{position:absolute;top:2px;right:2px;width:7px;height:7px;border-radius:50%%;background:#ef4444}
#sidebar .sep{height:1px;margin:4px 10px;background:rgba(148,163,184,.08)}
#sidebar .reconnect{color:#ef4444}
#sidebar .reconnect:hover{background:rgba(239,68,68,.15);color:#fca5a5}
/* ---- panel ---- */
#panel{position:fixed;left:48px;top:50%%;transform:translateY(-50%%) translateX(-320px);width:280px;max-height:420px;border-radius:12px;background:rgba(2,6,23,.82);backdrop-filter:blur(16px);border:1px solid rgba(99,102,241,.12);box-shadow:0 8px 40px rgba(0,0,0,.5);z-index:19;transition:transform .25s cubic-bezier(.22,1,.36,1);overflow:hidden;display:flex;flex-direction:column}
#panel.open{transform:translateY(-50%%) translateX(0)}
#panel .phead{display:flex;align-items:center;justify-content:space-between;padding:12px 14px 8px;border-bottom:1px solid rgba(148,163,184,.08)}
#panel .phead h3{font-size:13px;font-weight:600;color:#e2e8f0;letter-spacing:.02em}
#panel .phead .close{background:none;border:none;color:#64748b;cursor:pointer;padding:2px;border-radius:4px}
#panel .phead .close:hover{color:#e2e8f0;background:rgba(148,163,184,.1)}
#panel .pbody{padding:12px 14px 14px;overflow-y:auto;flex:1}
#panel .pbody .kbtn{display:flex;align-items:center;gap:10px;width:100%%;padding:9px 12px;margin-bottom:4px;border:1px solid rgba(148,163,184,.08);border-radius:8px;background:rgba(255,255,255,.03);color:#94a3b8;cursor:pointer;font:12px/1 system-ui,sans-serif;transition:all .12s}
#panel .pbody .kbtn:hover{background:rgba(99,102,241,.12);border-color:rgba(99,102,241,.2);color:#e2e8f0}
#panel .pbody .kbtn:active{transform:scale(.97)}
#panel .pbody .kbtn .kicon{width:28px;height:28px;display:flex;align-items:center;justify-content:center;border-radius:6px;background:rgba(99,102,241,.1);flex-shrink:0}
#panel .pbody .kbtn .kicon svg{width:14px;height:14px;color:var(--indigo-400)}
#panel .pbody .kbtn .klabel{flex:1}
#panel .pbody .kbtn .kkeys{font-size:10px;color:#64748b;background:rgba(148,163,184,.06);padding:2px 7px;border-radius:4px;font-family:monospace}
#panel .pbody .srow{display:flex;align-items:center;justify-content:space-between;padding:8px 0}
#panel .pbody .srow+.srow{border-top:1px solid rgba(148,163,184,.05)}
#panel .pbody .srow .slabel{font-size:12px;color:#94a3b8}
#panel .pbody .srow .sdesc{font-size:10px;color:#64748b;margin-top:1px}
#panel .pbody .srow .stoggle{position:relative;width:36px;height:20px;flex-shrink:0;border-radius:10px;background:rgba(148,163,184,.15);cursor:pointer;transition:background .2s}
#panel .pbody .srow .stoggle.on{background:rgba(99,102,241,.5)}
#panel .pbody .srow .stoggle .knob{position:absolute;top:2px;left:2px;width:16px;height:16px;border-radius:50%%;background:#94a3b8;transition:all .2s}
#panel .pbody .srow .stoggle.on .knob{left:18px;background:#e2e8f0;box-shadow:0 0 8px rgba(99,102,241,.3)}
#panel .pbody .irow{display:flex;justify-content:space-between;padding:5px 0;font-size:12px}
#panel .pbody .irow .ilabel{color:#64748b}
#panel .pbody .irow .ivalue{color:#94a3b8;font-family:monospace;font-size:11px}
#panel .pbody .srow.vertical{flex-direction:column;align-items:stretch;gap:6px}
#panel .pbody .srange{-webkit-appearance:none;appearance:none;width:100%%;height:4px;border-radius:2px;background:rgba(148,163,184,.15);outline:none}
#panel .pbody .srange::-webkit-slider-thumb{-webkit-appearance:none;appearance:none;width:14px;height:14px;border-radius:50%%;background:var(--indigo-400);cursor:pointer;box-shadow:0 0 6px rgba(99,102,241,.4)}
#panel .pbody .srange::-moz-range-thumb{width:14px;height:14px;border-radius:50%%;border:none;background:var(--indigo-400);cursor:pointer;box-shadow:0 0 6px rgba(99,102,241,.4)}
#panel .pbody .pbtn{display:flex;align-items:center;gap:10px;width:100%%;padding:9px 12px;margin-bottom:4px;border:1px solid rgba(148,163,184,.08);border-radius:8px;background:rgba(255,255,255,.03);color:#94a3b8;cursor:pointer;font:12px/1 system-ui,sans-serif;transition:all .12s}
#panel .pbody .pbtn:hover{background:rgba(99,102,241,.12);border-color:rgba(99,102,241,.2);color:#e2e8f0}
#panel .pbody .pbtn.danger:hover{background:rgba(239,68,68,.12);border-color:rgba(239,68,68,.25);color:#fca5a5}
#panel .pbody .pconfirm{display:flex;align-items:center;gap:8px;padding:8px 12px;margin-bottom:4px;border-radius:8px;background:rgba(239,68,68,.08);border:1px solid rgba(239,68,68,.2);font-size:11px;color:#fca5a5}
#panel .pbody .pconfirm button{border:none;border-radius:6px;padding:4px 9px;font-size:11px;cursor:pointer}
#panel .pbody .pconfirm .pyes{background:#ef4444;color:#fff}
#panel .pbody .pconfirm .pno{background:rgba(148,163,184,.15);color:#e2e8f0}
/* touch-friendly targets on coarse pointers (tablets/phones) */
@media (pointer:coarse){
#sidebar button{width:46px;height:46px}
#sidebar button svg{width:22px;height:22px}
#panel{width:300px}
#panel .pbody .kbtn,#panel .pbody .pbtn{padding:12px}
}
/* embedded mode for multiview / dashboard */
body.embedded #sidebar{display:none !important}
body.embedded #panel{display:none !important}
body.embedded #vkeyboard{display:none !important}
body.embedded #status{bottom:8px;right:8px;padding:3px 8px;font-size:10px}
/* ---- virtual keyboard ---- */
#vkeyboard{position:fixed;bottom:0;left:50%%;transform:translateX(-50%%) translateY(105%%);width:min(960px,98vw);max-height:48vh;border-radius:14px 14px 0 0;background:rgba(2,6,23,.5);backdrop-filter:blur(6px);-webkit-backdrop-filter:blur(6px);border:1px solid rgba(99,102,241,.25);border-bottom:none;box-shadow:0 -10px 40px rgba(0,0,0,.35);z-index:30;transition:transform .28s cubic-bezier(.16,1,.3,1),background .2s,backdrop-filter .2s;display:flex;flex-direction:column;padding:10px 12px 12px;user-select:none;-webkit-user-select:none;touch-action:manipulation}
#vkeyboard.open{transform:translateX(-50%%) translateY(0)}
#vkeyboard.opacity-low{background:rgba(2,6,23,.22);backdrop-filter:blur(3px);-webkit-backdrop-filter:blur(3px)}
#vkeyboard.opacity-low .vk-key{background:rgba(15,23,42,.28);border-color:rgba(148,163,184,.1)}
#vkeyboard.opacity-high{background:rgba(2,6,23,.85);backdrop-filter:blur(14px);-webkit-backdrop-filter:blur(14px)}
#vkeyboard.opacity-high .vk-key{background:rgba(30,41,59,.75)}
.vk-header{display:flex;align-items:center;justify-content:space-between;margin-bottom:8px;padding:0 4px}
.vk-title{display:flex;align-items:center;gap:7px;font-size:12px;font-weight:600;color:#c7d2fe;letter-spacing:.02em}
.vk-title svg{width:15px;height:15px;color:var(--indigo-400)}
.vk-actions{display:flex;align-items:center;gap:6px}
.vk-action-btn{background:rgba(148,163,184,.12);border:1px solid rgba(148,163,184,.18);color:#94a3b8;cursor:pointer;padding:4px 8px;border-radius:6px;display:flex;align-items:center;justify-content:center;gap:4px;font-size:11px;font-weight:500;transition:all .15s}
.vk-action-btn:hover{background:rgba(99,102,241,.25);color:#fff;border-color:rgba(99,102,241,.35)}
.vk-quick-bar{display:flex;align-items:center;gap:5px;overflow-x:auto;padding-bottom:6px;margin-bottom:6px;border-bottom:1px solid rgba(148,163,184,.12);scrollbar-width:none}
.vk-quick-bar::-webkit-scrollbar{display:none}
.vk-chip{background:rgba(255,255,255,.05);border:1px solid rgba(148,163,184,.15);color:#cbd5e1;font:11px/1 system-ui,sans-serif;padding:5px 9px;border-radius:5px;cursor:pointer;white-space:nowrap;transition:all .12s;flex-shrink:0}
.vk-chip:hover{background:rgba(99,102,241,.25);border-color:rgba(99,102,241,.4);color:#fff}
.vk-chip:active{transform:scale(.96)}
.vk-sep{width:1px;height:14px;background:rgba(148,163,184,.2);margin:0 2px;flex-shrink:0}
.vk-board{display:flex;flex-direction:column;gap:5px;width:100%%}
.vk-row{display:flex;gap:4px;width:100%%;justify-content:center}
.vk-key{display:flex;flex-direction:column;align-items:center;justify-content:center;height:36px;min-width:28px;flex:1;background:rgba(30,41,59,.42);border:1px solid rgba(148,163,184,.18);border-radius:6px;color:#f1f5f9;font:12px system-ui,sans-serif;cursor:pointer;transition:all .1s;position:relative;box-shadow:0 2px 0 rgba(0,0,0,.25)}
.vk-key:hover{background:rgba(99,102,241,.28);border-color:rgba(99,102,241,.4);color:#fff}
.vk-key:active,.vk-key.pressed{background:rgba(99,102,241,.45);border-color:rgba(99,102,241,.6);color:#fff;transform:translateY(2px);box-shadow:none}
.vk-key.active{background:rgba(99,102,241,.5);border-color:var(--indigo-400);color:#fff;box-shadow:0 0 10px rgba(99,102,241,.5)}
.vk-key .vk-sub{font-size:9px;color:#94a3b8;line-height:1;margin-bottom:1px}
.vk-key .vk-main{font-size:12px;font-weight:500;line-height:1}
.vk-key.fn{font-size:11px;background:rgba(15,23,42,.42);color:#94a3b8}
.vk-key.mod{font-size:11px;font-weight:500;color:#c7d2fe;background:rgba(49,46,129,.4)}
.vk-key.w-12{flex:1.2}
.vk-key.w-14{flex:1.4}
.vk-key.w-16{flex:1.6}
.vk-key.w-18{flex:1.8}
.vk-key.w-20{flex:2}
.vk-key.w-22{flex:2.2}
.vk-key.w-space{flex:4.5}
/* ---- status ---- */
#status{position:fixed;bottom:20px;right:20px;padding:6px 14px 6px 10px;border-radius:20px;font:11px/1.4 monospace;color:#94a3b8;background:rgba(2,6,23,.7);backdrop-filter:blur(8px);border:1px solid rgba(99,102,241,.08);pointer-events:none;transition:all .3s;display:flex;align-items:center;gap:6px}
#status .dot{width:6px;height:6px;border-radius:50%%;flex-shrink:0}
#status .dot.connecting{background:#facc15;animation:pulse 1.2s infinite}
#status .dot.ok{background:#22c55e;box-shadow:0 0 6px rgba(34,197,94,.4)}
#status .dot.error{background:#ef4444;box-shadow:0 0 6px rgba(239,68,68,.4)}
@keyframes pulse{0%%,100%%{opacity:1}50%%{opacity:.3}}
@keyframes fadeIn{from{opacity:0;transform:translateY(4px)}to{opacity:1;transform:translateY(0)}}
</style>
</head>
<body>
<div id="sidebar" class="visible">
  <button id="btnFullscreen" title="Fullscreen (F11)">
    <svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><path d="M8 3H5a2 2 0 0 0-2 2v3"/><path d="M21 8V5a2 2 0 0 0-2-2h-3"/><path d="M16 21h3a2 2 0 0 0 2-2v-3"/><path d="M3 16v3a2 2 0 0 0 2 2h3"/></svg>
  </button>
  <div class="sep"></div>
  <button id="btnToggleVKeyboard" title="Virtual Keyboard">
    <svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="M6 8h.01"/><path d="M10 8h.01"/><path d="M14 8h.01"/><path d="M18 8h.01"/><path d="M6 12h.01"/><path d="M10 12h.01"/><path d="M14 12h.01"/><path d="M18 12h.01"/><path d="M7 16h10"/></svg>
  </button>
  <button id="btnToggleKeys" title="Keyboard shortcuts">
    <svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><path d="M18 3a3 3 0 0 0-3 3v12a3 3 0 0 0 3 3 3 3 0 0 0 3-3 3 3 0 0 0-3-3H6a3 3 0 0 0-3 3 3 3 0 0 0 3 3 3 3 0 0 0 3-3V6a3 3 0 0 0-3-3 3 3 0 0 0-3 3 3 3 0 0 0 3 3h12a3 3 0 0 0 3-3 3 3 0 0 0-3-3z"/></svg>
  </button>
  <button id="btnToggleSettings" title="Settings">
    <svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
  </button>
  <button id="btnToggleInfo" title="Connection info">
    <svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><circle cx="12" cy="12" r="10"/><path d="M12 16v-4"/><path d="M12 8h.01"/></svg>
  </button>
  <div class="sep"></div>
  <button id="btnReconnect" class="reconnect" title="Reconnect" style="display:none">
    <svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><path d="M21 12a9 9 0 0 1-9 9m9-9a9 9 0 0 0-9-9m9 9H3m9 9a9 9 0 0 1-9-9m9 9c1.657 0 3-4.03 3-9s-1.343-9-3-9m0 18c-1.657 0-3-4.03-3-9s1.343-9 3-9m-9 9a9 9 0 0 1 9-9"/></svg>
  </button>
</div>
<div id="panel">
  <div class="phead">
    <h3 id="panelTitle"></h3>
    <button class="close" id="panelClose"><svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" width="16" height="16"><path d="M18 6 6 18"/><path d="m6 6 12 12"/></svg></button>
  </div>
  <div class="pbody" id="panelBody"></div>
</div>
<div id="vkeyboard">
  <div class="vk-header">
    <div class="vk-title">
      <svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="M6 8h.01"/><path d="M10 8h.01"/><path d="M14 8h.01"/><path d="M18 8h.01"/><path d="M6 12h.01"/><path d="M10 12h.01"/><path d="M14 12h.01"/><path d="M18 12h.01"/><path d="M7 16h10"/></svg>
      <span>Teclado Virtual</span>
    </div>
    <div class="vk-actions">
      <button class="vk-action-btn" id="vkLayoutBtn" title="Cambiar distribución (Español / English)">
        <span id="vkLayoutLabel" style="font-weight:700;font-size:10px;">ES</span>
      </button>
      <button class="vk-action-btn" id="vkOpacityBtn" title="Cambiar transparencia">
        <svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" width="13" height="13"><path d="M2 12s3-7 10-7 10 7 10 7-3 7-10 7-10-7-10-7Z"/><circle cx="12" cy="12" r="3"/></svg>
        <span id="vkOpacityLabel">50%%</span>
      </button>
      <button class="vk-action-btn" id="vkCloseBtn" title="Ocultar teclado">
        <svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24" width="14" height="14"><polyline points="6 9 12 15 18 9"/></svg>
      </button>
    </div>
  </div>
  <div class="vk-quick-bar">
    <button class="vk-chip" data-macro="cad">Ctrl+Alt+Del</button>
    <button class="vk-chip" data-macro="alttab">Alt+Tab</button>
    <button class="vk-chip" data-macro="altf4">Alt+F4</button>
    <button class="vk-chip" data-macro="ctrlc">Ctrl+C</button>
    <button class="vk-chip" data-macro="ctrlv">Ctrl+V</button>
    <button class="vk-chip" data-macro="ctrlz">Ctrl+Z</button>
    <button class="vk-chip" data-macro="ctrla">Ctrl+A</button>
    <button class="vk-chip" data-macro="taskmgr">Ctrl+Shift+Esc</button>
    <div class="vk-sep"></div>
    <button class="vk-chip" data-macro="home">Home</button>
    <button class="vk-chip" data-macro="end">End</button>
    <button class="vk-chip" data-macro="pgup">PgUp</button>
    <button class="vk-chip" data-macro="pgdn">PgDn</button>
  </div>
  <div class="vk-board" id="vkBoard"></div>
</div>
<div id="screen"></div>
<div id="spice-area" style="display:none;width:100%%;height:100%%;position:relative;overflow:hidden;background:#020617;">
  <div id="spice-screen" style="width:100%%;height:100%%;display:flex;align-items:center;justify-content:center;overflow:hidden;"></div>
  <div id="message-div" style="display:none;"></div>
</div>
<div id="status"><span class="dot connecting"></span>Connecting</div>
<script type="module">
import RFB from '/static/novnc.mjs';
import { SpiceMainConn, sendCtrlAltDel, sendSpiceKey, handle_keydown, handle_keyup, fit_spice_canvas, handle_resize } from '/static/spice.mjs';

history.pushState(null, null, location.href);
window.onpopstate = function () {
    history.pushState(null, null, location.href);
};

var wsProto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
var host = window.location.hostname;
var port = window.location.port || (window.location.protocol === 'https:' ? '443' : '80');
var searchParams = new URLSearchParams(window.location.search);
var vt = searchParams.get('vt') || '';
var isEmbedded = searchParams.get('embedded') === '1';
var consoleMode = searchParams.get('mode') || 'vnc'; // 'vnc' or 'spice'
if (isEmbedded) {
    document.body.classList.add('embedded');
}
var url = wsProto + '//' + host + ':' + port + '/api/vms/%s/' + (consoleMode === 'spice' ? 'spice-ws' : 'vnc') + '?vt=' + encodeURIComponent(vt);

var rfb = null, spiceConn = null, connected = false, activePanel = null, vmId = '%s';
var reconnectAttempts = 0, everConnected = false, autoRetry = true;
var statusEl = document.getElementById('status');
var sidebar = document.getElementById('sidebar');
var panel = document.getElementById('panel');
var panelTitle = document.getElementById('panelTitle');
var panelBody = document.getElementById('panelBody');
var vkeyboard = document.getElementById('vkeyboard');
var vkBoard = document.getElementById('vkBoard');

function updateStatus(state, msg) {
    var dot = statusEl.querySelector('.dot');
    dot.className = 'dot ' + state;
    statusEl.childNodes[1].nodeValue = ' ' + msg;
}

function sendKeyCombo(keys) {
    if (consoleMode === 'spice') {
        if (!spiceConn || !connected) return;
        if (keys.length === 3 && keys[2][0] === 0xffff) {
            sendCtrlAltDel(spiceConn);
            return;
        }
        for (var i = 0; i < keys.length; i++) {
            sendSpiceKey(spiceConn, keys[i][1], true);
        }
        setTimeout(function () {
            for (var i = keys.length - 1; i >= 0; i--) {
                sendSpiceKey(spiceConn, keys[i][1], false);
            }
        }, 80);
        return;
    }
    if (!rfb || !connected) return;
    for (var i = 0; i < keys.length; i++)
        rfb.sendKey(keys[i][0], keys[i][1], true);
    setTimeout(function () {
        for (var i = keys.length - 1; i >= 0; i--)
            rfb.sendKey(keys[i][0], keys[i][1], false);
    }, 80);
}

function openPanel(name, title, html) {
    panelTitle.textContent = title;
    panelBody.innerHTML = html;
    panel.classList.add('open');
    activePanel = name;
    document.querySelectorAll('#sidebar button').forEach(function (b) {
        b.classList.toggle('active', b.id === 'btnToggle' + name.charAt(0).toUpperCase() + name.slice(1));
    });
}

function closePanel() {
    panel.classList.remove('open');
    activePanel = null;
    document.querySelectorAll('#sidebar button').forEach(function (b) { b.classList.remove('active'); });
}

/* ---- virtual keyboard implementation ---- */
var vkOpen = false;
var vkModifiers = {
    shift: false,
    ctrl: false,
    alt: false,
    super: false,
    altgr: false,
    caps: false
};

var currentVkLayout = localStorage.getItem('webkvm_vk_layout') || (navigator.language && navigator.language.startsWith('es') ? 'es' : 'us');

var vkFnRow = [
    { main: 'Esc', sym: 0xff1b, code: 'Escape', cls: 'fn w-12' },
    { main: 'F1', sym: 0xffbe, code: 'F1', cls: 'fn' },
    { main: 'F2', sym: 0xffbf, code: 'F2', cls: 'fn' },
    { main: 'F3', sym: 0xffc0, code: 'F3', cls: 'fn' },
    { main: 'F4', sym: 0xffc1, code: 'F4', cls: 'fn' },
    { main: 'F5', sym: 0xffc2, code: 'F5', cls: 'fn' },
    { main: 'F6', sym: 0xffc3, code: 'F6', cls: 'fn' },
    { main: 'F7', sym: 0xffc4, code: 'F7', cls: 'fn' },
    { main: 'F8', sym: 0xffc5, code: 'F8', cls: 'fn' },
    { main: 'F9', sym: 0xffc6, code: 'F9', cls: 'fn' },
    { main: 'F10', sym: 0xffc7, code: 'F10', cls: 'fn' },
    { main: 'F11', sym: 0xffc8, code: 'F11', cls: 'fn' },
    { main: 'F12', sym: 0xffc9, code: 'F12', cls: 'fn' },
    { main: 'PrtSc', sym: 0xff61, code: 'PrintScreen', cls: 'fn' },
    { main: 'Del', sym: 0xffff, code: 'Delete', cls: 'fn w-12' }
];

var vkRowsES = [
    vkFnRow,
    // Row 1: Numbers & symbols (ES ISO)
    [
        { main: 'º', shift: 'ª', altgr: '\\', sym: 0xba, shiftSym: 0xaa, altgrSym: 0x5c, code: 'Backquote' },
        { main: '1', shift: '!', altgr: '|', sym: 0x31, shiftSym: 0x21, altgrSym: 0x7c, code: 'Digit1' },
        { main: '2', shift: '"', altgr: '@', sym: 0x32, shiftSym: 0x22, altgrSym: 0x40, code: 'Digit2' },
        { main: '3', shift: '·', altgr: '#', sym: 0x33, shiftSym: 0xb7, altgrSym: 0x23, code: 'Digit3' },
        { main: '4', shift: '$', altgr: '~', sym: 0x34, shiftSym: 0x24, altgrSym: 0x7e, code: 'Digit4' },
        { main: '5', shift: '%%', sym: 0x35, shiftSym: 0x25, code: 'Digit5' },
        { main: '6', shift: '&', altgr: '¬', sym: 0x36, shiftSym: 0x26, altgrSym: 0xac, code: 'Digit6' },
        { main: '7', shift: '/', sym: 0x37, shiftSym: 0x2f, code: 'Digit7' },
        { main: '8', shift: '(', sym: 0x38, shiftSym: 0x28, code: 'Digit8' },
        { main: '9', shift: ')', sym: 0x39, shiftSym: 0x29, code: 'Digit9' },
        { main: '0', shift: '=', sym: 0x30, shiftSym: 0x3d, code: 'Digit0' },
        { main: '\'', shift: '?', sym: 0x27, shiftSym: 0x3f, code: 'Minus' },
        { main: '¡', shift: '¿', sym: 0xa1, shiftSym: 0xbf, code: 'Equal' },
        { main: '⌫ Backspace', sym: 0xff08, code: 'Backspace', cls: 'mod w-18' }
    ],
    // Row 2: QWERTY (ES ISO)
    [
        { main: 'Tab', sym: 0xff09, code: 'Tab', cls: 'mod w-14' },
        { main: 'q', shift: 'Q', sym: 0x71, shiftSym: 0x51, code: 'KeyQ' },
        { main: 'w', shift: 'W', sym: 0x77, shiftSym: 0x57, code: 'KeyW' },
        { main: 'e', shift: 'E', altgr: '€', sym: 0x65, shiftSym: 0x45, altgrSym: 0x20ac, code: 'KeyE' },
        { main: 'r', shift: 'R', sym: 0x72, shiftSym: 0x52, code: 'KeyR' },
        { main: 't', shift: 'T', sym: 0x74, shiftSym: 0x54, code: 'KeyT' },
        { main: 'y', shift: 'Y', sym: 0x79, shiftSym: 0x59, code: 'KeyY' },
        { main: 'u', shift: 'U', sym: 0x75, shiftSym: 0x55, code: 'KeyU' },
        { main: 'i', shift: 'I', sym: 0x69, shiftSym: 0x49, code: 'KeyI' },
        { main: 'o', shift: 'O', sym: 0x6f, shiftSym: 0x4f, code: 'KeyO' },
        { main: 'p', shift: 'P', sym: 0x70, shiftSym: 0x50, code: 'KeyP' },
        { main: '\u0060', shift: '^', altgr: '[', sym: 0x60, shiftSym: 0x5e, altgrSym: 0x5b, code: 'BracketLeft' },
        { main: '+', shift: '*', altgr: ']', sym: 0x2b, shiftSym: 0x2a, altgrSym: 0x5d, code: 'BracketRight' },
        { main: 'ç', shift: 'Ç', altgr: '}', sym: 0xe7, shiftSym: 0xc7, altgrSym: 0x7d, code: 'Backslash', cls: 'w-12' }
    ],
    // Row 3: Home row (ES ISO with Ñ)
    [
        { main: 'Caps Lock', sym: 0xffe5, code: 'CapsLock', mod: 'caps', cls: 'mod w-16' },
        { main: 'a', shift: 'A', sym: 0x61, shiftSym: 0x41, code: 'KeyA' },
        { main: 's', shift: 'S', sym: 0x73, shiftSym: 0x53, code: 'KeyS' },
        { main: 'd', shift: 'D', sym: 0x64, shiftSym: 0x44, code: 'KeyD' },
        { main: 'f', shift: 'F', sym: 0x66, shiftSym: 0x46, code: 'KeyF' },
        { main: 'g', shift: 'G', sym: 0x67, shiftSym: 0x47, code: 'KeyG' },
        { main: 'h', shift: 'H', sym: 0x68, shiftSym: 0x48, code: 'KeyH' },
        { main: 'j', shift: 'J', sym: 0x6a, shiftSym: 0x4a, code: 'KeyJ' },
        { main: 'k', shift: 'K', sym: 0x6b, shiftSym: 0x4b, code: 'KeyK' },
        { main: 'l', shift: 'L', sym: 0x6c, shiftSym: 0x4c, code: 'KeyL' },
        { main: 'ñ', shift: 'Ñ', sym: 0xf1, shiftSym: 0xd1, code: 'Semicolon' },
        { main: '´', shift: '¨', altgr: '{', sym: 0xb4, shiftSym: 0xa8, altgrSym: 0x7b, code: 'Quote' },
        { main: '⏎ Enter', sym: 0xff0d, code: 'Enter', cls: 'mod w-20' }
    ],
    // Row 4: Shift row (ES ISO with < >)
    [
        { main: '⇧ Shift', sym: 0xffe1, code: 'ShiftLeft', mod: 'shift', cls: 'mod w-18' },
        { main: '<', shift: '>', sym: 0x3c, shiftSym: 0x3e, code: 'IntlBackslash' },
        { main: 'z', shift: 'Z', sym: 0x7a, shiftSym: 0x5a, code: 'KeyZ' },
        { main: 'x', shift: 'X', sym: 0x78, shiftSym: 0x58, code: 'KeyX' },
        { main: 'c', shift: 'C', sym: 0x63, shiftSym: 0x43, code: 'KeyC' },
        { main: 'v', shift: 'V', sym: 0x76, shiftSym: 0x56, code: 'KeyV' },
        { main: 'b', shift: 'B', sym: 0x62, shiftSym: 0x42, code: 'KeyB' },
        { main: 'n', shift: 'N', sym: 0x6e, shiftSym: 0x4e, code: 'KeyN' },
        { main: 'm', shift: 'M', sym: 0x6d, shiftSym: 0x4d, code: 'KeyM' },
        { main: ',', shift: ';', sym: 0x2c, shiftSym: 0x3b, code: 'Comma' },
        { main: '.', shift: ':', sym: 0x2e, shiftSym: 0x3a, code: 'Period' },
        { main: '-', shift: '_', sym: 0x2d, shiftSym: 0x5f, code: 'Slash' },
        { main: '⇧ Shift', sym: 0xffe2, code: 'ShiftRight', mod: 'shift', cls: 'mod w-16' },
        { main: '▲', sym: 0xff52, code: 'ArrowUp', cls: 'fn w-12' }
    ],
    // Row 5: Bottom row
    [
        { main: 'Ctrl', sym: 0xffe3, code: 'ControlLeft', mod: 'ctrl', cls: 'mod w-14' },
        { main: '⊞ Win', sym: 0xffeb, code: 'MetaLeft', mod: 'super', cls: 'mod w-12' },
        { main: 'Alt', sym: 0xffe9, code: 'AltLeft', mod: 'alt', cls: 'mod w-12' },
        { main: 'Space', sym: 0x20, code: 'Space', cls: 'w-space' },
        { main: 'AltGr', sym: 0xffea, code: 'AltRight', mod: 'altgr', cls: 'mod w-12' },
        { main: '◄', sym: 0xff51, code: 'ArrowLeft', cls: 'fn w-12' },
        { main: '▼', sym: 0xff54, code: 'ArrowDown', cls: 'fn w-12' },
        { main: '►', sym: 0xff53, code: 'ArrowRight', cls: 'fn w-12' }
    ]
];

var vkRowsUS = [
    vkFnRow,
    // Row 1: Numbers & symbols (US)
    [
        { main: '\u0060', shift: '~', sym: 0x60, shiftSym: 0x7e, code: 'Backquote' },
        { main: '1', shift: '!', sym: 0x31, shiftSym: 0x21, code: 'Digit1' },
        { main: '2', shift: '@', sym: 0x32, shiftSym: 0x40, code: 'Digit2' },
        { main: '3', shift: '#', sym: 0x33, shiftSym: 0x23, code: 'Digit3' },
        { main: '4', shift: '$', sym: 0x34, shiftSym: 0x24, code: 'Digit4' },
        { main: '5', shift: '%%', sym: 0x35, shiftSym: 0x25, code: 'Digit5' },
        { main: '6', shift: '^', sym: 0x36, shiftSym: 0x5e, code: 'Digit6' },
        { main: '7', shift: '&', sym: 0x37, shiftSym: 0x26, code: 'Digit7' },
        { main: '8', shift: '*', sym: 0x38, shiftSym: 0x2a, code: 'Digit8' },
        { main: '9', shift: '(', sym: 0x39, shiftSym: 0x28, code: 'Digit9' },
        { main: '0', shift: ')', sym: 0x30, shiftSym: 0x29, code: 'Digit0' },
        { main: '-', shift: '_', sym: 0x2d, shiftSym: 0x5f, code: 'Minus' },
        { main: '=', shift: '+', sym: 0x3d, shiftSym: 0x2b, code: 'Equal' },
        { main: '⌫ Backspace', sym: 0xff08, code: 'Backspace', cls: 'mod w-18' }
    ],
    // Row 2: QWERTY (US)
    [
        { main: 'Tab', sym: 0xff09, code: 'Tab', cls: 'mod w-14' },
        { main: 'q', shift: 'Q', sym: 0x71, shiftSym: 0x51, code: 'KeyQ' },
        { main: 'w', shift: 'W', sym: 0x77, shiftSym: 0x57, code: 'KeyW' },
        { main: 'e', shift: 'E', sym: 0x65, shiftSym: 0x45, code: 'KeyE' },
        { main: 'r', shift: 'R', sym: 0x72, shiftSym: 0x52, code: 'KeyR' },
        { main: 't', shift: 'T', sym: 0x74, shiftSym: 0x54, code: 'KeyT' },
        { main: 'y', shift: 'Y', sym: 0x79, shiftSym: 0x59, code: 'KeyY' },
        { main: 'u', shift: 'U', sym: 0x75, shiftSym: 0x55, code: 'KeyU' },
        { main: 'i', shift: 'I', sym: 0x69, shiftSym: 0x49, code: 'KeyI' },
        { main: 'o', shift: 'O', sym: 0x6f, shiftSym: 0x4f, code: 'KeyO' },
        { main: 'p', shift: 'P', sym: 0x70, shiftSym: 0x50, code: 'KeyP' },
        { main: '[', shift: '{', sym: 0x5b, shiftSym: 0x7b, code: 'BracketLeft' },
        { main: ']', shift: '}', sym: 0x5d, shiftSym: 0x7d, code: 'BracketRight' },
        { main: '\\', shift: '|', sym: 0x5c, shiftSym: 0x7c, code: 'Backslash', cls: 'w-12' }
    ],
    // Row 3: Home row (US)
    [
        { main: 'Caps Lock', sym: 0xffe5, code: 'CapsLock', mod: 'caps', cls: 'mod w-16' },
        { main: 'a', shift: 'A', sym: 0x61, shiftSym: 0x41, code: 'KeyA' },
        { main: 's', shift: 'S', sym: 0x73, shiftSym: 0x53, code: 'KeyS' },
        { main: 'd', shift: 'D', sym: 0x64, shiftSym: 0x44, code: 'KeyD' },
        { main: 'f', shift: 'F', sym: 0x66, shiftSym: 0x46, code: 'KeyF' },
        { main: 'g', shift: 'G', sym: 0x67, shiftSym: 0x47, code: 'KeyG' },
        { main: 'h', shift: 'H', sym: 0x68, shiftSym: 0x48, code: 'KeyH' },
        { main: 'j', shift: 'J', sym: 0x6a, shiftSym: 0x4a, code: 'KeyJ' },
        { main: 'k', shift: 'K', sym: 0x6b, shiftSym: 0x4b, code: 'KeyK' },
        { main: 'l', shift: 'L', sym: 0x6c, shiftSym: 0x4c, code: 'KeyL' },
        { main: ';', shift: ':', sym: 0x3b, shiftSym: 0x3a, code: 'Semicolon' },
        { main: '\'', shift: '"', sym: 0x27, shiftSym: 0x22, code: 'Quote' },
        { main: '⏎ Enter', sym: 0xff0d, code: 'Enter', cls: 'mod w-20' }
    ],
    // Row 4: Shift row (US)
    [
        { main: '⇧ Shift', sym: 0xffe1, code: 'ShiftLeft', mod: 'shift', cls: 'mod w-22' },
        { main: 'z', shift: 'Z', sym: 0x7a, shiftSym: 0x5a, code: 'KeyZ' },
        { main: 'x', shift: 'X', sym: 0x78, shiftSym: 0x58, code: 'KeyX' },
        { main: 'c', shift: 'C', sym: 0x63, shiftSym: 0x43, code: 'KeyC' },
        { main: 'v', shift: 'V', sym: 0x76, shiftSym: 0x56, code: 'KeyV' },
        { main: 'b', shift: 'B', sym: 0x62, shiftSym: 0x42, code: 'KeyB' },
        { main: 'n', shift: 'N', sym: 0x6e, shiftSym: 0x4e, code: 'KeyN' },
        { main: 'm', shift: 'M', sym: 0x6d, shiftSym: 0x4d, code: 'KeyM' },
        { main: ',', shift: '<', sym: 0x2c, shiftSym: 0x3c, code: 'Comma' },
        { main: '.', shift: '>', sym: 0x2e, shiftSym: 0x3e, code: 'Period' },
        { main: '/', shift: '?', sym: 0x2f, shiftSym: 0x3f, code: 'Slash' },
        { main: '⇧ Shift', sym: 0xffe2, code: 'ShiftRight', mod: 'shift', cls: 'mod w-16' },
        { main: '▲', sym: 0xff52, code: 'ArrowUp', cls: 'fn w-12' }
    ],
    // Row 5: Bottom row (US)
    [
        { main: 'Ctrl', sym: 0xffe3, code: 'ControlLeft', mod: 'ctrl', cls: 'mod w-14' },
        { main: '⊞ Win', sym: 0xffeb, code: 'MetaLeft', mod: 'super', cls: 'mod w-12' },
        { main: 'Alt', sym: 0xffe9, code: 'AltLeft', mod: 'alt', cls: 'mod w-12' },
        { main: 'Space', sym: 0x20, code: 'Space', cls: 'w-space' },
        { main: 'AltGr', sym: 0xffea, code: 'AltRight', mod: 'altgr', cls: 'mod w-12' },
        { main: '◄', sym: 0xff51, code: 'ArrowLeft', cls: 'fn w-12' },
        { main: '▼', sym: 0xff54, code: 'ArrowDown', cls: 'fn w-12' },
        { main: '►', sym: 0xff53, code: 'ArrowRight', cls: 'fn w-12' }
    ]
];

function renderVKeyboard() {
    vkBoard.innerHTML = '';
    var isShift = vkModifiers.shift;
    var isCaps = vkModifiers.caps;
    var isAltGr = vkModifiers.altgr;

    var vkRows = currentVkLayout === 'es' ? vkRowsES : vkRowsUS;

    for (var r = 0; r < vkRows.length; r++) {
        var rowEl = document.createElement('div');
        rowEl.className = 'vk-row';
        var rowKeys = vkRows[r];

        for (var k = 0; k < rowKeys.length; k++) {
            var item = rowKeys[k];
            var btn = document.createElement('button');
            var cls = 'vk-key ' + (item.cls || '');

            var isModActive = item.mod && vkModifiers[item.mod];
            if (isModActive) cls += ' active';

            var displayMain = item.main;
            var displaySub = item.shift || '';

            if (isAltGr && item.altgr) {
                displayMain = item.altgr;
                displaySub = item.main;
            } else if (item.shift) {
                if (isShift) {
                    displayMain = item.shift;
                    displaySub = item.main;
                }
            } else if (item.code && item.code.startsWith('Key')) {
                if (isShift !== isCaps) {
                    displayMain = item.main.toUpperCase();
                } else {
                    displayMain = item.main.toLowerCase();
                }
            }

            btn.className = cls;
            if (displaySub) {
                btn.innerHTML = '<span class="vk-sub">' + displaySub + '</span><span class="vk-main">' + displayMain + '</span>';
            } else {
                btn.innerHTML = '<span class="vk-main">' + displayMain + '</span>';
            }

            (function(keyDef) {
                btn.onclick = function(e) {
                    e.preventDefault();
                    e.stopPropagation();
                    handleVkKey(keyDef);
                };
            })(item);

            rowEl.appendChild(btn);
        }
        vkBoard.appendChild(rowEl);
    }
}

function handleVkKey(keyDef) {
    if (!connected) return;

    if (keyDef.mod) {
        vkModifiers[keyDef.mod] = !vkModifiers[keyDef.mod];
        renderVKeyboard();
        return;
    }

    var isShift = vkModifiers.shift || (vkModifiers.caps && keyDef.code && keyDef.code.startsWith('Key'));
    var isAltGr = vkModifiers.altgr;
    var sym = isAltGr && keyDef.altgrSym ? keyDef.altgrSym : (isShift && keyDef.shiftSym ? keyDef.shiftSym : keyDef.sym);
    var code = keyDef.code;

    if (consoleMode === 'spice') {
        if (!spiceConn) return;
        if (vkModifiers.ctrl) sendSpiceKey(spiceConn, 'ControlLeft', true);
        if (vkModifiers.alt) sendSpiceKey(spiceConn, 'AltLeft', true);
        if (vkModifiers.super) sendSpiceKey(spiceConn, 'MetaLeft', true);
        if (vkModifiers.altgr) sendSpiceKey(spiceConn, 'AltRight', true);
        if (vkModifiers.shift) sendSpiceKey(spiceConn, 'ShiftLeft', true);

        sendSpiceKey(spiceConn, code, true);
        setTimeout(function () {
            sendSpiceKey(spiceConn, code, false);
            if (vkModifiers.shift) { sendSpiceKey(spiceConn, 'ShiftLeft', false); vkModifiers.shift = false; }
            if (vkModifiers.ctrl) { sendSpiceKey(spiceConn, 'ControlLeft', false); vkModifiers.ctrl = false; }
            if (vkModifiers.alt) { sendSpiceKey(spiceConn, 'AltLeft', false); vkModifiers.alt = false; }
            if (vkModifiers.super) { sendSpiceKey(spiceConn, 'MetaLeft', false); vkModifiers.super = false; }
            if (vkModifiers.altgr) { sendSpiceKey(spiceConn, 'AltRight', false); vkModifiers.altgr = false; }
            renderVKeyboard();
        }, 50);
        return;
    }

    if (!rfb) return;

    // Send modifiers down
    if (vkModifiers.ctrl) rfb.sendKey(0xffe3, 'ControlLeft', true);
    if (vkModifiers.alt) rfb.sendKey(0xffe9, 'AltLeft', true);
    if (vkModifiers.super) rfb.sendKey(0xffeb, 'MetaLeft', true);
    if (vkModifiers.altgr) rfb.sendKey(0xffea, 'AltRight', true);
    if (vkModifiers.shift) rfb.sendKey(0xffe1, 'ShiftLeft', true);

    // Send key
    rfb.sendKey(sym, code, true);
    setTimeout(function () {
        rfb.sendKey(sym, code, false);
        if (vkModifiers.shift) { rfb.sendKey(0xffe1, 'ShiftLeft', false); vkModifiers.shift = false; }
        if (vkModifiers.ctrl) { rfb.sendKey(0xffe3, 'ControlLeft', false); vkModifiers.ctrl = false; }
        if (vkModifiers.alt) { rfb.sendKey(0xffe9, 'AltLeft', false); vkModifiers.alt = false; }
        if (vkModifiers.super) { rfb.sendKey(0xffeb, 'MetaLeft', false); vkModifiers.super = false; }
        if (vkModifiers.altgr) { rfb.sendKey(0xffea, 'AltRight', false); vkModifiers.altgr = false; }
        renderVKeyboard();
    }, 50);
}

function handleVkMacro(macro) {
    if (consoleMode === 'spice' && macro === 'cad') {
        if (spiceConn) sendCtrlAltDel(spiceConn);
        return;
    }
    if (!connected) return;
    switch (macro) {
        case 'cad':
            sendKeyCombo([[0xffe3, 'ControlLeft'], [0xffe9, 'AltLeft'], [0xffff, 'Delete']]);
            break;
        case 'alttab':
            sendKeyCombo([[0xffe9, 'AltLeft'], [0xff09, 'Tab']]);
            break;
        case 'altf4':
            sendKeyCombo([[0xffe9, 'AltLeft'], [0xffc1, 'F4']]);
            break;
        case 'ctrlc':
            sendKeyCombo([[0xffe3, 'ControlLeft'], [0x63, 'KeyC']]);
            break;
        case 'ctrlv':
            sendKeyCombo([[0xffe3, 'ControlLeft'], [0x76, 'KeyV']]);
            break;
        case 'ctrlz':
            sendKeyCombo([[0xffe3, 'ControlLeft'], [0x7a, 'KeyZ']]);
            break;
        case 'ctrla':
            sendKeyCombo([[0xffe3, 'ControlLeft'], [0x61, 'KeyA']]);
            break;
        case 'taskmgr':
            sendKeyCombo([[0xffe3, 'ControlLeft'], [0xffe1, 'ShiftLeft'], [0xff1b, 'Escape']]);
            break;
        case 'home':
            sendKeyCombo([[0xff50, 'Home']]);
            break;
        case 'end':
            sendKeyCombo([[0xff57, 'End']]);
            break;
        case 'pgup':
            sendKeyCombo([[0xff55, 'PageUp']]);
            break;
        case 'pgdn':
            sendKeyCombo([[0xff56, 'PageDown']]);
            break;
    }
}

document.querySelectorAll('.vk-chip').forEach(function (btn) {
    btn.onclick = function (e) {
        e.preventDefault();
        e.stopPropagation();
        var macro = btn.getAttribute('data-macro');
        handleVkMacro(macro);
    };
});

// MultiView (multi-console) embeds this page in an iframe and drives key
// macros (e.g. Ctrl+Alt+Del) via postMessage. Only accept messages from
// our own origin; unknown macro names are ignored by handleVkMacro.
window.addEventListener('message', function (e) {
    if (e.origin !== window.location.origin) return;
    var d = e.data;
    if (!d || d.action !== 'send_key_macro' || typeof d.macro !== 'string') return;
    handleVkMacro(d.macro);
});

function toggleVirtualKeyboard() {
    vkOpen = !vkOpen;
    vkeyboard.classList.toggle('open', vkOpen);
    document.getElementById('btnToggleVKeyboard').classList.toggle('active', vkOpen);
    if (vkOpen) {
        renderVKeyboard();
    }
}

document.getElementById('btnToggleVKeyboard').onclick = toggleVirtualKeyboard;
document.getElementById('vkCloseBtn').onclick = function () {
    if (vkOpen) toggleVirtualKeyboard();
};

var vkOpacities = [
    { label: '50%%', cls: '' },
    { label: '25%%', cls: 'opacity-low' },
    { label: '85%%', cls: 'opacity-high' }
];
var vkOpacityIdx = 0;

var vkOpacityBtn = document.getElementById('vkOpacityBtn');
if (vkOpacityBtn) {
    vkOpacityBtn.onclick = function (e) {
        e.preventDefault();
        e.stopPropagation();
        var prevCls = vkOpacities[vkOpacityIdx].cls;
        if (prevCls) vkeyboard.classList.remove(prevCls);
        vkOpacityIdx = (vkOpacityIdx + 1) %% vkOpacities.length;
        var nextCls = vkOpacities[vkOpacityIdx].cls;
        if (nextCls) vkeyboard.classList.add(nextCls);
        document.getElementById('vkOpacityLabel').textContent = vkOpacities[vkOpacityIdx].label;
    };
}

var vkLayoutBtn = document.getElementById('vkLayoutBtn');
var vkLayoutLabel = document.getElementById('vkLayoutLabel');
if (vkLayoutBtn) {
    vkLayoutBtn.onclick = function (e) {
        e.preventDefault();
        e.stopPropagation();
        currentVkLayout = currentVkLayout === 'es' ? 'us' : 'es';
        localStorage.setItem('webkvm_vk_layout', currentVkLayout);
        if (vkLayoutLabel) vkLayoutLabel.textContent = currentVkLayout.toUpperCase();
        renderVKeyboard();
    };
    if (vkLayoutLabel) vkLayoutLabel.textContent = currentVkLayout.toUpperCase();
}

renderVKeyboard();

/* ---- keys panel ---- */
document.getElementById('btnToggleKeys').onclick = function () {
    if (activePanel === 'keys') { closePanel(); return; }
    var keysHtml =
      '<div class="kbtn" data-combo=\'[[65507,"ControlLeft"],[65513,"AltLeft"],[65535,"Delete"]]\'><div class="kicon"><svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="M6 8h.01"/><path d="M10 8h.01"/><path d="M14 8h.01"/><path d="M18 8h.01"/><path d="M8 12h8"/><path d="M10 16h4"/></svg></div><span class="klabel">Ctrl+Alt+Del</span><span class="kkeys">Ctrl+Alt+Del</span></div>' +
      '<div class="kbtn" data-combo=\'[[65507,"ControlLeft"],[65307,"Escape"]]\'><div class="kicon"><svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="M6 8h.01"/><path d="M10 8h.01"/><path d="M14 8h.01"/><path d="M18 8h.01"/><path d="M12 12v4"/></svg></div><span class="klabel">Ctrl+Esc</span><span class="kkeys">Ctrl+Esc</span></div>' +
      '<div class="kbtn" data-combo=\'[[65513,"AltLeft"],[65289,"Tab"]]\'><div class="kicon"><svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="m8 12 4-4 4 4"/><path d="M12 8v8"/></svg></div><span class="klabel">Alt+Tab</span><span class="kkeys">Alt+Tab</span></div>' +
      '<div class="kbtn" data-combo=\'[[65513,"AltLeft"],[65289,"Tab"]]\'><div class="kicon"><svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><path d="M17 20H7a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h10a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2z"/><path d="M9 8h6"/></svg></div><span class="klabel">Alt+Shift+Tab</span><span class="kkeys">Alt+Shift+Tab</span></div>' +
      '<div class="kbtn" data-combo=\'[[65515,"MetaLeft"]]\'><div class="kicon"><svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><rect x="2" y="2" width="8" height="8" rx="1"/><rect x="14" y="2" width="8" height="8" rx="1"/><rect x="2" y="14" width="8" height="8" rx="1"/><rect x="14" y="14" width="8" height="8" rx="1"/></svg></div><span class="klabel">Super (Windows key)</span><span class="kkeys">Super</span></div>' +
      '<div class="kbtn" data-combo=\'[[65513,"AltLeft"],[65473,"F4"]]\'><div class="kicon"><svg fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24"><rect x="2" y="4" width="20" height="16" rx="2"/><path d="m9 12 6-6"/><path d="m15 12-6-6"/></svg></div><span class="klabel">Alt+F4</span><span class="kkeys">Alt+F4</span></div>';
    openPanel('keys', 'Keyboard', keysHtml);
    panelBody.querySelectorAll('.kbtn').forEach(function (btn) {
        btn.onclick = function () {
            var combo = JSON.parse(btn.getAttribute('data-combo'));
            sendKeyCombo(combo);
        };
    });
};

/* ---- direct clipboard ---- */
var lastClipText = '';

function syncClipToHost(text) {
    if (!text || text === lastClipText) return;
    lastClipText = text;
    navigator.clipboard.writeText(text).catch(function () {});
}

document.addEventListener('keydown', function (e) {
    if (!rfb || !connected) return;
    if (!(e.key === 'v' || e.key === 'V') || !(e.ctrlKey || e.metaKey)) return;
    var tag = document.activeElement && document.activeElement.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
    e.preventDefault();
    e.stopPropagation();
    var ctrlCode = e.ctrlKey && e.location === 1 ? 'ControlRight' : 'ControlLeft';
    var ctrlSym = ctrlCode === 'ControlRight' ? 0xffe4 : 0xffe3;
    navigator.clipboard.readText().then(function (txt) {
        if (txt && txt !== lastClipText) {
            lastClipText = txt;
            var x = new XMLHttpRequest();
            x.open('POST', '/api/vms/' + vmId + '/clipboard?vt=' + encodeURIComponent(vt), true);
            x.setRequestHeader('Content-Type', 'application/json');
            x.onloadend = function () { doPaste(ctrlSym, ctrlCode); };
            x.send(JSON.stringify({text: txt}));
        } else {
            doPaste(ctrlSym, ctrlCode);
        }
    }).catch(function () {
        doPaste(ctrlSym, ctrlCode);
    });
    function doPaste(kSym, kCode) {
        if (consoleMode === 'spice') {
            if (!spiceConn || !connected) return;
            sendSpiceKey(spiceConn, 'ControlLeft', true);
            sendSpiceKey(spiceConn, 'KeyV', true);
            setTimeout(function () {
                if (!spiceConn || !connected) return;
                sendSpiceKey(spiceConn, 'KeyV', false);
                sendSpiceKey(spiceConn, 'ControlLeft', false);
            }, 50);
            return;
        }
        if (!rfb || !connected) return;
        rfb.sendKey(kSym, kCode, true);
        rfb.sendKey(0x56, 'KeyV', true);
        setTimeout(function () {
            if (!rfb || !connected) return;
            rfb.sendKey(0x56, 'KeyV', false);
            rfb.sendKey(kSym, kCode, false);
        }, 50);
    }
}, true);

var spAreaEl = document.getElementById('spice-area');
if (spAreaEl) {
    spAreaEl.addEventListener('mousedown', function () {
        var c = document.querySelector('#spice-screen canvas');
        if (c) c.focus();
    });
}

window.addEventListener('keydown', function (e) {
    if (consoleMode === 'spice' && spiceConn && connected) {
        var tag = document.activeElement && document.activeElement.tagName;
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
        if (e.target && e.target.tagName === 'CANVAS') return;
        var canvas = document.querySelector('#spice-screen canvas');
        if (canvas) {
            handle_keydown.call(canvas, e);
        }
    }
}, true);

window.addEventListener('keyup', function (e) {
    if (consoleMode === 'spice' && spiceConn && connected) {
        var tag = document.activeElement && document.activeElement.tagName;
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return;
        if (e.target && e.target.tagName === 'CANVAS') return;
        var canvas = document.querySelector('#spice-screen canvas');
        if (canvas) {
            handle_keyup.call(canvas, e);
        }
    }
}, true);

window.addEventListener('resize', function () {
    if (consoleMode === 'spice' && spiceConn) {
        fit_spice_canvas(spiceConn);
        handle_resize();
    }
});

document.addEventListener('fullscreenchange', function () {
    if (consoleMode === 'spice' && spiceConn) {
        setTimeout(function () {
            fit_spice_canvas(spiceConn);
            handle_resize();
        }, 60);
    }
});

/* ---- settings panel ---- */
document.getElementById('btnToggleSettings').onclick = function () {
    if (activePanel === 'settings') { closePanel(); return; }
    var q = rfb ? rfb.qualityLevel : 6, c = rfb ? rfb.compressionLevel : 2;
    var settingsHtml =
      '<div class="srow"><div><div class="slabel">Scale viewport</div><div class="sdesc">Fit remote to screen</div></div><div class="stoggle on" id="togScale"><div class="knob"></div></div></div>' +
      '<div class="srow"><div><div class="slabel">Clip viewport</div><div class="sdesc">Show scrollbars when scaled</div></div><div class="stoggle on" id="togClip"><div class="knob"></div></div></div>' +
      '<div class="srow"><div><div class="slabel">Resize session</div><div class="sdesc">Remote resizes with window</div></div><div class="stoggle" id="togResize"><div class="knob"></div></div></div>' +
      '<div class="srow"><div><div class="slabel">Dot cursor</div><div class="sdesc">Fallback cursor if the guest draws none</div></div><div class="stoggle" id="togDotCursor"><div class="knob"></div></div></div>' +
      '<div class="srow vertical"><div class="slabel">Image quality <span id="qualityVal">' + q + '</span>/9</div><input type="range" class="srange" id="rngQuality" min="0" max="9" step="1" value="' + q + '"></div>' +
      '<div class="srow vertical"><div class="slabel">Compression <span id="compressionVal">' + c + '</span>/9</div><input type="range" class="srange" id="rngCompression" min="0" max="9" step="1" value="' + c + '"></div>';
    openPanel('settings', 'Settings', settingsHtml);
    setupToggle('togScale', function (on) { if (rfb) rfb.scaleViewport = on; });
    setupToggle('togClip', function (on) { if (rfb) rfb.clipViewport = on; });
    setupToggle('togResize', function (on) { if (rfb) rfb.resizeSession = on; });
    setupToggle('togDotCursor', function (on) { if (rfb) rfb.showDotCursor = on; });
    var rq = document.getElementById('rngQuality');
    rq.oninput = function () {
        document.getElementById('qualityVal').textContent = rq.value;
        if (rfb) rfb.qualityLevel = parseInt(rq.value, 10);
    };
    var rc = document.getElementById('rngCompression');
    rc.oninput = function () {
        document.getElementById('compressionVal').textContent = rc.value;
        if (rfb) rfb.compressionLevel = parseInt(rc.value, 10);
    };
};

function setupToggle(id, callback) {
    var el = document.getElementById(id);
    if (!el) return;
    if (typeof rfb !== 'undefined' && rfb) {
        if (id === 'togScale') el.classList.toggle('on', rfb.scaleViewport);
        if (id === 'togClip') el.classList.toggle('on', rfb.clipViewport);
        if (id === 'togResize') el.classList.toggle('on', rfb.resizeSession);
        if (id === 'togDotCursor') el.classList.toggle('on', rfb.showDotCursor);
    }
    el.onclick = function () {
        el.classList.toggle('on');
        callback(el.classList.contains('on'));
    };
}

/* ---- info panel ---- */
document.getElementById('btnToggleInfo').onclick = function () {
    if (activePanel === 'info') { closePanel(); return; }
    // Deliberately no server host:port row here: if this backend is ever
    // reachable from the internet, exposing the server address in the UI
    // is a data leak. The WebSocket URL is computed from the page origin
    // anyway, so the operator never needs to read the address back.
    var infoHtml =
      '<div class="irow"><span class="ilabel">Status</span><span class="ivalue" id="infoStatus">Disconnected</span></div>' +
      '<div class="irow"><span class="ilabel">VM</span><span class="ivalue">' + '%s' + '</span></div>' +
      '<div class="irow"><span class="ilabel">Protocol</span><span class="ivalue">' + (consoleMode === 'spice' ? 'SPICE (WebSocket)' : 'RFB 003.008 (noVNC)') + '</span></div>' +
      '<div class="irow"><span class="ilabel">Screen</span><span class="ivalue" id="infoRes">Waiting...</span></div>';
    openPanel('info', 'Connection Info', infoHtml);
    if (connected) {
        document.getElementById('infoStatus').textContent = 'Connected';
        if (consoleMode === 'spice') {
            var c = document.querySelector('#spice-screen canvas');
            if (c && c.width && c.height)
                document.getElementById('infoRes').textContent = c.width + 'x' + c.height;
        } else if (rfb && rfb._fb_width && rfb._fb_height) {
            document.getElementById('infoRes').textContent = rfb._fb_width + 'x' + rfb._fb_height;
        }
    }
};

/* ---- close panel ---- */
document.getElementById('panelClose').onclick = closePanel;

/* ---- connect ---- */
function connect() {
    if (consoleMode === 'spice') {
        document.getElementById('screen').style.display = 'none';
        var spArea = document.getElementById('spice-area');
        spArea.style.display = 'block';
        if (spiceConn) { try { spiceConn.stop(); } catch(e) {} spiceConn = null; }
        updateStatus('connecting', 'Connecting SPICE');
        try {
            spiceConn = new SpiceMainConn({
                uri: url,
                screen_id: 'spice-screen',
                dump_id: 'debug-div',
                message_id: 'message-div',
                password: '',
                onagent: function() {
                    handle_resize();
                },
                onerror: function(err) {
                    connected = false;
                    updateStatus('error', 'Disconnected');
                    document.getElementById('btnReconnect').style.display = '';
                    if (activePanel === 'info') document.getElementById('infoStatus').textContent = 'Disconnected';
                    if (autoRetry) {
                        reconnectAttempts++;
                        var wait = isEmbedded ? 2500 : Math.min(2000 * Math.pow(2, reconnectAttempts - 1), 10000);
                        updateStatus('connecting', 'Reconnecting in ' + Math.round(wait / 1000) + 's');
                        setTimeout(function () { if (autoRetry) connect(); }, wait);
                    }
                },
                onsuccess: function() {
                    connected = true;
                    everConnected = true;
                    reconnectAttempts = 0;
                    updateStatus('ok', 'Connected');
                    document.getElementById('btnReconnect').style.display = 'none';
                    if (activePanel === 'info') document.getElementById('infoStatus').textContent = 'Connected';
                    setTimeout(function() {
                        fit_spice_canvas(spiceConn);
                        handle_resize();
                    }, 100);
                }
            });
        } catch(e) {
            console.error(e);
            updateStatus('error', 'Error: ' + e.message);
        }
        return;
    }

    if (rfb) { try { rfb.disconnect(); } catch(e) {} rfb = null; }
    updateStatus('connecting', 'Connecting');
    rfb = new RFB(document.getElementById('screen'), url, {
        wsProtocols: ['binary'], repeaterID: '', shared: true, credentials: { password: '' }
    });
    rfb.addEventListener('connect', function () {
        connected = true;
        everConnected = true;
        reconnectAttempts = 0;
        updateStatus('ok', 'Connected');
        document.getElementById('btnReconnect').style.display = 'none';
        if (activePanel === 'info') {
            document.getElementById('infoStatus').textContent = 'Connected';
            if (rfb._fb_width && rfb._fb_height)
                document.getElementById('infoRes').textContent = rfb._fb_width + 'x' + rfb._fb_height;
        }
    });
    rfb.addEventListener('disconnect', function (e) {
        connected = false;
        updateStatus('error', 'Disconnected');
        document.getElementById('btnReconnect').style.display = '';
        if (activePanel === 'info') document.getElementById('infoStatus').textContent = 'Disconnected';
        if (autoRetry) {
            reconnectAttempts++;
            // In embedded/multiview mode, keep retrying smoothly so booting VMs connect automatically
            if (!isEmbedded && !everConnected && reconnectAttempts >= 5) return;
            var wait = isEmbedded ? 2500 : Math.min(2000 * Math.pow(2, reconnectAttempts - 1), 10000);
            updateStatus('connecting', 'Reconnecting in ' + Math.round(wait / 1000) + 's');
            setTimeout(function () { if (autoRetry) connect(); }, wait);
        }
    });
    rfb.addEventListener('clipboard', function (e) { syncClipToHost(e.detail.text); });
    rfb.scaleViewport = true;
    rfb.resizeSession = false;
    rfb.clipViewport = false;
    rfb.showDotCursor = false;
}

document.getElementById('btnReconnect').onclick = function () {
    this.style.display = 'none';
    reconnectAttempts = 0;
    autoRetry = true;
    connect();
};

connect();

/* ---- sidebar auto-hide ---- */
var sidebarTimer;
document.addEventListener('mousemove', function (e) {
    if (e.clientX < 80 || activePanel) {
        sidebar.classList.add('visible');
        clearTimeout(sidebarTimer);
    } else {
        sidebar.classList.add('visible');
        clearTimeout(sidebarTimer);
        sidebarTimer = setTimeout(function () {
            if (!activePanel) sidebar.classList.remove('visible');
        }, 2000);
    }
});
sidebar.addEventListener('mouseenter', function () { clearTimeout(sidebarTimer); });
sidebar.addEventListener('mouseleave', function () {
    if (!activePanel) {
        sidebarTimer = setTimeout(function () { sidebar.classList.remove('visible'); }, 1000);
    }
});
// Touch devices have no hover/mousemove — a tap anywhere shows the
// sidebar for a few seconds instead of it staying hidden forever.
document.addEventListener('touchstart', function () {
    sidebar.classList.add('visible');
    clearTimeout(sidebarTimer);
    sidebarTimer = setTimeout(function () {
        if (!activePanel) sidebar.classList.remove('visible');
    }, 3000);
}, { passive: true });

function toggleFullscreen() {
    if (!document.fullscreenElement) document.documentElement.requestFullscreen();
    else document.exitFullscreen();
}
document.getElementById('btnFullscreen').onclick = toggleFullscreen;
document.addEventListener('keydown', function (e) {
    if (e.key === 'F11') { e.preventDefault(); toggleFullscreen(); }
});
</script>
</body>
</html>`, id, id, id, id)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Console hardening: don't leak the server address (Referrer-Policy),
	// prevent embedding in other sites, and lock down the subresources.
	// The page uses inline <style>/<script> for self-containment, hence
	// the 'unsafe-inline' entries; connect-src still restricts where the
	// WebSocket may go.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self' ws: wss:; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'self'")
	fmt.Fprint(w, html)
}

func (h *Handler) DownloadRDP(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	vm, err := h.compute.GetDomain(id)
	if err != nil {
		jsonErr(w, http.StatusNotFound, err.Error())
		return
	}
	ip := h.compute.GetDomainIP(id)
	if ip == "" {
		ip = h.serverIP()
	}

	rdpContent := fmt.Sprintf(`full address:s:%s:3389
prompt for credentials:i:1
administrative session:i:1
`, ip)

	w.Header().Set("Content-Type", "application/x-rdp")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.rdp\"", vm.Name))
	w.Write([]byte(rdpContent))
}

func (h *Handler) DownloadSPICE(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	info, err := h.compute.GetSPICEInfo(id)
	if err != nil {
		info, err = h.compute.GetVNCInfo(id)
		if err != nil {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
	}
	spiceContent := fmt.Sprintf(`[virt-viewer]
type=%s
host=%s
port=%d
delete-this-file=1
full-screen=0
`, info.Type, h.serverIP(), info.Port)

	w.Header().Set("Content-Type", "application/x-virt-viewer")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s.vv\"", id))
	w.Write([]byte(spiceContent))
}

func (h *Handler) SetClipboard(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Text string `json:"text"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.compute.GuestSetClipboard(id, req.Text); err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}
