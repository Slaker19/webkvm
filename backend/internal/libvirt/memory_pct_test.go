package libvirt

import (
	"math"
	"testing"
)

// F1-01: el porcentaje de RAM de una VM se calculaba con
//
//	used := total - available   // ambos uint64
//	if used < 0 { ... }         // imposible: un uint64 nunca es negativo
//
// Cuando available > total la resta NO da un negativo: da la vuelta y se
// convierte en un numero astronomico (~1.8e19). El guard "used < 0" que
// pretendia cubrir ese caso es codigo muerto -- el compilador lo sabe --
// asi que nunca se usaba el respaldo (total - unused), y el porcentaje
// salia desbordado. Al recortarlo a [0,100] el resultado es siempre 100:
// la VM aparece con la RAM al maximo de forma permanente.
//
// available > total es normal, no una anomalia: ACTUAL_BALLOON es la
// memoria asignada AHORA al globo, y el agente invitado informa de
// AVAILABLE incluyendo cache reclamable. Un globo deshinchandose deja
// esas dos cifras descompasadas durante varios segundos.

func TestMemoryUsedPct_AvailableMayorQueTotal_NoSeDesborda(t *testing.T) {
	// El caso real: el globo se ha encogido a 2 GiB pero el agente aun
	// informa de 3 GiB disponibles. unused dice que se usan 512 MiB.
	const giB = 1024 * 1024 // las stats de libvirt vienen en KiB
	pct := memoryUsedPct(2*giB /*total*/, 3*giB /*available*/, 1536*1024 /*unused*/, 0)

	if pct > 100 || pct < 0 {
		t.Fatalf("porcentaje fuera de rango: %v", pct)
	}
	// Con el respaldo correcto: (2 GiB - 1,5 GiB) / 2 GiB = 25 %.
	if math.Abs(pct-25) > 0.01 {
		t.Fatalf("se esperaba el respaldo (total-unused) = 25%%, got %v%%\n"+
			"Si sale 100 es el desbordamiento del uint64: la resta dio la vuelta.", pct)
	}
}

// El caso normal debe seguir calculandose igual que antes.
func TestMemoryUsedPct_CasoNormal(t *testing.T) {
	const giB = 1024 * 1024
	pct := memoryUsedPct(4*giB, 1*giB, 0, 0)
	if math.Abs(pct-75) > 0.01 {
		t.Fatalf("(4 GiB - 1 GiB) / 4 GiB deberia ser 75%%, got %v%%", pct)
	}
}

// Sin agente invitado no hay available ni unused: se cae a RSS.
func TestMemoryUsedPct_SinAgenteUsaRSS(t *testing.T) {
	const giB = 1024 * 1024
	pct := memoryUsedPct(4*giB, 0, 0, 2*giB)
	if math.Abs(pct-50) > 0.01 {
		t.Fatalf("sin agente deberia usar RSS (2/4 = 50%%), got %v%%", pct)
	}
}

// Sin ninguna senal utilizable devuelve 0 para que el llamante aplique su
// ultimo respaldo (configurada vs asignada), en vez de inventar una cifra.
func TestMemoryUsedPct_SinDatos(t *testing.T) {
	if pct := memoryUsedPct(0, 0, 0, 0); pct != 0 {
		t.Fatalf("sin datos deberia devolver 0, got %v", pct)
	}
}

// El otro extremo del mismo fallo: unused tambien puede superar a total.
// Si los dos respaldos fallan, no hay que devolver basura.
func TestMemoryUsedPct_AvailableYUnusedMayoresQueTotal(t *testing.T) {
	const giB = 1024 * 1024
	pct := memoryUsedPct(2*giB, 3*giB, 4*giB, 0)
	if pct < 0 || pct > 100 {
		t.Fatalf("porcentaje fuera de rango: %v", pct)
	}
}
