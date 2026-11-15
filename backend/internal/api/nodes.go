package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"webkvm/internal/audit"
	"webkvm/internal/incusremote"
	"webkvm/internal/libvirt"
	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// NodeCreateRequest is the body of POST /api/nodes.
type NodeCreateRequest struct {
	Name       string        `json:"name"`
	URI        string        `json:"uri"`
	Backend    nodes.Backend `json:"backend,omitempty"`
	Project    string        `json:"project,omitempty"`
	TrustToken string        `json:"trust_token,omitempty"`
	UIURL      string        `json:"ui_url,omitempty"`
}

// NodeUpdateRequest is the body of PUT /api/nodes/{id}.
type NodeUpdateRequest struct {
	Name string `json:"name,omitempty"`
	URI  string `json:"uri,omitempty"`
	// UIURL: nil keeps the current address, "" clears it.
	UIURL   *string `json:"ui_url,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
}

// Seams so tests can exercise enrollment/revocation without a real Incus server.
var (
	incusEnroll = incusremote.Enroll
	incusRevoke = incusremote.Revoke
)

// nodeErr maps registry errors to HTTP statuses: validation 400 (with its
// code), conflict 409, not found 404 and anything else defaultStatus.
func nodeErr(w http.ResponseWriter, err error, defaultStatus int) {
	var ve *nodes.ValidationError
	switch {
	case errors.As(err, &ve):
		jsonErrCode(w, http.StatusBadRequest, ve.Code, ve.Msg)
	case errors.Is(err, nodes.ErrNotFound):
		jsonErrCode(w, http.StatusNotFound, "not_found", "node not found")
	case errors.Is(err, nodes.ErrConflict):
		jsonErrCode(w, http.StatusConflict, "name_conflict", err.Error())
	default:
		jsonErr(w, defaultStatus, err.Error())
	}
}

// libvirtURIUsable reports whether a stored libvirt URI may be handed to the
// libvirt client: URIs with query options (command=, netcat=...) saved before
// validation existed are never connected to.
func libvirtURIUsable(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

// sshSetupPendingCode reports whether err only means that the managed SSH
// access of the node is not set up yet (no key, host key not pinned, key not
// authorized), which is resolved after the node is registered.
func sshSetupPendingCode(err error) (string, bool) {
	var ne *libvirt.NodeError
	if !errors.As(libvirt.ClassifyNodeError(err), &ne) {
		return "", false
	}
	switch ne.Code() {
	case "no_key", "host_key_unpinned", "ssh_auth_denied":
		return ne.Code(), true
	}
	return "", false
}

func isRemoteLibvirtURI(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "qemu+ssh" && u.Scheme != "qemu+tcp") || u.Hostname() == "" || u.Path != "/system" {
		return false
	}
	if u.User != nil {
		_, hasPassword := u.User.Password()
		return !hasPassword
	}
	return true
}

// TestNodeConnection validates remote libvirt access without saving the URI.
func (h *Handler) TestNodeConnection(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URI        string        `json:"uri"`
		Backend    nodes.Backend `json:"backend,omitempty"`
		Project    string        `json:"project,omitempty"`
		TrustToken string        `json:"trust_token,omitempty"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	uri := strings.TrimSpace(req.URI)
	if req.Backend == nodes.BackendIncus {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		info, credentials, err := incusremote.TestEndpoint(ctx, uri, req.TrustToken)
		if err != nil {
			jsonErr(w, http.StatusBadGateway, err.Error())
			return
		}
		jsonResp(w, http.StatusOK, map[string]any{
			"ok":                 true,
			"backend":            nodes.BackendIncus,
			"server":             info,
			"server_fingerprint": incusremote.CertificateFingerprint(credentials.ServerCertificate),
			"project":            req.Project,
		})
		return
	}
	if req.Backend != "" && req.Backend != nodes.BackendLibvirt {
		jsonErr(w, http.StatusBadRequest, "unsupported node backend")
		return
	}
	if err := nodes.ValidateRemoteLibvirtURI(uri); err != nil {
		nodeErr(w, err, http.StatusBadRequest)
		return
	}
	domains, kvmAvailable, err := libvirt.TestRemoteConnection(uri)
	if err != nil {
		if code, ok := sshSetupPendingCode(err); ok {
			// The managed SSH access of a node that is not registered yet cannot
			// be completed first (the key line and the host key pin live on the
			// registered node). Answer 200 so the UI lets the admin save the node
			// and finish in "Cluster access".
			jsonResp(w, http.StatusOK, map[string]any{
				"ok": false, "setup_pending": true, "code": code, "error": libvirt.ClassifyNodeError(err).Error(),
			})
			return
		}
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"ok": true, "domains": domains, "kvm_available": kvmAvailable})
}

