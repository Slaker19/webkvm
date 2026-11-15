package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/audit"
	"webkvm/internal/ha"
	"webkvm/internal/nodes"
	"webkvm/internal/sysutil"
)

// Released VMs (STABLE-REPORT 8.19/8.20). Turning HA off for a KVM workload
// with mode stop/forget/keep_running first records an inventory entry with
// the domain XML and the storage stack, then removes the Pacemaker stack.
// The VM is listed as "released" and an admin can Restore it on this node.

// releaseReadFile reads a VirtualDomain definition (bounded).
var releaseReadFile = func(path string) ([]byte, error) {
	if !ha.ValidReleasePath(path) {
		return nil, errors.New("invalid path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20+1))
}

// releaseResolveDevice resolves DRBD by-res links when comparing mounts.
var releaseResolveDevice = filepath.EvalSymlinks

// releaseLocalNode is the Pacemaker identity of this host.
var releaseLocalNode = localNodeName

// releaseLocalDomainXML dumps a domain from this node's libvirt.
var releaseLocalDomainXML = func(h *Handler, name string) (string, error) {
	if h.isRemote() || h.compute == nil {
		return "", errors.New("local libvirt is not available")
	}
	kvm := kvmBackendOf(h.compute)
	if kvm == nil {
		return "", errors.New("local libvirt is not available")
	}
	return kvm.GetDomainXML(name)
}

// releasePeerDomainXML dumps a domain from the libvirt of another cluster
// member (registered node whose name or URI host is the Pacemaker name).
var releasePeerDomainXML = func(ctx context.Context, h *Handler, pcmkNode, name string) (string, error) {
	if h.nodes == nil || h.libvirtPool == nil {
		return "", errors.New("no remote libvirt connection for " + pcmkNode)
	}
	member := haProbeResult{Nodes: []string{pcmkNode}}
	for _, n := range h.nodes.List() {
		if n.IsLocal() || !n.Enabled || n.Driver() != nodes.BackendLibvirt || !member.isMember(n) || !libvirtURIUsable(n.URI) {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, remoteLibvirtConnectTimeout)
		conn, err := h.libvirtPool.Connector(cctx, n)
		cancel()
		if err != nil {
			return "", err
		}
		return conn.GetDomainXML(name)
	}
	return "", errors.New("node " + pcmkNode + " is not registered as a libvirt node")
}

// releaseHostNamespace reports whether the host-visible restore steps can
// escape this service's private mount namespace through systemd-run
// (package var so tests do not depend on the host having systemd).
var releaseHostNamespace = sysutil.SystemdRunAvailable

var releaseTuning = struct{ ConnectWait, Poll time.Duration }{ConnectWait: 15 * time.Second, Poll: time.Second}

func (h *Handler) releasedStore() ha.ReleasedStore { return ha.ReleasedStore{Dir: h.dataDir()} }

type releaseCapture struct {
	prev   *ha.ReleasedVM
	stored bool
	entry  ha.ReleasedVM
}

// captureDomainXML returns the domain XML of a running HA VM from the best
// source: the definition on the replicated filesystem (only when the VM runs
// on this node, so the filesystem is mounted here), this node's libvirt, the
// libvirt of the member running it, or a previous capture.
func (h *Handler) captureDomainXML(ctx context.Context, name string, wl ha.Workload, stack ha.KVMStack, prev *ha.ReleasedVM) (xmlText, source string, why []string) {
	local := releaseLocalNode()
	try := func(src string, raw []byte, err error) bool {
		if err != nil {
			why = append(why, src+": "+err.Error())
			return false
		}
		if _, cerr := ha.CheckDomainXML(raw, name, ""); cerr != nil {
			why = append(why, src+": "+cerr.Error())
			return false
		}
		xmlText, source = string(raw), src
		return true
	}
	if wl.Node != "" && wl.Node == local && ha.ValidReleasePath(stack.ConfigPath) {
		raw, err := releaseReadFile(stack.ConfigPath)
		if try(ha.ReleaseSourceConfigFile, raw, err) {
			return
		}
	}
	if wl.Node == "" || wl.Node == local {
		raw, err := releaseLocalDomainXML(h, name)
		if try(ha.ReleaseSourceLibvirtLocal, []byte(raw), err) {
			return
		}
	} else {
		raw, err := releasePeerDomainXML(ctx, h, wl.Node, name)
		if try(ha.ReleaseSourceLibvirtPeer, []byte(raw), err) {
			return
		}
	}
	if prev != nil && prev.XML != "" {
		if try(ha.ReleaseSourcePrevious, []byte(prev.XML), nil) {
			return
		}
	}
	if wl.Node == "" {
		why = append(why, "the VM is not running (paused or stopped): its definition is on the released filesystem; resume it and disable HA again")
	}
	return "", "", why
}

// captureRelease records the inventory entry of a KVM workload before its
// stack is removed. Without a restorable XML it refuses (nothing changed)
// unless the operator accepted an unrestorable release.
func (h *Handler) captureRelease(r *http.Request, key ha.WorkloadKey, wl ha.Workload, mode string, accept bool) (*releaseCapture, *apiErr) {
	ctx := r.Context()
	user, _, _ := audit.FromRequest(r)
	store := h.releasedStore()
	entry := ha.ReleasedVM{
		Name: key.Name, Status: ha.ReleaseStatusReleased, Mode: mode, LastNode: wl.Node,
		PreferredNode: wl.PreferredNode, AllowedNodes: append([]string{}, wl.AllowedNodes...),
		ReleasedAt: time.Now().UTC(), ReleasedBy: user,
	}
	var why []string
	stack, serr := ha.ReadKVMStack(ctx, sysutil.DefaultRunner, key.Name)
	if serr != nil {
		why = append(why, "storage stack: "+serr.Error())
	}
	entry.Stack = stack
	if stack.DRBDResource != "" {
		// Note the replica nodes now (read-only): after stop/forget the
		// resource is down everywhere and Restore needs them brought up.
		entry.DRBDPeers = ha.ReadDRBDPeers(ctx, sysutil.DefaultRunner, stack.DRBDResource)
	}
	var prev *ha.ReleasedVM
	if old, ok, err := store.Get(key.Name); err == nil && ok {
		prev = &old
	}
	xmlText, source, xwhy := h.captureDomainXML(ctx, key.Name, wl, stack, prev)
	why = append(why, xwhy...)
	if xmlText != "" {
		uuid, _ := ha.CheckDomainXML([]byte(xmlText), key.Name, "")
		entry.XML, entry.XMLSource, entry.XMLSHA256, entry.UUID = xmlText, source, ha.XMLDigest(xmlText), uuid
	}
	if xmlText == "" || serr != nil {
		entry.Unrestorable = true
		if !accept {
			h.auditWorkload(r, "ha_workload.release.capture", key, map[string]any{"mode": mode, "result": "refused", "code": ha.CodeReleaseCaptureFailed})
			return nil, &apiErr{Status: http.StatusConflict, Code: ha.CodeReleaseCaptureFailed,
				Msg: "the VM definition could not be saved, so it could not be restored after HA is turned off; nothing was changed (" + strings.Join(why, "; ") + "). Pass accept_unrestorable=true to continue anyway"}
		}
	}
	prevStored, err := store.Put(entry)
	if err != nil {
		slog.Warn("ha_release_store_failed", "key", key.String(), "err", err)
		if !accept {
			h.auditWorkload(r, "ha_workload.release.capture", key, map[string]any{"mode": mode, "result": "refused", "code": ha.CodeReleaseStoreFailed})
			return nil, &apiErr{Status: http.StatusInternalServerError, Code: ha.CodeReleaseStoreFailed,
				Msg: "the released-VM inventory could not be written; nothing was changed. Pass accept_unrestorable=true to continue anyway"}
		}
		h.auditWorkload(r, "ha_workload.release.capture", key, map[string]any{"mode": mode, "result": "not_stored", "accepted_unrestorable": true})
		return &releaseCapture{entry: entry}, nil
	}
	h.auditWorkload(r, "ha_workload.release.capture", key, map[string]any{
		"mode": mode, "result": "ok", "xml_source": source, "xml_sha256": entry.XMLSHA256,
		"unrestorable": entry.Unrestorable, "accepted_unrestorable": accept && entry.Unrestorable,
		"drbd_resource": stack.DRBDResource, "last_node": wl.Node,
	})
	return &releaseCapture{prev: prevStored, stored: true, entry: entry}, nil
}

// undoCapture reverts the entry written by this request when the disable
// failed without changing anything (no ghost "released" entry for a VM that
// is still under HA).
func (h *Handler) undoCapture(key ha.WorkloadKey, c *releaseCapture) {
	if c == nil || !c.stored {
		return
	}
	if err := h.releasedStore().Revert(key.Name, c.prev); err != nil {
		slog.Warn("ha_release_revert_failed", "key", key.String(), "err", err)
	}
}

// ListReleasedVMs GET /released.
func (h *Handler) ListReleasedVMs(w http.ResponseWriter, r *http.Request) {
	list, err := h.releasedStore().List()
	if err != nil {
		slog.Warn("ha_released_list_failed", "err", err)
		jsonErrCode(w, http.StatusInternalServerError, ha.CodeReleaseStoreFailed, "the released-VM inventory could not be read")
		return
	}
	out := make([]ha.ReleasedVMView, 0, len(list))
	for _, e := range list {
		out = append(out, e.View())
	}
	jsonResp(w, http.StatusOK, map[string]any{"released": out, "local_node": releaseLocalNode()})
}

type restoreRequest struct {
	Confirm string `json:"confirm"`
	Node    string `json:"node"`
	Start   bool   `json:"start"`
}

func releasedKey(r *http.Request) (ha.WorkloadKey, *apiErr) {
	key := ha.WorkloadKey{Kind: ha.KindKVM, Name: chi.URLParam(r, "name")}
	return key, validKey(key)
}

// RestoreReleasedVM POST /released/{name}/restore.
func (h *Handler) RestoreReleasedVM(w http.ResponseWriter, r *http.Request) {
	key, bad := releasedKey(r)
	if bad != nil {
		writeAPIErr(w, bad)
		return
	}
	var req restoreRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErrCode(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if req.Confirm != key.Name {
		writeAPIErr(w, &apiErr{Status: http.StatusConflict, Code: ha.CodeConfirmation, Msg: "type the VM name as confirmation"})
		return
	}
	local := releaseLocalNode()
	if h.isRemote() || (req.Node != "" && req.Node != local) {
		writeAPIErr(w, &apiErr{Status: http.StatusConflict, Code: ha.CodeRestoreNodeNotLocal,
			Msg: "a released VM can only be restored on the node serving this panel (" + local + ")"})
		return
	}
	unlock, ok := ha.LockKey(key.String())
	if !ok {
		writeAPIErr(w, &apiErr{Status: http.StatusConflict, Code: ha.CodeBusy, Msg: "another operation on " + key.String() + " is running"})
		return
	}
	defer unlock()
	store := h.releasedStore()
	entry, found, err := store.Get(key.Name)
	if err != nil {
		slog.Warn("ha_released_read_failed", "err", err)
		writeAPIErr(w, &apiErr{Status: http.StatusInternalServerError, Code: ha.CodeReleaseStoreFailed, Msg: "the released-VM inventory could not be read"})
		return
	}
	if !found {
		writeAPIErr(w, &apiErr{Status: http.StatusNotFound, Code: ha.CodeReleaseNotFound, Msg: "no released VM named " + key.Name})
		return
	}
	ctx := r.Context()
	detail := map[string]any{"node": local, "start": req.Start, "drbd_resource": entry.Stack.DRBDResource}
	if cerr := ha.RestoreConflicts(ctx, sysutil.DefaultRunner, key.Name, entry.Stack.DRBDResource, h.cpGroup()); cerr != nil {
		e := opToAPI(cerr)
		detail["result"], detail["code"] = "refused", e.Code
		h.auditWorkload(r, "ha_released.restore", key, detail)
		writeAPIErr(w, e)
		return
	}
	res, rerr := ha.RestoreReleased(ctx, sysutil.DefaultRunner, entry, ha.RestoreOptions{
		Start: req.Start, TmpDir: h.dataDir(), ConnectWait: releaseTuning.ConnectWait, Poll: releaseTuning.Poll,
		ReadFile: releaseReadFile, ResolveDevice: releaseResolveDevice,
		HostRunner:    sysutil.HostNamespaceRunner{Inner: sysutil.DefaultRunner, Available: releaseHostNamespace},
		DefinitionDir: libvirtDefinitionDir,
	})
	if rerr != nil {
		e := opToAPI(rerr)
		detail["result"], detail["code"], detail["error"], detail["steps"] = "error", e.Code, e.Msg, res.Steps
		if p, ok := e.Details["peers"]; ok {
			detail["peers_down"] = p
		}
		h.auditWorkload(r, "ha_released.restore", key, detail)
		writeAPIErr(w, e)
		return
	}
	body := map[string]any{"changed": res.Changed, "state": res.State, "uuid": res.UUID, "xml_source": res.XMLSource,
		"xml_differs": res.XMLDiffers, "xml_linked": res.XMLLinked, "steps": res.Steps, "node": local}
	if entry.Status != ha.ReleaseStatusRestored || res.Changed {
		now := time.Now().UTC()
		user, _, _ := audit.FromRequest(r)
		entry.Status, entry.RestoredAt, entry.RestoredNode, entry.RestoredBy = ha.ReleaseStatusRestored, &now, local, user
		if _, err := store.Put(entry); err != nil {
			slog.Warn("ha_released_mark_failed", "key", key.String(), "err", err)
			body["warning"] = ha.CodeReleaseStoreFailed
		}
	}
	body["released"] = entry.View()
	detail["result"], detail["changed"], detail["steps"], detail["xml_source"] = "ok", res.Changed, res.Steps, res.XMLSource
	h.auditWorkload(r, "ha_released.restore", key, detail)
	jsonResp(w, http.StatusOK, body)
}

// DiscardReleasedVM DELETE /released/{name}?confirm=<name>. Removes only the
// inventory entry; it never touches libvirt, DRBD or any data.
func (h *Handler) DiscardReleasedVM(w http.ResponseWriter, r *http.Request) {
	key, bad := releasedKey(r)
	if bad != nil {
		writeAPIErr(w, bad)
		return
	}
	if r.URL.Query().Get("confirm") != key.Name {
		writeAPIErr(w, &apiErr{Status: http.StatusConflict, Code: ha.CodeConfirmation, Msg: "type the VM name as confirmation"})
		return
	}
	unlock, ok := ha.LockKey(key.String())
	if !ok {
		writeAPIErr(w, &apiErr{Status: http.StatusConflict, Code: ha.CodeBusy, Msg: "another operation on " + key.String() + " is running"})
		return
	}
	defer unlock()
	found, err := h.releasedStore().Delete(key.Name)
	if err != nil {
		slog.Warn("ha_released_discard_failed", "err", err)
		writeAPIErr(w, &apiErr{Status: http.StatusInternalServerError, Code: ha.CodeReleaseStoreFailed, Msg: "the released-VM inventory could not be written"})
		return
	}
	if !found {
		writeAPIErr(w, &apiErr{Status: http.StatusNotFound, Code: ha.CodeReleaseNotFound, Msg: "no released VM named " + key.Name})
		return
	}
	h.auditWorkload(r, "ha_released.discard", key, map[string]any{"result": "ok"})
	jsonResp(w, http.StatusOK, map[string]any{"changed": true})
}
