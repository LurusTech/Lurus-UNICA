package rbac

import (
	"testing"
)

func TestPermissionMatrix(t *testing.T) {
	// Test the full permission matrix from the story
	tests := []struct {
		role       Role
		permission Permission
		expected   bool
	}{
		// SuperAdmin - has everything
		{RoleSuperAdmin, PermManageUsers, true},
		{RoleSuperAdmin, PermManageChannels, true},
		{RoleSuperAdmin, PermManageAIConfig, true},
		{RoleSuperAdmin, PermManageKnowledgeBase, true},
		{RoleSuperAdmin, PermViewConversations, true},
		{RoleSuperAdmin, PermHandleConversations, true},
		{RoleSuperAdmin, PermViewReports, true},
		{RoleSuperAdmin, PermManageProductLines, true},

		{RoleSuperAdmin, PermViewAuditLogs, true},

		// ProductAdmin - everything except manage_product_lines
		{RoleProductAdmin, PermManageUsers, true},
		{RoleProductAdmin, PermManageChannels, true},
		{RoleProductAdmin, PermManageAIConfig, true},
		{RoleProductAdmin, PermManageKnowledgeBase, true},
		{RoleProductAdmin, PermViewConversations, true},
		{RoleProductAdmin, PermHandleConversations, true},
		{RoleProductAdmin, PermViewReports, true},
		{RoleProductAdmin, PermManageProductLines, false},
		{RoleProductAdmin, PermViewAuditLogs, true},

		// Supervisor - view/handle conversations + reports
		{RoleSupervisor, PermManageUsers, false},
		{RoleSupervisor, PermManageChannels, false},
		{RoleSupervisor, PermViewConversations, true},
		{RoleSupervisor, PermHandleConversations, true},
		{RoleSupervisor, PermViewReports, true},
		{RoleSupervisor, PermManageProductLines, false},

		// Agent - handle conversations only
		{RoleAgent, PermManageUsers, false},
		{RoleAgent, PermManageChannels, false},
		{RoleAgent, PermViewConversations, false},
		{RoleAgent, PermHandleConversations, true},
		{RoleAgent, PermViewReports, false},
		{RoleAgent, PermManageProductLines, false},
		{RoleAgent, PermViewAuditLogs, false},

		// KnowledgeAdmin - AI config + knowledge base
		{RoleKnowledgeAdmin, PermManageUsers, false},
		{RoleKnowledgeAdmin, PermManageChannels, false},
		{RoleKnowledgeAdmin, PermManageAIConfig, true},
		{RoleKnowledgeAdmin, PermManageKnowledgeBase, true},
		{RoleKnowledgeAdmin, PermViewConversations, false},
		{RoleKnowledgeAdmin, PermHandleConversations, false},
		{RoleKnowledgeAdmin, PermManageProductLines, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.role)+"/"+string(tt.permission), func(t *testing.T) {
			got := HasPermission(tt.role, tt.permission)
			if got != tt.expected {
				t.Errorf("HasPermission(%s, %s) = %v, want %v", tt.role, tt.permission, got, tt.expected)
			}
		})
	}
}

func TestHasPermission_InvalidRole(t *testing.T) {
	if HasPermission(Role("nonexistent"), PermManageUsers) {
		t.Error("invalid role should not have any permissions")
	}
}

func TestGetPermissions(t *testing.T) {
	perms := GetPermissions(RoleAgent)
	if len(perms) != 1 {
		t.Errorf("agent should have 1 permission, got %d", len(perms))
	}
	if perms[0] != PermHandleConversations {
		t.Errorf("agent's permission should be handle_conversations, got %s", perms[0])
	}

	saPerms := GetPermissions(RoleSuperAdmin)
	if len(saPerms) != 9 {
		t.Errorf("super_admin should have 9 permissions, got %d", len(saPerms))
	}
}

func TestGetPermissions_InvalidRole(t *testing.T) {
	perms := GetPermissions(Role("invalid"))
	if perms != nil {
		t.Error("invalid role should return nil permissions")
	}
}

func TestAllPermissions(t *testing.T) {
	perms := AllPermissions()
	if len(perms) != 9 {
		t.Errorf("expected 9 permissions, got %d", len(perms))
	}
}
