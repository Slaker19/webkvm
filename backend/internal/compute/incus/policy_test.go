package incus

import (
	"errors"
	"testing"

	"webkvm/internal/compute"
	"webkvm/internal/models"
)

func boolp(b bool) *bool { return &b }

func TestAuthorizeSpec_NonAdmin(t *testing.T) {
	b := &IncusBackend{}
	op := models.MediaScope{}
	cases := map[string]struct {
		priv, nest *bool
		profiles   []string
		scope      models.MediaScope
		wantErr    bool
	}{
		"privileged":              {priv: boolp(true), scope: op, wantErr: true},
		"privileged false ok":     {priv: boolp(false), scope: op},
		"nesting no permission":   {nest: boolp(true), scope: op, wantErr: true},
		"nesting with permission": {nest: boolp(true), scope: models.MediaScope{CanNesting: true}},
		"default profile":         {profiles: []string{"default"}, scope: op},
		"other profile":           {profiles: []string{"default", "docker"}, scope: op, wantErr: true},
		"no profiles":             {scope: op},
		"admin anything":          {priv: boolp(true), nest: boolp(true), profiles: []string{"x"}, scope: models.MediaScope{Admin: true}},
	}
	for name, tc := range cases {
		err := b.authorizeSpec(tc.scope, tc.priv, tc.nest, tc.profiles)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", name, err, tc.wantErr)
		}
		if err != nil && !errors.Is(err, compute.ErrForbidden) {
			t.Errorf("%s: error does not wrap ErrForbidden: %v", name, err)
		}
	}
}

func TestAuthorizeSpec_AllowedProfilesSetting(t *testing.T) {
	b := &IncusBackend{}
	WithAllowedProfiles(func() []string { return []string{"default", "docker"} })(b)
	if err := b.authorizeSpec(models.MediaScope{}, nil, nil, []string{"docker"}); err != nil {
		t.Fatalf("listed profile rejected: %v", err)
	}
	if err := b.authorizeSpec(models.MediaScope{}, nil, nil, []string{"privileged-profile"}); err == nil {
		t.Fatal("unlisted profile accepted")
	}
}

func TestCreateDomain_NestingDefaultAndEnforcement(t *testing.T) {
	if findAnyPhysicalBridge() == "" {
		t.Skip("no physical Linux bridge on host; container creation requires shared L2")
	}
	sock, fake := newFakeLXD3(t, nil, nil)
	b, err := NewIncusBackend(sock)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Operator: nesting off by default.
	if _, err := b.CreateDomain(models.CreateVMRequest{Name: "op1", Image: "ubuntu:24.04"}); err != nil {
		t.Fatalf("operator create: %v", err)
	}
	fake.mu.Lock()
	got := fake.lastCreate.Config["security.nesting"]
	fake.mu.Unlock()
	if got != "false" {
		t.Errorf("operator security.nesting = %q, want false", got)
	}
	// Operator asking for privileged / nesting / profiles: refused before any create.
	for name, req := range map[string]models.CreateVMRequest{
		"privileged": {Name: "p", Image: "ubuntu:24.04", Privileged: boolp(true)},
		"nesting":    {Name: "n", Image: "ubuntu:24.04", Nesting: boolp(true)},
		"profile":    {Name: "f", Image: "ubuntu:24.04", Profiles: []string{"default", "evil"}},
	} {
		if _, err := b.CreateDomain(req); !errors.Is(err, compute.ErrForbidden) {
			t.Errorf("%s: err = %v, want ErrForbidden", name, err)
		}
	}
	// Admin keeps the historic default (nesting on) and can still go privileged.
	if _, err := b.CreateDomain(models.CreateVMRequest{
		Name: "adm", Image: "ubuntu:24.04", Privileged: boolp(true),
		Scope: models.MediaScope{Admin: true},
	}); err != nil {
		t.Fatalf("admin create: %v", err)
	}
	fake.mu.Lock()
	cfg := fake.lastCreate.Config
	fake.mu.Unlock()
	if cfg["security.nesting"] != "true" || cfg["security.privileged"] != "true" {
		t.Errorf("admin config = %v", cfg)
	}
}

func TestUpdateDomain_NonAdminCannotEscalate(t *testing.T) {
	b := &IncusBackend{}
	if _, err := b.UpdateDomain("web", models.UpdateVMRequest{Privileged: boolp(true)}); !errors.Is(err, compute.ErrForbidden) {
		t.Fatalf("privileged update: %v", err)
	}
	if _, err := b.UpdateDomain("web", models.UpdateVMRequest{Profiles: []string{"x"}}); !errors.Is(err, compute.ErrForbidden) {
		t.Fatalf("profiles update: %v", err)
	}
}
