package api

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"webkvm/internal/ha"
	"webkvm/internal/hainstall"
	"webkvm/internal/sysutil"
)

// clusterHAEnabled is read on every request so disabling the module hides the
// HA surface immediately, without restarting the backend.
func (h *Handler) clusterHAEnabled() bool {
	return h.settings != nil && h.settings.GetBool("modules.cluster_ha")
}

// requireClusterHA turns every HA endpoint into a 404 module_not_installed
// when the optional module is off. The handlers never run: no pcs/crm_mon,
// no firewall cluster profile, no readiness probes.
func (h *Handler) requireClusterHA(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.clusterHAEnabled() {
			jsonErrCode(w, http.StatusNotFound, "module_not_installed",
				"the Cluster and HA module is not enabled; install it from Nodes")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// haInstallRequest is the optional body of the HA preflight/install calls.
// Unknown or invalid fencing kinds are ignored by hainstall.
type haInstallRequest struct {
	Linstor            bool     `json:"linstor"`
	AcceptExperimental bool     `json:"accept_experimental"`
	Fencing            []string `json:"fencing"`
}

func (r haInstallRequest) options() hainstall.Options {
	return hainstall.Options{Linstor: r.Linstor, AcceptExperimental: r.AcceptExperimental, Fencing: r.Fencing}.Normalized()
}

type haModuleResponse struct {
	Enabled bool             `json:"enabled"`
	Status  hainstall.Status `json:"status"`
	Busy    bool             `json:"busy"`
}

func (h *Handler) haModuleReady(w http.ResponseWriter) bool {
	if h.haInstall == nil {
		jsonErr(w, http.StatusServiceUnavailable, "HA installer not initialized")
		return false
	}
	return true
}

func (h *Handler) GetHAModule(w http.ResponseWriter, r *http.Request) {
	if !h.haModuleReady(w) {
		return
	}
	// Reconcile with the local dpkg on every read: DATA_DIR is shared, so
	// the persisted state may describe a different node after a failover.
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	status := h.haInstall.Reconcile(ctx)
	jsonResp(w, http.StatusOK, haModuleResponse{
		Enabled: h.clusterHAEnabled(),
		Status:  status,
		Busy:    h.haInstall.Busy(),
	})
}

func (h *Handler) PreflightHAModule(w http.ResponseWriter, r *http.Request) {
	if !h.haModuleReady(w) {
		return
	}
	var req haInstallRequest
	_ = decodeBody(r, &req)
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	jsonResp(w, http.StatusOK, h.haInstall.Plan(ctx, req.options()))
}

func (h *Handler) InstallHAModule(w http.ResponseWriter, r *http.Request) {
	if !h.haModuleReady(w) {
		return
	}
	var req haInstallRequest
	_ = decodeBody(r, &req)
	if err := h.haInstall.Start(req.options()); err != nil {
		if errors.Is(err, hainstall.ErrBusy) {
			jsonErrCode(w, http.StatusConflict, "module_install_busy", err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "ha_module.install", "modules", map[string]any{
			"linstor":             req.Linstor,
			"accept_experimental": req.AcceptExperimental,
			"fencing":             req.options().Fencing,
		}))
	}
	jsonResp(w, http.StatusAccepted, h.haInstall.Snapshot())
}

func (h *Handler) VerifyHAModule(w http.ResponseWriter, r *http.Request) {
	if !h.haModuleReady(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	status := h.haInstall.Verify(ctx)
	if h.audit != nil {
		h.audit.Log(auditFor(r, "ha_module.verify", "modules", map[string]any{"state": status.State}))
	}
	jsonResp(w, http.StatusOK, status)
}

func (h *Handler) EnableHAModule(w http.ResponseWriter, r *http.Request) {
	if !h.haModuleReady(w) {
		return
	}
	if h.settings == nil {
		jsonErr(w, http.StatusServiceUnavailable, "settings store not initialized")
		return
	}
	if h.haInstall.Snapshot().State != hainstall.StateInstalled {
		jsonErrCode(w, http.StatusConflict, "install_required",
			"the Cluster and HA packages are not installed yet")
		return
	}
	applied, failed, err := h.settings.SetMany(map[string]any{"modules.cluster_ha": true})
	if err != nil || len(failed) > 0 {
		jsonErr(w, http.StatusInternalServerError, "could not enable the module")
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "ha_module.enable", "modules", map[string]any{"applied": applied}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"enabled": true})
}

func (h *Handler) DisableHAModule(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		jsonErr(w, http.StatusServiceUnavailable, "settings store not initialized")
		return
	}
	applied, failed, err := h.settings.SetMany(map[string]any{"modules.cluster_ha": false})
	if err != nil || len(failed) > 0 {
		jsonErr(w, http.StatusInternalServerError, "could not disable the module")
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "ha_module.disable", "modules", map[string]any{"applied": applied}))
	}
	// The packages stay installed and the state file is preserved; only the
	// HA routes and UI disappear until the module is enabled again.
	jsonResp(w, http.StatusOK, map[string]any{"enabled": false})
}

