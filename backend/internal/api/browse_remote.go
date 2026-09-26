package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"webkvm/internal/remotebrowse"
)

type browseRemoteRequest struct {
	Format    string `json:"format"` // "nfs" | "cifs"
	Host      string `json:"host"`
	SourceDir string `json:"source_dir"`
	Subpath   string `json:"subpath"`
	Username  string `json:"username,omitempty"`
	Password  string `json:"password,omitempty"`
}

type browseRemoteResponse struct {
	Entries []remotebrowse.Entry `json:"entries"`
}

// BrowseRemote lists the subfolders of an NFS export or SMB share, for
// the "browse folders" helper shared by the storage-pool and
// backup-target creation forms. Admin-only (see router.go): this
// contacts an arbitrary host supplied by the caller, the same
// SSRF-shaped surface already gated at admin level for pool creation.
func (h *Handler) BrowseRemote(w http.ResponseWriter, r *http.Request) {
	var req browseRemoteRequest
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	format := strings.ToLower(strings.TrimSpace(req.Format))
	if format != "nfs" && format != "cifs" {
		jsonErr(w, http.StatusBadRequest, "format must be 'nfs' or 'cifs'")
		return
	}
	if req.Host == "" || req.SourceDir == "" {
		jsonErr(w, http.StatusBadRequest, "host and source_dir are required")
		return
	}
	if format == "cifs" && (req.Username != "") != (req.Password != "") {
		jsonErr(w, http.StatusBadRequest, "cifs auth requires both username and password")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), remotebrowse.DefaultTimeout+2*time.Second)
	defer cancel()

	entries, err := remotebrowse.Browse(ctx, remotebrowse.Request{
		Format:    format,
		Host:      req.Host,
		SourceDir: req.SourceDir,
		Subpath:   req.Subpath,
		Username:  req.Username,
		Password:  req.Password,
	})
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, browseRemoteResponse{Entries: entries})
}
