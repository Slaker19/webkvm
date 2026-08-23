package libvirt

import (
	"fmt"
	"regexp"
	"strings"

	"webkvm/internal/models"
)

// nameRE is the libvirt pool name constraint: alphanumeric, dash,
// underscore, dot, plus. We use a stricter subset to keep it safe
// in shell/CLI contexts.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// pathRE is intentionally permissive — paths can contain spaces, but
// we forbid control characters and shell metachars that would let
// the caller escape the XML attribute. We rely on xmlEscape for
// the actual attribute-level escaping.
var pathRE = regexp.MustCompile(`^/[\x20-\x7e]*$`)

// buildPoolXML constructs the libvirt <pool> XML for the supported
// types. Currently: dir, netfs (NFS, CIFS).
//
// The XML is hand-rolled (rather than built with encoding/xml) so
// the output matches what the libvirt CLI tools would produce.
func buildPoolXML(poolType string, req models.CreatePoolRequest) (string, error) {
	if !nameRE.MatchString(req.Name) {
		return "", fmt.Errorf("invalid pool name %q (allowed: A-Z a-z 0-9 . _ -)", req.Name)
	}
	if !pathRE.MatchString(req.Path) {
		return "", fmt.Errorf("invalid target path %q (must be absolute)", req.Path)
	}

	switch poolType {
	case "dir":
		return fmt.Sprintf(`<pool type='dir'>
  <name>%s</name>
  <target>
    <path>%s</path>
  </target>
</pool>`, xmlEscape(req.Name), xmlEscape(req.Path)), nil

	case "netfs":
		// netfs handles both NFS and CIFS — the only difference is
		// the filesystem driver libvirt hands to mount(8) and the
		// optional source format hint.
		if req.SourceHost == "" {
			return "", fmt.Errorf("netfs pool requires source_host")
		}
		if req.SourceDir == "" {
			return "", fmt.Errorf("netfs pool requires source_dir")
		}
		format := req.SourceFormat
		if format == "" {
			format = "nfs"
		}
		if format != "nfs" && format != "cifs" {
			return "", fmt.Errorf("netfs format must be 'nfs' or 'cifs'")
		}

		// CIFS auth block: only emitted when the caller has both
		// populated SourceUsername and resolved a libvirt secret
		// (SecretUUID is set by storage.go after defineCIFSSecret).
		// The API handler rejects requests with only one of the two
		// fields, so seeing SourceUsername set implies the caller
		// meant to authenticate; the SecretUUID guard prevents an
		// unauthenticated pool from accidentally being defined
		// without an <auth> block.
		authBlock := ""
		if format == "cifs" && req.SourceUsername != "" {
			if req.SecretUUID == "" {
				return "", fmt.Errorf("cifs auth requires a libvirt secret (SecretUUID)")
			}
			authBlock = fmt.Sprintf(`  <auth type='cifs' username='%s'>
    <secret uuid='%s'/>
  </auth>
`, xmlEscape(req.SourceUsername), xmlEscape(req.SecretUUID))
		}

		return fmt.Sprintf(`<pool type='netfs'>
  <name>%s</name>
  <source>
    <host name='%s'/>
    <dir path='%s'/>
    <format type='%s'/>
  </source>
%s  <target>
    <path>%s</path>
  </target>
</pool>`,
			xmlEscape(req.Name),
			xmlEscape(req.SourceHost),
			xmlEscape(req.SourceDir),
			xmlEscape(format),
			authBlock,
			xmlEscape(req.Path),
		), nil

	default:
		return "", fmt.Errorf("unsupported pool type %q (use 'dir' or 'netfs')", poolType)
	}
}

// xmlEscape escapes the four XML attribute special characters.
func xmlEscape(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '<':
			out = append(out, '&', 'l', 't', ';')
		case '>':
			out = append(out, '&', 'g', 't', ';')
		case '&':
			out = append(out, '&', 'a', 'm', 'p', ';')
		case '\'':
			out = append(out, '&', 'a', 'p', 'o', 's', ';')
		case '"':
			out = append(out, '&', 'q', 'u', 'o', 't', ';')
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// extractPoolFormatFromXML pulls the <format type='...'> value out
// of a libvirt pool <source> block. Returns "" if absent.
func extractPoolFormatFromXML(xml string) string {
	const marker = "<format type='"
	i := strings.Index(xml, marker)
	if i < 0 {
		const marker2 = `<format type="`
		i = strings.Index(xml, marker2)
		if i < 0 {
			return ""
		}
		i += len(marker2)
	} else {
		i += len(marker)
	}
	j := strings.IndexAny(xml[i:], "'\"")
	if j < 0 {
		return ""
	}
	return xml[i : i+j]
}

// extractAuthUsernameFromXML returns the username from the pool's
// <auth type='cifs' username='...'> block. Returns "" if absent.
func extractAuthUsernameFromXML(xml string) string {
	const marker = "username='"
	i := strings.Index(xml, marker)
	if i < 0 {
		return ""
	}
	i += len(marker)
	j := strings.Index(xml[i:], "'")
	if j < 0 {
		return ""
	}
	return xml[i : i+j]
}

// extractAutostartFromXML returns true if the pool XML has
// <pool ... autostart='yes'> or <autostart>yes</autostart> in the
// newer style. Used so we can re-apply the flag after a redefine.
func extractAutostartFromXML(xml string) bool {
	if strings.Contains(xml, "autostart='yes'") ||
		strings.Contains(xml, `autostart="yes"`) {
		return true
	}
	if strings.Contains(xml, "<autostart>yes</autostart>") {
		return true
	}
	return false
}

// extractSourceHost returns the host name from <source><host name='...'/>
// in the pool XML. Returns "" if absent.
func extractSourceHost(xml string) string {
	const marker = "<host name='"
	i := strings.Index(xml, marker)
	if i < 0 {
		const m2 = `<host name="`
		i = strings.Index(xml, m2)
		if i < 0 {
			return ""
		}
		i += len(m2)
	} else {
		i += len(marker)
	}
	j := strings.IndexAny(xml[i:], "'\"")
	if j < 0 {
		return ""
	}
	return xml[i : i+j]
}

// extractSourceDir returns the dir path from <source><dir path='...'/>
// in the pool XML. Returns "" if absent.
func extractSourceDir(xml string) string {
	const marker = "<dir path='"
	i := strings.Index(xml, marker)
	if i < 0 {
		const m2 = `<dir path="`
		i = strings.Index(xml, m2)
		if i < 0 {
			return ""
		}
		i += len(m2)
	} else {
		i += len(marker)
	}
	j := strings.IndexAny(xml[i:], "'\"")
	if j < 0 {
		return ""
	}
	return xml[i : i+j]
}
