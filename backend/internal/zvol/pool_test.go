package zvol

import (
	"context"
	"testing"
)

func TestValidPoolName(t *testing.T) {
	valid := []string{
		"tank",
		"rpool",
		"pool_1",
		"pool-data",
		"tank.backup",
		"fast:nvme",
	}
	for _, name := range valid {
		if err := ValidPoolName(name); err != nil {
			t.Errorf("ValidPoolName(%q) returned unexpected error: %v", name, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"1pool",            // cannot begin with a digit
		"tank/dataset",     // no slashes in pool name
		"tank@snap",        // no @
		"tank#bookmark",    // no #
		"tank..pool",       // no ..
		"tank; rm -rf /",   // no metachars
		"mirror",           // reserved
		"raidz",            // reserved
		"raidz1",           // reserved
		"raidz2",           // reserved
		"spare",            // reserved
		"log",              // reserved
		"cache",            // reserved
		"tank with spaces", // no spaces
	}
	for _, name := range invalid {
		if err := ValidPoolName(name); err == nil {
			t.Errorf("ValidPoolName(%q) expected error, got nil", name)
		}
	}
}

func TestValidVolumeName(t *testing.T) {
	valid := []string{
		"vm-100-disk-0",
		"vol1",
		"vol_backup",
		"vol.img",
		"vol:1",
	}
	for _, name := range valid {
		if err := ValidVolumeName(name); err != nil {
			t.Errorf("ValidVolumeName(%q) returned unexpected error: %v", name, err)
		}
	}

	invalid := []string{
		"",
		"   ",
		"tank/vol", // dataset path with slash not allowed for volume name segment
		"vol@snap",
		"vol#bm",
		"vol..name",
		"vol with spaces",
		"vol;cat",
	}
	for _, name := range invalid {
		if err := ValidVolumeName(name); err == nil {
			t.Errorf("ValidVolumeName(%q) expected error, got nil", name)
		}
	}
}

func TestParseZpoolListOutput(t *testing.T) {
	raw := []byte("tank\t107374182400\t10737418240\t96636764160\tONLINE\nrpool\t536870912000\t53687091200\t483183820800\tONLINE\n")
	pools := parseZpoolListOutput(raw)
	if len(pools) != 2 {
		t.Fatalf("expected 2 pools, got %d", len(pools))
	}
	if pools[0].Name != "tank" || pools[0].Size != 107374182400 || pools[0].Health != "ONLINE" {
		t.Errorf("unexpected pool 0: %+v", pools[0])
	}
	if pools[1].Name != "rpool" || pools[1].Allocated != 53687091200 {
		t.Errorf("unexpected pool 1: %+v", pools[1])
	}
}

func TestListPools_FallbackWhenNoZFS(t *testing.T) {
	pools, err := ListPools(context.Background())
	if err != nil {
		t.Fatalf("ListPools() returned error: %v", err)
	}
	if pools == nil {
		t.Fatalf("ListPools() returned nil slice")
	}
}
