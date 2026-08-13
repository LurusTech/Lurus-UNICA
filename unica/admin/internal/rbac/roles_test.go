package rbac

import "testing"

func TestRoleNames(t *testing.T) {
	if RoleAdmin != "admin" {
		t.Errorf("RoleAdmin = %q, want admin", RoleAdmin)
	}
	if RoleUser != "user" {
		t.Errorf("RoleUser = %q, want user", RoleUser)
	}
}

func TestIsAdmin(t *testing.T) {
	tests := []struct {
		role string
		want bool
	}{
		{RoleAdmin, true},
		{RoleUser, false},
		{"", false},
		// Roles the old matrix defined must not survive as admins by accident.
		{"super_admin", false},
		{"product_admin", false},
		{"Admin", false},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			if got := IsAdmin(tt.role); got != tt.want {
				t.Errorf("IsAdmin(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}
