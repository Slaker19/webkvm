package api

import (
	"encoding/json"
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
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 || limit > 500 {
		limit = 50
	}
	offset, err := strconv.Atoi(r.URL.Query().Get("offset"))
	if err != nil || offset < 0 {
		offset = 0
	}
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

// ExportAudit exports audit logs as CSV or JSONL.
func (h *Handler) ExportAudit(w http.ResponseWriter, r *http.Request) {
	if h.audit == nil {
		jsonErr(w, http.StatusServiceUnavailable, "audit log not initialized")
		return
	}
	opts := audit.ListOptions{
		Q:      r.URL.Query().Get("q"),
		User:   r.URL.Query().Get("user"),
		Action: r.URL.Query().Get("action"),
	}
	format := r.URL.Query().Get("format")
	entries, _, err := h.audit.List(opts, 5000, 0)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"audit-log.csv\"")
		w.Write([]byte("Time,User,Role,Action,Resource,IP,Error\n"))
		for _, e := range entries {
			w.Write([]byte(e.Time + "," + e.User + "," + e.Role + "," + e.Action + "," + e.Resource + "," + e.IP + "," + e.Error + "\n"))
		}
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\"audit-log.jsonl\"")
	for _, e := range entries {
		_ = json.NewEncoder(w).Encode(e)
	}
}
