package compute

import (
	"testing"
)

// Compile-time assertion: KVMBackend must satisfy the full Backend seam.
// If the interface grows a method the adapter misses, this line fails.
var _ Backend = (*KVMBackend)(nil)

// TestKVMBackendSatisfiesInterface is redundant with the var above but
// documents intent: the whole point of Fase 0 is that the seam compiles.
func TestKVMBackendSatisfiesInterface(t *testing.T) {
	if NewKVMBackend(nil) == nil {
		t.Fatal("NewKVMBackend must return a non-nil adapter")
	}
}

// TestKVMBackendCapabilities: KVM supports every feature — the handlers
// must keep all UI sections visible for VMs.
func TestKVMBackendCapabilities(t *testing.T) {
	b := NewKVMBackend(nil)
	c := b.Capabilities()
	if !c.SupportsOVA || !c.SupportsSnapshots || !c.SupportsVNC ||
		!c.SupportsSerialConsole || !c.SupportsUSB || !c.SupportsNoCloudISO ||
		!c.SupportsQemuGuestAgent || !c.SupportsNftPortForwards {
		t.Fatalf("KVM must support the full feature set, got %+v", c)
	}
}

// TestManagedBridgePredicates: before BindHelpers, the neutral predicates
// are safe no-ops (handlers never panic).
func TestManagedBridgePredicates(t *testing.T) {
	if IsManagedBridge("virbr0") {
		t.Error("IsManagedBridge must be false before BindHelpers")
	}
	if IsManagedNetwork("n1") {
		t.Error("IsManagedNetwork must be false before BindHelpers")
	}
}

// TestNeutralSentinels: the compute error sentinels are stable so
// handlers can errors.Is() against them regardless of backend.
func TestNeutralSentinels(t *testing.T) {
	if ErrDomainNotRunning == nil {
		t.Error("ErrDomainNotRunning must be set")
	}
	if ErrMemorySnapshotRequiresRunning == nil {
		t.Error("ErrMemorySnapshotRequiresRunning must be set")
	}
}
