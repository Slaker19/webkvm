package api

import (
	"net/http"
	"testing"
)

// stateInspector is a fakeInspector that also knows the live instance state.
type stateInspector struct {
	fakeInspector
	status, location string
}

func (s stateInspector) InstanceState(string) (string, string) { return s.status, s.location }

func TestUnmanagedIncusWorkloadReportsLiveStateAndNode(t *testing.T) {
	insp := stateInspector{
		fakeInspector: fakeInspector{driver: "linstor", online: map[string]bool{"ha1": true, "ha2": true}},
		status:        "Running", location: "ha2",
	}
	h := setupWL(t, &wlRunner{watchdog: true, hasCP: true}, fakeFacts{insp: insp, members: []string{"ha1", "ha2"}}, true)
	code, body := doWL(h, http.MethodGet, "/workloads/incus/c9?project=default", "")
	if code != http.StatusOK {
		t.Fatalf("get: %d %v", code, body)
	}
	if body["managed"] != false || body["state"] != "started" || body["node"] != "ha2" {
		t.Fatalf("unmanaged running instance must show started on ha2: %v", body)
	}
}

func TestUnmanagedIncusStoppedAndWithoutStateFacts(t *testing.T) {
	stopped := stateInspector{fakeInspector: fakeInspector{driver: "linstor"}, status: "Stopped", location: "ha1"}
	h := setupWL(t, &wlRunner{watchdog: true, hasCP: true}, fakeFacts{insp: stopped, members: []string{"ha1"}}, true)
	_, body := doWL(h, http.MethodGet, "/workloads/incus/c9?project=default", "")
	if body["state"] != "stopped" {
		t.Fatalf("stopped instance: %v", body)
	}
	// An inspector without state facts keeps the old behaviour.
	h = setupWL(t, &wlRunner{watchdog: true, hasCP: true}, fakeFacts{insp: fakeInspector{driver: "linstor"}, members: []string{"ha1"}}, true)
	_, body = doWL(h, http.MethodGet, "/workloads/incus/c9?project=default", "")
	if body["state"] != "stopped" || body["node"] != "" {
		t.Fatalf("no facts: %v", body)
	}
}

func TestUnmanagedKVMWorkloadReportsRunningOnLocalNode(t *testing.T) {
	f := goodFacts()
	f.vm.Running = true
	h := setupWL(t, &wlRunner{watchdog: true, hasCP: true}, f, true)
	_, body := doWL(h, http.MethodGet, "/workloads/kvm/web", "")
	if body["state"] != "started" || body["node"] != localNodeName() {
		t.Fatalf("kvm running: %v", body)
	}
}
