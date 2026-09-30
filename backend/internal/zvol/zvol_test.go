package zvol

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func stubZFS(t *testing.T, fn func(args ...string) ([]byte, error)) {
	t.Helper()
	orig := runZFS
	runZFS = func(_ context.Context, args ...string) ([]byte, error) { return fn(args...) }
	t.Cleanup(func() { runZFS = orig })
}

func stubDev(t *testing.T, err error) {
	t.Helper()
	orig := statDev
	statDev = func(string) error { return err }
	t.Cleanup(func() { statDev = orig })
}

func TestValidateName(t *testing.T) {
	good := []string{"tank/vm1", "tank/vms/web-01", "rpool/data/vm-100-disk-0", "Pool_1/a.b:c"}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{
		"", "tank", "/tank/vm1", "tank/", "tank//vm", "tank/../etc",
		"tank/./vm", "tank/vm@snap", "tank/vm#bm", "-o/vm", "1tank/vm",
		"tank/vm 1", "tank/vm'x", "tank/vm\n", strings.Repeat("a/", 200),
	}
	for _, n := range bad {
		if err := ValidateName(n); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", n)
		}
	}
}

func TestNameFromDev(t *testing.T) {
	if n, ok := NameFromDev("/dev/zvol/tank/vm1"); !ok || n != "tank/vm1" {
		t.Errorf("got %q %v", n, ok)
	}
	for _, p := range []string{"/dev/sda", "/dev/zvol/../sda", "/var/lib/libvirt/images/a.qcow2", ""} {
		if _, ok := NameFromDev(p); ok {
			t.Errorf("NameFromDev(%q) ok, want !ok", p)
		}
	}
}

func TestGetVolume(t *testing.T) {
	stubDev(t, nil)
	stubZFS(t, func(args ...string) ([]byte, error) {
		if args[0] != "get" || args[len(args)-1] != "tank/vm1" {
			t.Fatalf("unexpected args %v", args)
		}
		return []byte("type\tvolume\nvolsize\t21474836480\nlogicalreferenced\t12288\n"), nil
	})
	v, err := Get(context.Background(), "tank/vm1")
	if err != nil {
		t.Fatal(err)
	}
	if v.Dev != "/dev/zvol/tank/vm1" || v.SizeBytes != 21474836480 || v.HasData {
		t.Errorf("unexpected volume %+v", v)
	}
}

func TestGetHasData(t *testing.T) {
	stubDev(t, nil)
	stubZFS(t, func(...string) ([]byte, error) {
		return []byte("type\tvolume\nvolsize\t1073741824\nlogicalreferenced\t524288000\n"), nil
	})
	v, err := Get(context.Background(), "tank/vm1")
	if err != nil || !v.HasData {
		t.Fatalf("want HasData, got %+v %v", v, err)
	}
}

func TestGetRejectsFilesystem(t *testing.T) {
	stubDev(t, nil)
	stubZFS(t, func(...string) ([]byte, error) {
		return []byte("type\tfilesystem\nvolsize\t-\nlogicalreferenced\t1000\n"), nil
	})
	if _, err := Get(context.Background(), "tank/data"); !errors.Is(err, ErrNotVolume) {
		t.Fatalf("want ErrNotVolume, got %v", err)
	}
}

func TestGetInvalidNameNeverRunsZFS(t *testing.T) {
	stubZFS(t, func(...string) ([]byte, error) {
		t.Fatal("zfs must not run for an invalid name")
		return nil, nil
	})
	if _, err := Get(context.Background(), "tank/../../etc"); err == nil {
		t.Fatal("want error")
	}
}

func TestGetMissingDevice(t *testing.T) {
	stubDev(t, errors.New("no link"))
	stubZFS(t, func(...string) ([]byte, error) {
		return []byte("type\tvolume\nvolsize\t1024\nlogicalreferenced\t0\n"), nil
	})
	if _, err := Get(context.Background(), "tank/vm1"); err == nil {
		t.Fatal("want error when /dev/zvol link is missing")
	}
}

func TestList(t *testing.T) {
	stubZFS(t, func(args ...string) ([]byte, error) {
		return []byte("tank/a\t1073741824\t4096\ntank/b\t2147483648\t999999999\nbroken-line\n"), nil
	})
	vols, err := List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 || vols[0].Name != "tank/a" || vols[0].HasData || !vols[1].HasData {
		t.Fatalf("unexpected %+v", vols)
	}
}

func TestListWithoutZFS(t *testing.T) {
	stubZFS(t, func(...string) ([]byte, error) { return nil, ErrZFSUnavailable })
	vols, err := List(context.Background())
	if err != nil || vols == nil || len(vols) != 0 {
		t.Fatalf("want empty list, got %v %v", vols, err)
	}
}
