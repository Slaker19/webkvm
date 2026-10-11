package models

import "testing"

// "nesting" is the one capability an operator does NOT have by default.
func TestHasPermission_NestingIsOptIn(t *testing.T) {
	op := &User{Username: "op", Role: RoleOperator}
	if op.HasPermission("nesting") {
		t.Fatal("operator must not have nesting by default")
	}
	op.Permissions = &UserPermissions{CanNesting: boolPtr(true)}
	if !op.HasPermission("nesting") {
		t.Fatal("explicit grant ignored")
	}
	if !(&User{Role: RoleAdmin}).HasPermission("nesting") {
		t.Fatal("admin must always have nesting")
	}
	if (&User{Role: RoleViewer, Permissions: &UserPermissions{CanNesting: boolPtr(true)}}).HasPermission("nesting") {
		t.Fatal("viewer must never have nesting")
	}
}
