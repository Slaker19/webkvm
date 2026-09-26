package user

import (
	"testing"

	"webkvm/internal/models"
)

func bPtr(b bool) *bool { return &b }
func slicePtr(v []string) *[]string {
	return &v
}

// epochOf reads the session epoch of a user, failing the test if absent.
func epochOf(t *testing.T, s *Store, username string) int {
	t.Helper()
	u, err := s.Get(username)
	if err != nil {
		t.Fatalf("Get(%q): %v", username, err)
	}
	return u.SessionEpoch
}

// mkUser creates an operator for tests.
func mkUser(t *testing.T, s *Store, name string) {
	t.Helper()
	if _, err := s.Create(models.CreateUserRequest{
		Username: name,
		Password: "Str0ng-Pass#2026",
		Role:     models.RoleOperator,
	}); err != nil {
		t.Fatalf("Create(%q): %v", name, err)
	}
}

// A role change must invalidate live sessions.
//
// This is the demotion escalation: RequireRole reads the role out of the
// JWT, so a token minted while the account was an admin keeps admin
// powers after the demotion unless the epoch moves and invalidates it.
func TestUpdate_RoleChangeBumpsSessionEpoch(t *testing.T) {
	s, _, _ := seedStore(t)
	mkUser(t, s, "demoted")
	before := epochOf(t, s, "demoted")

	if _, err := s.Update("demoted", models.UpdateUserRequest{
		Role: strPtr(models.RoleViewer),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if after := epochOf(t, s, "demoted"); after <= before {
		t.Fatalf("session epoch %d -> %d: a role change must invalidate live sessions", before, after)
	}
}

// Promotions bump too: the epoch means "the role in your token is stale",
// which is equally true in both directions.
func TestUpdate_PromotionBumpsSessionEpoch(t *testing.T) {
	s, _, _ := seedStore(t)
	mkUser(t, s, "promoted")
	before := epochOf(t, s, "promoted")

	if _, err := s.Update("promoted", models.UpdateUserRequest{
		Role: strPtr(models.RoleAdmin),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if after := epochOf(t, s, "promoted"); after <= before {
		t.Fatalf("session epoch %d -> %d: a promotion must invalidate live sessions too", before, after)
	}
}

// Narrowing capabilities must invalidate live sessions.
func TestUpdate_PermissionChangeBumpsSessionEpoch(t *testing.T) {
	s, _, _ := seedStore(t)
	mkUser(t, s, "narrowed")
	before := epochOf(t, s, "narrowed")

	if _, err := s.Update("narrowed", models.UpdateUserRequest{
		Permissions: &models.UserPermissions{CanConsole: bPtr(false)},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if after := epochOf(t, s, "narrowed"); after <= before {
		t.Fatalf("session epoch %d -> %d: revoking a capability must invalidate live sessions", before, after)
	}
}

// Revoking only can_export must invalidate live sessions too
// (permissionsEqual once omitted CanExport, so this was a no-op).
func TestUpdate_ExportOnlyChangeBumpsSessionEpoch(t *testing.T) {
	s, _, _ := seedStore(t)
	mkUser(t, s, "exporter")
	if _, err := s.Update("exporter", models.UpdateUserRequest{
		Permissions: &models.UserPermissions{CanConsole: bPtr(true), CanExport: bPtr(true)},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	before := epochOf(t, s, "exporter")
	if _, err := s.Update("exporter", models.UpdateUserRequest{
		Permissions: &models.UserPermissions{CanConsole: bPtr(true), CanExport: bPtr(false)},
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if after := epochOf(t, s, "exporter"); after <= before {
		t.Fatalf("session epoch %d -> %d: revoking can_export must invalidate live sessions", before, after)
	}
}

// Narrowing scope (pools, networks, tags, groups) must invalidate too.
func TestUpdate_ScopeChangeBumpsSessionEpoch(t *testing.T) {
	cases := []struct {
		name string
		req  models.UpdateUserRequest
	}{
		{"pools", models.UpdateUserRequest{AllowedPools: slicePtr([]string{"p1"})}},
		{"networks", models.UpdateUserRequest{AllowedNetworks: slicePtr([]string{"br0"})}},
		{"tags", models.UpdateUserRequest{AllowedTags: slicePtr([]string{"prod"})}},
		{"groups", models.UpdateUserRequest{AllowedGroups: slicePtr([]string{"APPS"})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := seedStore(t)
			mkUser(t, s, "scoped")
			before := epochOf(t, s, "scoped")
			if _, err := s.Update("scoped", tc.req); err != nil {
				t.Fatalf("Update: %v", err)
			}
			if after := epochOf(t, s, "scoped"); after <= before {
				t.Fatalf("%s: session epoch %d -> %d: a scope change must invalidate live sessions", tc.name, before, after)
			}
		})
	}
}

// A no-op save must NOT log the user out. An admin opening the edit
// dialog and pressing Save without changing anything is a common action;
// if that killed sessions, every unrelated edit would kick the user.
func TestUpdate_NoOpDoesNotBumpSessionEpoch(t *testing.T) {
	s, _, _ := seedStore(t)
	mkUser(t, s, "stable")
	if _, err := s.Update("stable", models.UpdateUserRequest{
		Role:          strPtr(models.RoleOperator),
		Permissions:   &models.UserPermissions{CanConsole: bPtr(true)},
		AllowedPools:  slicePtr([]string{"p1", "p2"}),
		AllowedGroups: slicePtr([]string{"APPS"}),
	}); err != nil {
		t.Fatalf("seed update: %v", err)
	}
	before := epochOf(t, s, "stable")

	// Same values, pools listed in a different order: the UI rebuilds
	// these from checkbox groups, so order is not meaningful.
	if _, err := s.Update("stable", models.UpdateUserRequest{
		Role:          strPtr(models.RoleOperator),
		Permissions:   &models.UserPermissions{CanConsole: bPtr(true)},
		AllowedPools:  slicePtr([]string{"p2", "p1"}),
		AllowedGroups: slicePtr([]string{"APPS"}),
	}); err != nil {
		t.Fatalf("no-op update: %v", err)
	}
	if after := epochOf(t, s, "stable"); after != before {
		t.Fatalf("session epoch %d -> %d: an unchanged save must not invalidate sessions", before, after)
	}
}

// Usernames must be stored in the exact form that was validated.
//
// " admin " previously passed the emptiness check (which trimmed) but was
// stored untrimmed, producing a second account that renders identically
// to "admin" everywhere: the users table, the audit log, every dialog.
func TestCreate_RejectsLookalikeUsernames(t *testing.T) {
	bad := []string{
		" admin ",
		"admin ",
		" admin",
		"ad min",
		"admin\t",
		"admin\n",
		"\u0430dmin", // Cyrillic 'а'
		"admin\u200b",
		"-admin",
		"admin-",
		".admin",
		"",
		"   ",
	}
	for _, name := range bad {
		t.Run(name, func(t *testing.T) {
			s, _, _ := seedStore(t)
			if _, err := s.Create(models.CreateUserRequest{
				Username: name,
				Password: "Str0ng-Pass#2026",
				Role:     models.RoleAdmin,
			}); err == nil {
				t.Fatalf("Create(%q) succeeded; a lookalike of an existing account must be rejected", name)
			}
		})
	}
}

// Legitimate names still work, and are stored verbatim.
func TestCreate_AcceptsOrdinaryUsernames(t *testing.T) {
	good := []string{"demo", "jane.doe", "ops-team", "user_1", "a", "A1"}
	for _, name := range good {
		t.Run(name, func(t *testing.T) {
			s, _, _ := seedStore(t)
			u, err := s.Create(models.CreateUserRequest{
				Username: name,
				Password: "Str0ng-Pass#2026",
				Role:     models.RoleOperator,
			})
			if err != nil {
				t.Fatalf("Create(%q): %v", name, err)
			}
			if u.Username != name {
				t.Fatalf("stored username = %q, want %q", u.Username, name)
			}
		})
	}
}
