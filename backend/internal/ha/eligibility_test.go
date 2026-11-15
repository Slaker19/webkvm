package ha

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"webkvm/internal/sysutil"
)

const drbdOK = `kvm_data node-id:0 role:Primary suspended:no
  volume:0 minor:1001 disk:UpToDate
  ha2 node-id:1 connection:Connected role:Secondary
    volume:0 replication:Established peer-disk:UpToDate
  ha3 node-id:2 connection:Connected role:Secondary
    volume:0 replication:Established peer-disk:UpToDate
`

const drbdPlain = `kvm_data role:Primary
  disk:UpToDate
  ha2 role:Secondary
    peer-disk:Inconsistent
  ha3 connection:Connecting
`

func eligRunner(drbd string, mounts map[string]string) sysutil.CommandRunner {
	return &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "drbdsetup":
			return []byte(drbd), nil
		case "findmnt":
			p := args[len(args)-1]
			for prefix, line := range mounts {
				if strings.HasPrefix(p, prefix) {
					return []byte(line + "\n"), nil
				}
			}
		}
		return nil, context.DeadlineExceeded
	}}
}

func codes(e Eligibility) string {
	c := []string{}
	for _, r := range e.Reasons {
		c = append(c, r.Code)
	}
	return strings.Join(c, ",")
}

func TestParseDRBDStatus(t *testing.T) {
	v := ParseDRBDStatus(drbdOK)["kvm_data"]
	if v == nil || v.Minor != "1001" || v.UpToDate() != 3 {
		t.Fatalf("%+v", v)
	}
	if v := ParseDRBDStatus(drbdPlain)["kvm_data"]; v.UpToDate() != 1 {
		t.Fatalf("%+v", v)
	}
}

func TestEvalKVMEligible(t *testing.T) {
	r := eligRunner(drbdOK, map[string]string{"/mnt/vm": "/mnt/vm /dev/drbd1001 ext4"})
	e, facts := EvalKVM(context.Background(), r, KVMInputs{Name: "web", XMLPath: "/mnt/vm/web.xml", DiskPaths: []string{"/mnt/vm/web.qcow2"},
		ClusterNodes: []string{"ha1"}, Bridges: []string{"br0"}, NodeBridges: map[string][]string{"ha1": {"br0"}}})
	if !e.Eligible || len(e.Reasons) != 0 {
		t.Fatalf("%+v", e)
	}
	if facts.DRBDResource != "kvm_data" || facts.MountDir != "/mnt/vm" || facts.FSType != "ext4" || facts.Device != "/dev/drbd1001" {
		t.Fatalf("%+v", facts)
	}
}

func TestEvalKVMReasons(t *testing.T) {
	r := eligRunner(drbdPlain, map[string]string{
		"/mnt/vm":   "/mnt/vm /dev/drbd/by-res/kvm_data/0 ext4",
		"/var/lib/": "/ /dev/sda1 ext4",
	})
	e, _ := EvalKVM(context.Background(), r, KVMInputs{Name: "web", XMLPath: "/mnt/vm/web.xml", Running: true,
		DiskPaths:    []string{"/mnt/vm/a.qcow2", "/var/lib/libvirt/images/b.qcow2"},
		ClusterNodes: []string{"ha1", "ha2"}, Bridges: []string{"br0"}, NodeBridges: map[string][]string{"ha1": {"br0"}},
		Existing: []Workload{{Key: "kvm:web-", ResourceID: "ha-kvm-web"}}})
	got := codes(e)
	for _, want := range []string{ReasonDiskNotReplicated, ReasonDRBDNotUpToDate, ReasonVMRunning, ReasonBridgeMissing, ReasonNameCollision} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if e.Eligible || strings.Contains(got, ReasonXMLNotReplicated) {
		t.Fatalf("%s", got)
	}
	for _, r := range e.Reasons {
		if r.Fix == "" || r.Detail == "" {
			t.Errorf("reason without fix/detail: %+v", r)
		}
	}
	e2, _ := EvalKVM(context.Background(), eligRunner(drbdOK, map[string]string{"/x": "/ /dev/sda1 ext4"}), KVMInputs{Name: "w", XMLPath: "/x/w.xml"})
	if !strings.Contains(codes(e2), ReasonXMLNotReplicated) {
		t.Fatalf("%s", codes(e2))
	}
}

