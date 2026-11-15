package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"webkvm/internal/firewall"
)

// clusterFirewallRequest is the JSON body shared by preview/save/apply.
// The profile is applied locally by the node that serves this request;
// other members must receive the same file before they apply it.
type clusterFirewallRequest struct {
	Profile firewall.ClusterProfile `json:"profile"`
}

func (h *Handler) clusterFWReady() bool { return h.clusterFW != nil }

func (h *Handler) clusterPeerStatesPath() string {
	if h.cfg == nil || h.cfg.DataDir == "" {
		return ""
	}
	return firewall.PeerStatesPath(h.cfg.DataDir)
}

// distributeClusterProfile pushes the confirmed file to the configured
// peers. It is deliberately best-effort per peer: the local confirm already
// succeeded, and a dead peer must be visible in the UI instead of rolling
// back the active node.
func (h *Handler) distributeClusterProfile(r *http.Request) []firewall.PeerSyncResult {
	if h.clusterFW == nil || h.settings == nil {
		return nil
	}
	peers := h.settings.GetList("ha.firewall_peers")
	if len(peers) == 0 {
		return nil
	}
	payload, err := h.clusterFW.Snapshot()
	if err != nil {
		results := make([]firewall.PeerSyncResult, 0, len(peers))
		for _, peer := range peers {
			results = append(results, firewall.PeerSyncResult{Peer: peer, Error: "could not read the confirmed profile: " + err.Error(), UpdatedAt: time.Now().UTC()})
		}
		return results
	}
	clusterCIDR := strings.TrimSpace(h.settings.GetString("ha.cluster_cidr"))
	if clusterCIDR == "" {
		if profile, _, _, _ := h.clusterFW.Status(); profile.ClusterCIDR != "" {
			clusterCIDR = profile.ClusterCIDR
		}
	}
	if clusterCIDR == "" {
		results := make([]firewall.PeerSyncResult, 0, len(peers))
		for _, peer := range peers {
			results = append(results, firewall.PeerSyncResult{
				Peer: peer, Error: "cluster network is not configured (ha.cluster_cidr); refusing to push", UpdatedAt: time.Now().UTC(),
			})
		}
		return results
	}
	cfg := firewall.SSHConfig{
		User:        h.settings.GetString("ha.firewall_ssh_user"),
		KeyPath:     h.settings.GetString("ha.firewall_ssh_key"),
		KnownHosts:  h.settings.GetString("ha.firewall_known_hosts"),
		ClusterCIDR: clusterCIDR,
	}
	// The push must finish even if the admin closes the browser after the
	// confirm; the local Safe-Apply already succeeded.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	results := firewall.DistributeProfile(ctx, payload, peers, clusterCIDR, firewall.SSHRunner(cfg))
	if path := h.clusterPeerStatesPath(); path != "" {
		if serr := firewall.SavePeerStates(path, results); serr != nil && h.audit != nil {
			h.audit.Log(auditFor(r, "firewall.cluster.peers_state", "webkvm", map[string]any{"error": serr.Error()}))
		}
	}
	if h.audit != nil {
		for _, result := range results {
			h.audit.Log(auditFor(r, "firewall.cluster.distribute", result.Peer, map[string]any{
				"ok": result.Error == "", "version": result.Version,
				"sha256": result.SHA256, "error": result.Error,
			}))
		}
	}
	return results
}

func (h *Handler) decodeClusterProfile(w http.ResponseWriter, r *http.Request) (firewall.ClusterProfile, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req clusterFirewallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return firewall.ClusterProfile{}, false
	}
	return h.applyClusterSettings(req.Profile), true
}

// applyClusterSettings fills the network fields from configstore when the
// request/profile leaves them empty. No lab address is ever a default.
func (h *Handler) applyClusterSettings(p firewall.ClusterProfile) firewall.ClusterProfile {
	if h.settings == nil {
		return p
	}
	if strings.TrimSpace(p.ClusterCIDR) == "" {
		p.ClusterCIDR = strings.TrimSpace(h.settings.GetString("ha.cluster_cidr"))
	}
	if len(p.ManagementCIDRs) == 0 {
		p.ManagementCIDRs = h.settings.GetList("ha.management_cidrs")
	}
	return p
}

