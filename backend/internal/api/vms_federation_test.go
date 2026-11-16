package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"webkvm/internal/models"
	"webkvm/internal/nodes"
)

func stubFederation(t *testing.T, nodeTimeout, deadline time.Duration, fn func(ctx context.Context, uri string) ([]models.VM, error)) {
	t.Helper()
	pn, pd, pf, pb := federationNodeTimeout, federationDeadline, listRemoteDomains, federationBackoff
	federationNodeTimeout, federationDeadline, listRemoteDomains = nodeTimeout, deadline, fn
	federationBackoff = 50 * time.Millisecond
	federationFailMu.Lock()
	federationFailed = map[string]time.Time{}
	federationFailMu.Unlock()
	t.Cleanup(func() {
		federationNodeTimeout, federationDeadline, listRemoteDomains, federationBackoff = pn, pd, pf, pb
	})
}

func twoNodeHandler(t *testing.T) (*Handler, nodes.Node, nodes.Node) {
	t.Helper()
	reg, err := nodes.New(t.TempDir(), "qemu:///system")
	if err != nil {
		t.Fatal(err)
	}
	slow, _ := reg.Create("slow", "qemu+ssh://u@slow.example/system")
	fast, _ := reg.Create("fast", "qemu+ssh://u@fast.example/system")
	return &Handler{nodes: reg}, slow, fast
}

// A node whose libvirt never answers must not block the VM list: the other
// nodes' VMs come back, the slow node is skipped and marked unreachable.
func TestFederateVMsReturnsPartialResultsWhenNodeHangs(t *testing.T) {
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	stubFederation(t, time.Minute, 300*time.Millisecond, func(ctx context.Context, uri string) ([]models.VM, error) {
		if uri == "qemu+ssh://u@slow.example/system" {
			<-block // ignores ctx on purpose: only the global deadline can free the caller
			return nil, nil
		}
		return []models.VM{{ID: "f1", Name: "fast-vm"}}, nil
	})
	h, slow, _ := twoNodeHandler(t)
	start := time.Now()
	vms := h.federateVMsContext(context.Background(), []models.VM{{ID: "l1", Name: "local-vm"}}, models.RoleAdmin)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("federation blocked for %v", d)
	}
	if len(vms) != 2 || vms[1].Name != "fast-vm" || vms[1].Node != "fast" {
		t.Fatalf("vms = %+v", vms)
	}
	got, _ := h.nodes.Get(slow.ID)
	if got.Status != "offline" || got.LastError == "" {
		t.Fatalf("slow node not marked unreachable: %+v", got)
	}
}

func TestFederateVMsQueriesNodesInParallel(t *testing.T) {
	// Deterministic concurrency check: each remote call waits until both
	// calls have started. A serial implementation deadlocks the first call
	// until the timeout, so the test fails without relying on wall-clock
	// scheduling (which was flaky on loaded CI runners).
	var started int32
	bothStarted := make(chan struct{})
	stubFederation(t, 2*time.Second, 5*time.Second, func(ctx context.Context, uri string) ([]models.VM, error) {
		if atomic.AddInt32(&started, 1) == 2 {
			close(bothStarted)
		}
		select {
		case <-bothStarted:
			return []models.VM{{ID: uri, Name: uri}}, nil
		case <-time.After(2 * time.Second):
			return nil, context.DeadlineExceeded
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	h, _, _ := twoNodeHandler(t)
	vms := h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	if len(vms) != 2 {
		t.Fatalf("vms = %+v", vms)
	}
	if got := atomic.LoadInt32(&started); got != 2 {
		t.Fatalf("remote calls started = %d, want 2", got)
	}
}

func TestFederateVMsPerNodeTimeoutAndBackoff(t *testing.T) {
	calls := 0
	stubFederation(t, 100*time.Millisecond, 5*time.Second, func(ctx context.Context, uri string) ([]models.VM, error) {
		calls++
		<-ctx.Done()
		return nil, ctx.Err()
	})
	reg, _ := nodes.New(t.TempDir(), "qemu:///system")
	reg.Create("only", "qemu+ssh://u@only.example/system")
	h := &Handler{nodes: reg}
	h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	h.federateVMsContext(context.Background(), nil, models.RoleAdmin) // inside backoff: skipped
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (second request must skip the failed node)", calls)
	}
}

func TestFederateVMsSkipsURIsWithOptions(t *testing.T) {
	called := false
	stubFederation(t, time.Second, time.Second, func(context.Context, string) ([]models.VM, error) {
		called = true
		return nil, nil
	})
	reg, _ := nodes.New(t.TempDir(), "qemu:///system")
	n, _ := reg.Create("legacy", "qemu+ssh://u@h.example/system")
	_, _ = reg.Update(n.ID, "", "qemu+ssh://u@h.example/system?command=/bin/evil", nil)
	(&Handler{nodes: reg}).federateVMsContext(context.Background(), nil, models.RoleAdmin)
	if called {
		t.Fatal("a URI with options reached libvirt")
	}
}

// A degraded local libvirt must not hide the remote nodes: the listing
// still answers 200 with the remote VMs when at least one remote node exists.
func TestListVMsLocalFailureStillFederatesRemote(t *testing.T) {
	stubFederation(t, time.Second, 2*time.Second, func(ctx context.Context, uri string) ([]models.VM, error) {
		return []models.VM{{ID: "r1", Name: "remote-vm"}}, nil
	})
	reg, _ := nodes.New(t.TempDir(), "qemu:///system")
	reg.Create("fast", "qemu+ssh://u@fast.example/system")
	h := &Handler{compute: &fakeVMBackend{err: errListDomains}, nodes: reg}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/vms", nil)
	req.Header.Set("X-User", "admin")
	req.Header.Set("X-Role", "admin")
	h.ListVMs(rr, req)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "remote-vm") {
		t.Fatalf("code=%d body=%s", rr.Code, rr.Body.String())
	}
}

// With no remote node a failing local libvirt is still a hard 500.
func TestListVMsLocalFailureWithoutRemotesIs500(t *testing.T) {
	reg, _ := nodes.New(t.TempDir(), "qemu:///system")
	h := &Handler{compute: &fakeVMBackend{err: errListDomains}, nodes: reg}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/vms", nil)
	req.Header.Set("X-Role", "admin")
	h.ListVMs(rr, req)
	if rr.Code != 500 {
		t.Fatalf("code=%d", rr.Code)
	}
}

// An empty listing is the JSON array [], never null.
func TestListVMsEmptyIsArrayNotNull(t *testing.T) {
	reg, _ := nodes.New(t.TempDir(), "qemu:///system")
	h := &Handler{compute: &fakeVMBackend{}, nodes: reg}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/vms", nil)
	req.Header.Set("X-Role", "admin")
	h.ListVMs(rr, req)
	if rr.Code != 200 || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("code=%d body=%q", rr.Code, rr.Body.String())
	}
}

