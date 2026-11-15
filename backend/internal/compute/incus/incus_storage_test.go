package incus

import "testing"

func TestRemoteIncusBackendDoesNotInspectLocalDeviceForPoolPath(t *testing.T) {
	b := &IncusBackend{}
	if got := b.localDeviceID("/var/lib/incus/storage-pools/remote"); got != 0 {
		t.Fatalf("remote pool DeviceID = %d, want unknown (0)", got)
	}
}
