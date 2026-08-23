package libvirt

import (
	"fmt"

	"github.com/libvirt/libvirt-go"
)

// OpenSerialConsole opens a stream to the serial console of the given domain.
// The caller must close the stream when done. Returns the libvirt stream
// that can be used with Stream.Recv / Stream.Send.
func (c *Connector) OpenSerialConsole(id string) (*libvirt.Domain, *libvirt.Stream, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	dom, err := c.lookupDomain(id)
	if err != nil {
		return nil, nil, err
	}

	// Fail fast with a clear, mapped error when the domain is off —
	// otherwise the caller gets a raw virError and the UI retries forever.
	if state, _, err := dom.GetState(); err == nil {
		if libvirt.DomainState(state) != libvirt.DOMAIN_RUNNING {
			return nil, nil, ErrDomainNotRunning
		}
	}

	stream, err := c.conn.NewStream(0)
	if err != nil {
		return nil, nil, fmt.Errorf("new stream: %w", err)
	}

	// FORCE = the newest web console takes over the serial device
	// (single-viewer policy, like Proxmox). Without it, a stale session
	// blocks every reconnection with "Active console session exists".
	if err := dom.OpenConsole("", stream, libvirt.DOMAIN_CONSOLE_FORCE); err != nil {
		stream.Free()
		return nil, nil, fmt.Errorf("open console: %w", err)
	}

	return dom, stream, nil
}

// SetUserPassword changes the password of a user inside a running guest
// via the QEMU guest agent (virDomainSetUserPassword). Requires the VM
// to be running with qemu-guest-agent installed and active.
func (c *Connector) SetUserPassword(id, user, password string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	dom, err := c.lookupDomain(id)
	if err != nil {
		return err
	}
	defer dom.Free()

	if err := dom.SetUserPassword(user, password, 0); err != nil {
		return fmt.Errorf("set user password (is qemu-guest-agent running in the VM?): %w", err)
	}
	return nil
}
