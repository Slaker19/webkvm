package libvirt

import (
	"context"
	"errors"
	"strings"

	"webkvm/internal/nodes"
)

// Domain and storage operational errors. Handlers map them to specific
// HTTP response codes (e.g. 409 Conflict) instead of a generic 500.
var (
	ErrDomainNotPaused             = errors.New("the VM is not paused")
	ErrDomainAlreadyPaused         = errors.New("the VM is already paused")
	ErrDomainAlreadyRunning        = errors.New("the VM is already running")
	ErrDomainMustBeStoppedToRename = errors.New("the VM must be stopped to rename it")
	ErrDomainMustBeStoppedToClone  = errors.New("the VM must be stopped to clone it (its disk cannot be safely copied while running)")
	ErrDomainMustBeStoppedToMove   = errors.New("the VM must be stopped to move its storage (its disk cannot be safely copied while running)")
	ErrSamePool                    = errors.New("the source and destination pools are the same")
	ErrVolumeInUse                 = errors.New("the volume is attached to an instance; detach it or stop the instance first")
	ErrVolumeHasDependents         = errors.New("the volume is the backing file of one or more linked clones, which would stop working")
	ErrInsufficientSpace           = errors.New("not enough free space in the destination pool")
)

// NodeError is a typed failure talking to a remote node. Its message is a
// fixed, safe text: raw libvirt/ssh stderr is never carried to the client.
type NodeError struct {
	code   string
	status int
	msg    string
}

func (e *NodeError) Error() string { return e.msg }

// Code is the stable machine-readable error code.
func (e *NodeError) Code() string { return e.code }

// HTTPStatus is the status API handlers answer with.
func (e *NodeError) HTTPStatus() int { return e.status }

// Typed remote-node errors. Compare with errors.Is.
var (
	ErrNodeUnreachable      = &NodeError{"node_unreachable", 502, "the node is unreachable"}
	ErrNodeTimeout          = &NodeError{"node_timeout", 504, "the node did not answer in time"}
	ErrSSHAuthDenied        = &NodeError{"ssh_auth_denied", 502, "the node rejected the cluster SSH key"}
	ErrHostKeyUnpinned      = &NodeError{"host_key_unpinned", 409, "the node's SSH host key has not been confirmed"}
	ErrHostKeyChanged       = &NodeError{"host_key_changed", 409, "the node's SSH host key differs from the confirmed one"}
	ErrTransportUnsupported = &NodeError{"transport_unsupported", 501, "this libvirt build does not support the configured SSH transport"}
	ErrNoSSHKey             = &NodeError{"no_key", 409, "no cluster SSH key has been generated"}
	ErrLibvirtUnavailable   = &NodeError{"libvirt_unavailable", 502, "SSH works but libvirt is not available on the node"}
)

// ClassifyNodeError maps any connection failure (store errors, context
// errors, libvirt/ssh error text) to one of the typed errors above. The
// original text is only inspected, never returned.
func ClassifyNodeError(err error) error {
	if err == nil {
		return nil
	}
	var ne *NodeError
	if errors.As(err, &ne) {
		return ne
	}
	switch {
	case errors.Is(err, nodes.ErrNoKey):
		return ErrNoSSHKey
	case errors.Is(err, nodes.ErrHostKeyUnpinned):
		return ErrHostKeyUnpinned
	case errors.Is(err, nodes.ErrHostKeyChanged), errors.Is(err, nodes.ErrFingerprintMismatch):
		return ErrHostKeyChanged
	case errors.Is(err, nodes.ErrSSHAuthDenied):
		return ErrSSHAuthDenied
	case errors.Is(err, nodes.ErrHostTimeout), errors.Is(err, context.DeadlineExceeded):
		return ErrNodeTimeout
	case errors.Is(err, nodes.ErrHostUnreachable), errors.Is(err, context.Canceled):
		return ErrNodeUnreachable
	}
	t := strings.ToLower(err.Error())
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(t, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("transport support was not enabled", "transport methods unimplemented", "not recognised", "not recognized",
		"unsupported transport", "no connection driver available"):
		return ErrTransportUnsupported
	case has("identification has changed", "differs from stored identity", "host key verification failed !!!",
		"offending key", "host key has changed", "host key mismatch"):
		return ErrHostKeyChanged
	case has("host key verification failed", "no suitable callback for host key", "was not accepted",
		"unknown host key", "host key is not known", "can't verify the session host key", "no user interaction callback", "failed to add ssh host key", "no matching host key"):
		return ErrHostKeyUnpinned
	case has("permission denied", "authentication failed", "authentication with private key", "access denied",
		"too many authentication failures", "unable to authenticate", "no supported authentication", "publickey"):
		return ErrSSHAuthDenied
	case has("timed out", "timeout"):
		return ErrNodeTimeout
	case has("libvirt-sock", "virt-ssh-helper", "command not found", "failed to connect socket", "nc:", "netcat",
		"failed to connect to the hypervisor"):
		return ErrLibvirtUnavailable
	}
	return ErrNodeUnreachable
}
