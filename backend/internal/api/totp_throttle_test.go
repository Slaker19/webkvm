package api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestEnable2FA_RequiresPasswordIfAlreadyEnabled(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WEBKVM_ADMIN_PASSWORD", "Str0ng-Pass#2026")
	us, err := user.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := us.Create(models.CreateUserRequest{Username: "tfa2", Password: "Original-Pass#2026", Role: models.RoleOperator}); err != nil {
		t.Fatal(err)
	}
	secret, err := auth.GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := us.EnableTOTP("tfa2", secret, []string{"backup-1"}); err != nil {
		t.Fatal(err)
	}

	h := &Handler{
		userStore: us,
	}

	newSecret, _ := auth.GenerateTOTPSecret()
	newCode, _ := auth.TOTPCodeAt(newSecret, time.Now())

	// 1. Attempt to re-enable without password -> should be 400 Bad Request
	bodyNoPass := `{"secret":` + strconv.Quote(newSecret) + `,"code":` + strconv.Quote(newCode) + `}`
	req1 := httptest.NewRequest(http.MethodPost, "/api/auth/2fa/enable", strings.NewReader(bodyNoPass))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-User", "tfa2")
	rr1 := httptest.NewRecorder()
	h.Enable2FA(rr1, req1)
	if rr1.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when password is missing, got %d", rr1.Code)
	}

	// 2. Attempt with wrong password -> should be 401 Unauthorized
	bodyWrongPass := `{"secret":` + strconv.Quote(newSecret) + `,"code":` + strconv.Quote(newCode) + `,"password":"WrongPassword123"}`
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/2fa/enable", strings.NewReader(bodyWrongPass))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-User", "tfa2")
	rr2 := httptest.NewRecorder()
	h.Enable2FA(rr2, req2)
	if rr2.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for wrong password, got %d", rr2.Code)
	}

	// 3. Attempt with correct password -> should succeed 200 OK
	bodyCorrect := `{"secret":` + strconv.Quote(newSecret) + `,"code":` + strconv.Quote(newCode) + `,"password":"Original-Pass#2026"}`
	req3 := httptest.NewRequest(http.MethodPost, "/api/auth/2fa/enable", strings.NewReader(bodyCorrect))
	req3.Header.Set("Content-Type", "application/json")
	req3.Header.Set("X-User", "tfa2")
	rr3 := httptest.NewRecorder()
	h.Enable2FA(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("expected 200 OK with correct password, got %d: %s", rr3.Code, rr3.Body.String())
	}
}
