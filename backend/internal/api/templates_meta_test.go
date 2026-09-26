package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"webkvm/internal/config"
	"webkvm/internal/models"

	"github.com/go-chi/chi/v5"
)

// metaRecBackend extends aclTemplateBackend with the calls MakeVMTemplate,
// InstantiateTemplate and RevertSnapshot make, recording every meta
// update and the meta as it stood when cloud-init tried to attach its
// seed.
type metaRecBackend struct {
	aclTemplateBackend
	updates      []models.VMMetaUpdate
	metaAtAttach *models.VMMeta
	revertShares bool // RevertSnapshot flips Shared to this value
}

func (b *metaRecBackend) UpdateVMMeta(id string, upd models.VMMetaUpdate) (models.VMMeta, error) {
	b.updates = append(b.updates, upd)
	m, err := b.aclTemplateBackend.UpdateVMMeta(id, upd)
	if err != nil {
		return m, err
	}
	if upd.Tags != nil {
		m.Tags = *upd.Tags
	}
	if upd.Groups != nil {
		m.Groups = *upd.Groups
	}
	b.metas[id] = m
	return m, nil
}

func (b *metaRecBackend) GetDomain(id string) (models.VM, error) {
	return models.VM{ID: id, Name: id, State: models.VMStateShutoff}, nil
}

func (b *metaRecBackend) CloneDomain(id string, req models.CloneVMRequest) (models.VM, error) {
	// A clone inherits the source's metadata verbatim, as libvirt does.
	b.metas["clone-1"] = b.metas[id]
	return models.VM{ID: "clone-1", Name: req.Name}, nil
}

func (b *metaRecBackend) AttachDisk(id string, req models.AttachDiskRequest) error {
	m := b.metas[id]
	b.metaAtAttach = &m
	return errors.New("attach failed")
}

func (b *metaRecBackend) RevertSnapshot(id, sid string) error {
	m := b.metas[id]
	m.Shared = b.revertShares
	b.metas[id] = m
	return nil
}

func newMetaRecHarness(t *testing.T, metas map[string]models.VMMeta) (*Handler, *metaRecBackend) {
	b := &metaRecBackend{aclTemplateBackend: aclTemplateBackend{metas: metas}}
	return &Handler{compute: b, cfg: &config.Config{DataDir: t.TempDir()}}, b
}

// An imported archive keeps its domain.xml <metadata>, so it can claim
// Shared. The post-import update must always clear it, together with
// setting the owner.
func TestImportedMetaUpdate_ClearsShared(t *testing.T) {
	upd := importedMetaUpdate("bob")
	if upd.Shared == nil || *upd.Shared {
		t.Fatalf("la importación debe forzar shared=false: %+v", upd)
	}
	if upd.OwnerID == nil || *upd.OwnerID != "bob" {
		t.Fatalf("el dueño debe ir en la misma actualización: %+v", upd)
	}
	upd = importedMetaUpdate("")
	if upd.Shared == nil || *upd.Shared || upd.OwnerID != nil {
		t.Fatalf("sin dueño: shared=false y owner intacto: %+v", upd)
	}
}

// A VM that already carries Shared (imported, reverted...) must not
// become a shared template just by being templated.
func TestMakeVMTemplate_ClearsShared(t *testing.T) {
	h, b := newMetaRecHarness(t, map[string]models.VMMeta{
		"vm-bob": {OwnerID: "bob", Shared: true},
	})
	rec := httptest.NewRecorder()
	h.MakeVMTemplate(rec, withID(requestAs("bob", models.RoleOperator), "vm-bob"))
	if rec.Code != http.StatusOK {
		t.Fatalf("MakeVMTemplate devolvió %d: %s", rec.Code, rec.Body.String())
	}
	if m := b.metas["vm-bob"]; !m.Template || m.Shared {
		t.Fatalf("la plantilla debe quedar sin compartir: %+v", m)
	}
}

