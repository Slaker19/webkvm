package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"webkvm/internal/configstore"
	"webkvm/internal/firewall"
)

func apiClusterProfile() firewall.ClusterProfile {
	p := firewall.DefaultClusterProfile()
	p.ClusterCIDR = "10.20.30.0/24"
	p.ManagementCIDRs = []string{"10.20.30.0/24", "203.0.113.0/24"}
	return p
}

func newClusterFWHandler(t *testing.T, applied *[]string) (*Handler, *firewall.ClusterManager) {
	t.Helper()
	m, err := firewall.NewClusterManager(filepath.Join(t.TempDir(), "cluster-firewall.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	m.SetSafeHooks(func(s string) error { *applied = append(*applied, s); return nil }, func() error { return nil }, func() string { return "" })
	settings, err := configstore.New(t.TempDir(), configstore.DefaultSchema())
	if err != nil {
		t.Fatal(err)
	}
	if _, failed, err := settings.SetMany(map[string]any{
		"ha.cluster_cidr":     "10.20.30.0/24",
		"ha.management_cidrs": []string{"10.20.30.0/24", "203.0.113.0/24"},
	}); err != nil || len(failed) > 0 {
		t.Fatalf("settings: %v %v", err, failed)
	}
	return &Handler{clusterFW: m, settings: settings}, m
}

func clusterPOST(handler http.HandlerFunc, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/firewall/cluster/apply", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestClusterFirewallGetAndPreview(t *testing.T) {
	h, _ := newClusterFWHandler(t, &[]string{})
	rec := httptest.NewRecorder()
	h.GetClusterFirewall(rec, httptest.NewRequest(http.MethodGet, "/api/firewall/cluster", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["rendered"] == "" || !strings.Contains(got["rendered"].(string), "table inet webkvm_cluster") {
		t.Fatalf("rendered ruleset missing: %v", got["rendered"])
	}

	profile := apiClusterProfile()
	profile.Enabled = true
	body, _ := json.Marshal(map[string]any{"profile": profile})
	rec = clusterPOST(h.PreviewClusterFirewall, string(body))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "policy drop") {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body.String())
	}

	profile.ClusterCIDR = "nope"
	body, _ = json.Marshal(map[string]any{"profile": profile})
	rec = clusterPOST(h.PreviewClusterFirewall, string(body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid profile must be rejected, got %d", rec.Code)
	}
}

func TestClusterFirewallSaveApplyConfirmRollback(t *testing.T) {
	applied := []string{}
	h, m := newClusterFWHandler(t, &applied)

	// Disabled: save works, apply is refused before touching nft.
	disabled := apiClusterProfile()
	body, _ := json.Marshal(map[string]any{"profile": disabled})
	if rec := clusterPOST(h.SaveClusterFirewall, string(body)); rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	if rec := clusterPOST(h.ApplyClusterFirewall, string(body)); rec.Code != http.StatusBadRequest {
		t.Fatalf("disabled apply = %d, want 400", rec.Code)
	}
	if len(applied) != 0 {
		t.Fatal("a disabled profile must not reach nft")
	}

	enabled := apiClusterProfile()
	enabled.Enabled = true
	body, _ = json.Marshal(map[string]any{"profile": enabled})
	if rec := clusterPOST(h.ApplyClusterFirewall, string(body)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "pending_confirm") {
		t.Fatalf("apply = %d: %s", rec.Code, rec.Body.String())
	}
	if len(applied) != 1 {
		t.Fatalf("nft apply calls = %d, want 1", len(applied))
	}

	rec := clusterPOST(h.ConfirmClusterFirewall, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "confirmed") {
		t.Fatalf("confirm = %d: %s", rec.Code, rec.Body.String())
	}
	if !m.Profile().Enabled {
		t.Fatal("confirmed profile must persist enabled=true")
	}

	// Stage again and roll back: the previous ruleset is restored.
	if rec := clusterPOST(h.ApplyClusterFirewall, string(body)); rec.Code != http.StatusOK {
		t.Fatalf("second apply = %d: %s", rec.Code, rec.Body.String())
	}
	rec = clusterPOST(h.RollbackClusterFirewall, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "rolled_back") {
		t.Fatalf("rollback = %d: %s", rec.Code, rec.Body.String())
	}
	rec = clusterPOST(h.RollbackClusterFirewall, "")
	if !strings.Contains(rec.Body.String(), "no_pending") {
		t.Fatalf("second rollback = %s", rec.Body.String())
	}
}
