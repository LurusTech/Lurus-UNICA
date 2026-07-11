package rbac

// Role represents a named role in the system.
type Role string

const (
	RoleSuperAdmin    Role = "super_admin"
	RoleProductAdmin  Role = "product_admin"
	RoleSupervisor    Role = "supervisor"
	RoleAgent         Role = "agent"
	RoleKnowledgeAdmin Role = "knowledge_admin"
)

// AllRoles returns all valid role names.
func AllRoles() []Role {
	return []Role{
		RoleSuperAdmin,
		RoleProductAdmin,
		RoleSupervisor,
		RoleAgent,
		RoleKnowledgeAdmin,
	}
}

// IsValidRole checks whether a role name is recognized.
func IsValidRole(name string) bool {
	for _, r := range AllRoles() {
		if string(r) == name {
			return true
		}
	}
	return false
}

// rolePriority defines the hierarchy ordering (lower number = higher privilege).
var rolePriority = map[Role]int{
	RoleSuperAdmin:    0,
	RoleProductAdmin:  1,
	RoleSupervisor:    2,
	RoleKnowledgeAdmin: 3,
	RoleAgent:         4,
}

// HighestRole returns the role with the highest privilege from the given list.
func HighestRole(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	highest := roles[0]
	highestPri := rolePriority[Role(highest)]
	for _, r := range roles[1:] {
		pri, ok := rolePriority[Role(r)]
		if ok && pri < highestPri {
			highest = r
			highestPri = pri
		}
	}
	return highest
}

// IsGlobalRole returns true if the role operates at the global level (no product line scope).
func IsGlobalRole(role Role) bool {
	return role == RoleSuperAdmin
}