// ListNodes returns every node. Read-only for any authenticated user.
func (h *Handler) ListNodes(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	// Refresh stale entries instead of serving the identity and health that
	// the previous active node persisted in the shared DATA_DIR. TTL keeps
	// a polling UI from opening one SSH/libvirt connection per request.
	h.nodes.ProbeAllStale(10 * time.Second)
	jsonResp(w, http.StatusOK, map[string]any{"nodes": h.nodes.List()})
}

// GetNode returns a single node.
func (h *Handler) GetNode(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	id := chiURLParam(r, "id")
	n, ok := h.nodes.Get(id)
	if !ok {
		jsonErr(w, http.StatusNotFound, "node not found")
		return
	}
	jsonResp(w, http.StatusOK, n)
}

// CreateNode registers a new remote node. Admin only.
func (h *Handler) CreateNode(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	var req NodeCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.Backend == nodes.BackendIncus {
		h.createIncusNode(w, r, req)
		return
	}
	if req.Backend != "" && req.Backend != nodes.BackendLibvirt {
		jsonErr(w, http.StatusBadRequest, "unsupported node backend")
		return
	}
	if err := nodes.ValidateRemoteLibvirtURI(req.URI); err != nil {
		nodeErr(w, err, http.StatusBadRequest)
		return
	}
	n, err := h.nodes.CreateWithUI(req.Name, req.URI, nodes.BackendLibvirt, "", "", "", req.UIURL)
	if err != nil {
		nodeErr(w, err, http.StatusBadRequest)
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "node.create", n.ID, map[string]any{"name": n.Name, "uri": nodes.RedactURI(n.URI)}))
	}
	jsonResp(w, http.StatusCreated, n)
}

func (h *Handler) createIncusNode(w http.ResponseWriter, r *http.Request, req NodeCreateRequest) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// Validate everything that does not need the one-time trust token first:
	// Enroll consumes it, so a bad or duplicate name must fail before.
	if err := h.nodes.CheckNewName(req.Name); err != nil {
		nodeErr(w, err, http.StatusBadRequest)
		return
	}
	credentialStore := h.nodes.IncusCredentials()
	if credentialStore == nil {
		jsonErr(w, http.StatusServiceUnavailable, "Incus credential storage is unavailable")
		return
	}
	endpoint, err := incusremote.NormalizeEndpoint(req.URI)
	if err != nil {
		jsonErrCode(w, http.StatusBadRequest, nodes.CodeInvalidURI, err.Error())
		return
	}
	credentials, info, err := incusEnroll(ctx, endpoint, req.TrustToken, req.Project)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, err.Error())
		return
	}
	// The request context may already be exhausted; the cleanup of a
	// freshly trusted certificate must not depend on it.
	revoke := func() error {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cleanupCancel()
		return incusRevoke(cleanupCtx, endpoint, credentials, req.Project)
	}
	credentialID := incusremote.CertificateFingerprint(credentials.ClientCertificate)
	serverFingerprint := incusremote.CertificateFingerprint(credentials.ServerCertificate)
	if err := credentialStore.Save(credentialID, nodes.IncusCredentials{
		ClientCertificate: credentials.ClientCertificate,
		ClientKey:         credentials.ClientKey,
		ServerCertificate: credentials.ServerCertificate,
	}); err != nil {
		if revokeErr := revoke(); revokeErr != nil {
			err = errors.Join(err, fmt.Errorf("remote trust cleanup also failed: %w", revokeErr))
		}
		jsonErr(w, http.StatusInternalServerError, "store Incus credentials: "+err.Error())
		return
	}
	n, err := h.nodes.CreateWithUI(req.Name, endpoint, nodes.BackendIncus, req.Project, credentialID, serverFingerprint, req.UIURL)
	if err != nil {
		cleanupErr := revoke()
		storeErr := credentialStore.Delete(credentialID)
		nodeErr(w, errors.Join(err, cleanupErr, storeErr), http.StatusBadRequest)
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "node.create", n.ID, map[string]any{
			"name": n.Name, "backend": n.Backend, "project": n.Project, "server": info.Name,
		}))
	}
	jsonResp(w, http.StatusCreated, n)
}

