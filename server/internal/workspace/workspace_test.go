package workspace

import "testing"

func TestRolePermissions(t *testing.T) {
	for _, role := range []string{RoleOwner, RoleAdmin} {
		if !CanDelete(role) || !CanModifyProvider(role) {
			t.Fatalf("%s should manage resources", role)
		}
	}
	if CanDelete(RoleMember) || CanModifyProvider(RoleMember) {
		t.Fatal("member must not delete or modify providers")
	}
}