type fakeIncus struct {
	driver  string
	devs    []DeviceInfo
	cfg     map[string]string
	offline string
	nics    []NICInfo
	nets    map[string]map[string]bool
	managed bool
}

func (f fakeIncus) RootPoolDriver(string) string    { return f.driver }
func (f fakeIncus) Devices(string) []DeviceInfo     { return f.devs }
func (f fakeIncus) Config(string) map[string]string { return f.cfg }
func (f fakeIncus) MemberOnline(m string) bool      { return m != f.offline }
func (f fakeIncus) NICs(string) []NICInfo           { return f.nics }
func (f fakeIncus) NetworkMembers(name string) (map[string]bool, bool, error) {
	p, ok := f.nets[name]
	if !ok {
		return nil, false, errors.New("not probed")
	}
	return p, f.managed, nil
}

func TestEvalIncus(t *testing.T) {
	ok := EvalIncus(fakeIncus{driver: "linstor", cfg: map[string]string{}}, IncusInputs{Name: "c", Project: "default", Members: []string{"a"}})
	if !ok.Eligible {
		t.Fatalf("%+v", ok)
	}
	bad := EvalIncus(fakeIncus{driver: "dir", devs: []DeviceInfo{{Name: "u", Type: "usb"}, {Name: "d", Type: "disk", Source: "/srv"}, {Name: "root", Type: "disk", Pool: "p"}},
		cfg: map[string]string{"boot.autostart": "true"}, offline: "b"}, IncusInputs{Name: "c", Project: "default", Members: []string{"a", "b"}})
	for _, want := range []string{ReasonPoolNotShared, ReasonLocalDevices, ReasonBootAutostart, ReasonMemberOffline} {
		if !strings.Contains(codes(bad), want) {
			t.Errorf("missing %s: %s", want, codes(bad))
		}
	}
	if strings.Count(bad.Reasons[1].Detail, "(") != 2 {
		t.Errorf("only usb and host-path disk should be listed: %s", bad.Reasons[1].Detail)
	}
}

func TestVerifyControlPlane(t *testing.T) {
	cib := `<cib><configuration><resources>` + strings.Split(strings.Split(labCIB, "<resources>")[1], "</resources>")[0] + `</resources><constraints>
<rsc_colocation id="c" rsc="webkvm-cp" with-rsc="webkvm-data-clone" with-rsc-role="Promoted" score="INFINITY"/>
<rsc_order id="o" first="webkvm-data-clone" first-action="promote" then="webkvm-cp" then-action="start"/></constraints></configuration></cib>`
	if f := VerifyControlPlane([]byte(cib), "", "/opt/webkvm"); len(f) != 0 {
		t.Fatalf("lab control plane must verify: %+v", f)
	}
	if f := VerifyControlPlane([]byte(cib), "webkvm-cp", "/srv"); len(f) != 1 || f[0].Code != CPFsNotDataDir {
		t.Fatalf("%+v", f)
	}
	if f := VerifyControlPlane([]byte(emptyCIB), "webkvm-cp"); len(f) != 1 || f[0].Code != CPGroupMissing {
		t.Fatalf("%+v", f)
	}
	bare := strings.Replace(strings.Replace(cib, `<rsc_colocation`, `<x`, 1), `<rsc_order`, `<y`, 1)
	got := ""
	for _, c := range VerifyControlPlane([]byte(bare), "webkvm-cp") {
		got += c.Code + ","
	}
	if !strings.Contains(got, CPColocationMissing) || !strings.Contains(got, CPOrderMissing) {
		t.Fatal(got)
	}
	if f := VerifyControlPlane([]byte(strings.ReplaceAll(cib, "IPaddr2", "Dummy")), "webkvm-cp"); len(f) != 1 || f[0].Code != CPNoVIP {
		t.Fatalf("%+v", f)
	}
	if f := VerifyControlPlane([]byte(strings.Replace(cib, `provider="webkvm" type="webkvm"`, `provider="x" type="y"`, 1)), "webkvm-cp"); len(f) != 1 || f[0].Code != CPAppAgentMissing {
		t.Fatalf("%+v", f)
	}
}

