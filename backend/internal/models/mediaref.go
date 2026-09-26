package models

import "strings"

// Media references are the single way an image is pointed at from
// anywhere else in the system: a user's avatar, a VM cover, the site
// logo. They always have the shape:
//
//	/api/media/<id>/raw
//
// where <id> is "system:<file>" or "custom:<file>".
//
// Keeping one canonical form matters for two reasons. First, every image
// then lives in exactly one place (the media library) instead of being
// copied into per-feature directories that drift apart. Second, these
// strings are rendered by the frontend as <img src>, so they must never
// be attacker-controlled free text: a "javascript:" or "data:" URL stored
// in a profile field is a stored XSS. Validating on write means the UI
// can render the value without having to sanitise it at every use site.

const mediaRawPrefix = "/api/media/"
const mediaRawSuffix = "/raw"

// MediaRawURL builds the canonical reference for a media id.
func MediaRawURL(mediaID string) string {
	return mediaRawPrefix + mediaID + mediaRawSuffix
}

// MediaIDFromRawURL extracts the media id from a canonical reference.
// The second return value reports whether the URL was well-formed.
func MediaIDFromRawURL(raw string) (string, bool) {
	// The length check matters: "/api/media/raw" passes both the prefix
	// and suffix checks (they overlap on the "/"), and slicing it below
	// would panic with raw[11:10].
	if len(raw) <= len(mediaRawPrefix)+len(mediaRawSuffix) ||
		!strings.HasPrefix(raw, mediaRawPrefix) || !strings.HasSuffix(raw, mediaRawSuffix) {
		return "", false
	}
	id := raw[len(mediaRawPrefix) : len(raw)-len(mediaRawSuffix)]
	if id == "" {
		return "", false
	}
	// The id must be a single path segment with a known category. This
	// rejects traversal ("../"), nested paths, and query/fragment
	// smuggling, all of which would otherwise round-trip through the
	// prefix/suffix check above.
	if strings.ContainsAny(id, "/?#\\") {
		return "", false
	}
	if !strings.HasPrefix(id, "system:") && !strings.HasPrefix(id, "custom:") {
		return "", false
	}
	name := id[strings.IndexByte(id, ':')+1:]
	if name == "" || name == "." || name == ".." || strings.Contains(name, "..") {
		return "", false
	}
	return id, true
}

// IsMediaRawURL reports whether raw is a well-formed media reference.
func IsMediaRawURL(raw string) bool {
	_, ok := MediaIDFromRawURL(raw)
	return ok
}
