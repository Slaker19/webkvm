package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/auth"
	"webkvm/internal/libvirt"
	"webkvm/internal/nodes"
)

// clusterSSHTests remembers the outcome of the last connection test per node
// (in memory only) so the overview can show it.
type clusterSSHTests struct {
	mu sync.Mutex
	m  map[string]sshLastTest
	// store persists the verdicts (DATA_DIR/cluster-ssh/last_test.json) so a
	// restart does not make a working node look "not set up". Nil = memory only.
	store *nodes.SSHKeyStore
	// rot tracks the verification of a pending key rotation.
	rot rotationVerified
}

type sshLastTest = nodes.SSHLastTest

// persistLocked writes the verdicts; a failure only costs the restart cache.
func (t *clusterSSHTests) persistLocked() {
	if t.store != nil {
		_ = t.store.SaveLastTests(t.m)
	}
}

func (t *clusterSSHTests) set(id string, v sshLastTest) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[string]sshLastTest{}
	}
	t.m[id] = v
	t.persistLocked()
}

func (t *clusterSSHTests) get(id string) *sshLastTest {
	t.mu.Lock()
	defer t.mu.Unlock()
	if v, ok := t.m[id]; ok {
		return &v
	}
	return nil
}

func (t *clusterSSHTests) del(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.m[id]; !ok {
		return
	}
	delete(t.m, id)
	t.persistLocked()
}

// clear forgets every verdict (the key changed: none of them is valid).
func (t *clusterSSHTests) clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.m) == 0 {
		return
	}
	t.m = map[string]sshLastTest{}
	t.persistLocked()
}

// rotationVerifyWindow is how long a successful test of the pending key
// counts towards committing the rotation. A variable so tests can shorten it.
var rotationVerifyWindow = 15 * time.Minute

// rotationNow is the clock of the verification window (tests replace it).
var rotationNow = time.Now

// rotationVerified remembers, in memory only, which nodes accepted the
// PENDING key, keyed by the pending fingerprint. It is cleared when the
// rotation starts again, is aborted, committed or rolled back.
type rotationVerified struct {
	mu   sync.Mutex
	fp   string
	ok   map[string]time.Time
	last map[string]string // last failure code per node id
}

func (rv *rotationVerified) reset(fp string) {
	rv.fp, rv.ok, rv.last = fp, map[string]time.Time{}, map[string]string{}
}

func (rv *rotationVerified) record(fp, id string, ok bool, code string) {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if rv.fp != fp || rv.ok == nil {
		rv.reset(fp)
	}
	if ok {
		rv.ok[id] = rotationNow()
		delete(rv.last, id)
		return
	}
	delete(rv.ok, id)
	rv.last[id] = code
}

// passed reports whether node id passed within the window for fingerprint fp.
func (rv *rotationVerified) passed(fp, id string) bool {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if rv.fp != fp {
		return false
	}
	at, ok := rv.ok[id]
	return ok && rotationNow().Sub(at) <= rotationVerifyWindow
}

func (rv *rotationVerified) lastCode(fp, id string) string {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	if rv.fp != fp || rv.last[id] == "" {
		return "not_tested"
	}
	return rv.last[id]
}

func (rv *rotationVerified) clear() {
	rv.mu.Lock()
	defer rv.mu.Unlock()
	rv.fp, rv.ok, rv.last = "", nil, nil
}

// libvirtProbeResult is what the connection test reads from a live node.
type libvirtProbeResult struct {
	Version string
	KVM     bool
	Domains int
}

// libvirtProbe reads version, KVM support and domain count through a pooled
// connector. A variable so tests do not need a libvirt daemon.
var libvirtProbe = func(c *libvirt.Connector) (libvirtProbeResult, error) {
	domains, kvm, err := libvirt.TestRemoteConnectionOn(c)
	if err != nil {
		return libvirtProbeResult{}, err
	}
	return libvirtProbeResult{Version: libvirt.ConnectorVersion(c), KVM: kvm, Domains: domains}, nil
}

