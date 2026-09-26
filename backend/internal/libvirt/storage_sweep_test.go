package libvirt

import "testing"

// Base images share disk pools as base-<id>.qcow2; deleting a VM named
// "base-rocky" must not sweep base-rocky-9.qcow2 via <vm>-<N>.qcow2.
func TestVMDiskSweepMatcherSparesBaseImages(t *testing.T) {
	m := vmDiskSweepMatcher("base-rocky", nil)
	for _, name := range []string{"base-rocky-9.qcow2", "base-rocky-8.qcow2", "base-rocky.qcow2"} {
		if m(name) {
			t.Errorf("name sweep matched base image cache file %q", name)
		}
	}
	if !m("base-rocky.img") {
		t.Error("non-base-image name following the convention should still match")
	}

	// A base-image-shaped name listed explicitly from the domain XML is
	// the VM's own disk and is still deleted.
	m = vmDiskSweepMatcher("base-rocky", []string{"base-rocky.qcow2"})
	if !m("base-rocky.qcow2") {
		t.Error("exact disk name must match")
	}
	if m("base-rocky-9.qcow2") {
		t.Error("exact list must not widen the sweep to base images")
	}
}

func TestVMDiskSweepMatcherConvention(t *testing.T) {
	m := vmDiskSweepMatcher("web", nil)
	for name, want := range map[string]bool{
		"web.qcow2":      true,
		"web.img":        true,
		"web-2.qcow2":    true,
		"web-vdb.qcow2":  true,
		"web.iso":        false,
		"webapp.qcow2":   false,
		"base-web.qcow2": false,
	} {
		if got := m(name); got != want {
			t.Errorf("m(%q) = %v, want %v", name, got, want)
		}
	}
	if vmDiskSweepMatcher("", nil)("x.qcow2") {
		t.Error("empty vm name with no exact list must match nothing")
	}
}
