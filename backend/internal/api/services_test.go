package api

import (
	"context"
	"errors"
	"testing"
)

func TestQueryUnit(t *testing.T) {
	orig := systemctlShowRunner
	defer func() { systemctlShowRunner = orig }()

	t.Run("found and active", func(t *testing.T) {
		systemctlShowRunner = func(ctx context.Context, unit string) ([]byte, error) {
			return []byte("LoadState=loaded\nActiveState=active\nDescription=Some daemon\n"), nil
		}
		got := queryUnit(context.Background(), "libvirtd.service")
		if !got.found || !got.active || got.state != "active" || got.desc != "Some daemon" {
			t.Fatalf("unexpected result: %+v", got)
		}
	})

	t.Run("found and inactive", func(t *testing.T) {
		systemctlShowRunner = func(ctx context.Context, unit string) ([]byte, error) {
			return []byte("LoadState=loaded\nActiveState=inactive\nDescription=Some daemon\n"), nil
		}
		got := queryUnit(context.Background(), "libvirtd.service")
		if !got.found || got.active || got.state != "inactive" {
			t.Fatalf("unexpected result: %+v", got)
		}
	})

	t.Run("not found", func(t *testing.T) {
		systemctlShowRunner = func(ctx context.Context, unit string) ([]byte, error) {
			return []byte("LoadState=not-found\nActiveState=inactive\nDescription=\n"), nil
		}
		got := queryUnit(context.Background(), "nope.service")
		if got.found {
			t.Fatalf("expected found=false, got %+v", got)
		}
	})

	t.Run("systemctl missing entirely", func(t *testing.T) {
		systemctlShowRunner = func(ctx context.Context, unit string) ([]byte, error) {
			return nil, errors.New("exec: \"systemctl\": executable file not found in $PATH")
		}
		got := queryUnit(context.Background(), "libvirtd.service")
		if got.found || got.state != "unknown" {
			t.Fatalf("expected found=false/state=unknown, got %+v", got)
		}
	})
}

func TestProbeUnitFallback(t *testing.T) {
	orig := systemctlShowRunner
	defer func() { systemctlShowRunner = orig }()

	// First candidate not found, second one is — probeUnit should
	// return the second candidate's info (the libvirtd/virtqemud
	// fallback this mirrors).
	systemctlShowRunner = func(ctx context.Context, unit string) ([]byte, error) {
		if unit == "libvirtd.service" {
			return []byte("LoadState=not-found\n"), nil
		}
		return []byte("LoadState=loaded\nActiveState=active\nDescription=virtqemud\n"), nil
	}
	got := probeUnit(context.Background(), serviceSpec{
		key:        "libvirt",
		candidates: []string{"libvirtd.service", "virtqemud.service"},
	})
	if got.Unit != "virtqemud.service" || !got.Found || !got.Active {
		t.Fatalf("unexpected fallback result: %+v", got)
	}
}

// TestProbeUnitPrefersActiveOverFirstFound covers the real case this
// fallback exists for: Arch's own libvirt package installs BOTH
// libvirtd.service (enabled, but left inactive/dead — socket-activated
// only for compat) and virtqemud.service (the one actually running).
// "First found wins" would keep reporting libvirtd's real "stopped"
// state even though libvirt is fully up via virtqemud — probeUnit must
// prefer whichever candidate is actually active.
func TestProbeUnitPrefersActiveOverFirstFound(t *testing.T) {
	orig := systemctlShowRunner
	defer func() { systemctlShowRunner = orig }()

	systemctlShowRunner = func(ctx context.Context, unit string) ([]byte, error) {
		if unit == "libvirtd.service" {
			return []byte("LoadState=loaded\nActiveState=inactive\nDescription=libvirt legacy monolithic daemon\n"), nil
		}
		return []byte("LoadState=loaded\nActiveState=active\nDescription=Virtualization qemu daemon\n"), nil
	}
	got := probeUnit(context.Background(), serviceSpec{
		key:        "libvirt",
		candidates: []string{"libvirtd.service", "virtqemud.service"},
	})
	if got.Unit != "virtqemud.service" || !got.Found || !got.Active {
		t.Fatalf("expected the active virtqemud.service to win over the inactive-but-found libvirtd.service, got: %+v", got)
	}
}

// TestProbeUnitFallsBackWhenNoneActive covers the ordinary case: none
// of the candidates are active (both stopped) — the first one that
// exists on the host should still be reported (not the last one tried),
// so the UI shows the "real" unit name rather than an unrelated one.
func TestProbeUnitFallsBackWhenNoneActive(t *testing.T) {
	orig := systemctlShowRunner
	defer func() { systemctlShowRunner = orig }()

	systemctlShowRunner = func(ctx context.Context, unit string) ([]byte, error) {
		if unit == "libvirtd.service" {
			return []byte("LoadState=loaded\nActiveState=inactive\nDescription=libvirt legacy monolithic daemon\n"), nil
		}
		return []byte("LoadState=not-found\n"), nil
	}
	got := probeUnit(context.Background(), serviceSpec{
		key:        "libvirt",
		candidates: []string{"libvirtd.service", "virtqemud.service"},
	})
	if got.Unit != "libvirtd.service" || !got.Found || got.Active {
		t.Fatalf("expected the found-but-inactive libvirtd.service, got: %+v", got)
	}
}