// mountClusterSSH registers the managed-SSH endpoints. Admin only.
func (h *Handler) mountClusterSSH(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(modelsRoleAdmin()))
		r.Get("/api/cluster/ssh", h.GetClusterSSH)
		r.Post("/api/cluster/ssh/key", h.GenerateClusterSSHKey)
		r.Post("/api/cluster/ssh/rotate", h.StartClusterSSHRotation)
		r.Post("/api/cluster/ssh/rotate/commit", h.CommitClusterSSHRotation)
		r.Post("/api/cluster/ssh/rotate/abort", h.AbortClusterSSHRotation)
		r.Post("/api/cluster/ssh/rotate/test", h.TestClusterSSHRotation)
		r.Post("/api/cluster/ssh/rotate/rollback", h.RollbackClusterSSHRotation)
		r.Get("/api/nodes/{id}/ssh-authorize", h.GetNodeSSHAuthorize)
		r.Post("/api/nodes/{id}/ssh/hostkey", h.CaptureNodeHostKey)
		r.Post("/api/nodes/{id}/ssh/pin", h.PinNodeHostKey)
		r.Post("/api/nodes/{id}/ssh/test", h.TestNodeSSH)
	})
}

// sshReady answers 503 when the SSH key store is not wired.
func (h *Handler) sshReady(w http.ResponseWriter) bool {
	if h.sshKeys == nil || h.nodes == nil {
		jsonErrCode(w, http.StatusServiceUnavailable, "ssh_unavailable", "cluster SSH storage is not initialized")
		return false
	}
	return true
}

func (h *Handler) clusterTransport() nodes.Transport {
	if h.settings == nil {
		return nodes.DefaultTransport
	}
	return nodes.ParseTransport(h.settings.GetString("cluster.libvirt_transport"))
}

// transportView reports the configured transport, the one actually used and
// whether the local libvirt client forced a fallback (libssh2 -> libssh).
func (h *Handler) transportView() map[string]any {
	configured := h.clusterTransport()
	effective, fellBack := nodes.EffectiveTransport(configured)
	v := map[string]any{"transport": string(effective), "transport_configured": string(configured)}
	if fellBack {
		v["transport_fallback"] = "libssh2_unsupported"
	}
	return v
}

// ClusterSSHConnURI builds the connection URI libvirt uses for a node with
// the managed key and the configured transport, resolved against what the
// local libvirt client supports (nodes.EffectiveTransport). Used by the pool
// in main.
func ClusterSSHConnURI(keys *nodes.SSHKeyStore, transport func() nodes.Transport) func(nodes.Node) (string, error) {
	return func(n nodes.Node) (string, error) {
		t, _ := nodes.EffectiveTransport(transport())
		return nodes.BuildConnURI(n, keys, nodes.WithTransport(t))
	}
}

// sshNode resolves a libvirt remote node for the SSH endpoints and answers
// the error itself (404 unknown or not libvirt, 400 local_node).
func (h *Handler) sshNode(w http.ResponseWriter, r *http.Request) (nodes.Node, string, string, int, bool) {
	n, ok := h.nodeForTarget(chiURLParam(r, "id"))
	if !ok || n.Driver() != nodes.BackendLibvirt {
		jsonErrCode(w, http.StatusNotFound, "not_found", "libvirt node not found")
		return nodes.Node{}, "", "", 0, false
	}
	if n.IsLocal() {
		jsonErrCode(w, http.StatusBadRequest, "local_node", "the local node does not use SSH")
		return nodes.Node{}, "", "", 0, false
	}
	user, host, port, err := nodes.NodeSSHTarget(n.URI)
	if err != nil {
		jsonErrCode(w, http.StatusBadRequest, nodes.CodeInvalidURI, "the node URI is not a plain qemu+ssh URI; edit the node")
		return nodes.Node{}, "", "", 0, false
	}
	return n, user, host, port, true
}

type sshHostKeyJSON struct {
	State       string `json:"state"`
	Fingerprint string `json:"fingerprint"`
}

