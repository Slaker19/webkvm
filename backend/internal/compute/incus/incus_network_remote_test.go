package incus

import (
	"errors"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
)

type remoteNetworkClientStub struct {
	incus.InstanceServer
	apiNetwork api.Network
	err        error
}

func (remoteNetworkClientStub) GetNetwork(name string) (*api.Network, string, error) {
	return nil, "", errors.New("unexpected network lookup")
}

type selectedRemoteNetworkClient struct {
	remoteNetworkClientStub
	name string
}

func (c selectedRemoteNetworkClient) GetNetwork(name string) (*api.Network, string, error) {
	if c.err != nil {
		return nil, "", c.err
	}
	if name != c.name {
		return nil, "", errors.New("unexpected network name")
	}
	return &c.apiNetwork, "etag", nil
}

func TestRemoteIncusBridgeForNetworkUsesTargetIncusNetwork(t *testing.T) {
	b := &IncusBackend{
		client: selectedRemoteNetworkClient{
			remoteNetworkClientStub: remoteNetworkClientStub{apiNetwork: api.Network{Name: "remote-bridge", Type: "bridge"}},
			name:                    "remote-bridge",
		},
		socketPath: "",
	}
	got, err := b.bridgeForNetwork("remote-bridge")
	if err != nil || got != "remote-bridge" {
		t.Fatalf("bridgeForNetwork() = %q, %v", got, err)
	}
}

func TestRemoteIncusBridgeRequiresExplicitNetwork(t *testing.T) {
	b := &IncusBackend{client: selectedRemoteNetworkClient{}, socketPath: ""}
	if _, err := b.bridgeForNetwork(""); err == nil {
		t.Fatal("remote Incus creation without a selected network should fail closed")
	}
}

func TestRemoteIncusBridgeRejectsNonBridgeNetwork(t *testing.T) {
	b := &IncusBackend{
		client: selectedRemoteNetworkClient{
			remoteNetworkClientStub: remoteNetworkClientStub{apiNetwork: api.Network{Name: "ovn0", Type: "ovn"}},
			name:                    "ovn0",
		},
		socketPath: "",
	}
	if _, err := b.bridgeForNetwork("ovn0"); err == nil {
		t.Fatal("non-bridge Incus network should not be attached as a Linux bridge")
	}
}
