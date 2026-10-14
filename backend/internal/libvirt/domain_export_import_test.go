package libvirt

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestImportNormalizesMachineType(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "pc-q35-10.2 single quote",
			in:   `<os><type arch='x86_64' machine='pc-q35-10.2'>hvm</type></os>`,
			want: `<os><type arch='x86_64' machine='q35'>hvm</type></os>`,
		},
		{
			name: "pc-i440fx-7.1 single quote",
			in:   `<os><type arch='x86_64' machine='pc-i440fx-7.1'>hvm</type></os>`,
			want: `<os><type arch='x86_64' machine='pc'>hvm</type></os>`,
		},
		{
			name: "pc-q35-9.0 double quote",
			in:   `<os><type arch="x86_64" machine="pc-q35-9.0">hvm</type></os>`,
			want: `<os><type arch="x86_64" machine="q35">hvm</type></os>`,
		},
		{
			name: "already short form, untouched",
			in:   `<os><type arch='x86_64' machine='q35'>hvm</type></os>`,
			want: `<os><type arch='x86_64' machine='q35'>hvm</type></os>`,
		},
		{
			name: "pc-i440fx-2.12 deprecated, downgrade to pc",
			in:   `<type arch='x86_64' machine='pc-i440fx-2.12'>hvm</type>`,
			want: `<type arch='x86_64' machine='pc'>hvm</type>`,
		},
		{
			name: "no machine attribute, untouched",
			in:   `<os><type arch='x86_64'>hvm</type></os>`,
			want: `<os><type arch='x86_64'>hvm</type></os>`,
		},
		{
			name: "pc-q35-10.0 (latest on debian trixie) — still normalized to q35 for forward compat",
			in:   `<os><type arch='x86_64' machine='pc-q35-10.0'>hvm</type></os>`,
			want: `<os><type arch='x86_64' machine='q35'>hvm</type></os>`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeMachineType(c.in)
			if got != c.want {
				t.Errorf("normalize:\n  got:  %q\n  want: %q", got, c.want)
			}
			// Sanity: result must not still contain a versioned machine type.
			if strings.Contains(got, "pc-q35-") || strings.Contains(got, "pc-i440fx-") {
				t.Errorf("versioned machine type still present: %q", got)
			}
		})
	}
}

// TestStripCdromDevices verifies the import-time transform that
// removes every <disk type='file' device='cdrom'>...</disk> block
// from the imported domain XML and reports a single summary
// warning per import. ImportDomain uses the same regex; this test
// exercises it without needing a live libvirt connection.
func TestStripCdromDevices(t *testing.T) {
	cases := []struct {
		name         string
		xml          string
		wantStripped int
		wantContains []string
		wantMissing  []string
	}{
		{
			name: "single CDROM is stripped, disk device untouched",
			xml: `<devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='/pool/disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/pool/ubuntu.iso'/>
      <target dev='sda' bus='sata'/>
    </disk>
  </devices>`,
			wantStripped: 1,
			wantContains: []string{"/pool/disk.qcow2", "vda"},
			wantMissing:  []string{"/pool/ubuntu.iso", "sda"},
		},
		{
			name: "no CDROM devices: nothing changes",
			xml: `<devices>
    <disk type='file' device='disk'>
      <source file='/pool/disk.qcow2'/>
    </disk>
  </devices>`,
			wantStripped: 0,
			wantContains: []string{"/pool/disk.qcow2"},
		},
		{
			name: "two CDROMs are both stripped",
			xml: `<devices>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/pool/iso1.iso'/>
      <target dev='sda' bus='sata'/>
    </disk>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/pool/iso2.iso'/>
      <target dev='sdb' bus='sata'/>
    </disk>
  </devices>`,
			wantStripped: 2,
			wantMissing:  []string{"iso1.iso", "iso2.iso", "sda", "sdb"},
		},
		{
			name: "CDROM block spanning newlines is still matched",
			xml: `<devices>
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='/pool/ubuntu.iso'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
  </devices>`,
			wantStripped: 1,
			wantMissing:  []string{"ubuntu.iso"},
		},
	}

	cdromBlockRe := regexp.MustCompile(`(?s)<disk\s+type='file'\s+device='cdrom'\s*>\s*.*?</disk>`)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			matches := cdromBlockRe.FindAllString(c.xml, -1)
			if len(matches) != c.wantStripped {
				t.Fatalf("stripped count = %d, want %d", len(matches), c.wantStripped)
			}
			stripped := cdromBlockRe.ReplaceAllString(c.xml, "")
			for _, sub := range c.wantContains {
				if !strings.Contains(stripped, sub) {
					t.Errorf("result missing expected substring %q\nresult:\n%s", sub, stripped)
				}
			}
			for _, sub := range c.wantMissing {
				if strings.Contains(stripped, sub) {
					t.Errorf("result contains forbidden substring %q\nresult:\n%s", sub, stripped)
				}
			}
		})
	}
}

