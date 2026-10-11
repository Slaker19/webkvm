package libvirt

import "testing"

const metaDomain = `<domain type='kvm'><name>x</name><metadata>
<webkvm:meta xmlns:webkvm="https://webvm.local/ns"><alias>db</alias><notes>n</notes>
<groups><group>g1</group></groups><tags><tag>prod</tag></tags>
<owner>root</owner><template>true</template><shared>true</shared></webkvm:meta>
</metadata></domain>`

func TestArchiveMeta(t *testing.T) {
	m, ok := archiveMeta([]byte(metaDomain), false)
	if !ok || m.Alias != "db" || m.Notes != "n" {
		t.Fatalf("descriptive fields lost: %+v ok=%v", m, ok)
	}
	if len(m.Tags) != 0 || len(m.Groups) != 0 {
		t.Fatalf("non-admin must not carry RBAC labels: %+v", m)
	}
	if m.OwnerID != "" || m.Template || m.Shared {
		t.Fatalf("ownership/template/shared must never be imported: %+v", m)
	}
	a, _ := archiveMeta([]byte(metaDomain), true)
	if len(a.Tags) != 1 || len(a.Groups) != 1 || a.OwnerID != "" || a.Shared || a.Template {
		t.Fatalf("admin import: %+v", a)
	}
	if _, ok := archiveMeta([]byte(`<domain type='kvm'/>`), true); ok {
		t.Fatal("no metadata should report not-ok")
	}
	if _, ok := archiveMeta([]byte(`<!DOCTYPE x [<!ENTITY e "x">]><domain/>`), true); ok {
		t.Fatal("garbage accepted")
	}
}
