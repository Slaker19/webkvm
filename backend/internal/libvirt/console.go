package libvirt

import (
	"fmt"
	"strings"

	"libvirt.org/go/libvirt"
)

// OpenSerialConsole opens a stream to the serial console of the given domain.
// The caller must close the stream when done. Returns the libvirt stream
// that can be used with Stream.Recv / Stream.Send.
func (c *Connector) OpenSerialConsole(id string) (*libvirt.Domain, *libvirt.Stream, error) {
	// Do NOT hold c.mu across lookupDomain: lookupDomain calls
	// ensureConnected, which may call Open() and take c.mu.Lock() when
	// the connection has dropped. Holding RLock here makes that a
	// self-deadlock (sync.RWMutex is not reentrant), funnelling every
	// concurrent Connector call through this goroutine forever.
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

	// A domain with no <serial>/<console> device can never serve a
	// console, no matter how long the caller waits. Without this check
	// the failure was indistinguishable from "still booting", so the
	// websocket handler retried for its full 30s grace and then told the
	// operator the VM was "powered off or still booting" — about a
	// running VM. Detecting it here turns a misleading, repeating
	// warning into one accurate, actionable message.
	if xml, xerr := dom.GetXMLDesc(0); xerr == nil && !hasSerialDevice(xml) {
		return nil, nil, ErrNoSerialDevice
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

// hasSerialDevice reports whether the domain XML declares a serial or
// console device.
//
// The check is textual on purpose: unmarshalling the full domain XML
// just to look for one element would couple this to libvirt's schema for
// no gain, and a false "device present" here is harmless (the open then
// fails as it did before), while a false negative would wrongly refuse a
// working console. <virtio-serial> controllers and <channel> elements do
// NOT count — they carry guest agent traffic, not a console.
func hasSerialDevice(xml string) bool {
	for _, tag := range []string{"<serial ", "<serial>", "<console ", "<console>"} {
		if strings.Contains(xml, tag) {
			return true
		}
	}
	return false
}

// SetUserPassword changes the password of a user inside a running guest
// via the QEMU guest agent (virDomainSetUserPassword). Requires the VM
// to be running with qemu-guest-agent installed and active.
func (c *Connector) SetUserPassword(id, user, password string) error {
	// Same reentrancy hazard as OpenSerialConsole: lookupDomain ->
	// ensureConnected -> Open() takes c.mu.Lock(). Do not hold RLock here.
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
