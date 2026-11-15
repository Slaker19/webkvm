package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"webkvm/internal/compute"
	"webkvm/internal/libvirt"
	"webkvm/internal/nodes"
)

// Remote console: VNC/SPICE of a VM that lives on another node is reached by
// a direct-tcpip forward over SSH (managed cluster key, pinned host key) to
// 127.0.0.1:<port> ON that node. The port is read from the remote domain's
// live XML through the node's libvirt connector, never from this host.

const remoteConsoleDialTimeout = 10 * time.Second

// tcpDialer is what the remote console needs from nodes.SSHDialer.
type tcpDialer interface {
	DialTCP(ctx context.Context, addr string) (net.Conn, error)
}

// remoteGraphicsInfo reads the live graphics endpoint of a VM on the node h
// is bound to. Tests replace it.
var remoteGraphicsInfo = func(h *Handler, id, typ string) (compute.GraphicsInfo, error) {
	if h.lv == nil {
		return compute.GraphicsInfo{}, libvirt.ErrNodeUnreachable
	}
	gi, err := h.lv.RemoteGraphicsInfo(id, typ)
	return compute.GraphicsInfo{Type: gi.Type, Port: gi.Port, WebSocket: gi.WebSocket, Host: gi.Host}, err
}

// remoteDialerFor returns the (cached) SSH dialer of a node. Tests replace it.
var remoteDialerFor = func(h *Handler, n nodes.Node) (tcpDialer, error) {
	store := h.sshKeys
	if store == nil && h.cfg != nil {
		store = nodes.SharedSSHKeyStore(h.cfg.DataDir)
	}
	return cachedNodeDialer(store, n)
}

type dialerEntry struct {
	uri string
	d   *nodes.SSHDialer
}

var (
	nodeDialersMu sync.Mutex
	nodeDialers   = map[string]*dialerEntry{}
)

func cachedNodeDialer(store *nodes.SSHKeyStore, n nodes.Node) (tcpDialer, error) {
	nodeDialersMu.Lock()
	defer nodeDialersMu.Unlock()
	if e, ok := nodeDialers[n.ID]; ok {
		if e.uri == n.URI {
			return e.d, nil
		}
		_ = e.d.Close()
		delete(nodeDialers, n.ID)
	}
	d, err := nodes.NewSSHDialer(store, n)
	if err != nil {
		return nil, err
	}
	nodeDialers[n.ID] = &dialerEntry{uri: n.URI, d: d}
	return d, nil
}

// forgetNodeDialer closes and drops the cached SSH dialer of one node (edit,
// delete, enable/disable, host-key pinning, key rotation).
func forgetNodeDialer(id string) {
	nodeDialersMu.Lock()
	defer nodeDialersMu.Unlock()
	if e, ok := nodeDialers[id]; ok {
		_ = e.d.Close()
		delete(nodeDialers, id)
	}
}

// forgetAllNodeDialers does the same for every node (key rotation commit).
func forgetAllNodeDialers() {
	nodeDialersMu.Lock()
	defer nodeDialersMu.Unlock()
	for id, e := range nodeDialers {
		_ = e.d.Close()
		delete(nodeDialers, id)
	}
}

// consoleTarget is where a console WebSocket proxies to.
type consoleTarget struct {
	Info compute.GraphicsInfo
	Addr string
	// Dial opens the TCP connection to the graphics port.
	Dial func(ctx context.Context) (net.Conn, error)
	// Close releases anything opened ahead of Dial. Always safe to call.
	Close func()
}

// consoleTarget resolves the graphics endpoint of VM id (typ "vnc" or
// "spice") for this handler. It enforces the ticket binding and answers the
// HTTP error itself, returning ok=false. Local handlers keep the exact
// historical behaviour (dial happens later, from the proxy); remote ones
// connect BEFORE the WebSocket upgrade so a typed JSON error can still be
// returned.
func (h *Handler) consoleTarget(w http.ResponseWriter, r *http.Request, id, typ string) (consoleTarget, bool) {
	if !h.checkConsoleTicket(w, r, id) {
		return consoleTarget{}, false
	}
	if !h.isRemote() {
		var info compute.GraphicsInfo
		var err error
		if typ == "spice" {
			info, err = h.compute.GetSPICEInfo(id)
		} else {
			info, err = h.compute.GetVNCInfo(id)
		}
		if err != nil {
			jsonErr(w, http.StatusNotFound, err.Error())
			return consoleTarget{}, false
		}
		host := h.cfg.VNCProxyHost
		if host == "" {
			host = "127.0.0.1"
		}
		addr := net.JoinHostPort(host, strconv.Itoa(info.Port))
		return consoleTarget{Info: info, Addr: addr, Close: func() {},
			Dial: func(context.Context) (net.Conn, error) { return net.DialTimeout("tcp", addr, 10*time.Second) }}, true
	}

	node := h.remote.node
	info, err := remoteGraphicsInfo(h, id, typ)
	if err != nil {
		h.writeRemoteConsoleError(w, node, err, true)
		return consoleTarget{}, false
	}
	d, err := remoteDialerFor(h, node)
	if err != nil {
		h.writeRemoteConsoleError(w, node, err, false)
		return consoleTarget{}, false
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(info.Port))
	ctx, cancel := context.WithTimeout(r.Context(), remoteConsoleDialTimeout)
	defer cancel()
	conn, err := d.DialTCP(ctx, addr)
	if err != nil {
		h.writeRemoteConsoleError(w, node, err, false)
		return consoleTarget{}, false
	}
	var once sync.Once
	taken := false
	h.auditRemoteConsole(r, "console."+typ+".open", id)
	return consoleTarget{
		Info: info, Addr: addr + " via ssh " + node.Name,
		Dial: func(context.Context) (net.Conn, error) { taken = true; return conn, nil },
		Close: func() {
			once.Do(func() {
				if !taken {
					_ = conn.Close()
				}
			})
		},
	}, true
}

