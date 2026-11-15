package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"webkvm/internal/firewall"
	"webkvm/internal/ha"
	"webkvm/internal/nodes"
	"webkvm/internal/sysutil"
)

type incusHAReadiness struct {
	NodeID  string `json:"node_id"`
	Name    string `json:"name"`
	Project string `json:"project"`
	State   string `json:"state"`
}

type controlPlaneHAReadiness struct {
	State                 string `json:"state"`
	SharedDataDirVerified bool   `json:"shared_data_dir_verified"`
	VirtualIPConfigured   bool   `json:"virtual_ip_configured"`
	FencingTested         bool   `json:"fencing_tested"`
	SharedStorageVerified bool   `json:"shared_storage_verified"`
	// Node is the Pacemaker node currently running the WebKVM control plane
	// ("" when none); the UI uses it to keep it out of the fencing-test picker.
	Node string `json:"node,omitempty"`
	// Group is the Pacemaker group adopted as the control plane
	// (setting ha.cp_group, default webkvm-cp). Checks lists what is wrong
	// with it; WebKVM adopts an existing control plane and never recreates it.
	Group  string     `json:"group,omitempty"`
	Checks []ha.Check `json:"checks,omitempty"`
}

// pveFenceReadiness describes the optional fence_pve device and the pcs
// commands the operator must run by hand. Commands never contain the token
// secret: they use <PVE_TOKEN_SECRET> so the response cannot leak it.
type pveFenceReadiness struct {
	Configured     bool     `json:"configured"`
	AgentInstalled bool     `json:"agent_installed"`
	Commands       []string `json:"commands"`
	Notes          []string `json:"notes"`
}

type fencingHAReadiness struct {
	Enabled    bool `json:"enabled"`
	Hypervisor bool `json:"hypervisor"`
	Watchdog   bool `json:"watchdog"`
	Critical   bool `json:"critical"`
	// TestedAt is the RFC 3339 time of the last successful fencing test
	// recorded by WebKVM for this cluster, or null.
	TestedAt *string `json:"tested_at"`
	// Mode is watchdog_only | device | none; EvidenceCount is the number of
	// fence_history events exposed (see GET /api/ha/fencing/evidence).
	Mode          string            `json:"mode"`
	EvidenceCount int               `json:"evidence_count"`
	Agents        []string          `json:"agents"`
	PVE           pveFenceReadiness `json:"pve"`
}

type haReadiness struct {
	State         string                  `json:"state"`
	CanEnable     bool                    `json:"can_enable"`
	CheckedAt     time.Time               `json:"checked_at"`
	Pacemaker     ha.PacemakerStatus      `json:"pacemaker"`
	Fencing       fencingHAReadiness      `json:"fencing"`
	Install       ha.LocalInstallStatus   `json:"install"`
	Prerequisites []ha.Prerequisite       `json:"prerequisites"`
	ControlPlane  controlPlaneHAReadiness `json:"control_plane"`
	IncusTargets  []incusHAReadiness      `json:"incus_targets"`
	Blockers      []string                `json:"blockers"`
	Warnings      []string                `json:"warnings"`
	// ExpiredMaintenance lists workloads left unmanaged past their TTL.
	ExpiredMaintenance []string `json:"expired_maintenance,omitempty"`
	// DefaultProfileNIC lists the Incus default-profile NIC problems behind
	// the incus_default_profile_network_not_portable warning (with commands).
	DefaultProfileNIC []ha.Reason `json:"default_profile_nic,omitempty"`
	// DRBD lists the unhealthy DRBD connections seen from this node, each
	// with the repair commands and the node every command must run on. It is
	// additive: drbd_link_degraded stays in Warnings.
	DRBD ha.DRBDHealth `json:"drbd"`
}

// incusProfileChecker is implemented by the Incus adapter.
type incusProfileChecker interface {
	DefaultProfileNICIssues(ctx context.Context) ([]ha.Reason, error)
}

