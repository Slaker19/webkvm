package incus

import (
	"regexp"
	"testing"
)

// The catalog descriptions are shown verbatim in every UI language, so they
// must be English (the API's neutral language), never Spanish.
var spanishMarkers = regexp.MustCompile(`(?i)[áéíóúñ¿¡]|\b(para|desde|con|basada?|distribución|plantilla|sistema operativo|estable|siempre|rama|versión|bifurcación)\b`)

func TestIncusCatalogDescriptionsAreEnglish(t *testing.T) {
	products := []string{}
	for _, p := range []string{
		"ubuntu:resolute", "ubuntu:noble", "ubuntu:jammy", "ubuntu:questing", "ubuntu:oracular",
		"debian:forky", "debian:trixie", "debian:bookworm", "debian:bullseye", "debian:sid", "debian:buster",
		"alpine:3.22", "fedora:43", "fedora:41", "fedora:39", "rockylinux:9", "almalinux:9",
		"archlinux:current", "kali:current", "nixos:25.05", "opensuse:tumbleweed", "opensuse:15.6",
		"openwrt:24.10", "voidlinux:current", "busybox:1.36.1", "gentoo:openrc", "oracle:9",
		"centos:10-Stream", "devuan:daedalus", "mint:wilma", "freebsd:14.2", "netbsd:10.1", "plamo:8.x",
	} {
		products = append(products, p+":amd64:default")
	}
	items := append(defaultIncusCatalog(), parseSimplestreamsProductsForArch(products, "amd64")...)
	if len(items) < 40 {
		t.Fatalf("only %d catalog items generated", len(items))
	}
	for _, it := range items {
		if spanishMarkers.MatchString(it.Description) || spanishMarkers.MatchString(it.Label) {
			t.Errorf("%s: non-English catalog text %q / %q", it.Ref, it.Label, it.Description)
		}
	}
}
