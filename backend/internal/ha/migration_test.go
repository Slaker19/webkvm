package ha

import (
	"context"
	"strings"
	"testing"

	"webkvm/internal/sysutil"
)

const migrationCIB = `<cib><configuration><resources>
  <primitive id="kvm-vm" type="VirtualDomain">
    <instance_attributes>
      <nvpair name="config" value="/var/lib/libvirt/images-ha/kvm-vm.xml"/>
      <nvpair name="migration_transport" value="tcp"/>
    </instance_attributes>
    <meta_attributes>
      <nvpair name="allow-migrate" value="true"/>
    </meta_attributes>
  </primitive>
  <primitive id="kvm-vm2" type="VirtualDomain">
    <instance_attributes>
      <nvpair name="config" value="/var/lib/libvirt/images-ha/kvm-vm2.xml"/>
    </instance_attributes>
  </primitive>
</resources></configuration></cib>`

func TestParseVirtualDomainMigrations(t *testing.T) {
	vms := ParseVirtualDomainMigrations([]byte(migrationCIB))
	if len(vms) != 2 {
		t.Fatalf("vms = %+v", vms)
	}
	ready := vms[0]
	if ready.ResourceID != "kvm-vm" || !ready.AllowMigrate || ready.MigrationTransport != "tcp" || !ready.Ready {
		t.Fatalf("ready VM misparsed: %+v", ready)
	}
	pending := vms[1]
	if pending.ResourceID != "kvm-vm2" || pending.AllowMigrate || pending.Ready {
		t.Fatalf("pending VM misparsed: %+v", pending)
	}
}

func TestMigrationCommands(t *testing.T) {
	vms := ParseVirtualDomainMigrations([]byte(migrationCIB))
	commands, notes := MigrationCommands(vms, "")
	joined := strings.Join(commands, "\n")
	if !strings.Contains(joined, "pcs resource meta kvm-vm2 allow-migrate=true") {
		t.Fatalf("missing allow-migrate command:\n%s", joined)
	}
	if !strings.Contains(joined, "pcs resource update kvm-vm2 migration_transport=tcp") {
		t.Fatalf("missing migration_transport command:\n%s", joined)
	}
	if strings.Contains(joined, "kvm-vm allow-migrate") {
		t.Fatalf("a ready VM must not be rewritten:\n%s", joined)
	}
	if len(notes) == 0 {
		t.Fatal("expected operator notes")
	}

	commands, _ = MigrationCommands(vms, "kvm-b")
	joined = strings.Join(commands, "\n")
	for _, want := range []string{"pcs resource move kvm-vm kvm-b", "pcs resource move kvm-vm2 kvm-b", "pcs resource clear <resource>"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q:\n%s", want, joined)
		}
	}
}

func TestProbeVirtualDomainMigrations(t *testing.T) {
	runner := &sysutil.MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "pcs" || strings.Join(args, " ") != "cluster cib" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		return []byte(migrationCIB), nil
	}}
	vms, err := ProbeVirtualDomainMigrations(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	if len(vms) != 2 {
		t.Fatalf("vms = %+v", vms)
	}
}