// UpdateNode edits a remote node. Admin only.
func (h *Handler) UpdateNode(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	id := chiURLParam(r, "id")
	var req NodeUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	current, ok := h.nodes.Get(id)
	if !ok {
		nodeErr(w, nodes.ErrNotFound, http.StatusNotFound)
		return
	}
	if current.IsLocal() && req.Enabled != nil && !*req.Enabled {
		jsonErrCode(w, http.StatusBadRequest, "local_node", "the local node cannot be disabled")
		return
	}
	newURI := strings.TrimSpace(req.URI)
	uriChanged := false
	if newURI != "" && newURI != current.URI && !current.IsLocal() && current.Driver() == nodes.BackendLibvirt {
		// Only a changed URI is validated, so nodes stored with an older,
		// looser URI can still be renamed or disabled.
		if err := nodes.ValidateRemoteLibvirtURI(newURI); err != nil {
			nodeErr(w, err, http.StatusBadRequest)
			return
		}
		uriChanged = true
	}
	n, err := h.nodes.UpdateWithUI(id, req.Name, req.URI, req.UIURL, req.Enabled)
	if err != nil {
		nodeErr(w, err, http.StatusInternalServerError)
		return
	}
	// Edited or enabled/disabled: never keep a pooled connection built from
	// the old definition; a moved endpoint also loses its pinned host key.
	h.forgetNodeSSH(current, uriChanged)
	if h.audit != nil {
		detail := map[string]any{"name": n.Name}
		if uriChanged {
			detail["uri"] = nodes.RedactURI(n.URI)
		}
		h.audit.Log(auditFor(r, "node.update", id, detail))
	}
	jsonResp(w, http.StatusOK, n)
}

// DeleteNode removes a node. Admin only.
//
// For Incus nodes the client certificate is revoked on the remote server
// first. If that fails the node is kept (502 incus_revoke_failed) unless the
// administrator passes ?force=true, which removes the local entry and
// credentials anyway and returns a warning: a client certificate may remain
// trusted on the remote Incus server.
func (h *Handler) DeleteNode(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	id := chiURLParam(r, "id")
	force := r.URL.Query().Get("force") == "true"
	if force {
		if _, role, _ := audit.FromRequest(r); role != models.RoleAdmin {
			jsonErr(w, http.StatusForbidden, "force delete is restricted to administrators")
			return
		}
	}
	node, exists := h.nodes.Get(id)
	if !exists {
		nodeErr(w, nodes.ErrNotFound, http.StatusNotFound)
		return
	}
	if node.IsLocal() {
		jsonErrCode(w, http.StatusBadRequest, "local_node", "cannot delete the local node")
		return
	}
	var warnings []string
	incusNode := node.Backend == nodes.BackendIncus && node.CredentialID != ""
	if incusNode {
		var err error
		credentials, loadErr := h.nodes.IncusCredentials().Load(node.CredentialID)
		err = loadErr
		if err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			err = incusRevoke(ctx, node.URI, incusremote.Credentials{
				ClientCertificate: credentials.ClientCertificate,
				ClientKey:         credentials.ClientKey,
				ServerCertificate: credentials.ServerCertificate,
			}, node.Project)
			cancel()
		}
		if err != nil {
			hint := "incus config trust remove " + node.CredentialID
			if !force {
				jsonResp(w, http.StatusBadGateway, map[string]string{
					"error": "cannot revoke the Incus client certificate; node was not deleted: " + err.Error() +
						". Retry with ?force=true to remove it locally anyway, then run `" + hint + "` on the remote Incus server.",
					"code": "incus_revoke_failed",
					"hint": "force=true",
				})
				return
			}
			warnings = append(warnings, "the client certificate may remain trusted on the remote Incus server ("+err.Error()+
				"); remove it manually with `"+hint+"`")
		}
	}
	if err := h.nodes.Delete(id); err != nil {
		nodeErr(w, err, http.StatusBadRequest)
		return
	}
	h.forgetNodeSSH(node, true)
	if incusNode {
		if err := h.nodes.IncusCredentials().Delete(node.CredentialID); err != nil {
			warnings = append(warnings, "node deleted but local Incus credentials could not be removed: "+err.Error())
		}
	}
	if h.audit != nil {
		detail := map[string]any{"name": node.Name}
		if force {
			detail["force"] = true
		}
		if len(warnings) > 0 {
			detail["warning"] = strings.Join(warnings, "; ")
		}
		h.audit.Log(auditFor(r, "node.delete", id, detail))
	}
	resp := map[string]any{"ok": true}
	if len(warnings) > 0 {
		resp["warning"] = strings.Join(warnings, "; ")
	}
	jsonResp(w, http.StatusOK, resp)
}

// PingNode triggers an immediate probe and returns the node with updated
// latency and status. An unknown id is a 404; a failed probe is still 200
// (the node is returned with its offline status).
func (h *Handler) PingNode(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	id := chiURLParam(r, "id")
	node, err := h.nodes.ProbeNode(id)
	if errors.Is(err, nodes.ErrNotFound) {
		nodeErr(w, err, http.StatusNotFound)
		return
	}
	jsonResp(w, http.StatusOK, node)
}

// PingAllNodes triggers an immediate probe of all fleet nodes concurrently.
func (h *Handler) PingAllNodes(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	list := h.nodes.ProbeAll()
	jsonResp(w, http.StatusOK, map[string]any{
		"nodes":   list,
		"summary": h.nodes.ClusterSummary(),
	})
}

// GetClusterSummary returns aggregated telemetry across all fleet nodes.
func (h *Handler) GetClusterSummary(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	jsonResp(w, http.StatusOK, h.nodes.ClusterSummary())
}