// applyIncusProfileWarning warns when the default profile NIC points at a
// network that does not exist on every Incus member (read-only, cheap).
func (h *Handler) applyIncusProfileWarning(ctx context.Context, result *haReadiness) {
	if h.compute == nil {
		return
	}
	sp, ok := h.compute.(secondaryProvider)
	if !ok {
		return
	}
	checker, ok := sp.Secondary().(incusProfileChecker)
	if !ok {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	issues, err := checker.DefaultProfileNICIssues(cctx)
	if err != nil || len(issues) == 0 {
		return
	}
	result.DefaultProfileNIC = issues
	result.Warnings = addUnique(result.Warnings, ha.WarnDefaultProfileNetworkNotPortable)
}

func buildHAReadiness(pacemaker ha.PacemakerStatus, prerequisites []ha.Prerequisite, registered []nodes.Node, controlPlane controlPlaneHAReadiness, fencing fencingHAReadiness, install ha.LocalInstallStatus, checkedAt time.Time) haReadiness {
	result := haReadiness{
		State:         "blocked",
		CanEnable:     false,
		CheckedAt:     checkedAt.UTC(),
		Pacemaker:     pacemaker,
		Fencing:       fencing,
		Install:       install,
		Prerequisites: prerequisites,
		ControlPlane:  controlPlane,
		IncusTargets:  []incusHAReadiness{},
		Blockers:      append([]string{}, pacemaker.Blockers...),
		Warnings:      []string{},
		DRBD:          ha.DRBDHealth{Healthy: true, Issues: []ha.DRBDIssue{}},
	}
	if fencing.Critical {
		result.Blockers = append(result.Blockers, "pacemaker_no_hypervisor_fencing")
	}
	if install.Critical {
		result.Blockers = append(result.Blockers, "webkvm_local_install_outside_pacemaker")
	}
	if install.DivergentJWT {
		result.Blockers = append(result.Blockers, "webkvm_divergent_jwt_copy")
	}
	if !controlPlane.SharedDataDirVerified {
		result.Blockers = append(result.Blockers, "control_plane_shared_data_not_verified")
	}
	if !controlPlane.VirtualIPConfigured {
		result.Blockers = append(result.Blockers, "control_plane_vip_not_configured")
	}
	if !controlPlane.FencingTested {
		result.Blockers = append(result.Blockers, "fencing_test_required")
	}
	if !controlPlane.SharedStorageVerified {
		result.Blockers = append(result.Blockers, "shared_storage_not_verified")
	}
	for _, prerequisite := range prerequisites {
		if prerequisite.State != ha.PrereqOK {
			result.Blockers = append(result.Blockers, "node_prerequisites_missing")
			break
		}
	}
	for _, node := range registered {
		if node.Driver() != nodes.BackendIncus || node.IsLocal() || !node.Enabled {
			continue
		}
		result.IncusTargets = append(result.IncusTargets, incusHAReadiness{
			NodeID: node.ID, Name: node.Name, Project: node.Project,
			State: "cluster_admin_certificate_required",
		})
		result.Blockers = append(result.Blockers, "incus_ha_certificate_required")
	}
	if len(result.Blockers) == 0 {
		result.State = "ready"
		result.CanEnable = true
	}
	return result
}

// fencingReadiness combines the CIB detection with the optional fence_pve
// configuration. A formed cluster with no hypervisor/hardware agent is
// critical: the SBD watchdog cannot power off a hung hypervisor host.
func (h *Handler) fencingReadiness(ctx context.Context, pacemaker ha.PacemakerStatus) fencingHAReadiness {
	out := fencingHAReadiness{
		Enabled:    pacemaker.FencingEnabled != nil && *pacemaker.FencingEnabled,
		Hypervisor: pacemaker.HypervisorFencing,
		Watchdog:   pacemaker.WatchdogFencing != nil && *pacemaker.WatchdogFencing,
		Agents:     append([]string{}, pacemaker.FenceAgents...),
		PVE:        pveFenceReadiness{Commands: []string{}, Notes: []string{}},
	}
	evidence := h.fencingEvidence(pacemaker)
	out.Mode = evidence.Mode
	out.EvidenceCount = len(evidence.Events)
	if evidence.Tested && evidence.TestedAt != "" {
		ts := evidence.TestedAt
		out.TestedAt = &ts
	}
	if (pacemaker.State == ha.StateObserved || pacemaker.State == ha.StateBlocked) && !out.Hypervisor {
		out.Critical = true
	}
	if h.settings == nil {
		return out
	}
	cfg := ha.FencePVEConfig{
		Enabled:     h.settings.GetBool("ha.fence_pve_enabled"),
		Endpoint:    h.settings.GetString("ha.fence_pve_endpoint"),
		TokenID:     h.settings.GetString("ha.fence_pve_token_id"),
		TokenSecret: h.settings.GetString("ha.fence_pve_token_secret"),
		PVENode:     h.settings.GetString("ha.fence_pve_node"),
		HostMap:     h.settings.GetString("ha.fence_pve_host_map"),
		InsecureTLS: h.settings.GetBool("ha.fence_pve_insecure_tls"),
	}
	out.PVE.Configured = cfg.Enabled && cfg.Endpoint != "" && cfg.TokenID != ""
	if !cfg.Enabled {
		out.PVE.Notes = append(out.PVE.Notes,
			"fence_pve is disabled in Settings; the SBD watchdog is not hypervisor fencing.")
		return out
	}
	out.PVE.AgentInstalled = ha.ProbeFencePVEAgent(ctx, sysutil.DefaultRunner)
	commands, notes, err := ha.FencePVECommands(cfg, pacemaker.NodeNames)
	out.PVE.Notes = append(out.PVE.Notes, notes...)
	if err != nil {
		out.PVE.Notes = append(out.PVE.Notes, "fence_pve config incomplete: "+err.Error())
		return out
	}
	out.PVE.Commands = commands
	return out
}

// VerifyFencePVE checks the configured Proxmox token read-only and reports
// whether it carries VM.PowerMgmt. It never fences, reboots or creates a
// stonith device.
func (h *Handler) VerifyFencePVE(w http.ResponseWriter, r *http.Request) {
	if h.settings == nil {
		jsonErr(w, http.StatusServiceUnavailable, "settings store not available")
		return
	}
	cfg := ha.FencePVEConfig{
		Enabled:     h.settings.GetBool("ha.fence_pve_enabled"),
		Endpoint:    h.settings.GetString("ha.fence_pve_endpoint"),
		TokenID:     h.settings.GetString("ha.fence_pve_token_id"),
		TokenSecret: h.settings.GetString("ha.fence_pve_token_secret"),
		PVENode:     h.settings.GetString("ha.fence_pve_node"),
		HostMap:     h.settings.GetString("ha.fence_pve_host_map"),
		InsecureTLS: h.settings.GetBool("ha.fence_pve_insecure_tls"),
	}
	if !cfg.Enabled {
		jsonErr(w, http.StatusBadRequest, "fence_pve is disabled in Settings")
		return
	}
	check, err := ha.CheckPVEToken(r.Context(), cfg)
	if err != nil {
		// Validation errors name the bad field, never the secret.
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, check)
}

// cpGroup returns the control-plane group id stored in ha.cp_group (default
// webkvm-cp). Handlers built without settings fall back to the default.
func (h *Handler) cpGroup() string {
	if h.settings != nil {
		if v := strings.TrimSpace(h.settings.GetString("ha.cp_group")); v != "" {
			return v
		}
	}
	return ha.DefaultCPGroup
}

func addUnique(list []string, v string) []string {
	for _, x := range list {
		if x == v {
			return list
		}
	}
	return append(list, v)
}

// applyWorkloadReadiness layers the per-workload HA checks on top of the
// base readiness without touching its existing fields: native Incus healing
// must be off, the existing control plane group is verified (adopted), and
// expired maintenance windows are surfaced as warnings (never auto-managed).
func (h *Handler) applyWorkloadReadiness(ctx context.Context, result *haReadiness) {
	if threshold, ok := incusHealingProbe(ctx); ok && threshold > 0 {
		result.Blockers = addUnique(result.Blockers, "incus_healing_enabled")
		result.State, result.CanEnable = "blocked", false
	}
	h.applyIncusProfileWarning(ctx, result)
	st := result.Pacemaker.State
	if st != ha.StateObserved && st != ha.StateBlocked {
		return
	}
	group := h.cpGroup()
	if checks, err := ha.ControlPlaneChecks(ctx, sysutil.DefaultRunner, group, h.dataDir()); err == nil {
		result.ControlPlane.Checks = checks
		for _, c := range checks {
			result.Warnings = addUnique(result.Warnings, "control_plane_"+c.Code)
		}
	}
	if list, err := ha.ManagedWorkloads(ctx, sysutil.DefaultRunner, group); err == nil {
		if expired := ha.ExpiredMaintenance(list, time.Now()); len(expired) > 0 {
			result.Warnings = addUnique(result.Warnings, "workload_unmanaged_expired")
			for _, w := range expired {
				result.ExpiredMaintenance = append(result.ExpiredMaintenance, w.Key)
			}
		}
	}
}

func (h *Handler) dataDir() string {
	if h.cfg == nil {
		return ""
	}
	return h.cfg.DataDir
}

func (h *Handler) controlPlaneReadiness(ctx context.Context, pacemaker ha.PacemakerStatus) controlPlaneHAReadiness {
	controlPlane := controlPlaneHAReadiness{
		State:                 "not_configured",
		SharedDataDirVerified: h.sharedDataDirOnDRBD(ctx),
		VirtualIPConfigured:   pacemaker.ManagedIP,
		FencingTested:         h.fencingEvidence(pacemaker).Tested,
		SharedStorageVerified: h.hasSharedWorkloadStorage(),
		Node:                  ha.ControlPlaneNode(pacemaker, h.cpGroup()),
		Group:                 h.cpGroup(),
	}
	if controlPlane.SharedDataDirVerified && controlPlane.VirtualIPConfigured &&
		controlPlane.FencingTested && controlPlane.SharedStorageVerified {
		controlPlane.State = "active"
	}
	return controlPlane
}

// fencingEvidence combines Pacemaker's fence_history with the secondary
// ha-fencing.json record (device mode only).
func (h *Handler) fencingEvidence(pacemaker ha.PacemakerStatus) ha.FencingEvidence {
	var rec *ha.FencingRecord
	if r, ok := ha.LoadFencingRecord(h.dataDir(), pacemaker.ClusterName); ok {
		rec = &r
	}
	ledger := ha.MergeFencingLedger(h.dataDir(), pacemaker, time.Now())
	return ha.BuildFencingEvidenceWithLedger(pacemaker, rec, ledger)
}

// fencingRecorded reports whether fencing is proven for the current cluster
// (kept for callers that only need the boolean).
func (h *Handler) fencingRecorded(pacemaker ha.PacemakerStatus) bool {
	return h.fencingEvidence(pacemaker).Tested
}

func (h *Handler) sharedDataDirOnDRBD(ctx context.Context) bool {
	if h.cfg == nil || h.cfg.DataDir == "" {
		return false
	}
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := sysutil.DefaultRunner.Run(commandCtx, "findmnt", "-n", "-o", "SOURCE,FSTYPE", "--target", h.cfg.DataDir)
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "/dev/drbd")
}

