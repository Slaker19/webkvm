package auth

import "testing"

// TestTicketAllowedPath locks the endpoint scope of the single-use
// terminal ticket. It must only be accepted where a terminal is served
// (VM serial console, host PTY) — never on a generic API route, which
// is what turned a short-lived console ticket into a full session.
func TestTicketAllowedPath(t *testing.T) {
	allowed := []string{
		"/api/host/terminal",
		"/api/events",
		"/api/vms/abc-123/serial",
	}
	for _, p := range allowed {
		if !ticketAllowedPath(p) {
			t.Errorf("ticketAllowedPath(%q) = false, want true", p)
		}
	}

	refused := []string{
		"/api/auth/password",
		"/api/vms",
		"/api/vms/abc-123",
		"/api/vms/abc-123/disks",
		"/api/vms/abc-123/serial/extra",
		"/api/vms/abc-123/serial/",
		"/api/vms/abc-123/console-ticket",
		"/api/storage/volumes",
		"/api/events/stream",
		"/api/host/terminal/../vms",
		"",
	}
	for _, p := range refused {
		if ticketAllowedPath(p) {
			t.Errorf("ticketAllowedPath(%q) = true, want false", p)
		}
	}
}
