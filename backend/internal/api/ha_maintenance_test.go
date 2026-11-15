package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestHAMaintenanceBeginEndAndRefusals(t *testing.T) {
	r := &wlRunner{watchdog: true, hasCP: true, vm: true, started: true}
	h := setupWL(t, r, goodFacts(), true)
	code, body := doWL(h, http.MethodPost, "/workloads/kvm/web/maintenance", `{"action":"begin","ttl_s":600}`)
	if code != 200 || body["changed"] != true || body["until"] == "" || !r.has("pcs resource unmanage ha-kvm-web-grp") {
		t.Fatalf("%d %v %v", code, body, r.cmds)
	}
	// While unmanaged, risky operations are refused with maintenance_active.
	for _, tc := range []struct{ m, p, b string }{
		{http.MethodPost, "/workloads/kvm/web/move", `{"node":"ha2"}`},
		{http.MethodDelete, "/workloads/kvm/web?mode=stop", ``},
		{http.MethodPut, "/workloads/kvm/web/placement", `{"allowed_nodes":["ha1"]}`},
	} {
		c, b := doWL(h, tc.m, tc.p, tc.b)
		if c != http.StatusConflict || b["code"] != "maintenance_active" {
			t.Errorf("%s %s: %d %v", tc.m, tc.p, c, b)
		}
	}
	// A second begin is idempotent.
	if code, body = doWL(h, http.MethodPost, "/workloads/kvm/web/maintenance", `{"action":"begin","ttl_s":600}`); code != 200 || body["changed"] != false {
		t.Fatalf("%d %v", code, body)
	}
	if code, body = doWL(h, http.MethodPost, "/workloads/kvm/web/maintenance", `{"action":"end"}`); code != 200 || body["state"] != "started" || !r.has("pcs resource manage ha-kvm-web-grp") {
		t.Fatalf("%d %v", code, body)
	}
	if code, _ = doWL(h, http.MethodPost, "/workloads/kvm/web/maintenance", `{"action":"begin","ttl_s":5}`); code != 400 {
		t.Fatalf("ttl below minimum accepted: %d", code)
	}
	if code, body = doWL(h, http.MethodPost, "/workloads/kvm/ghost/maintenance", `{"action":"begin","ttl_s":600}`); code != 404 {
		t.Fatalf("%d %v", code, body)
	}
	if strings.Contains(strings.Join(r.cmds, "\n"), "defineXML") {
		t.Fatal("never defineXML")
	}
}
