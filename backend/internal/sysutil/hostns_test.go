package sysutil

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHostNamespaceRunnerWrapsInSystemdRun(t *testing.T) {
	var got []string
	inner := &MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return []byte("ok"), nil
	}}
	h := HostNamespaceRunner{Inner: inner, Available: func() bool { return true }}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := h.Run(ctx, "mount", "-t", "ext4", "/dev/drbd1", "/mnt/x")
	if err != nil || string(out) != "ok" {
		t.Fatalf("%q %v", out, err)
	}
	line := strings.Join(got, " ")
	if !strings.HasPrefix(line, "systemd-run --pipe --wait --collect --quiet --setenv=LC_ALL=C --property=RuntimeMaxSec=") ||
		!strings.HasSuffix(line, " -- mount -t ext4 /dev/drbd1 /mnt/x") {
		t.Fatalf("not wrapped: %q", line)
	}
	if strings.Contains(line, "nsenter") {
		t.Fatalf("nsenter needs CAP_SYS_CHROOT, which the unit does not grant: %q", line)
	}
	// No deadline: no RuntimeMaxSec.
	_, _ = h.Run(context.Background(), "findmnt", "-rn")
	if line = strings.Join(got, " "); line != "systemd-run --pipe --wait --collect --quiet --setenv=LC_ALL=C -- findmnt -rn" {
		t.Fatalf("%q", line)
	}
}

func TestHostNamespaceRunnerFallsBackWithoutSystemd(t *testing.T) {
	var got []string
	inner := &MockCommandRunner{Handler: func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return nil, nil
	}}
	h := HostNamespaceRunner{Inner: inner, Available: func() bool { return false }}
	if _, err := h.RunWithTimeout("umount", time.Second, "/mnt/x"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "umount /mnt/x" {
		t.Fatalf("%v", got)
	}
}
