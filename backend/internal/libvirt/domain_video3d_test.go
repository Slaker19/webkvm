package libvirt

import (
	"strings"
	"testing"
)

const testVideoSelfClosing = `    <video>
      <model type='virtio' heads='1' vram='16384'/>
    </video>
    <graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
    </graphics>`

// Regression: changing the console type used to match every <graphics>
// element, deleting the 3D renderer along with VNC/SPICE. 3D then stopped
// working with no visible cause.
func TestStripVNCSpiceGraphics_PreservesEGLHeadless(t *testing.T) {
	in := `    <graphics type='vnc' port='-1' autoport='yes'/>
    <graphics type='egl-headless'>
      <gl rendernode='/dev/dri/renderD128'/>
    </graphics>`
	out := stripVNCSpiceGraphics(in)
	if strings.Contains(out, "type='vnc'") {
		t.Fatal("VNC graphics was not stripped")
	}
	if !strings.Contains(out, "type='egl-headless'") || !strings.Contains(out, "renderD128") {
		t.Fatalf("egl-headless renderer was stripped: %q", out)
	}
}

func TestSetAccel3DInVideoXML_SelfClosingModel(t *testing.T) {
	got := setAccel3DInVideoXML(testVideoSelfClosing, true)
	if !hasAccel3D(got) {
		t.Fatalf("acceleration not added: %q", got)
	}
	// The model must become a container so the child has a parent.
	if strings.Contains(got, "vram='16384'/>") {
		t.Fatalf("self-closing model left unclosed: %q", got)
	}
	if n := strings.Count(got, "</model>"); n != 1 {
		t.Fatalf("want exactly one </model>, got %d: %q", n, got)
	}
}

func TestSetAccel3DInVideoXML_Remove(t *testing.T) {
	with := setAccel3DInVideoXML(testVideoSelfClosing, true)
	got := setAccel3DInVideoXML(with, false)
	if hasAccel3D(got) {
		t.Fatalf("acceleration not removed: %q", got)
	}
	if !strings.Contains(got, "<model type='virtio'") {
		t.Fatalf("video model lost: %q", got)
	}
}

func TestApplyAccel3D_RejectsNonVirtio(t *testing.T) {
	if _, err := applyAccel3D(testVideoSelfClosing, true, "vga"); err == nil {
		t.Fatal("expected an error for a non-virtio video model")
	}
}

func TestApplyAccel3D_OffRemovesRenderer(t *testing.T) {
	in := testVideoSelfClosing + `
    <graphics type='egl-headless'>
      <gl rendernode='/dev/dri/renderD128'/>
    </graphics>`
	got, err := applyAccel3D(in, false, "virtio")
	if err != nil {
		t.Fatal(err)
	}
	if hasEGLHeadless(got) {
		t.Fatalf("egl-headless survived disable: %q", got)
	}
}

// The import/restore sanitiser must accept the 3D renderer with a valid
// node and refuse an arbitrary host path.
func TestSanitizeDomainXML_EGLHeadlessNode(t *testing.T) {
	base := `<domain type='kvm'>
  <name>t</name>
  <memory unit='MiB'>512</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64'>hvm</type></os>
  <devices>
    <graphics type='vnc' port='-1' autoport='yes'/>
    <graphics type='egl-headless'><gl rendernode='%s'/></graphics>
  </devices>
</domain>`

	ok := strings.Replace(base, "%s", "/dev/dri/renderD128", 1)
	out, err := SanitizeDomainXML(ok, DomainPolicy{})
	if err != nil {
		t.Fatalf("valid render node rejected: %v", err)
	}
	if !strings.Contains(out, "renderD128") {
		t.Fatalf("renderer dropped on import: %q", out)
	}

	bad := strings.Replace(base, "%s", "/etc/shadow", 1)
	if _, err := SanitizeDomainXML(bad, DomainPolicy{}); err == nil {
		t.Fatal("arbitrary render node path was accepted")
	}
}
