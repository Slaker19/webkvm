package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"webkvm/internal/auth"
	"webkvm/internal/config"
	"webkvm/internal/models"
	"webkvm/internal/user"
)

// TestLogin2FA_ThrottlesPerAccount: wrong TOTP codes count against the
// login limiter like wrong passwords do (5 fails -> 429), so a 6-digit
// code cannot be brute-forced within the MFA token lifetime.
func TestLogin2FA_ThrottlesPerAccount(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")
	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := us.Create(models.CreateUserRequest{Username: "tfa", Password: "Str0ng-Pass#2026", Role: models.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := us.EnableTOTP("tfa", secret, []string{"backup-1"}); err != nil {
		t.Fatal(err)
	}

	mgr := auth.NewManager("test-secret-2fa-throttle", nil)
	mgr.SetSecureCookies(false)
	t.Cleanup(mgr.Close)

	h := &Handler{
		auth:         mgr,
		userStore:    us,
		loginLimiter: auth.NewLoginRateLimiter(),
		cfg:          &config.Config{},
	}

	mfa, err := mgr.GenerateMFAToken("tfa")
	if err != nil {
		t.Fatal(err)
	}
	try := func(code string) int {
		body := `{"mfa_token":` + strconv.Quote(mfa) + `,"code":` + strconv.Quote(code) + `}`
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login/2fa", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		h.Login2FA(rr, req)
		return rr.Code
	}
	for i := 0; i < 5; i++ {
		if code := try("000000"); code != http.StatusUnauthorized {
			t.Fatalf("bad code %d: status = %d, want 401", i, code)
		}
	}
	if code := try("000000"); code != http.StatusTooManyRequests {
		t.Fatalf("6th bad code: status = %d, want 429", code)
	}
}
