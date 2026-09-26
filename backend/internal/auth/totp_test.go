package auth

import (
	"testing"
)

func TestTOTPGenerationAndValidation(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret failed: %v", err)
	}
	if len(secret) < 16 {
		t.Fatalf("Secret too short: %s", secret)
	}

	backupCodes := GenerateBackupCodes(8)
	if len(backupCodes) != 8 {
		t.Fatalf("Expected 8 backup codes, got %d", len(backupCodes))
	}

	url := BuildOTPAuthURL("WebKVM", "admin", secret)
	if url == "" {
		t.Fatalf("BuildOTPAuthURL returned empty")
	}

	// Validate bad codes
	if ValidateTOTP(secret, "000000") && ValidateTOTP(secret, "123456") && ValidateTOTP(secret, "999999") {
		// Extremely unlikely that all 3 arbitrary codes are valid
		t.Errorf("ValidateTOTP accepted invalid codes")
	}
}
