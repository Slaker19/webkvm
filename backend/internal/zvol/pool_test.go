package zvol

import (
	"context"
	"strings"
	"testing"

	"webkvm/internal/sysutil"
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

func TestListPools_MockRunner(t *testing.T) {
	mock := &sysutil.MockCommandRunner{
		Handler: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			if name == "zpool" && len(args) > 0 && args[0] == "list" {
				return []byte("tank\t107374182400\t10737418240\t96636764160\tONLINE\n"), nil
			}
			if name == "zpool" && len(args) > 0 && args[0] == "status" {
				return []byte("  pool: tank\n state: ONLINE\nconfig:\n\tNAME\tSTATE\n\ttank\tONLINE\n\t  /dev/sdb\tONLINE\n"), nil
			}
			return nil, nil
		},
	}

	SetRunner(mock)
	defer SetRunner(nil)

	if !IsPoolAvailable() {
		t.Errorf("IsPoolAvailable() should be true when using MockCommandRunner")
	}

	pools, err := ListPools(context.Background())
	if err != nil {
		t.Fatalf("ListPools() unexpected error: %v", err)
	}
	if len(pools) != 1 {
		t.Fatalf("expected 1 pool, got %d", len(pools))
	}
	if pools[0].Name != "tank" || pools[0].Health != "ONLINE" {
		t.Errorf("unexpected pool: %+v", pools[0])
	}
	if len(pools[0].Devices) != 1 || pools[0].Devices[0] != "/dev/sdb" {
		t.Errorf("expected device /dev/sdb, got: %v", pools[0].Devices)
	}
}

func TestCreatePool_MockRunner(t *testing.T) {
	var executed []string
	mock := &sysutil.MockCommandRunner{
		Handler: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			executed = append([]string{name}, args...)
			return []byte(""), nil
		},
	}

	SetRunner(mock)
	defer SetRunner(nil)

	err := CreatePool(context.Background(), "tank", "mirror", []string{"/dev/sdb", "/dev/sdc"})
	if err != nil {
		t.Fatalf("CreatePool() unexpected error: %v", err)
	}
	cmdStr := strings.Join(executed, " ")
	if !strings.Contains(cmdStr, "zpool create -f tank mirror /dev/sdb /dev/sdc") {
		t.Errorf("unexpected command executed: %q", cmdStr)
	}
}

func TestScrubPool_MockRunner(t *testing.T) {
	var executed []string
	mock := &sysutil.MockCommandRunner{
		Handler: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			executed = append([]string{name}, args...)
			return []byte(""), nil
		},
	}

	SetRunner(mock)
	defer SetRunner(nil)

	err := ScrubPool(context.Background(), "tank", "start")
	if err != nil {
		t.Fatalf("ScrubPool() unexpected error: %v", err)
	}
	cmdStr := strings.Join(executed, " ")
	if !strings.Contains(cmdStr, "zpool scrub tank") {
		t.Errorf("unexpected scrub command: %q", cmdStr)
	}
}
