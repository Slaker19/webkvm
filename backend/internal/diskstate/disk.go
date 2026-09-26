package diskstate

import (
	"sort"
	"strings"
)

// ForDisk aggregates the state of a whole disk and every partition
// belonging to it. Callers pass the disk path (/dev/sdb) plus the
// partition paths they already enumerated via lsblk; the disk is in use
// if *any* of them is.
//
// This is the entry point the wipe/format guards use: wiping /dev/sda
// destroys /dev/sda1, so a declaration against the partition must block
// the operation on the parent.
func (t Table) ForDisk(diskPath string, partitions []string) State {
	agg := State{Device: canonicalDevice(diskPath)}

	candidates := append([]string{diskPath}, partitions...)
	for _, dev := range candidates {
		s, ok := t.Lookup(dev)
		if !ok {
			continue
		}
		agg.Active = agg.Active || s.Active
		agg.Configured = agg.Configured || s.Configured
		for _, mp := range s.MountPoints {
			if !contains(agg.MountPoints, mp) {
				agg.MountPoints = append(agg.MountPoints, mp)
			}
		}
		for _, src := range s.Sources {
			if !containsSource(agg.Sources, src) {
				agg.Sources = append(agg.Sources, src)
			}
		}
		for _, u := range s.Units {
			if !contains(agg.Units, u) {
				agg.Units = append(agg.Units, u)
			}
		}
		for _, p := range s.Pools {
			if !contains(agg.Pools, p) {
				agg.Pools = append(agg.Pools, p)
			}
		}
	}

	// Deterministic output: the UI shows MountPoints[0] and the guard
	// error quotes Units[0], so an unstable map iteration order would
	// make both flap between refreshes.
	sort.Strings(agg.MountPoints)
	sort.Strings(agg.Units)
	sort.Strings(agg.Pools)
	sortSources(agg.Sources)

	return agg
}

// sourceRank orders sources from "most concrete" to "most declarative"
// so Reason() and the UI badge pick the clearest explanation first.
var sourceRank = map[Source]int{
	SourceKernel:    0,
	SourceAutomount: 1,
	SourceMountUnit: 2,
	SourceFstab:     3,
	SourceLibvirt:   4,
}

func sortSources(s []Source) {
	sort.Slice(s, func(i, j int) bool { return sourceRank[s[i]] < sourceRank[s[j]] })
}

// Status is the coarse three-way verdict rendered in the UI.
type Status string

const (
	// StatusActive: mounted right now.
	StatusActive Status = "active"
	// StatusConfigured: not mounted at this instant, but something will
	// mount it (armed automount, fstab entry, libvirt pool). Destructive
	// operations must refuse this exactly like StatusActive.
	StatusConfigured Status = "configured"
	// StatusFree: no declaration anywhere; safe to format.
	StatusFree Status = "free"
)

// Status collapses the flags into the value the frontend renders.
func (s State) Status() Status {
	switch {
	case s.Active:
		return StatusActive
	case s.Configured:
		return StatusConfigured
	default:
		return StatusFree
	}
}

// SourceStrings returns Sources as plain strings for JSON payloads that
// should not depend on the Source type.
func (s State) SourceStrings() []string {
	out := make([]string, 0, len(s.Sources))
	for _, src := range s.Sources {
		out = append(out, string(src))
	}
	return out
}

// IsSystemMount reports whether a mountpoint belongs to the running OS
// or to WebKVM's own state, and therefore must never be handed to a VM
// or formatted. Shared with the PCI passthrough detector so both
// subsystems agree on what "critical" means.
func IsSystemMount(mp string) bool {
	switch mp {
	case "/", "/boot", "/boot/efi", "/etc", "/usr", "/var", "/home":
		return true
	}
	return strings.HasPrefix(mp, "/boot/") ||
		strings.HasPrefix(mp, "/var/lib/incus") ||
		strings.HasPrefix(mp, "/var/lib/libvirt") ||
		strings.HasPrefix(mp, "/opt/webkvm")
}

// HasSystemMount reports whether any mountpoint in the state is a
// critical host path.
func (s State) HasSystemMount() bool {
	for _, mp := range s.MountPoints {
		if IsSystemMount(mp) {
			return true
		}
	}
	return false
}
