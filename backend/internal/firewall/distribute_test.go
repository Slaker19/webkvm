package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testClusterCIDR = "10.20.30.0/24"

func TestValidPeerIP(t *testing.T) {
	for _, ip := range []string{"10.20.30.10", "10.20.30.12", "10.20.30.255"} {
		if !ValidPeerIP(ip, testClusterCIDR) {
			t.Fatalf("%s must be accepted", ip)
		}
	}
	for _, ip := range []string{"203.0.113.10", "10.20.31.10", "198.51.100.999", ""} {
		if ValidPeerIP(ip, testClusterCIDR) {
			t.Fatalf("%q must be rejected", ip)
		}
	}
	if ValidPeerIP("10.20.30.10", "") {
		t.Fatal("an empty cluster CIDR must reject every peer")
	}
}

func TestDistributePeerDownDoesNotStopOthers(t *testing.T) {
	state := SyncState{Version: 7, SHA256: "abc", AppliedAt: time.Now().UTC()}
	okOut, _ := json.Marshal(state)
	run := func(_ context.Context, peer string, payload []byte) ([]byte, error) {
		if len(payload) == 0 {
			t.Fatal("payload must not be empty")
		}
		if peer == "10.20.30.11" {
			return nil, errors.New("ssh: connect to host 10.20.30.11 port 22: Connection timed out")
		}
		return okOut, nil
	}
	results := DistributeProfile(context.Background(), []byte("{}"), []string{"10.20.30.10", "10.20.30.11", "10.20.30.12"}, testClusterCIDR, run)
	if len(results) != 3 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Error != "" || results[0].Version != 7 || results[0].SHA256 != "abc" {
		t.Fatalf("peer 10: %+v", results[0])
	}
	if results[1].Error == "" || !strings.Contains(results[1].Error, "timed out") {
		t.Fatalf("peer 11 must carry its own error: %+v", results[1])
	}
	if results[2].Error != "" {
		t.Fatalf("peer 12 must still succeed: %+v", results[2])
	}
}

func TestDistributeRefusesNonClusterPeer(t *testing.T) {
	called := false
	run := func(context.Context, string, []byte) ([]byte, error) {
		called = true
		return nil, nil
	}
	results := DistributeProfile(context.Background(), []byte("{}"), []string{"203.0.113.50"}, testClusterCIDR, run)
	if called {
		t.Fatal("the runner must not be invoked for a non-cluster peer")
	}
	if len(results) != 1 || !strings.Contains(results[0].Error, testClusterCIDR) {
		t.Fatalf("results = %+v", results)
	}
}

func TestDistributeRefusesWithoutConfiguredNetwork(t *testing.T) {
	called := false
	run := func(context.Context, string, []byte) ([]byte, error) {
		called = true
		return nil, nil
	}
	results := DistributeProfile(context.Background(), []byte("{}"), []string{"10.20.30.10"}, "", run)
	if called {
		t.Fatal("the runner must not be invoked without a cluster CIDR")
	}
	if len(results) != 1 || !strings.Contains(results[0].Error, "cluster network") {
		t.Fatalf("results = %+v", results)
	}
}

func TestDistributeInvalidPeerOutput(t *testing.T) {
	run := func(context.Context, string, []byte) ([]byte, error) {
		return []byte("not json"), nil
	}
	results := DistributeProfile(context.Background(), []byte("{}"), []string{"10.20.30.10"}, testClusterCIDR, run)
	if len(results) != 1 || !strings.Contains(results[0].Error, "invalid sync state") {
		t.Fatalf("results = %+v", results)
	}
}

func TestDivergentHashes(t *testing.T) {
	local := SyncState{Version: 3, SHA256: "aaa"}
	if DivergentHashes(local, []PeerSyncResult{{SHA256: "aaa"}}) {
		t.Fatal("equal hashes must not be divergent")
	}
	if !DivergentHashes(local, []PeerSyncResult{{SHA256: "aaa"}, {SHA256: "bbb"}}) {
		t.Fatal("a different hash must be divergent")
	}
	if DivergentHashes(local, []PeerSyncResult{{Error: "unreachable"}}) {
		t.Fatal("an unreachable peer is reported by its error, not as divergence")
	}
	if !DivergentHashes(SyncState{}, []PeerSyncResult{{SHA256: "aaa"}}) {
		t.Fatal("a node with no applied profile must diverge from peers that have one")
	}
}

func TestPeerStatesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	in := []PeerSyncResult{{Peer: "10.20.30.10", Version: 2, SHA256: "abc", UpdatedAt: time.Now().UTC()}}
	if err := SavePeerStates(path, in); err != nil {
		t.Fatal(err)
	}
	out := LoadPeerStates(path)
	if len(out) != 1 || out[0].Peer != "10.20.30.10" || out[0].Version != 2 || out[0].SHA256 != "abc" {
		t.Fatalf("out = %+v", out)
	}
	if got := LoadPeerStates(filepath.Join(t.TempDir(), "missing.json")); got != nil {
		t.Fatalf("missing file must return nil, got %+v", got)
	}
}
