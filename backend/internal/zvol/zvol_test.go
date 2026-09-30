package zvol

import (
	"context"
	"testing"
)

func TestValidName(t *testing.T) {
	valid := []string{
		"tank/vol1",
		"zroot/data/vm-100-disk-0",
		"pool_1/dataset-2/vol.img",
		"mypool/sub/sub2/vol:1",
	}
	for _, name := range valid {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) returned unexpected error: %v", name, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"tank", // must have at least pool/vol
		"/tank/vol",
		"tank/vol/",
		"tank/vol@snap1",
		"tank/vol#bookmark",
		"tank/../etc",
		"tank/vol; rm -rf /",
		"tank/vol|cat",
		"tank/vol$(whoami)",
		"tank/vol with spaces",
		"tank/vol\nname",
	}
	for _, name := range invalid {
		if err := ValidName(name); err == nil {
			t.Errorf("ValidName(%q) expected error, got nil", name)
		}
	}
}

func TestDevicePath(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"tank/vol1", "/dev/zvol/tank/vol1"},
		{"/dev/zvol/tank/vol1", "/dev/zvol/tank/vol1"},
		{"rpool/data/vm-disk", "/dev/zvol/rpool/data/vm-disk"},
	}
	for _, c := range cases {
		if got := DevicePath(c.in); got != c.want {
			t.Errorf("DevicePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseZfsGetOutput(t *testing.T) {
	raw := []byte("type\tvolume\nvolsize\t10737418240\nused\t1048576\n")
	info, err := parseZfsGetOutput("tank/vm-disk-1", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Name != "tank/vm-disk-1" {
		t.Errorf("Name = %q, want %q", info.Name, "tank/vm-disk-1")
	}
	if info.Pool != "tank" {
		t.Errorf("Pool = %q, want %q", info.Pool, "tank")
	}
	if info.VolSize != 10737418240 {
		t.Errorf("VolSize = %d, want %d", info.VolSize, 10737418240)
	}
	if info.Used != 1048576 {
		t.Errorf("Used = %d, want %d", info.Used, 1048576)
	}
	if info.Device != "/dev/zvol/tank/vm-disk-1" {
		t.Errorf("Device = %q, want %q", info.Device, "/dev/zvol/tank/vm-disk-1")
	}

	// Reject filesystem
	fsRaw := []byte("type\tfilesystem\nvolsize\t0\nused\t1048576\n")
	if _, err := parseZfsGetOutput("tank/dataset", fsRaw); err == nil {
		t.Errorf("expected error for type=filesystem, got nil")
	}

	// Reject missing dataset
	if _, err := parseZfsGetOutput("tank/notfound", []byte("")); err == nil {
		t.Errorf("expected error for empty output, got nil")
	}
}

func TestParseZfsListOutput(t *testing.T) {
	raw := []byte("tank/vol1\t10737418240\t1048576\ntank/vol2\t21474836480\t2097152\n")
	zvols := parseZfsListOutput(raw)
	if len(zvols) != 2 {
		t.Fatalf("expected 2 zvols, got %d", len(zvols))
	}
	if zvols[0].Name != "tank/vol1" || zvols[0].VolSize != 10737418240 || zvols[0].Pool != "tank" {
		t.Errorf("unexpected zvol 0: %+v", zvols[0])
	}
	if zvols[1].Name != "tank/vol2" || zvols[1].VolSize != 21474836480 || zvols[1].Pool != "tank" {
		t.Errorf("unexpected zvol 1: %+v", zvols[1])
	}
}

func TestList_FallbackWhenNoZFS(t *testing.T) {
	// Should return empty list, no error if zfs is not available or mock env
	zvols, err := List(context.Background())
	if err != nil {
		t.Fatalf("List() returned error: %v", err)
	}
	if zvols == nil {
		t.Fatalf("List() returned nil slice")
	}
}

func TestResolve_InvalidName(t *testing.T) {
	_, err := Resolve(context.Background(), "invalid@snap")
	if err == nil {
		t.Fatalf("expected error for invalid name, got nil")
	}
}
