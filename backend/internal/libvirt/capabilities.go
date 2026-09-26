package libvirt

import (
	"fmt"
	"strings"

	"webkvm/internal/hostcaps"
)

// Device-model validation against the local engine's real capabilities.
//
// Rationale: libvirt rejects a domain whose <video><model type='...'/>
// names a device QEMU does not have, but it does so with a terse
// message and only at define time. Validating up front lets us return an
// actionable error ("qxl is not available on this host; use one of:
// ...") instead of a bare libvirt failure, and stops an API client that
// bypasses the UI from creating an unstartable VM.
//
// Every check is a no-op when the capability probe failed (Parsed=false):
// we would rather let a genuinely capable-but-unprobeable host through
// than block a valid request on incomplete information.

// validateVideoModel rejects a video model the engine cannot provide.
// This is the check that would have caught the QXL bug: QEMU 10 dropped
// the qxl device, so `virsh domcapabilities` omits it, and any attempt
// to define a qxl VM fails.
func validateVideoModel(model string) error {
	if model == "" || model == "none" {
		return nil
	}
	c := hostcaps.Get()
	if !c.Parsed {
		return nil
	}
	return requireIn("video model", model, c.VideoModels)
}

func validateNetworkModel(model string) error {
	if model == "" {
		return nil
	}
	c := hostcaps.Get()
	if !c.Parsed {
		return nil
	}
	return requireIn("network model", model, c.NetworkModels)
}

func validateDiskBus(bus string) error {
	if bus == "" {
		return nil
	}
	c := hostcaps.Get()
	if !c.Parsed {
		return nil
	}
	return requireIn("disk bus", bus, c.DiskBuses)
}

func validateAudioModel(model string) error {
	if model == "" || model == "none" {
		return nil
	}
	c := hostcaps.Get()
	if !c.Parsed {
		return nil
	}
	return requireIn("audio model", model, c.SoundModels)
}

// validateCPUModel rejects a custom CPU model libvirt does not know
// about (`virsh cpu-models <arch>`). Unlike the device-model checks,
// this list does not depend on the domcapabilities probe succeeding —
// it comes from a separate `virsh` call — so it is not gated on
// c.Parsed, only on the list being non-empty.
func validateCPUModel(model string) error {
	if model == "" {
		return nil
	}
	c := hostcaps.Get()
	if len(c.CPUModels) == 0 {
		return nil
	}
	return requireIn("CPU model", model, c.CPUModels)
}

// validateCPUFlags rejects any `+flag`/`-flag` entry whose bare name is
// not a CPUID feature QEMU recognizes (`qemu -cpu help`). An unprobeable
// host (empty CPUFlags) allows everything through.
func validateCPUFlags(flags []string) error {
	c := hostcaps.Get()
	if len(c.CPUFlags) == 0 {
		return nil
	}
	for _, f := range flags {
		name := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(f), "+"), "-")
		if name == "" {
			continue
		}
		if err := requireIn("CPU flag", name, c.CPUFlags); err != nil {
			return err
		}
	}
	return nil
}

// requireIn reports a friendly error when want is not among the
// supported values. Matching is case-insensitive because the UI
// upper-cases some labels for display but the XML wants lowercase.
//
// Long lists (CPU models/flags run into the hundreds) are truncated in
// the message: naming every option is only useful for the short,
// hand-curated lists (video/network/audio/disk-bus).
func requireIn(kind, want string, supported []string) error {
	if len(supported) == 0 {
		return nil
	}
	for _, s := range supported {
		if strings.EqualFold(s, want) {
			return nil
		}
	}
	const maxListed = 15
	shown := supported
	suffix := ""
	if len(supported) > maxListed {
		shown = supported[:maxListed]
		suffix = fmt.Sprintf(", … (%d more)", len(supported)-maxListed)
	}
	return fmt.Errorf("%s %q is not available on this host; supported: %s%s",
		kind, want, strings.Join(shown, ", "), suffix)
}