func (h *Handler) auditRemoteConsole(r *http.Request, action, id string) {
	if h.audit != nil {
		h.audit.Log(auditFor(r, action, id, nil))
	}
}

// writeRemoteConsoleError maps a console failure on a remote node to a typed
// JSON answer. Raw ssh/libvirt text never reaches the client.
func (h *Handler) writeRemoteConsoleError(w http.ResponseWriter, node nodes.Node, err error, graphics bool) {
	body := func(status int, code, msg string) {
		jsonResp(w, status, map[string]string{"error": msg, "code": code, "node": node.Name, "node_id": node.ID})
	}
	switch {
	case errors.Is(err, nodes.ErrTooManyForwards):
		body(http.StatusTooManyRequests, "console_busy", "too many open consoles on this node")
	case errors.Is(err, nodes.ErrForwardDenied):
		body(http.StatusBadGateway, "console_forward_denied", "the node refused the console tunnel (check the authorized_keys options)")
	case errors.Is(err, libvirt.ErrGraphicsNotRunning):
		body(http.StatusConflict, "vm_not_running", "the VM has no graphics port (is it running?)")
	case errors.Is(err, libvirt.ErrGraphicsNotTunnel):
		h.remoteUnsupported(w, "graphics_not_loopback")
	case graphics && errors.Is(err, libvirt.ErrNodeUnreachable):
		writeNodeError(w, node, err)
	case graphics && isNotFoundLike(err):
		jsonErr(w, http.StatusNotFound, err.Error())
	default:
		writeNodeError(w, node, err)
	}
}

func isNotFoundLike(err error) bool {
	var ne *libvirt.NodeError
	if errors.As(err, &ne) {
		return false
	}
	t := err.Error()
	return strings.Contains(t, "not found") || strings.Contains(t, "no domain") ||
		strings.Contains(t, "not configured") || strings.Contains(t, "no graphics")
}

// --- tickets bound to (node, vm) --------------------------------------------------

type consoleBinding struct {
	nodeID, vmID string
	exp          time.Time
}

var (
	consoleBindMu sync.Mutex
	consoleBinds  = map[[32]byte]consoleBinding{}
)

// bindConsoleTicket records which (node, VM) a freshly minted ticket was
// issued for. nodeID is "" for the local host.
func bindConsoleTicket(tk, nodeID, vmID string, ttl time.Duration) {
	now := time.Now()
	consoleBindMu.Lock()
	defer consoleBindMu.Unlock()
	for k, b := range consoleBinds {
		if now.After(b.exp) {
			delete(consoleBinds, k)
		}
	}
	consoleBinds[sha256.Sum256([]byte(tk))] = consoleBinding{nodeID: nodeID, vmID: vmID, exp: now.Add(ttl)}
}

func ticketFromRequest(r *http.Request) string {
	q := r.URL.Query()
	if v := q.Get("vt"); v != "" {
		return v
	}
	return q.Get("ticket")
}

func (h *Handler) consoleNodeID() string {
	if h.remote == nil {
		return ""
	}
	return h.remote.node.ID
}

// checkConsoleTicket refuses (403) a console ticket used on another node or
// VM than the one it was issued for. Requests that authenticate with a
// session token carry no ticket and are unaffected. An unknown ticket is only
// tolerated on the local host (legacy behaviour); a remote node never accepts
// one it did not mint.
func (h *Handler) checkConsoleTicket(w http.ResponseWriter, r *http.Request, vmID string) bool {
	tk := ticketFromRequest(r)
	if tk == "" {
		return true
	}
	consoleBindMu.Lock()
	b, ok := consoleBinds[sha256.Sum256([]byte(tk))]
	consoleBindMu.Unlock()
	node := h.consoleNodeID()
	if ok && time.Now().After(b.exp) {
		ok = false
	}
	if (ok && (b.nodeID != node || b.vmID != vmID)) || (!ok && node != "") {
		jsonErrCode(w, http.StatusForbidden, "ticket_scope", "ticket not valid for this node or VM")
		return false
	}
	return true
}

// --- .rdp / .vv host --------------------------------------------------------------

// remoteNodeHost is the address of the node's own host, for downloadable
// console files. ok=false when the node has no usable address (loopback, this
// very machine, or not an SSH node).
func (h *Handler) remoteNodeHost() (string, bool) {
	if h.remote == nil {
		return "", false
	}
	_, host, _, err := nodes.NodeSSHTarget(h.remote.node.URI)
	if err != nil || host == "" || nodes.IsLocalHost(host) {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsUnspecified() || ip.IsLoopback()) {
		return "", false
	}
	return host, true
}