// 8.29 #3: two chained federated listings (~6 s each) after a create. A node
// listed less than federationFreshTTL ago is answered from that listing...
func TestFederateVMsReusesAFreshListing(t *testing.T) {
	var calls int
	var mu sync.Mutex
	stubFederation(t, time.Second, time.Second, func(ctx context.Context, uri string) ([]models.VM, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return []models.VM{{ID: "r1", Name: "remote-vm"}}, nil
	})
	h, _, _ := twoNodeHandler(t)
	first := h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	second := h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	if calls != 2 {
		t.Fatalf("remote listings = %d, want 2 (one per node, second call cached)", calls)
	}
	if len(first) != 2 || len(second) != 2 || second[0].Stale || second[0].Node == "" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

// ...but never after a change routed through this server, nor once it aged.
func TestFederateVMsFreshListingDroppedOnChangeAndAge(t *testing.T) {
	var calls int
	var mu sync.Mutex
	stubFederation(t, time.Second, time.Second, func(ctx context.Context, uri string) ([]models.VM, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return []models.VM{{ID: "r1", Name: "remote-vm"}}, nil
	})
	h, slow, _ := twoNodeHandler(t)
	h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	invalidateFreshListings(slow.ID)
	h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	if calls != 3 {
		t.Fatalf("after invalidating one node: listings = %d, want 3", calls)
	}
	invalidateFreshListings()
	h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	if calls != 5 {
		t.Fatalf("after invalidating all: listings = %d, want 5", calls)
	}
	prev := federationFreshTTL
	federationFreshTTL = 0
	t.Cleanup(func() { federationFreshTTL = prev })
	h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	if calls != 7 {
		t.Fatalf("with TTL 0: listings = %d, want 7", calls)
	}
}

// A mutating request routed to a node drops that node's shortcut.
func TestNodeDispatchMutationInvalidatesFreshListing(t *testing.T) {
	stubFederation(t, time.Second, time.Second, func(ctx context.Context, uri string) ([]models.VM, error) {
		return []models.VM{{ID: "r1"}}, nil
	})
	h, slow, _ := twoNodeHandler(t)
	h.federateVMsContext(context.Background(), nil, models.RoleAdmin)
	if _, ok := freshFor(slow.ID); !ok {
		t.Fatal("listing not cached")
	}
	r := httptest.NewRequest("POST", "/api/vms/r1/start?node=slow&hv=kvm", nil)
	r.Header.Set("X-Role", "admin")
	h.nodeDispatch(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(httptest.NewRecorder(), r)
	if _, ok := freshFor(slow.ID); ok {
		t.Fatal("a POST routed to the node kept its fresh listing")
	}
}

// ?scope=local answers with this server's instances only, without asking
// any remote node.
func TestListVMsScopeLocalSkipsFederation(t *testing.T) {
	stubFederation(t, time.Second, time.Second, func(ctx context.Context, uri string) ([]models.VM, error) {
		t.Errorf("remote node %s was listed for scope=local", uri)
		return nil, nil
	})
	h, _, _ := twoNodeHandler(t)
	h.compute = &fakeVMBackend{vms: []models.VM{{ID: "l1", Name: "local-vm"}}}
	r := httptest.NewRequest("GET", "/api/vms?scope=local", nil)
	r.Header.Set("X-Role", "admin")
	w := httptest.NewRecorder()
	h.ListVMs(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "local-vm") {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
}