func TestControlPlaneCommands(t *testing.T) {
	cmds, err := ControlPlaneCommands(CPSpec{DRBDResource: "webkvm_data", Device: "/dev/drbd/by-res/webkvm_data/0", DataDir: "/opt/webkvm", VIP: "192.0.2.10", VIPCIDR: 24})
	if err != nil || len(cmds) != 7 || !strings.Contains(cmds[2], "ip=192.0.2.10 cidr_netmask=24") {
		t.Fatalf("%v %v", cmds, err)
	}
	if _, err := ControlPlaneCommands(CPSpec{DRBDResource: "x", Device: "/dev/drbd1", DataDir: "/opt", VIP: "1.1.1.1; rm", VIPCIDR: 24}); err == nil {
		t.Fatal("injection accepted")
	}
}

func TestEvalIncusNICNetworkNotOnAllMembers(t *testing.T) {
	members := []string{"a", "b", "c"}
	// Real live case: the default profile NIC parent vmbr0 exists on one member only.
	unmanaged := EvalIncus(fakeIncus{driver: "linstor", cfg: map[string]string{},
		nics: []NICInfo{{Name: "eth0", Parent: "vmbr0", Type: "bridged"}},
		nets: map[string]map[string]bool{"vmbr0": {"a": true, "b": false, "c": false}}},
		IncusInputs{Name: "c1", Project: "default", Members: members})
	if unmanaged.Eligible || len(unmanaged.Reasons) != 1 || unmanaged.Reasons[0].Code != ReasonNICNetworkNotOnAllMembers {
		t.Fatalf("%+v", unmanaged)
	}
	r := unmanaged.Reasons[0]
	if !strings.Contains(r.Detail, "b, c") || strings.Contains(r.Detail, "a,") || !strings.Contains(r.Fix, "incus network create <name> --target <member>") {
		t.Fatalf("%+v", r)
	}
	if want := []string{"incus network create ha-net --target a", "incus network create ha-net --target b", "incus network create ha-net --target c", "incus network create ha-net"}; !reflect.DeepEqual(r.Commands, want) {
		t.Fatalf("%v", r.Commands)
	}
	// Managed network present on a only: commands target just the missing members.
	managed := EvalIncus(fakeIncus{driver: "ceph", cfg: map[string]string{}, managed: true,
		nics: []NICInfo{{Name: "eth0", Network: "lan"}},
		nets: map[string]map[string]bool{"lan": {"a": true, "b": true, "c": false}}},
		IncusInputs{Name: "c1", Project: "default", Members: members})
	if want := []string{"incus network create lan --target c", "incus network create lan"}; len(managed.Reasons) != 1 || !reflect.DeepEqual(managed.Reasons[0].Commands, want) {
		t.Fatalf("%+v", managed.Reasons)
	}
	// Present everywhere, unknown probe, single member and NIC without network: no reason.
	for name, f := range map[string]fakeIncus{
		"everywhere":  {nics: []NICInfo{{Name: "eth0", Network: "lan"}}, nets: map[string]map[string]bool{"lan": {"a": true, "b": true, "c": true}}},
		"unknown":     {nics: []NICInfo{{Name: "eth0", Network: "lan"}}, nets: map[string]map[string]bool{"lan": {"a": true}}},
		"probe error": {nics: []NICInfo{{Name: "eth0", Network: "lan"}}},
		"no network":  {nics: []NICInfo{{Name: "eth0", Type: "p2p"}}},
	} {
		f.driver, f.cfg = "linstor", map[string]string{}
		if e := EvalIncus(f, IncusInputs{Name: "c1", Project: "default", Members: members}); !e.Eligible {
			t.Fatalf("%s: %+v", name, e)
		}
	}
	one := EvalIncus(fakeIncus{driver: "linstor", cfg: map[string]string{}, nics: []NICInfo{{Name: "eth0", Parent: "vmbr0"}}, nets: map[string]map[string]bool{"vmbr0": {"a": false}}},
		IncusInputs{Name: "c1", Project: "default", Members: []string{"a"}})
	if !one.Eligible {
		t.Fatalf("single node cannot move: %+v", one)
	}
}
