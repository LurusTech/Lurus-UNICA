package rbac

import (
	"testing"
)

func TestIsValidRole(t *testing.T) {
	tests := []struct {
		name  string
		role  string
		valid bool
	}{
		{"super_admin", "super_admin", true},
		{"product_admin", "product_admin", true},
		{"supervisor", "supervisor", true},
		{"agent", "agent", true},
		{"knowledge_admin", "knowledge_admin", true},
		{"invalid", "invalid_role", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidRole(tt.role); got != tt.valid {
				t.Errorf("IsValidRole(%q) = %v, want %v", tt.role, got, tt.valid)
			}
		})
	}
}

func TestHighestRole(t *testing.T) {
	tests := []struct {
		name     string
		roles    []string
		expected string
	}{
		{"single super_admin", []string{"super_admin"}, "super_admin"},
		{"single agent", []string{"agent"}, "agent"},
		{"mixed roles", []string{"agent", "super_admin", "supervisor"}, "super_admin"},
		{"product_admin and agent", []string{"agent", "product_admin"}, "product_admin"},
		{"supervisor and knowledge_admin", []string{"knowledge_admin", "supervisor"}, "supervisor"},
		{"empty", []string{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HighestRole(tt.roles); got != tt.expected {
				t.Errorf("HighestRole(%v) = %q, want %q", tt.roles, got, tt.expected)
			}
		})
	}
}

func TestIsGlobalRole(t *testing.T) {
	if !IsGlobalRole(RoleSuperAdmin) {
		t.Error("super_admin should be a global role")
	}
	if IsGlobalRole(RoleProductAdmin) {
		t.Error("product_admin should not be a global role")
	}
	if IsGlobalRole(RoleAgent) {
		t.Error("agent should not be a global role")
	}
}

func TestAllRoles(t *testing.T) {
	roles := AllRoles()
	if len(roles) != 5 {
		t.Errorf("expected 5 roles, got %d", len(roles))
	}
}
