package user

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"webkvm/internal/models"
)

func strPtr(s string) *string { return &s }

func longRepeat(n int, pattern string) string {
	s := strings.Repeat(pattern, n/len(pattern)+1)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// TestValidatePasswordStrengthPolicy: matriz completa de la política.
func TestValidatePasswordStrengthPolicy(t *testing.T) {
	cases := []struct {
		name  string
		pw    string
		valid bool
	}{
		{"11 chars, insuficiente", "abcDEFG1$23", false},
		{"12 chars, 4 clases", "xkU7!mQa$2Zp", true},
		{"12 chars, 1 clase", "abcdefghijkl", false},
		{"12 chars, 2 clases (solo minus+dig)", "abcdefgh1234", false},
		{"12 chars, 2 clases (solo mayus+minus)", "Abcdefghijkl", false},
		{"12 chars, 3 clases", "Abcdefgh1234", true},
		{"15 chars, 2 clases (solo minus+dig)", "abcdefghj1q2w3e", false},
		{"16 chars, 1 clase (liberado)", "abcdefghijklmnop", true},
		{"denylist literal", "password123", false},
		{"denylist case-insensitiva", "PASSWORD123", false},
		{"denylist decorado con dígitos", "freedom123", false},
		{"denylist decorado con símbolos", "freedom!!!", false},
		{"cola alfabética (igual 2 clases)", "password1234xyz", false},
		{"valida larga variada", "kR9#zLq2vN8$mWx4aJK", true},
		{"128 OK", longRepeat(128, "xkU7!mQa$Zp"), true},
		{"129 overflow", longRepeat(129, "xK9#"), false},
	}
	for _, c := range cases {
		err := validatePasswordStrength(c.pw)
		if c.valid && err != nil {
			t.Errorf("%s: expected valid, got %v", c.name, err)
		}
		if !c.valid && err == nil {
			t.Errorf("%s: expected rejection", c.name)
		}
	}
}

// Denylist embebida poblada y cubriendo los clásicos.
func TestDenylistPopulated(t *testing.T) {
	if len(commonPasswords) < 500 {
		t.Fatalf("denylist too small: %d entries", len(commonPasswords))
	}
	for _, p := range []string{"password", "123456", "qwerty", "letmein", "123456789"} {
		if _, ok := commonPasswords[p]; !ok {
			t.Fatalf("denylist missing %q", p)
		}
	}
}

// seedStore crea el store con un admin-password.initial "seed-secret"
// pre-escrito; NewStore lo usa como contraseña inicial del admin.
// seedStore crea un store nuevo; NewStore con users vacíos genera un
// admin aleatorio y lo escribe en admin-password.initial. Devolvemos el
// valor REAL (post-generación) para usarlo como password actual.
func seedStore(t *testing.T) (*Store, string, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if v, _ := s.MustChangePassword("admin"); !v {
		t.Fatalf("seeded admin should require password change")
	}
	initial := filepath.Join(dir, "admin-password.initial")
	b, err := os.ReadFile(initial)
	if err != nil {
		t.Fatalf("seed password file missing: %v", err)
	}
	return s, dir, strings.TrimSpace(string(b))
}

// Cambio exitoso de admin → ficheros iniciales destruidos.
func TestInitialFilesRemovedAfterPersistence(t *testing.T) {
	s, dir, seed := seedStore(t)
	initial := filepath.Join(dir, "admin-password.initial")
	if err := s.ChangePassword("admin", seed, "NewStr0ng!Pass#2026"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}
	if _, err := os.Stat(initial); !os.IsNotExist(err) {
		t.Fatalf("admin-password.initial survived successful change")
	}
}

// Cambios que fallan (policy / old incorrecta) NO tocan los ficheros.
func TestInitialFilesKeptOnFailedChange(t *testing.T) {
	s, dir, seed := seedStore(t)
	initial := filepath.Join(dir, "admin-password.initial")
	if err := s.ChangePassword("admin", seed, "password123"); err == nil {
		t.Fatalf("weak password accepted")
	}
	if err := s.ChangePassword("admin", "wrong-old", "NewStr0ng!Pass#2026"); err == nil {
		t.Fatalf("wrong old password accepted")
	}
	if _, err := os.Stat(initial); err != nil {
		t.Fatalf("initial destroyed by a failed change: %v", err)
	}
}

// Reset de admin vía Update: mismo comportamiento transaccional.
func TestInitialFilesRemovedAfterAdminReset(t *testing.T) {
	s, dir, _ := seedStore(t)
	initial := filepath.Join(dir, "admin-password.initial")
	if _, err := s.Update("admin", models.UpdateUserRequest{Password: strPtr("ResetStrong!Pass9")}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if _, err := os.Stat(initial); !os.IsNotExist(err) {
		t.Fatalf("initial file survived admin reset")
	}
}

// Cambios de contraseña de NO-admin no tocan los ficheros del inicial.
func TestInitialFilesUntouchedForNonAdmin(t *testing.T) {
	s, dir, _ := seedStore(t)
	initial := filepath.Join(dir, "admin-password.initial")
	if _, err := s.Create(models.CreateUserRequest{Username: "op1", Password: "OpUser!Secure1", Role: "operator"}); err != nil {
		t.Fatalf("Create op1: %v", err)
	}
	if err := s.ChangePassword("op1", "OpUser!Secure1", "V2Operat0r!Pass9"); err != nil {
		t.Fatalf("ChangePassword op1: %v", err)
	}
	if _, err := os.Stat(initial); err != nil {
		t.Fatalf("non-admin change destroyed admin initial files: %v", err)
	}
}

// Los no-admin pasan por la misma policy (grandfather excluye: solo hashes
// nuevos se validan).
func TestPolicyAppliesToNonAdmins(t *testing.T) {
	s, _, seed := seedStore(t)
	if err := s.ChangePassword("admin", seed, "KubernetesAdmin2026!"); err != nil {
		t.Fatalf("baseline admin change: %v", err)
	}
	if _, err := s.Create(models.CreateUserRequest{Username: "opA", Password: "password123", Role: "operator"}); err == nil {
		t.Fatalf("weak password accepted for new operator")
	}
}
