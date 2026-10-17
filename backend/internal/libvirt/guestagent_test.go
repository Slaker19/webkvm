package libvirt

import (
	"encoding/json"
	"testing"
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

func TestVirshArgsIncludeURI(t *testing.T) {
	c := &Connector{uri: "qemu:///system"}

	args := c.virshArgs("version")
	foundURI := false
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) && args[i+1] == "qemu:///system" {
			foundURI = true
			break
		}
	}
	if !foundURI {
		t.Errorf("virshArgs %v missing expected '-c qemu:///system'", args)
	}

	// No URI configured -> args pass through untouched.
	bare := (&Connector{}).virshArgs("version")
	if len(bare) != 1 || bare[0] != "version" {
		t.Errorf("virshArgs = %v, want [version]", bare)
	}
}
