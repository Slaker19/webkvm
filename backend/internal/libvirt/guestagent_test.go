package libvirt

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestBuildGuestExec(t *testing.T) {
	qcmd, err := buildGuestExec("/bin/echo", []string{"hello", "world"}, true)
	if err != nil {
		t.Fatalf("buildGuestExec() failed: %v", err)
	}

	var payload guestExecPayload
	if err := json.Unmarshal([]byte(qcmd), &payload); err != nil {
		t.Fatalf("unmarshal payload failed: %v", err)
	}

	if payload.Execute != "guest-exec" {
		t.Errorf("execute = %q, want 'guest-exec'", payload.Execute)
	}
	if payload.Arguments.Path != "/bin/echo" {
		t.Errorf("path = %q, want '/bin/echo'", payload.Arguments.Path)
	}
	if len(payload.Arguments.Arg) != 2 || payload.Arguments.Arg[0] != "hello" || payload.Arguments.Arg[1] != "world" {
		t.Errorf("args = %v, want ['hello', 'world']", payload.Arguments.Arg)
	}
	if !payload.Arguments.CaptureOutput {
		t.Errorf("capture_output = false, want true")
	}
}

func TestVirshCmdContext(t *testing.T) {
	c := &Connector{uri: "qemu:///system"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := c.virshCmdContext(ctx, 5*time.Second, "version")
	if cmd == nil {
		t.Fatal("virshCmdContext returned nil")
	}

	// Verify args include -c URI
	foundURI := false
	for i, arg := range cmd.Args {
		if arg == "-c" && i+1 < len(cmd.Args) && cmd.Args[i+1] == "qemu:///system" {
			foundURI = true
			break
		}
	}
	if !foundURI {
		t.Errorf("cmd.Args %v missing expected '-c qemu:///system'", cmd.Args)
	}
}
