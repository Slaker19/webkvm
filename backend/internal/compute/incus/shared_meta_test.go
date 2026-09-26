package incus

import (
	"testing"

	"webkvm/internal/models"
)

// The Shared flag travels inside the existing user.webkvm.desc JSON and
// survives an encode/decode round-trip; a meta whose only set field is
// Shared must not be dropped as "all empty".
func TestMetaConfig_SharedRoundTrip(t *testing.T) {
	cfg := map[string]string{}
	applyMetaToConfig(cfg, models.VMMeta{Template: true, Shared: true})
	got := lxdMetaToModel(cfg)
	if !got.Template || !got.Shared {
		t.Fatalf("shared/template perdidos en el round-trip: %+v (config %v)", got, cfg)
	}

	cfg = map[string]string{}
	applyMetaToConfig(cfg, models.VMMeta{Shared: true})
	if _, ok := cfg[metaDescKey]; !ok {
		t.Fatal("una meta con solo Shared no debe borrar la clave de configuración")
	}

	cfg = map[string]string{}
	applyMetaToConfig(cfg, models.VMMeta{})
	if _, ok := cfg[metaDescKey]; ok {
		t.Fatal("una meta vacía debe borrar la clave de configuración")
	}
}
