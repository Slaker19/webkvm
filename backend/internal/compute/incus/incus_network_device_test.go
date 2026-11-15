package incus

import (
	"reflect"
	"testing"
)

func TestIncusBridgedNICIncludesOptionalVLAN(t *testing.T) {
	tag := 42
	got := incusBridgedNIC("eth0", "incusbr0", &tag)
	want := map[string]string{
		"type": "nic", "nictype": "bridged", "parent": "incusbr0", "name": "eth0", "vlan": "42",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("incusBridgedNIC() = %#v, want %#v", got, want)
	}
}

func TestIncusBridgedNICOmitsUnsetVLAN(t *testing.T) {
	got := incusBridgedNIC("eth0", "incusbr0", nil)
	want := map[string]string{"type": "nic", "nictype": "bridged", "parent": "incusbr0", "name": "eth0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("incusBridgedNIC() = %#v, want %#v", got, want)
	}
}
