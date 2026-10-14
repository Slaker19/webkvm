package libvirt

import "errors"

// Domain and storage operational errors. Handlers map them to specific
// HTTP response codes (e.g. 409 Conflict) instead of a generic 500.
var (
	ErrDomainNotPaused             = errors.New("the VM is not paused")
	ErrDomainAlreadyRunning        = errors.New("the VM is already running")
	ErrDomainMustBeStoppedToRename = errors.New("the VM must be stopped to rename it")
	ErrDomainMustBeStoppedToClone  = errors.New("the VM must be stopped to clone it (its disk cannot be safely copied while running)")
	ErrDomainMustBeStoppedToMove   = errors.New("the VM must be stopped to move its storage (its disk cannot be safely copied while running)")
	ErrSamePool                    = errors.New("the source and destination pools are the same")
	ErrVolumeInUse                 = errors.New("the volume is attached to an instance; detach it or stop the instance first")
	ErrVolumeHasDependents         = errors.New("the volume is the backing file of one or more linked clones, which would stop working")
	ErrInsufficientSpace           = errors.New("not enough free space in the destination pool")
)
