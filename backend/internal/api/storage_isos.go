package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"webkvm/internal/compute"
	"webkvm/internal/config"
	"webkvm/internal/models"
)

func (h *Handler) ListISOs(w http.ResponseWriter, r *http.Request) {
	poolName := r.URL.Query().Get("pool")
	if poolName != "" {
		if !h.poolVisibleTo(r, poolName) {
			jsonResp(w, http.StatusOK, []models.ISOScanResult{})
			return
		}
		isos, err := h.compute.GetISOs(poolName)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jsonResp(w, http.StatusOK, isos)
		return
	}
	allPools, err := h.compute.ListStoragePools()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var isos []models.ISOScanResult
	for _, p := range allPools {
		if !h.poolVisibleTo(r, p.Name) {
			continue
		}
		poolISOs, err := h.compute.GetISOs(p.Name)
		if err != nil {
			continue
		}
		isos = append(isos, poolISOs...)
	}
	if isos == nil {
		isos = []models.ISOScanResult{}
	}
	jsonResp(w, http.StatusOK, isos)
}

func (h *Handler) DeleteISO(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		jsonErr(w, http.StatusBadRequest, "invalid name: path traversal not allowed")
		return
	}
	if pool == "" {
		pool = config.ISOPoolName
	}
	if !h.poolVisibleTo(r, pool) {
		jsonErr(w, http.StatusForbidden, "access denied to storage pool")
		return
	}
	if atts, err := h.compute.FindVolumeAttachments(pool, name); err == nil && len(atts) > 0 {
		volumeInUse(w, fmt.Sprintf("ISO %s/%s", pool, name), atts)
		return
	}
	if err := h.compute.DeleteISO(name, pool); err != nil {
		if errors.Is(err, compute.ErrVolumeNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "iso deleted"})
}

func (h *Handler) RenameISO(w http.ResponseWriter, r *http.Request) {
	pool := chi.URLParam(r, "pool")
	name := chi.URLParam(r, "name")
	if strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
		jsonErr(w, http.StatusBadRequest, "invalid name: path traversal not allowed")
		return
	}
	if pool == "" {
		pool = config.ISOPoolName
	}
	if !h.poolVisibleTo(r, pool) {
		jsonErr(w, http.StatusForbidden, "access denied to storage pool")
		return
	}
	var req struct {
		NewName string `json:"new_name"`
	}
	if err := decodeBody(r, &req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.NewName == "" {
		jsonErr(w, http.StatusBadRequest, "new_name is required")
		return
	}
	safeNew, err := safeISOFilename(req.NewName)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.compute.RenameISO(name, safeNew, pool); err != nil {
		if errors.Is(err, compute.ErrVolumeNotFound) {
			jsonErr(w, http.StatusNotFound, err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"status": "renamed", "name": safeNew})
}