// GetClusterSSH is the overview: key, pending rotation, transport and the
// SSH status of every libvirt node.
func (h *Handler) GetClusterSSH(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	info, err := h.sshKeys.Info()
	if err != nil {
		jsonErrCode(w, http.StatusInternalServerError, "ssh_store_error", "cannot read the cluster SSH key store")
		return
	}
	list := []map[string]any{}
	for _, n := range h.nodes.List() {
		if n.Driver() != nodes.BackendLibvirt || n.IsLocal() {
			continue
		}
		user, host, port, err := nodes.NodeSSHTarget(n.URI)
		if err != nil {
			continue
		}
		hk := sshHostKeyJSON{State: nodes.HostKeyUnknown}
		if fp, ok := h.sshKeys.PinnedHostKey(host, port); ok {
			hk = sshHostKeyJSON{State: nodes.HostKeyPinned, Fingerprint: fp}
		}
		var authorized any
		last := h.sshTestState().get(n.ID)
		if last != nil {
			switch {
			case last.OK:
				authorized = true
			case last.Code == "ssh_auth_denied":
				authorized = false
			}
		}
		var lastJSON any
		if last != nil {
			lastJSON = last
		}
		row := map[string]any{
			"id": n.ID, "name": n.Name, "user": user, "host": host, "port": port,
			"host_key": hk, "authorized": authorized, "last_test": lastJSON,
		}
		if n.UIURL != "" {
			row["ui_url"] = n.UIURL
		}
		// The same machine registered again (control plane moved here):
		// the UI shows it once as "this server", never as a node to set up.
		if n.IsSelf {
			row["is_self"] = true
		}
		// The periodic node probe already logs in over SSH: its verdict is
		// the single source of "access works" for every screen.
		if n.Access != "" {
			row["access"] = n.Access
			if n.AccessCode != "" {
				row["access_code"] = n.AccessCode
			}
		}
		// The pool knows why the node's connection is failing right now
		// (typed code, never raw text): lets the VM list name the real cause
		// even when no explicit test was ever run.
		if h.libvirtPool != nil {
			if st := h.libvirtPool.Status(n.ID); st.State == libvirt.PoolStateFailing && st.LastError != "" {
				row["pool_error"] = st.LastError
			}
		}
		if info.Pending != nil {
			row["pending_verified"] = h.sshTestState().rot.passed(info.Pending.Fingerprint, n.ID)
			row["enabled"] = n.Enabled
		}
		list = append(list, row)
	}
	resp := map[string]any{
		"configured": info.Configured, "algorithm": info.Algorithm, "fingerprint": info.Fingerprint,
		"public_key": info.PublicKey, "created_at": info.CreatedAt,
		"nodes": list,
	}
	for k, v := range h.transportView() {
		resp[k] = v
	}
	if info.Pending != nil {
		resp["pending"] = map[string]any{"fingerprint": info.Pending.Fingerprint, "public_key": info.Pending.PublicKey}
	}
	if info.Previous != nil {
		resp["previous"] = map[string]any{"fingerprint": info.Previous.Fingerprint}
	}
	jsonResp(w, http.StatusOK, resp)
}