// Instantiating a shared template: the clone drops the template's tags
// (they would grant the template's tag holders access to bob's VM), and
// is owned by bob before cloud-init runs — so a cloud-init failure
// cannot leave it owned by the template's owner.
func TestInstantiateTemplate_ResetsTagsAndOwnerBeforeCloudInit(t *testing.T) {
	h, b := newMetaRecHarness(t, map[string]models.VMMeta{
		"tpl-shared": {OwnerID: "alice", Template: true, Shared: true, Tags: []string{"prod"}, Groups: []string{"g1"}},
	})
	body := `{"name":"clon-bob","cloud_init":{"user":"ubuntu","password":"S3cret-pass"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/templates/tpl-shared/instantiate", strings.NewReader(body))
	req.Header.Set("X-User", "bob")
	req.Header.Set("X-Role", models.RoleAdmin) // skip quota; ownership is what is under test
	rec := httptest.NewRecorder()
	h.InstantiateTemplate(rec, withID(req, "tpl-shared"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("InstantiateTemplate devolvió %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cloud-init failed") {
		t.Fatalf("se esperaba el aviso de cloud-init fallido: %s", rec.Body.String())
	}
	if b.metaAtAttach != nil && b.metaAtAttach.OwnerID != "bob" {
		t.Fatalf("al lanzar cloud-init el clon era de %q, no de bob", b.metaAtAttach.OwnerID)
	}
	if len(b.updates) == 0 || b.updates[0].OwnerID == nil || *b.updates[0].OwnerID != "bob" {
		t.Fatalf("el dueño debe fijarse en la primera actualización: %+v", b.updates)
	}
	m := b.metas["clone-1"]
	if m.OwnerID != "bob" || m.Template || m.Shared || len(m.Tags) != 0 || len(m.Groups) != 0 {
		t.Fatalf("el clon no debe heredar dueño/plantilla/compartida/tags/grupos: %+v", m)
	}
}

// A snapshot revert restores the snapshot's metadata; it must not change
// the Shared flag in either direction.
func TestRevertSnapshot_PreservesShared(t *testing.T) {
	for _, tc := range []struct {
		name         string
		before, snap bool
	}{
		{"no vuelve a compartir", false, true},
		{"no deja de compartir", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, b := newMetaRecHarness(t, map[string]models.VMMeta{
				"tpl": {OwnerID: "alice", Template: true, Shared: tc.before},
			})
			b.revertShares = tc.snap
			req := httptest.NewRequest(http.MethodPost, "/api/vms/tpl/snapshots/s1/revert", nil)
			req.Header.Set("X-User", "root")
			req.Header.Set("X-Role", models.RoleAdmin)
			rec := httptest.NewRecorder()
			req = withID(req, "tpl")
			rctx := chi.RouteContext(req.Context())
			rctx.URLParams.Add("sid", "s1")
			h.RevertSnapshot(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("RevertSnapshot devolvió %d: %s", rec.Code, rec.Body.String())
			}
			if got := b.metas["tpl"].Shared; got != tc.before {
				t.Fatalf("shared tras revertir = %v, se esperaba %v", got, tc.before)
			}
		})
	}
}

func TestNormalizeAttachDiskRequest(t *testing.T) {
	req := models.AttachDiskRequest{SizeGB: 5}
	if err := normalizeAttachDiskRequest(&req); err != nil || req.Device != "disk" {
		t.Fatalf("sin device debe ser disk: %v %+v", err, req)
	}
	for _, bad := range []models.AttachDiskRequest{
		{Device: "floppy"},
		{Device: "disk", Bus: "usb3"},
	} {
		if err := normalizeAttachDiskRequest(&bad); err == nil {
			t.Errorf("se esperaba error para %+v", bad)
		}
	}
	ok := models.AttachDiskRequest{Device: "cdrom", Bus: "sata"}
	if err := normalizeAttachDiskRequest(&ok); err != nil {
		t.Errorf("cdrom/sata es válido: %v", err)
	}
}

// Appliance deploy pool defaults: admins keep the historical behaviour
// (empty container pool = Incus profile default), non-admins get the
// resolved container pool so ACL and quota name a real pool.
func TestDeployPoolName(t *testing.T) {
	h, _ := newCreateContainerHarness(t, models.Quota{})
	cases := []struct {
		pool      string
		container bool
		role      string
		want      string
	}{
		{"", true, models.RoleAdmin, ""},
		{"", true, models.RoleOperator, "webkvm-incus"},
		{"", false, models.RoleAdmin, config.DiskPoolName},
		{"", false, models.RoleOperator, config.DiskPoolName},
		{" mine ", true, models.RoleOperator, "mine"},
		{"mine", true, models.RoleAdmin, "mine"},
	}
	for _, c := range cases {
		if got := h.deployPoolName(c.pool, c.container, c.role); got != c.want {
			t.Errorf("deployPoolName(%q, %v, %s) = %q, want %q", c.pool, c.container, c.role, got, c.want)
		}
	}
}
