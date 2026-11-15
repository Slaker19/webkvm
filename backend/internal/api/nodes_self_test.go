package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

// selfEntry registers a remote libvirt entry that points at this machine
// (loopback on the default SSH port is always this host).
func selfEntry(t *testing.T, reg *nodes.Registry) nodes.Node {
	t.Helper()
	n, err := reg.Create("dup-self", "qemu+ssh://u@127.0.0.1/system")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reg.Get(n.ID)
	if !got.IsSelf {
		t.Fatal("fixture: loopback entry must be is_self")
	}
	return got
}

func TestNodeForTargetResolvesSelfEntryToLocal(t *testing.T) {
	f := newDispatchFixture(t)
	dup := selfEntry(t, f.h.nodes)
	for _, sel := range []string{dup.ID, "dup-self"} {
		n, ok := f.h.nodeForTarget(sel)
		if !ok || !n.IsLocal() {
			t.Fatalf("%s: resolved to %+v, want the local node", sel, n)
		}
	}
	// A genuine remote stays remote.
	if n, ok := f.h.nodeForTarget(f.remote.ID); !ok || n.IsLocal() {
		t.Fatalf("remote resolved to %+v", n)
	}
}

// ?node=<self entry> must be served by the local handlers: no SSH, no pool.
func TestNodeDispatchSelfEntryIsLocal(t *testing.T) {
	f := newDispatchFixture(t)
	dup := selfEntry(t, f.h.nodes)
	*f.fail = errors.New("must not connect over SSH to ourselves")
	for _, sel := range []string{dup.ID, "dup-self"} {
		*f.nextOK = false
		res := f.do(http.MethodGet, "/api/vms/vm1/snapshots?node="+sel, models.RoleAdmin)
		if res.Code != http.StatusOK || !*f.nextOK {
			t.Fatalf("%s: %d %s (not served locally)", sel, res.Code, res.Body.String())
		}
	}
}

func TestOperatorForSelfEntryIsLocalCompute(t *testing.T) {
	f := newDispatchFixture(t)
	dup := selfEntry(t, f.h.nodes)
	op, release, err := f.h.operatorFor(dup.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if op != f.h.compute {
		t.Fatal("self entry must use the local compute backend")
	}
}

func TestFederateVMsSkipsSelfEntry(t *testing.T) {
	var called []string
	stubFederation(t, time.Second, 2*time.Second, func(_ context.Context, uri string) ([]models.VM, error) {
		called = append(called, uri)
		return []models.VM{{ID: "x", Name: "dup"}}, nil
	})
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	selfEntry(t, reg)
	h := &Handler{nodes: reg}
	vms := h.federateVMsContext(context.Background(), []models.VM{{ID: "l1", Name: "local-vm"}}, models.RoleAdmin)
	if len(vms) != 1 || len(called) != 0 {
		t.Fatalf("self entry federated: vms=%+v calls=%v", vms, called)
	}
}

// A listing that fails only because SSH access is not set up must not mark
// the node offline: it records access not_set_up and keeps the status.
func TestFederateVMsNoKeyMarksAccessNotOffline(t *testing.T) {
	stubFederation(t, time.Second, 2*time.Second, func(_ context.Context, uri string) ([]models.VM, error) {
		return nil, fmt.Errorf("connect: %w", nodes.ErrNoKey)
	})
	h, slow, _ := twoNodeHandler(t)
	_ = h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	got, _ := h.nodes.Get(slow.ID)
	if got.Status == "offline" || got.Access != nodes.AccessNotSetUp || got.AccessCode != "no_key" {
		t.Fatalf("got status=%q access=%q code=%q", got.Status, got.Access, got.AccessCode)
	}
}

// /api/cluster/ssh carries is_self (the UI must not offer to set up SSH to
// this same server) and the node-list probe's access state, so every screen
// derives "access works" from the same source.
func TestClusterSSHOverviewCarriesIsSelfAndProbeAccess(t *testing.T) {
	f := newSSHAPIFixture(t)
	dup := selfEntry(t, f.reg)
	f.reg.MarkAccessNotSetUp(f.node.ID, "host_key_unpinned")
	m := bodyJSON(t, f.admin(http.MethodGet, "/api/cluster/ssh", ""))
	rows := map[string]map[string]any{}
	for _, x := range m["nodes"].([]any) {
		row := x.(map[string]any)
		rows[row["id"].(string)] = row
	}
	if rows[dup.ID]["is_self"] != true {
		t.Fatalf("self row = %v", rows[dup.ID])
	}
	lab := rows[f.node.ID]
	if _, has := lab["is_self"]; has {
		t.Fatalf("is_self must be absent on a real remote: %v", lab)
	}
	if lab["access"] != nodes.AccessNotSetUp || lab["access_code"] != "host_key_unpinned" {
		t.Fatalf("probe access not carried: %v", lab)
	}
}

// GET /api/vms/{id}?node=<self entry id> is answered locally and must look
// local (local node name, Pacemaker badge), not carry the entry id as node.
func TestIsLocalTargetResolvesSelfEntry(t *testing.T) {
	f := newDispatchFixture(t)
	dup := selfEntry(t, f.h.nodes)
	for _, sel := range []string{"", "local", dup.ID, "dup-self"} {
		if !f.h.isLocalTarget(sel) {
			t.Fatalf("%q must be local", sel)
		}
	}
	if f.h.isLocalTarget(f.remote.ID) || f.h.isLocalTarget("remote") {
		t.Fatal("a real remote is not local")
	}
}
