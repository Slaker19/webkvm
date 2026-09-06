package api

import (
	"testing"

	"webkvm/internal/models"
)

// TestNetworkInList: verifica la lógica pura de validación de red
// (V12-CAT-08). El handler la usa para rechazar con 400 un deploy con
// red inexistente ANTES de crear el job.
func TestNetworkInList(t *testing.T) {
	nets := []models.Network{
		{Name: "default"},
		{Name: "vm-bridge"},
		{Name: "direct-vlan"},
	}
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"existe default", "default", true},
		{"existe vm-bridge", "vm-bridge", true},
		{"inexistente", "no-such-net", false},
		{"vacio", "", false},
		{"case-sensitive", "Default", false},
	}
	for _, c := range cases {
		got := networkInList(c.in, nets)
		if got != c.want {
			t.Errorf("%s: networkInList(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// TestDeployDiskCandidates y locks ya cubren el resto; este test protege
// el contrato de deploy de red vacía = permitida (el job resuelve la red
// por defecto), mientras un nombre inválido se rechaza.
func TestNetworkEmptyIsAllowed(t *testing.T) {
	if networkInList("", []models.Network{{Name: "default"}}) {
		t.Fatalf("empty network must not be treated as a real network")
	}
}