// GetClusterFirewall returns the profile, the rendered ruleset and the
// Safe-Apply state. Read-only: nothing is applied by this handler.
func (h *Handler) GetClusterFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.clusterFWReady() {
		jsonErr(w, http.StatusServiceUnavailable, "cluster firewall subsystem not initialized")
		return
	}
	profile, deadline, pending, hash := h.clusterFW.Status()
	profile = h.applyClusterSettings(profile)
	rendered, err := h.clusterFW.Preview(profile)
	configError := ""
	if err != nil {
		// First boot has no networks configured yet. Returning the profile
		// and the reason is more useful than a 500.
		rendered = ""
		configError = err.Error()
	}
	version, profileHash := h.clusterFW.FileMeta()
	resp := map[string]any{
		"profile":        profile,
		"rendered":       rendered,
		"pending":        pending,
		"confirmed_hash": hash,
		"table_present":  h.clusterFW.TableApplied(),
		"version":        version,
		"sha256":         profileHash,
		"sync":           firewall.LoadSyncState(firewall.SyncStatePath(h.clusterFW.ProfileFilePath())),
		"peers":          firewall.LoadPeerStates(h.clusterPeerStatesPath()),
		"config_error":   configError,
	}
	if pending {
		resp["deadline"] = deadline.Unix()
	}
	jsonResp(w, http.StatusOK, resp)
}

// PreviewClusterFirewall renders the exact nftables file for a profile
// without persisting or applying it.
func (h *Handler) PreviewClusterFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.clusterFWReady() {
		jsonErr(w, http.StatusServiceUnavailable, "cluster firewall subsystem not initialized")
		return
	}
	profile, ok := h.decodeClusterProfile(w, r)
	if !ok {
		return
	}
	rendered, err := h.clusterFW.Preview(profile)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"rendered": rendered})
}

// SaveClusterFirewall persists the profile without applying it. The admin
// can enable it here and apply it in a later step.
func (h *Handler) SaveClusterFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.clusterFWReady() {
		jsonErr(w, http.StatusServiceUnavailable, "cluster firewall subsystem not initialized")
		return
	}
	profile, ok := h.decodeClusterProfile(w, r)
	if !ok {
		return
	}
	if err := h.clusterFW.SaveProfile(profile); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "firewall.cluster.profile", "webkvm", map[string]any{
			"enabled": profile.Enabled, "cluster_cidr": profile.ClusterCIDR,
			"management_cidrs": len(profile.ManagementCIDRs), "bridges": len(profile.Bridges),
		}))
	}
	jsonResp(w, http.StatusOK, map[string]any{"profile": h.clusterFW.Profile()})
}

// ApplyClusterFirewall starts the Safe-Apply window: the ruleset is loaded
// and automatically rolled back unless Confirm arrives in time.
func (h *Handler) ApplyClusterFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.clusterFWReady() {
		jsonErr(w, http.StatusServiceUnavailable, "cluster firewall subsystem not initialized")
		return
	}
	profile, ok := h.decodeClusterProfile(w, r)
	if !ok {
		return
	}
	deadline, err := h.clusterFW.Stage(profile)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "firewall.cluster.apply", "webkvm", map[string]any{
			"cluster_cidr": profile.ClusterCIDR, "management_cidrs": len(profile.ManagementCIDRs),
			"deadline": deadline.Unix(),
		}))
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"status":      "pending_confirm",
		"deadline":    deadline.Unix(),
		"window_secs": int(time.Until(deadline).Seconds()),
	})
}

// ConfirmClusterFirewall makes the staged ruleset the new baseline.
func (h *Handler) ConfirmClusterFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.clusterFWReady() {
		jsonErr(w, http.StatusServiceUnavailable, "cluster firewall subsystem not initialized")
		return
	}
	if err := h.clusterFW.Confirm(); err != nil {
		jsonErr(w, http.StatusBadRequest, err.Error())
		return
	}
	profile := h.clusterFW.Profile()
	version, profileHash := h.clusterFW.FileMeta()
	if h.audit != nil {
		h.audit.Log(auditFor(r, "firewall.cluster.confirm", "webkvm", map[string]any{
			"enabled": profile.Enabled, "cluster_cidr": profile.ClusterCIDR,
			"version": version, "sha256": profileHash,
		}))
	}
	// Only after the local Safe-Apply is confirmed do peers receive the
	// profile. A failure on one peer is reported per node and does not
	// revert the others.
	peers := h.distributeClusterProfile(r)
	jsonResp(w, http.StatusOK, map[string]any{
		"status": "confirmed", "profile": profile,
		"version": version, "sha256": profileHash, "peers": peers,
		"sync": firewall.LoadSyncState(firewall.SyncStatePath(h.clusterFW.ProfileFilePath())),
	})
}

// RollbackClusterFirewall restores the previous ruleset.
func (h *Handler) RollbackClusterFirewall(w http.ResponseWriter, r *http.Request) {
	if !h.clusterFWReady() {
		jsonErr(w, http.StatusServiceUnavailable, "cluster firewall subsystem not initialized")
		return
	}
	rolled, err := h.clusterFW.Rollback()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.audit != nil {
		h.audit.Log(auditFor(r, "firewall.cluster.rollback", "webkvm", map[string]any{
			"rolled_back": rolled,
		}))
	}
	status := "no_pending"
	if rolled {
		status = "rolled_back"
	}
	jsonResp(w, http.StatusOK, map[string]any{"status": status})
}