// Cluster artifacts that `apt-get purge` would delete. Package-level so the
// tests can point them at a temp tree instead of the host's real files.
var (
	haClusterArtifactFiles = []string{"/etc/corosync/corosync.conf", "/var/lib/pacemaker/cib/cib.xml"}
	haClusterArtifactGlobs = []string{"/etc/drbd.d/*.res", "/var/lib/linstor.d/*.res"}
)

// clusterArtifactBlockers checks the on-disk artifacts in Go (no shell, no
// timeouts). Only a definite "does not exist" clears a path: a present file
// blocks with config_present, and any other error (ENOTDIR, EACCES, I/O)
// blocks with config_check_failed so an unanswerable check never reads as
// "absent".
func clusterArtifactBlockers() []string {
	blockers := []string{}
	for _, path := range haClusterArtifactFiles {
		switch _, err := os.Stat(path); {
		case err == nil:
			blockers = append(blockers, "config_present:"+path)
		case errors.Is(err, fs.ErrNotExist):
		default:
			blockers = append(blockers, "config_check_failed:"+path)
		}
	}
	for _, pattern := range haClusterArtifactGlobs {
		// filepath.Glob swallows directory read errors, so the directory is
		// listed explicitly and matched entry by entry.
		dir, base := filepath.Split(pattern)
		entries, err := os.ReadDir(dir)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			blockers = append(blockers, "config_check_failed:"+pattern)
			continue
		}
		for _, entry := range entries {
			if ok, err := filepath.Match(base, entry.Name()); err != nil {
				blockers = append(blockers, "config_check_failed:"+pattern)
				break
			} else if ok {
				blockers = append(blockers, "config_present:"+pattern)
				break
			}
		}
	}
	return blockers
}

// uninstallBlockers fails closed: a failing/unknown Pacemaker probe or any
// on-disk cluster artifact blocks the removal. The artifacts are the ones
// `apt-get purge` would delete (corosync configuration, CIB, DRBD resources).
func (h *Handler) uninstallBlockers(ctx context.Context) []string {
	blockers := []string{}
	pacemaker := ha.ProbePacemaker(ctx, sysutil.DefaultRunner)
	switch pacemaker.State {
	case ha.StateNotInstalled, ha.StateNotConfigured:
		// pcs is absent or explicitly reports no cluster: keep checking
		// the files below, because a cluster may be configured but stopped.
	case ha.StateObserved, ha.StateBlocked:
		if pacemaker.ClusterName != "" || pacemaker.NodesConfigured > 0 || len(pacemaker.NodeNames) > 0 {
			blockers = append(blockers, "pacemaker_cluster_configured")
		} else {
			blockers = append(blockers, "pacemaker_state_unknown")
		}
	default:
		blockers = append(blockers, "pacemaker_probe_failed")
	}

	blockers = append(blockers, clusterArtifactBlockers()...)
	return blockers
}

func (h *Handler) UninstallHAModule(w http.ResponseWriter, r *http.Request) {
	if !h.haModuleReady(w) {
		return
	}
	var req struct {
		Confirm bool `json:"confirm"`
		Purge   bool `json:"purge"`
	}
	if err := decodeBody(r, &req); err != nil || !req.Confirm {
		jsonErrCode(w, http.StatusBadRequest, "confirmation_required",
			"uninstalling requires explicit confirmation")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if blockers := h.uninstallBlockers(ctx); len(blockers) > 0 {
		jsonResp(w, http.StatusConflict, map[string]any{
			"error":    "refusing to remove the module: a cluster or its configuration is present",
			"code":     "uninstall_blocked",
			"blockers": blockers,
		})
		return
	}
	if err := h.haInstall.StartUninstall(req.Purge); err != nil {
		if errors.Is(err, hainstall.ErrBusy) {
			jsonErrCode(w, http.StatusConflict, "module_uninstall_busy", err.Error())
			return
		}
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Fail-closed: hide HA while the packages are being removed. If the
	// removal fails the operator can enable the module again.
	if h.settings != nil {
		_, _, _ = h.settings.SetMany(map[string]any{"modules.cluster_ha": false})
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "ha_module.uninstall", "modules", map[string]any{"purge": req.Purge}))
	}
	jsonResp(w, http.StatusAccepted, h.haInstall.Snapshot())
}
