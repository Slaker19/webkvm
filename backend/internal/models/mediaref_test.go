package models

import "testing"

func TestMediaRawURL_RoundTrips(t *testing.T) {
	for _, id := range []string{
		"system:avatar-default.svg",
		"custom:1790124038-1c0e9fb8-wallhaven.jpg",
	} {
		raw := MediaRawURL(id)
		got, ok := MediaIDFromRawURL(raw)
		if !ok {
			t.Fatalf("MediaIDFromRawURL(%q) rejected a URL we built ourselves", raw)
		}
		if got != id {
			t.Errorf("round-trip: got %q, want %q", got, id)
		}
	}
}

// Avatars and covers are rendered by the frontend as <img src>. If an
// arbitrary string could be stored here, a profile field would become a
// stored XSS vector. These are the payloads that must never validate.
func TestIsMediaRawURL_RejectsDangerousValues(t *testing.T) {
	bad := []struct {
		val, why string
	}{
		{"javascript:alert(1)", "script URL"},
		{"data:text/html;base64,PHNjcmlwdD4=", "data URL"},
		{"https://evil.example/pixel.png", "external URL (tracker)"},
		{"//evil.example/x.png", "protocol-relative URL"},
		{"/api/media/../../etc/passwd/raw", "path traversal"},
		{"/api/media/custom:../../../etc/passwd/raw", "traversal inside the id"},
		{"/api/media/custom:a/b/raw", "nested path in the id"},
		{"/api/media//raw", "empty id"},
		{"/api/media/unknown:x.png/raw", "unknown category"},
		{"/api/media/x.png/raw", "missing category"},
		{"/api/media/custom:x.png/raw?next=//evil", "query smuggling"},
		{"/api/media/custom:x.png/raw#frag", "fragment smuggling"},
		{"/api/covers/x.png", "the legacy cover path is not a media ref"},
		{"", "empty string"},
		{"   ", "whitespace"},
		{"/api/media/custom:x.png", "missing /raw suffix"},
		{"api/media/custom:x.png/raw", "missing leading slash"},
		{"/api/media/custom:.." + "/raw", "dot-dot id"},
	}
	for _, tc := range bad {
		if IsMediaRawURL(tc.val) {
			t.Errorf("IsMediaRawURL(%q) = true, want false (%s)", tc.val, tc.why)
		}
	}
}

func TestIsMediaRawURL_AcceptsLegitimateValues(t *testing.T) {
	good := []string{
		"/api/media/system:avatar-default.svg/raw",
		"/api/media/custom:1790124038-1c0e9fb8-wallhaven-3q5lmy.jpg/raw",
		"/api/media/custom:upload.png/raw",
	}
	for _, v := range good {
		if !IsMediaRawURL(v) {
			t.Errorf("IsMediaRawURL(%q) = false, want true", v)
		}
	}
}

// "/api/media/raw" satisfies both the prefix and the suffix check (they
// share the "/"), and used to panic slicing raw[11:10].
func TestMediaIDFromRawURLOverlappingPrefixSuffix(t *testing.T) {
	for _, raw := range []string{"/api/media/raw", "/api/media//raw", "/api/media/"} {
		if id, ok := MediaIDFromRawURL(raw); ok {
			t.Errorf("MediaIDFromRawURL(%q) = %q, true; want rejection", raw, id)
		}
	}
}
