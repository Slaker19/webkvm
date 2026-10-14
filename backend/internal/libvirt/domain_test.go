package libvirt

import (
	"testing"

	"webkvm/internal/models"

	"libvirt.org/go/libvirt"
)

func TestStateToVMState(t *testing.T) {
	cases := []struct {
		state libvirt.DomainState
		want  models.VMState
	}{
		{libvirt.DOMAIN_RUNNING, models.VMStateRunning},
		{libvirt.DOMAIN_SHUTOFF, models.VMStateShutoff},
		{libvirt.DOMAIN_SHUTDOWN, models.VMStateShutoff},
		{libvirt.DOMAIN_PAUSED, models.VMStatePaused},
		{libvirt.DOMAIN_CRASHED, models.VMStateCrashed},
		{libvirt.DOMAIN_NOSTATE, models.VMStateUnknown},
		{libvirt.DOMAIN_PMSUSPENDED, models.VMStateUnknown},
	}

	for _, c := range cases {
		got := stateToVMState(c.state)
		if got != c.want {
			t.Errorf("stateToVMState(%v) = %v, want %v", c.state, got, c.want)
		}
	}
}

func TestLookupDomainNotConnected(t *testing.T) {
	c := &Connector{}
	_, err := c.lookupDomain("non-existent")
	if err == nil {
		t.Fatal("expected error on disconnected connector")
	}
}
