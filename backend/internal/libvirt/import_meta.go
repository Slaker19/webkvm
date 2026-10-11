package libvirt

import (
	"encoding/xml"
	"strings"

	"webkvm/internal/models"
)

const maxImportedMetaField = 64 << 10

// archiveMeta extracts the WebKVM metadata (<metadata><webkvm:meta>) from an
// archive's domain.xml. The XML itself is validated and re-serialised without
// <metadata> (see SanitizeDomainXML); this only recovers the descriptive
// fields so a backup restore keeps alias/notes/etc. It never carries over
// ownership or template/sharing flags, and tags/groups (RBAC policy labels)
// only for administrators.
func archiveMeta(domainXML []byte, admin bool) (models.VMMeta, bool) {
	var doc struct {
		Metadata struct {
			Meta struct {
				Alias   string   `xml:"alias"`
				Notes   string   `xml:"notes"`
				Cover   string   `xml:"cover"`
				Groups  []string `xml:"groups>group"`
				Tags    []string `xml:"tags>tag"`
				CiUser  string   `xml:"ci_user"`
				AppInfo string   `xml:"app_info"`
			} `xml:"https://webvm.local/ns meta"`
		} `xml:"metadata"`
	}
	d := xml.NewDecoder(strings.NewReader(string(domainXML)))
	d.Strict = true
	if err := d.Decode(&doc); err != nil {
		return models.VMMeta{}, false
	}
	m := doc.Metadata.Meta
	lim := func(s string) string {
		if len(s) > maxImportedMetaField {
			return s[:maxImportedMetaField]
		}
		return s
	}
	out := models.VMMeta{
		Alias:  lim(m.Alias),
		Notes:  lim(m.Notes),
		Cover:  lim(m.Cover),
		CiUser: lim(m.CiUser),
	}
	if admin {
		out.Groups = m.Groups
		out.Tags = m.Tags
		out.AppInfo = lim(m.AppInfo)
	}
	out.UpdatedAt = nowUnix()
	has := out.Alias != "" || out.Notes != "" || out.Cover != "" || out.CiUser != "" || out.AppInfo != "" || len(out.Groups) > 0 || len(out.Tags) > 0
	return out, has
}
