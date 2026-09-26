package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Reproduce el fallo C-3.
//
// Un token de API no lleva claims JWT, asi que la rama del validador de
// tokens en Middleware no escribe HeaderTokenEpoch. SessionEnforcer lee ese
// header ausente como epoca 0 y lo compara con la epoca REAL de la cuenta.
// En cuanto un admin toca la cuenta (cambio de rol, revoke-sessions, reset
// de contrasena...) la epoca pasa a 1 y ya nunca vuelve a valer 0, asi que
// TODOS los tokens de API de ese usuario quedan muertos de forma permanente
// — incluidos los emitidos DESPUES del cambio, que deberian ser validos.
//
// Encadena los dos middlewares igual que router.go: Middleware -> SessionEnforcer.
// epocaToken es la epoca sellada en el token al emitirlo; epocaCuenta es la
// que tiene la cuenta ahora mismo en el user store.
func cadenaAuth(t *testing.T, epocaToken, epocaCuenta int, rolActual string) http.Handler {
	t.Helper()

	m := NewManager("secreto-de-pruebas-suficientemente-largo-123456", nil)
	t.Cleanup(m.Close)
	// El validador imita a cmd/server/main.go.
	m.SetTokenValidator(func(plain string) (string, string, int, error) {
		if plain != "wvmb_token-valido" {
			return "", "", 0, errors.New("invalid token")
		}
		return "alice", rolActual, epocaToken, nil
	})

	lookup := func(username string) (bool, int, bool, error) {
		return false, epocaCuenta, true, nil
	}

	return m.Middleware(SessionEnforcer(lookup, "/api/auth/", "/api/users/me/password")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}),
	))
}

func peticionConToken(h http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/vms", nil)
	req.Header.Set("Authorization", "Bearer wvmb_token-valido")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// Caso de control: cuenta intacta (epoca 0). El token debe funcionar.
// Esto ya pasa hoy; su unico papel es demostrar que el arnes es correcto y
// que el fallo del siguiente test no viene de un test mal montado.
func TestTokenAPI_CuentaIntacta_Funciona(t *testing.T) {
	rr := peticionConToken(cadenaAuth(t, 0, 0, "operator"))
	if rr.Code != http.StatusOK {
		t.Fatalf("una cuenta sin tocar deberia aceptar su token de API; got %d: %s",
			rr.Code, rr.Body.String())
	}
}

// EL FALLO: la cuenta ha sido tocada por un admin alguna vez (epoca 1), y el
// usuario emite un token NUEVO despues. Ese token deberia funcionar: se creo
// despues de la revocacion, no es una sesion vieja que haya que matar.
func TestTokenAPI_EmitidoTrasBumpDeEpoca_DebeFuncionar(t *testing.T) {
	// Emitido con la cuenta ya en epoca 1: token y cuenta coinciden.
	rr := peticionConToken(cadenaAuth(t, 1, 1, "viewer"))
	if rr.Code != http.StatusOK {
		t.Fatalf("un token emitido DESPUES del bump de epoca debe ser valido, "+
			"pero la cadena de auth lo rechaza con %d: %s\n"+
			"Causa: la rama del validador de tokens en Middleware no escribe %s, "+
			"asi que SessionEnforcer lo lee como epoca 0 y no coincide con la epoca 1 de la cuenta.",
			rr.Code, rr.Body.String(), HeaderTokenEpoch)
	}
}

// Una vez tocada la cuenta, la epoca nunca vuelve a 0: el fallo es permanente
// y crece con cada accion administrativa. Sin arreglo, la funcion de tokens de
// API queda inutilizada para siempre en esa cuenta.
func TestTokenAPI_FalloEsPermanente(t *testing.T) {
	for _, epoca := range []int{1, 2, 5} {
		rr := peticionConToken(cadenaAuth(t, epoca, epoca, "operator"))
		if rr.Code != http.StatusOK {
			t.Fatalf("epoca %d: token de API rechazado (%d) — el fallo persiste "+
				"tras cada accion administrativa sobre la cuenta", epoca, rr.Code)
		}
	}
}

// La otra mitad del contrato: sellar la epoca no debe romper la revocacion.
// Un token emitido ANTES del bump (epoca 1) contra una cuenta que ya va por
// la 2 tiene que morir, igual que muere un JWT viejo.
func TestTokenAPI_RevocacionSigueFuncionando(t *testing.T) {
	rr := peticionConToken(cadenaAuth(t, 1, 2, "operator"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("un token anterior al bump de epoca debe quedar revocado, "+
			"pero la cadena lo acepto con %d: %s", rr.Code, rr.Body.String())
	}
}