// GenerateClusterSSHKey creates the cluster key.
func (h *Handler) GenerateClusterSSHKey(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	var req struct {
		Algorithm string `json:"algorithm"`
	}
	if r.ContentLength != 0 {
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	if a := strings.ToLower(strings.TrimSpace(req.Algorithm)); a != "" && a != "ed25519" {
		jsonErrCode(w, http.StatusBadRequest, "unsupported_algorithm", "only ed25519 is supported")
		return
	}
	info, err := h.sshKeys.Generate()
	if errors.Is(err, nodes.ErrKeyExists) {
		jsonErrCode(w, http.StatusConflict, "key_exists", "a cluster SSH key already exists; use rotation to replace it")
		return
	}
	if err != nil {
		jsonErrCode(w, http.StatusInternalServerError, "ssh_store_error", "cannot create the cluster SSH key")
		return
	}
	h.auditSSH(r, "cluster_ssh.generate", "cluster", map[string]any{"fingerprint": info.Fingerprint})
	jsonResp(w, http.StatusCreated, map[string]any{
		"configured": true, "algorithm": info.Algorithm, "fingerprint": info.Fingerprint,
		"public_key": info.PublicKey, "created_at": info.CreatedAt,
	})
}

// StartClusterSSHRotation generates a pending key.
func (h *Handler) StartClusterSSHRotation(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	p, err := h.sshKeys.StartRotation()
	switch {
	case errors.Is(err, nodes.ErrNoKey):
		jsonErrCode(w, http.StatusConflict, "no_key", "no cluster SSH key to rotate")
		return
	case err != nil:
		jsonErrCode(w, http.StatusInternalServerError, "ssh_store_error", "cannot create the pending key")
		return
	}
	h.sshTestState().rot.clear()
	h.auditSSH(r, "cluster_ssh.rotate", "cluster", map[string]any{"pending_fingerprint": p.Fingerprint})
	jsonResp(w, http.StatusCreated, map[string]any{"pending": map[string]any{
		"fingerprint": p.Fingerprint, "public_key": p.PublicKey,
		"authorized_keys_line": nodes.AuthorizedKeysLine(p.PublicKey),
		"command":              nodes.AuthorizeCommandForKey(p.PublicKey),
	}})
}

// rotationTargets lists the enabled remote libvirt nodes a rotation affects.
func (h *Handler) rotationTargets() []nodes.Node {
	var out []nodes.Node
	for _, n := range h.nodes.List() {
		if n.Driver() == nodes.BackendLibvirt && !n.IsLocal() && n.Enabled {
			if _, _, _, err := nodes.NodeSSHTarget(n.URI); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

type rotationNodeResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty"`
}

// TestClusterSSHRotation logs in to every enabled libvirt node with the
// PENDING key (a throwaway SSH connection: the pooled connections that use
// the active key are never touched) and remembers who accepted it, so the
// commit can refuse a switch that would lock WebKVM out.

// nodeErrorCode extracts the typed code without panicking on wrapped or
// foreign errors.
func nodeErrorCode(err error) string {
	var ne *libvirt.NodeError
	if errors.As(libvirt.ClassifyNodeError(err), &ne) && ne != nil {
		return ne.Code()
	}
	return "unknown_error"
}

func (h *Handler) TestClusterSSHRotation(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	info, err := h.sshKeys.Info()
	if err != nil {
		jsonErrCode(w, http.StatusInternalServerError, "ssh_store_error", "cannot read the cluster SSH key store")
		return
	}
	if info.Pending == nil {
		jsonErrCode(w, http.StatusConflict, "rotation_no_pending", "start a key rotation first")
		return
	}
	fp := info.Pending.Fingerprint
	targets := h.rotationTargets()
	results := make([]rotationNodeResult, len(targets))
	var wg sync.WaitGroup
	for i, n := range targets {
		wg.Add(1)
		go func(i int, n nodes.Node) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			res := rotationNodeResult{ID: n.ID, Name: n.Name}
			if err := h.sshKeys.ProbePendingKey(ctx, n); err != nil {
				res.Code = nodeErrorCode(err)
			} else {
				res.OK = true
			}
			results[i] = res
		}(i, n)
	}
	wg.Wait()
	all := true
	for _, res := range results {
		h.sshTestState().rot.record(fp, res.ID, res.OK, res.Code)
		all = all && res.OK
	}
	h.auditSSH(r, "cluster_ssh.rotate_test", "cluster", map[string]any{"pending_fingerprint": fp, "ok": all, "nodes": len(results)})
	jsonResp(w, http.StatusOK, map[string]any{
		"ok": all, "pending_fingerprint": fp, "nodes": results,
		"valid_for_seconds": int(rotationVerifyWindow.Seconds()),
	})
}

// CommitClusterSSHRotation switches to the pending key. It refuses (409
// rotation_unverified) unless every enabled libvirt node accepted the pending
// key recently, or the admin sends {"force":true}.
func (h *Handler) CommitClusterSSHRotation(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	var req struct {
		Force bool `json:"force"`
	}
	if r.ContentLength != 0 {
		if err := decodeBody(r, &req); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	info, err := h.sshKeys.Info()
	if err != nil {
		jsonErrCode(w, http.StatusInternalServerError, "ssh_store_error", "cannot read the cluster SSH key store")
		return
	}
	if info.Pending == nil {
		jsonErrCode(w, http.StatusConflict, "no_pending", "there is no pending key rotation")
		return
	}
	rot := &h.sshTestState().rot
	unverified := []map[string]any{}
	for _, n := range h.rotationTargets() {
		if !rot.passed(info.Pending.Fingerprint, n.ID) {
			unverified = append(unverified, map[string]any{"id": n.ID, "name": n.Name, "code": rot.lastCode(info.Pending.Fingerprint, n.ID)})
		}
	}
	if len(unverified) > 0 && !req.Force {
		resp := map[string]any{
			"error": "the new key has not been verified on every node; test it first or force the switch",
			"code":  "rotation_unverified", "failing": unverified,
		}
		jsonResp(w, http.StatusConflict, resp)
		return
	}
	if err := h.sshKeys.CommitRotation(); err != nil {
		h.rotationErr(w, err)
		return
	}
	rot.clear()
	h.sshTestState().clear()
	h.invalidateAllNodes()
	if len(unverified) > 0 {
		h.auditSSH(r, "cluster_ssh.rotate_commit_forced", "cluster", map[string]any{"unverified": unverified})
	} else {
		h.auditSSH(r, "cluster_ssh.rotate_commit", "cluster", nil)
	}
	jsonResp(w, http.StatusOK, map[string]any{"ok": true, "forced": len(unverified) > 0, "can_rollback": true})
}

func (h *Handler) AbortClusterSSHRotation(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	if err := h.sshKeys.AbortRotation(); err != nil {
		h.rotationErr(w, err)
		return
	}
	h.sshTestState().rot.clear()
	h.auditSSH(r, "cluster_ssh.rotate_abort", "cluster", nil)
	jsonResp(w, http.StatusOK, map[string]any{"ok": true})
}

// RollbackClusterSSHRotation restores the key that was active before the last
// committed rotation. Only that one previous key is kept (until the next
// committed rotation), and the key being replaced is discarded.
func (h *Handler) RollbackClusterSSHRotation(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	if err := h.sshKeys.RollbackRotation(); err != nil {
		if errors.Is(err, nodes.ErrNoPrevious) {
			jsonErrCode(w, http.StatusConflict, "rotation_no_previous", "there is no previous key to roll back to")
			return
		}
		jsonErrCode(w, http.StatusInternalServerError, "ssh_store_error", "cluster SSH key store error")
		return
	}
	h.sshTestState().rot.clear()
	h.sshTestState().clear()
	h.invalidateAllNodes()
	h.auditSSH(r, "cluster_ssh.rotate_rollback", "cluster", nil)
	jsonResp(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) rotationErr(w http.ResponseWriter, err error) {
	if errors.Is(err, nodes.ErrNoPending) {
		jsonErrCode(w, http.StatusConflict, "no_pending", "there is no pending key rotation")
		return
	}
	jsonErrCode(w, http.StatusInternalServerError, "ssh_store_error", "cluster SSH key store error")
}

// GetNodeSSHAuthorize shows what the operator must run on the node. WebKVM
// itself never connects to the node to do it.
func (h *Handler) GetNodeSSHAuthorize(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	_, user, host, _, ok := h.sshNode(w, r)
	if !ok {
		return
	}
	info, err := h.sshKeys.Info()
	if err != nil {
		jsonErrCode(w, http.StatusInternalServerError, "ssh_store_error", "cannot read the cluster SSH key store")
		return
	}
	if !info.Configured {
		jsonErrCode(w, http.StatusConflict, "no_key", "generate the cluster SSH key first")
		return
	}
	resp := map[string]any{
		"user": user, "host": host,
		"authorized_keys_line": nodes.AuthorizedKeysLine(info.PublicKey),
		"command":              h.sshKeys.AuthorizeCommand(user),
		"steps":                []string{"run_command_on_node", "capture_host_key", "confirm_fingerprint", "test_connection"},
	}
	if info.Pending != nil {
		resp["pending"] = map[string]any{
			"fingerprint":          info.Pending.Fingerprint,
			"authorized_keys_line": nodes.AuthorizedKeysLine(info.Pending.PublicKey),
			"command":              nodes.AuthorizeCommandForKey(info.Pending.PublicKey),
		}
	}
	jsonResp(w, http.StatusOK, resp)
}

func hostKeyErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, nodes.ErrHostTimeout):
		jsonErrCode(w, http.StatusGatewayTimeout, "node_timeout", "the node did not answer in time")
	case errors.Is(err, nodes.ErrFingerprintMismatch):
		jsonErrCode(w, http.StatusConflict, "fingerprint_mismatch", "the host key no longer matches the fingerprint you confirmed; capture it again")
	case errors.Is(err, nodes.ErrHostKeyChanged):
		jsonErrCode(w, http.StatusConflict, "host_key_changed", "a different host key is already pinned; pass replace=true only if you know the node was reinstalled")
	case errors.Is(err, nodes.ErrHostUnreachable):
		jsonErrCode(w, http.StatusBadGateway, "node_unreachable", "cannot reach the node's SSH port")
	default:
		jsonErrCode(w, http.StatusInternalServerError, "ssh_store_error", "cannot update the pinned host keys")
	}
}

// CaptureNodeHostKey fetches the node's host key for the operator to confirm.
// It authenticates nothing and pins nothing.
func (h *Handler) CaptureNodeHostKey(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	_, _, host, port, ok := h.sshNode(w, r)
	if !ok {
		return
	}
	hk, err := h.sshKeys.CaptureHostKey(r.Context(), host, port)
	if err != nil {
		hostKeyErr(w, err)
		return
	}
	state := nodes.HostKeyUnknown
	if fp, ok := h.sshKeys.PinnedHostKey(host, port); ok {
		state = nodes.HostKeyChanged
		if fp == hk.Fingerprint {
			state = nodes.HostKeyPinned
		}
	}
	jsonResp(w, http.StatusOK, map[string]any{"fingerprint": hk.Fingerprint, "key_type": hk.Type, "state": state})
}

// PinNodeHostKey pins the host key the operator confirmed by fingerprint.
func (h *Handler) PinNodeHostKey(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	n, _, host, port, ok := h.sshNode(w, r)
	if !ok {
		return
	}
	var req struct {
		Fingerprint string `json:"fingerprint"`
		Replace     bool   `json:"replace"`
	}
	if err := decodeBody(r, &req); err != nil || strings.TrimSpace(req.Fingerprint) == "" {
		jsonErrCode(w, http.StatusBadRequest, "invalid_request", "fingerprint is required")
		return
	}
	if err := h.sshKeys.PinHostKey(host, port, req.Fingerprint, req.Replace); err != nil {
		hostKeyErr(w, err)
		return
	}
	h.invalidateNode(n.ID)
	h.sshTestState().del(n.ID)
	h.auditSSH(r, "cluster_ssh.pin_hostkey", n.ID, map[string]any{
		"node": n.Name, "node_id": n.ID, "fingerprint": req.Fingerprint, "replace": req.Replace,
	})
	jsonResp(w, http.StatusOK, map[string]any{"ok": true, "fingerprint": strings.TrimSpace(req.Fingerprint), "state": nodes.HostKeyPinned})
}

type sshStep struct {
	OK   bool   `json:"ok"`
	Code string `json:"code,omitempty"`
}

var sshTestHints = map[string]string{
	"no_key":                "Generate the cluster SSH key first.",
	"host_key_unpinned":     "Capture the node's host key and confirm its fingerprint.",
	"host_key_changed":      "The host key changed. If the node was reinstalled, capture and pin it again with replace.",
	"ssh_auth_denied":       "Run the authorize command on the node as the node user, then test again.",
	"node_unreachable":      "Check the host, port and firewall of the node's SSH service.",
	"node_timeout":          "The node did not answer in time; check the network path and load.",
	"libvirt_unavailable":   "SSH works but libvirt is not reachable on the node: check libvirtd/virtqemud, and that virt-ssh-helper or nc is installed.",
	"transport_unsupported": "This libvirt client lacks the configured SSH transport; set cluster.libvirt_transport to libssh2, libssh or ssh (try the others in that order).",
}

// TestNodeSSH walks the whole chain (key, host key, SSH login, libvirt) and
// reports the first failure as a stable code, never raw ssh output.
func (h *Handler) TestNodeSSH(w http.ResponseWriter, r *http.Request) {
	if !h.sshReady(w) {
		return
	}
	n, _, host, port, ok := h.sshNode(w, r)
	if !ok {
		return
	}
	resp := map[string]any{"ssh": sshStep{}, "libvirt": map[string]any{"ok": false}}
	fail := func(sshOK bool, code string) {
		for k, v := range h.transportView() {
			resp[k] = v
		}
		resp["ok"], resp["code"] = false, code
		resp["ssh"] = sshStep{OK: sshOK, Code: codeIf(!sshOK, code)}
		if sshOK {
			resp["libvirt"] = map[string]any{"ok": false, "code": code}
		}
		resp["hint"] = sshTestHints[code]
		h.sshTestState().set(n.ID, sshLastTest{OK: false, Code: code, At: time.Now().UTC()})
		jsonResp(w, http.StatusOK, resp)
	}
	info, err := h.sshKeys.Info()
	if err != nil || !info.Configured {
		fail(false, "no_key")
		return
	}
	if h.libvirtPool == nil {
		fail(false, "node_unreachable")
		return
	}
	if _, pinned := h.sshKeys.PinnedHostKey(host, port); !pinned {
		fail(false, "host_key_unpinned")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	// A changed host key must be caught before libvirt ever dials.
	if hk, err := h.sshKeys.CaptureHostKey(ctx, host, port); err != nil {
		fail(false, nodeErrorCode(err))
		return
	} else if fp, _ := h.sshKeys.PinnedHostKey(host, port); fp != hk.Fingerprint {
		fail(false, "host_key_changed")
		return
	}
	// An explicit test is the operator asking "does it work now?" (typically
	// right after authorizing the key): never answer it from the pool's
	// circuit-breaker cache of an earlier failure.
	if h.libvirtPool.Status(n.ID).State == libvirt.PoolStateFailing {
		h.invalidateNode(n.ID)
	}
	c, err := h.libvirtPool.Connector(ctx, n)
	if err != nil {
		code := nodeErrorCode(err)
		fail(code == "libvirt_unavailable", code)
		return
	}
	probe, err := libvirtProbe(c)
	if err != nil {
		h.invalidateNode(n.ID)
		fail(true, "libvirt_unavailable")
		return
	}
	// After the connect: the pool may have fallen back to libssh.
	for k, v := range h.transportView() {
		resp[k] = v
	}
	resp["ok"] = true
	resp["ssh"] = sshStep{OK: true}
	resp["libvirt"] = map[string]any{"ok": true, "version": probe.Version, "kvm": probe.KVM, "domains": probe.Domains}
	resp["hint"] = ""
	h.sshTestState().set(n.ID, sshLastTest{OK: true, At: time.Now().UTC()})
	jsonResp(w, http.StatusOK, resp)
}

func codeIf(cond bool, code string) string {
	if cond {
		return code
	}
	return ""
}

func (h *Handler) auditSSH(r *http.Request, action, resource string, detail map[string]any) {
	if h.audit != nil {
		h.audit.Log(auditFor(r, action, resource, detail))
	}
}

// forgetNodeSSH drops pooled connection state for a node (edit, disable,
// delete) and, on delete, its pinned host key. Safe with no SSH wiring.
func (h *Handler) forgetNodeSSH(n nodes.Node, removeHostKey bool) {
	h.invalidateNode(n.ID)
	h.sshTestState().del(n.ID)
	if removeHostKey && h.sshKeys != nil && n.Driver() == nodes.BackendLibvirt && !n.IsLocal() {
		if _, host, port, err := nodes.NodeSSHTarget(n.URI); err == nil {
			// Only drop the key when no other node shares the endpoint.
			for _, o := range h.nodes.List() {
				if o.ID == n.ID || o.Driver() != nodes.BackendLibvirt {
					continue
				}
				if _, oh, op, err := nodes.NodeSSHTarget(o.URI); err == nil && oh == host && op == port {
					return
				}
			}
			_ = h.sshKeys.RemoveHostKey(host, port)
		}
	}
}
