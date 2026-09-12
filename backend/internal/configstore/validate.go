package configstore

import (
	"fmt"
	"net"
	"regexp"
	"strings"
)

// Semantic (format) validation for specific fields, layered on top of the
// type-level checks in validateValue. Each entry returns "" when the value
// is acceptable, or a human-readable reason when it is not. This lives
// outside the schema (which is serialized to the UI) so validators stay
// server-side and can use net/regexp without bloating the JSON contract.
var fieldValidators = map[string]func(Field, interface{}) string{
	"server.bind_addr":     validateHostOrIP,
	"server.public_host":   validateHostOrIP,
	"server.tls_domain":    validateHostname,
	"server.trusted_cidrs": validateCIDRList,
}

// hostnameRE is a permissive DNS hostname check: dot-separated labels of
// letters/digits/hyphens, no leading/trailing hyphen, 1..253 chars. It is
// deliberately laxer than the RFC (accepts single-label LAN names) because
// this gates a bind/public host, not a certificate CN.
var hostnameRE = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)(\.[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$`)

// validateHostOrIP accepts an IP address, a hostname, or an empty string
// (empty means "let the server decide its default").
func validateHostOrIP(_ Field, v interface{}) string {
	s, ok := v.(string)
	if !ok {
		return "" // type mismatch is reported by validateValue
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if net.ParseIP(s) != nil {
		return ""
	}
	if validHostname(s) {
		return ""
	}
	return "must be an IP address or a hostname"
}

// validateHostname accepts a hostname or empty string (no IP).
func validateHostname(_ Field, v interface{}) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if net.ParseIP(s) != nil {
		return "must be a hostname, not an IP address"
	}
	if validHostname(s) {
		return ""
	}
	return "must be a valid hostname (e.g. webkvm.example.com)"
}

// validateCIDRList checks that every entry in a list field is either a
// CIDR ("10.0.0.0/8", "fd00::/8") or a single IP.
func validateCIDRList(_ Field, v interface{}) string {
	list := toStringSlice(v)
	for _, raw := range list {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			return "contains an empty entry"
		}
		if _, _, err := net.ParseCIDR(entry); err == nil {
			continue
		}
		if net.ParseIP(entry) != nil {
			continue
		}
		return fmt.Sprintf("%q is not a valid CIDR or IP", entry)
	}
	return ""
}

// toStringSlice normalizes the []string / []interface{} shapes the JSON
// decoder and the store can hand us.
func toStringSlice(v interface{}) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []interface{}:
		out := make([]string, 0, len(list))
		for _, x := range list {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func validHostname(s string) bool {
	if len(s) > 253 {
		return false
	}
	return hostnameRE.MatchString(s)
}

// validateCrossFieldValue performs checks that depend on more than one
// field at once, using the *effective* value (incoming change if present,
// otherwise the stored value). Keyed by the fields it implicates.
//
// Deliberately empty right now: an earlier version rejected saving the
// TLS certificate and key one at a time (only accepting them together),
// which broke the legitimate case of an operator setting one field now
// and the other in a follow-up call — configureTLS (cmd/server) already
// falls back to tlsModeOff safely whenever the pair is incomplete, so
// there is no need to block the write itself. Kept as an empty, callable
// hook so a future genuinely cross-field rule has somewhere to live
// without re-plumbing SetMany.
func (s *Store) validateCrossField(in Set) map[string]string {
	return map[string]string{}
}
