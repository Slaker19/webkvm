package incus

import "testing"

func TestSimplestreamsArchitectureFromIncus(t *testing.T) {
	for input, want := range map[string]string{
		"x86_64":  "amd64",
		"amd64":   "amd64",
		"aarch64": "arm64",
		"arm64":   "arm64",
	} {
		got, err := simplestreamsArchitecture(input)
		if err != nil || got != want {
			t.Errorf("simplestreamsArchitecture(%q) = %q, %v, want %q", input, got, err, want)
		}
	}
	if _, err := simplestreamsArchitecture("mips64"); err == nil {
		t.Fatal("unknown Incus architecture was accepted")
	}
}

func TestParseSimplestreamsProductsForArchitecture(t *testing.T) {
	products := []string{
		"ubuntu:noble:amd64:default",
		"ubuntu:noble:arm64:default",
	}
	got := parseSimplestreamsProductsForArch(products, "arm64")
	if len(got) != 1 || got[0].Ref != "images:ubuntu/noble" {
		t.Fatalf("arm64 image results = %+v", got)
	}
}
