package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// GenerateTOTPSecret returns a 20-byte cryptographically random secret encoded in standard Base32 (no padding).
func GenerateTOTPSecret() (string, error) {
	buf := make([]byte, 20)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), nil
}

// GenerateBackupCodes creates n random 8-character alphanumeric backup codes.
func GenerateBackupCodes(n int) []string {
	if n <= 0 {
		n = 8
	}
	codes := make([]string, n)
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	for i := 0; i < n; i++ {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		code := make([]byte, 8)
		for j := 0; j < 8; j++ {
			code[j] = charset[int(b[j])%len(charset)]
		}
		codes[i] = string(code[:4]) + "-" + string(code[4:])
	}
	return codes
}

// BuildOTPAuthURL creates a standard otpauth:// URL for authenticator apps.
func BuildOTPAuthURL(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return fmt.Sprintf("otpauth://totp/%s?%s", label, q.Encode())
}

// ValidateTOTP validates a 6-digit TOTP code against a Base32 secret for
// current time with window +/- 1 step. It does NOT prevent replay of an
// already-used code; login must use MatchTOTP + the stored last-used step.
func ValidateTOTP(secret, code string) bool {
	_, ok := MatchTOTP(secret, code, time.Now())
	return ok
}

// MatchTOTP checks code against the secret at the +/-1 step window around
// now and returns the matching 30-second step number. Comparison is
// constant-time and always evaluates all three steps. The caller must
// reject a step <= the last one it accepted (replay protection, RFC 6238 5.2).
func MatchTOTP(secret, code string, now time.Time) (step int64, ok bool) {
	cleanSecret := strings.ToUpper(strings.TrimSpace(secret))
	cleanCode := strings.TrimSpace(code)
	if len(cleanCode) != 6 {
		return 0, false
	}

	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(cleanSecret)
	if err != nil {
		key, err = base32.StdEncoding.DecodeString(cleanSecret)
		if err != nil {
			return 0, false
		}
	}

	currentInterval := now.Unix() / 30
	matched := int64(-1)
	// Check intervals t-1, t, t+1 without early exit.
	for _, offset := range []int64{-1, 0, 1} {
		interval := currentInterval + offset
		if subtle.ConstantTimeCompare([]byte(computeHOTP(key, interval)), []byte(cleanCode)) == 1 {
			matched = interval
		}
	}
	if matched < 0 {
		return 0, false
	}
	return matched, true
}

// CurrentTOTPStep returns the 30-second step number of t.
func CurrentTOTPStep(t time.Time) int64 { return t.Unix() / 30 }

func computeHOTP(key []byte, counter int64) string {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(counter))

	mac := hmac.New(sha1.New, key)
	mac.Write(buf)
	h := mac.Sum(nil)

	offset := h[len(h)-1] & 0x0f
	code := (int32(h[offset]&0x7f) << 24) |
		(int32(h[offset+1]&0xff) << 16) |
		(int32(h[offset+2]&0xff) << 8) |
		(int32(h[offset+3] & 0xff))

	otp := code % 1000000
	return fmt.Sprintf("%06d", otp)
}

// TOTPCodeAt returns the code an authenticator would show for secret at t.
// Used by tests and tooling; login paths only validate.
func TOTPCodeAt(secret string, t time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return "", err
	}
	return computeHOTP(key, t.Unix()/30), nil
}
