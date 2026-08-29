package api

import (
	"net/http"
	"strconv"

	"webkvm/internal/audit"
)

// ListAudit returns paginated audit-log entries, newest first. Admin
// only — the log carries every user's actions, IPs, and action detail.
func (h *Handler) ListAudit(w http.ResponseWriter, r *http.Request) {
	if h.audit == nil {
		jsonErr(w, http.StatusServiceUnavailable, "audit log not initialized")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	opts := audit.ListOptions{
		Q:      r.URL.Query().Get("q"),
		User:   r.URL.Query().Get("user"),
		Action: r.URL.Query().Get("action"),
	}
	entries, total, err := h.audit.List(opts, limit, offset)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"entries": entries, "total": total})
}
