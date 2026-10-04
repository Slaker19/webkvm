package netguard

import (
	"testing"
)

func TestExtractSSHFailureIP(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		expected string
	}{
		{
			name:     "standard password failure",
			line:     "Failed password for root from 198.51.100.20 port 54321 ssh2",
			expected: "198.51.100.20",
		},
		{
			name:     "invalid user password failure",
			line:     "Failed password for invalid user admin from 203.0.113.88 port 41234 ssh2",
			expected: "203.0.113.88",
		},
		{
			name:     "invalid user check",
			line:     "Invalid user test from 198.51.100.99 port 39182",
			expected: "198.51.100.99",
		},
		{
			name:     "pam auth failure with rhost",
			line:     "pam_unix(sshd:auth): authentication failure; logname= uid=0 euid=0 tty=ssh ruser= rhost=198.51.100.15 user=root",
			expected: "198.51.100.15",
		},
		{
			name:     "disconnected preauth",
			line:     "Disconnected from authenticating user root 198.51.100.22 port 65070 [preauth]",
			expected: "198.51.100.22",
		},
		{
			name:     "connection closed preauth",
			line:     "Connection closed by authenticating user root 198.51.100.23 port 65070 [preauth]",
			expected: "198.51.100.23",
		},
		{
			name:     "accepted password should not match",
			line:     "Accepted password for alvin from 192.168.1.100 port 52608 ssh2",
			expected: "",
		},
		{
			name:     "unrelated systemd line",
			line:     "Starting OpenBSD Secure Shell server...",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ExtractSSHFailureIP(tc.line)
			if got != tc.expected {
				t.Errorf("ExtractSSHFailureIP(%q) = %q, want %q", tc.line, got, tc.expected)
			}
		})
	}
}
