package ha

import "testing"

func TestPacemakerFailures(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"No failcounts", false},
		{"", false},
		{"Resource 'kvm-vm' failcount 3", true},
	}
	for _, tc := range cases {
		if got := pacemakerFailures([]byte(tc.in)); got != tc.want {
			t.Fatalf("pacemakerFailures(%q) = %v", tc.in, got)
		}
	}
}

func TestDRBDProblems(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"kvm_data role:Primary\n  disk:UpToDate\n  peer-disk:UpToDate", false},
		{"peer-disk:Diskless peer-client:yes", false},
		{"StandAlone", true},
		{"SplitBrain", true},
	}
	for _, tc := range cases {
		if got := drbdProblems([]byte(tc.in)); got != tc.want {
			t.Fatalf("drbdProblems(%q) = %v", tc.in, got)
		}
	}
}

func TestLowSpace(t *testing.T) {
	if lowSpace(5, 100) != true {
		t.Fatal("5% free must warn")
	}
	if lowSpace(50, 100) != false {
		t.Fatal("50% free must not warn")
	}
	if lowSpace(0, 0) != false {
		t.Fatal("zero total must not warn")
	}
}
