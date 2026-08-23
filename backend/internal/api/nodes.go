package api

import (
	"encoding/json"
	"net/http"

	"webkvm/internal/nodes"
)

// NodeCreateRequest is the body of POST /api/nodes.
type NodeCreateRequest struct {
	Name string `json:"name"`
	URI  string `json:"uri"`
}

// NodeUpdateRequest is the body of PUT /api/nodes/{id}.
type NodeUpdateRequest struct {
	Name    string `json:"name,omitempty"`
	URI     string `json:"uri,omitempty"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// ListNodes returns every node. Read-only for any authenticated user.
func (h *Handler) ListNodes(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
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
	n, err := h.nodes.Create(req.Name, req.URI)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "node.create", n.ID, map[string]any{"name": n.Name, "uri": n.URI}))
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
	n, err := h.nodes.Update(id, req.Name, req.URI, req.Enabled)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "node.update", id, map[string]any{"name": n.Name}))
	}
	jsonResp(w, http.StatusOK, n)
}

// DeleteNode removes a node. Admin only.
func (h *Handler) DeleteNode(w http.ResponseWriter, r *http.Request) {
	if h.nodes == nil {
		jsonErr(w, http.StatusServiceUnavailable, "nodes registry not initialized")
		return
	}
	id := chiURLParam(r, "id")
	if err := h.nodes.Delete(id); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "node.delete", id, nil))
	}
	jsonResp(w, http.StatusOK, map[string]bool{"ok": true})
}

// helper used by system.go to expose the local node
type nodesListResult = []nodes.Node
