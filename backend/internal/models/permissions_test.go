package models

import "testing"

func boolPtr(b bool) *bool { return &b }

// An explicitly denied capability must be denied. Before the fix the
// 'console' and 'export' flags were never consulted by any route, but
// the model-level contract is the foundation the routes rely on.
func TestHasPermission_ExplicitDenyIsHonoured(t *testing.T) {
	u := &User{
		Username: "op",
		Role:     RoleOperator,
		Permissions: &UserPermissions{
			CanCreateVM:     boolPtr(false),
			CanDeleteVM:     boolPtr(false),
			CanControlPower: boolPtr(false),
			CanConsole:      boolPtr(false),
			CanSnapshots:    boolPtr(false),
			CanBackups:      boolPtr(false),
			CanMedia:        boolPtr(false),
			CanExport:       boolPtr(false),
		},
	}
	for _, perm := range Capabilities {
		if u.HasPermission(perm) {
			t.Errorf("HasPermission(%q) = true, want false (explicitly denied)", perm)
		}
	}
}

// An operator with no explicit overrides keeps every default capability.
func TestHasPermission_OperatorDefaultsAllow(t *testing.T) {
	u := &User{Username: "op", Role: RoleOperator}
	for _, perm := range Capabilities {
		if !u.HasPermission(perm) {
			t.Errorf("HasPermission(%q) = false, want true (operator default)", perm)
		}
	}
}

// Unknown capability names must fail closed. The operator branch defaults
// to "allowed", so without an explicit known-name check a typo'd or
// removed permission string silently granted access to everyone.
func TestHasPermission_UnknownCapabilityIsDenied(t *testing.T) {
	for _, u := range []*User{
		{Username: "op", Role: RoleOperator},
		{Username: "op2", Role: RoleOperator, Permissions: &UserPermissions{}},
		{Username: "v", Role: RoleViewer},
	} {
		for _, perm := range []string{"", "consol", "admin", "delete_everything"} {
			if u.HasPermission(perm) {
				t.Errorf("role %s: HasPermission(%q) = true, want false (unknown capability)", u.Role, perm)
			}
		}
	}
}

// Viewers are read-only, with console as the single grantable exception.
func TestHasPermission_ViewerIsReadOnlyExceptConsole(t *testing.T) {
	v := &User{Username: "v", Role: RoleViewer}
	for _, perm := range Capabilities {
		if v.HasPermission(perm) {
			t.Errorf("bare viewer: HasPermission(%q) = true, want false", perm)
		}
	}

	granted := &User{
		Username: "v2", Role: RoleViewer,
		Permissions: &UserPermissions{
			CanConsole:   boolPtr(true),
			CanSnapshots: boolPtr(true),
			CanExport:    boolPtr(true),
		},
	}
	if !granted.HasPermission("console") {
		t.Error("viewer with can_console=true: HasPermission(console) = false, want true")
	}
	// Granting anything else to a viewer must not take effect: the role
	// is the ceiling, the flags only carve out of it.
	for _, perm := range []string{"snapshots", "export", "create_vm", "delete_vm"} {
		if granted.HasPermission(perm) {
			t.Errorf("viewer with %s=true: HasPermission(%q) = true, want false (role is the ceiling)", perm, perm)
		}
	}
}

// Admins hold every known capability regardless of flags, including ones
// explicitly set to false.
func TestHasPermission_AdminAlwaysAllowed(t *testing.T) {
	a := &User{
		Username: "admin", Role: RoleAdmin,
		Permissions: &UserPermissions{
			CanConsole: boolPtr(false),
			CanExport:  boolPtr(false),
		},
	}
	for _, perm := range Capabilities {
		if !a.HasPermission(perm) {
			t.Errorf("admin: HasPermission(%q) = false, want true", perm)
		}
	}
}

// Every capability in the canonical list must be wired to a real flag.
// This is what stops a new capability being added to the list (or to a
// route) without a field behind it, which would deny it to every operator.
func TestCapabilities_AllMapToAFlag(t *testing.T) {
	full := &UserPermissions{
		CanCreateVM:     boolPtr(true),
		CanDeleteVM:     boolPtr(true),
		CanControlPower: boolPtr(true),
		CanConsole:      boolPtr(true),
		CanSnapshots:    boolPtr(true),
		CanBackups:      boolPtr(true),
		CanMedia:        boolPtr(true),
		CanExport:       boolPtr(true),
	}
	for _, perm := range Capabilities {
		flag, known := full.capabilityFlag(perm)
		if !known {
			t.Errorf("capability %q is listed but has no backing flag", perm)
			continue
		}
		if flag == nil {
			t.Errorf("capability %q maps to a nil flag despite all flags being set", perm)
		}
	}
}
