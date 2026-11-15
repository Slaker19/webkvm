package libvirt

import (
	"testing"

	"webkvm/internal/nodes"
)

func TestMigrationURI(t *testing.T) {
	cases := []struct {
		name string
		n    nodes.Node
		want string
	}{
		{"ipv4 node", nodes.Node{Type: nodes.NodeTypeRemote, URI: "qemu+ssh://operator@10.20.30.11/system"}, "tcp://10.20.30.11"},
		{"dns node", nodes.Node{Type: nodes.NodeTypeRemote, URI: "qemu+ssh://root@kvm2.lan:2222/system"}, "tcp://kvm2.lan"},
		{"ipv6 node", nodes.Node{Type: nodes.NodeTypeRemote, URI: "qemu+ssh://root@[fd00::5]/system"}, "tcp://[fd00::5]"},
		{"loopback node", nodes.Node{Type: nodes.NodeTypeRemote, URI: "qemu+ssh://root@127.0.0.1/system"}, ""},
		{"local node", nodes.Node{Type: nodes.NodeTypeLocal, URI: "qemu:///system"}, ""},
		{"not ssh", nodes.Node{Type: nodes.NodeTypeRemote, URI: "qemu+tcp://10.0.0.1/system"}, ""},
	}
	for _, c := range cases {
		if got := MigrationURI(c.n); got != c.want {
			t.Errorf("%s: MigrationURI = %q, want %q", c.name, got, c.want)
		}
	}
}