func (h *Handler) hasSharedWorkloadStorage() bool {
	if h.compute == nil {
		return false
	}
	pools, err := h.compute.ListStoragePools()
	if err != nil {
		return false
	}
	for _, pool := range pools {
		switch strings.ToLower(pool.Type) {
		case "linstor", "ceph":
			return true
		}
	}
	return false
}

// GetHAReadiness performs read-only local Pacemaker probes and lists the
// additional checks still needed before any HA activation can be offered.
// It never creates resources, changes Incus healing settings, or fences nodes.
func (h *Handler) GetHAReadiness(w http.ResponseWriter, r *http.Request) {
	pacemaker := ha.ProbePacemaker(r.Context(), sysutil.DefaultRunner)
	prerequisites := ha.ProbePrerequisites(r.Context(), sysutil.DefaultRunner)
	var registered []nodes.Node
	if h.nodes != nil {
		registered = h.nodes.List()
	}
	controlPlane := h.controlPlaneReadiness(r.Context(), pacemaker)
	fencing := h.fencingReadiness(r.Context(), pacemaker)
	install := ha.ProbeLocalInstall(r.Context(), sysutil.DefaultRunner, h.dataDir(), pacemaker, h.cpGroup())
	result := buildHAReadiness(pacemaker, prerequisites, registered, controlPlane, fencing, install, time.Now())
	h.applyWorkloadReadiness(r.Context(), &result)
	if h.cfg != nil {
		result.Warnings = append(result.Warnings, ha.HealthWarnings(r.Context(), sysutil.DefaultRunner, h.cfg.DataDir)...)
		result.DRBD = ha.ProbeDRBD(r.Context(), sysutil.DefaultRunner, localNodeName())
		if !result.DRBD.Healthy {
			result.Warnings = addUnique(result.Warnings, "drbd_link_degraded")
		}
		if h.clusterFW != nil {
			local := firewall.LoadSyncState(firewall.SyncStatePath(h.clusterFW.ProfileFilePath()))
			peers := firewall.LoadPeerStates(firewall.PeerStatesPath(h.cfg.DataDir))
			if firewall.DivergentHashes(local, peers) {
				result.Warnings = append(result.Warnings, "firewall_profile_divergent")
			}
		}
	}
	jsonResp(w, http.StatusOK, result)
}
