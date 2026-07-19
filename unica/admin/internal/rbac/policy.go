package rbac

// Permission represents a named permission in the system.
type Permission string

const (
	PermManageUsers         Permission = "manage_users"
	PermManageChannels      Permission = "manage_channels"
	PermManageAIConfig      Permission = "manage_ai_config"
	PermManageKnowledgeBase Permission = "manage_knowledge_base"
	PermViewConversations   Permission = "view_conversations"
	PermHandleConversations Permission = "handle_conversations"
	PermViewReports         Permission = "view_reports"
	PermManageProductLines  Permission = "manage_product_lines"
	PermViewAuditLogs       Permission = "view_audit_logs"
)

// AllPermissions returns all defined permissions.
func AllPermissions() []Permission {
	return []Permission{
		PermManageUsers,
		PermManageChannels,
		PermManageAIConfig,
		PermManageKnowledgeBase,
		PermViewConversations,
		PermHandleConversations,
		PermViewReports,
		PermManageProductLines,
		PermViewAuditLogs,
	}
}

// permissionMatrix defines which roles have which permissions.
// SuperAdmin is handled separately (has all permissions).
var permissionMatrix = map[Role]map[Permission]bool{
	RoleSuperAdmin: {
		PermManageUsers:         true,
		PermManageChannels:      true,
		PermManageAIConfig:      true,
		PermManageKnowledgeBase: true,
		PermViewConversations:   true,
		PermHandleConversations: true,
		PermViewReports:         true,
		PermManageProductLines:  true,
		PermViewAuditLogs:       true,
	},
	RoleProductAdmin: {
		PermManageUsers:         true, // Own product line only
		PermManageChannels:      true,
		PermManageAIConfig:      true,
		PermManageKnowledgeBase: true,
		PermViewConversations:   true,
		PermHandleConversations: true,
		PermViewReports:         true,
		PermViewAuditLogs:       true, // Own product line only
	},
	RoleSupervisor: {
		PermViewConversations:   true,
		PermHandleConversations: true,
		PermViewReports:         true,
	},
	RoleAgent: {
		PermHandleConversations: true,
	},
	RoleKnowledgeAdmin: {
		PermManageAIConfig:      true,
		PermManageKnowledgeBase: true,
	},
}

// HasPermission checks whether a role has the specified permission.
func HasPermission(role Role, perm Permission) bool {
	perms, ok := permissionMatrix[role]
	if !ok {
		return false
	}
	return perms[perm]
}

// CanAssignRole reports whether a caller holding effectiveRoles (scoped to
// callerProductLineIDs) may assign targetRole, optionally scoped to
// targetProductLineID.
//
//   - A super admin may assign any role.
//   - A non-super-admin may NOT assign a global role (e.g. super_admin) — this
//     is what prevents a product admin from self-granting super_admin.
//   - A non-super-admin may assign a scoped role only within a product line
//     they administer.
func CanAssignRole(effectiveRoles []string, callerProductLineIDs []string, targetRole string, targetProductLineID *string) bool {
	for _, r := range effectiveRoles {
		if r == string(RoleSuperAdmin) {
			return true
		}
	}

	// Non-super-admin: global roles are off limits entirely.
	if IsGlobalRole(Role(targetRole)) {
		return false
	}

	// Scoped role: the caller must administer the exact target product line.
	if targetProductLineID == nil || *targetProductLineID == "" {
		return false
	}
	for _, id := range callerProductLineIDs {
		if id == *targetProductLineID {
			return true
		}
	}
	return false
}

// GetPermissions returns all permissions granted to a role.
func GetPermissions(role Role) []Permission {
	perms, ok := permissionMatrix[role]
	if !ok {
		return nil
	}
	result := make([]Permission, 0, len(perms))
	for p, granted := range perms {
		if granted {
			result = append(result, p)
		}
	}
	return result
}