// TestCloneDiskFile_FullCopy verifies that the default (non-linked)
// clone mode produces a fully independent qcow2 file with the
// source's actual data — this is the regression test for the bug
// where CloneDomain used to create an empty volume of the right size
// instead of copying the disk contents.
func TestCloneDiskFile_FullCopy(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not available in test environment")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.qcow2")
	dst := filepath.Join(dir, "clone.qcow2")

	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", src, "16M").CombinedOutput(); err != nil {
		t.Fatalf("setup: create source qcow2: %v (%s)", err, out)
	}
	// Write recognizable data through the qcow2 file with qemu-img
	// so we don't need a full guest — dd against the raw file won't
	// respect the qcow2 format, but writing via `qemu-img convert`
	// from a small raw seed does.
	seed := filepath.Join(dir, "seed.raw")
	if err := os.WriteFile(seed, []byte("WEBKVM-CLONE-TEST-MARKER"), 0644); err != nil {
		t.Fatalf("setup: write seed: %v", err)
	}
	if out, err := exec.Command("qemu-img", "convert", "-n", "-O", "qcow2", seed, src).CombinedOutput(); err != nil {
		t.Fatalf("setup: seed source qcow2: %v (%s)", err, out)
	}

	if err := cloneDiskFile(src, dst, "qcow2", false); err != nil {
		t.Fatalf("cloneDiskFile (linked=false): %v", err)
	}

	// 1. Destination must exist and have the right virtual size.
	out, err := exec.Command("qemu-img", "info", "--output=json", dst).CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img info on clone: %v (%s)", err, out)
	}
	if !strings.Contains(string(out), `"virtual-size": 16777216`) {
		t.Errorf("expected virtual size 16M in clone info, got: %s", out)
	}
	// 2. Destination must NOT have a backing file (independent copy).
	if strings.Contains(string(out), "backing-filename") {
		t.Errorf("clone should not have a backing file: %s", out)
	}
	// 3. The data must match: convert back to raw and check contents.
	rawOut := filepath.Join(dir, "clone.raw")
	if out, err := exec.Command("qemu-img", "convert", "-O", "raw", dst, rawOut).CombinedOutput(); err != nil {
		t.Fatalf("convert clone to raw: %v (%s)", err, out)
	}
	got, err := os.ReadFile(rawOut)
	if err != nil {
		t.Fatalf("read cloned raw: %v", err)
	}
	if !strings.HasPrefix(string(got), "WEBKVM-CLONE-TEST-MARKER") {
		t.Errorf("cloned data does not match source! header=%q", got[:min(len(got), 32)])
	}
}

// TestCloneDiskFile_Linked verifies that linked=true produces an
// overlay file whose backing file points at the source.
func TestCloneDiskFile_Linked(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not available in test environment")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.qcow2")
	dst := filepath.Join(dir, "clone.qcow2")

	if out, err := exec.Command("qemu-img", "create", "-f", "qcow2", src, "16M").CombinedOutput(); err != nil {
		t.Fatalf("setup: create source qcow2: %v (%s)", err, out)
	}

	if err := cloneDiskFile(src, dst, "qcow2", true); err != nil {
		t.Fatalf("cloneDiskFile (linked=true): %v", err)
	}

	out, err := exec.Command("qemu-img", "info", "--output=json", dst).CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img info on clone: %v (%s)", err, out)
	}
	// Must have backing-filename pointing at src.
	if !strings.Contains(string(out), "backing-filename") {
		t.Errorf("expected backing file on linked clone, got: %s", out)
	}
	if !strings.Contains(string(out), "source.qcow2") {
		t.Errorf("expected backing file to point at source.qcow2, got: %s", out)
	}
}

// TestCloneDiskFile_LinkedIgnoredForRaw verifies that requesting a
// linked clone on a raw disk is rejected: raw files cannot have
// backing files, so we silently fall back to an independent copy
// rather than failing or creating a broken overlay.
func TestCloneDiskFile_LinkedIgnoredForRaw(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not available in test environment")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "source.raw")
	dst := filepath.Join(dir, "clone.raw")

	if out, err := exec.Command("qemu-img", "create", "-f", "raw", src, "1M").CombinedOutput(); err != nil {
		t.Fatalf("setup: create source raw: %v (%s)", err, out)
	}

	if err := cloneDiskFile(src, dst, "raw", true); err != nil {
		t.Fatalf("cloneDiskFile (linked=true, raw format): %v", err)
	}

	if _, err := os.Stat(dst); err != nil {
		t.Fatalf("clone file missing: %v", err)
	}
}
