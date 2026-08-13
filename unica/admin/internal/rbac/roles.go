// Package rbac holds the platform's role vocabulary. The model is exactly two
// layers: an admin runs the platform and belongs to no tenant, a user belongs
// to exactly one tenant and is confined to it. Everything beyond that
// distinction is decided by the route a request arrives on, which is why there
// is no permission table here to keep in sync with one.
package rbac

const (
	// RoleAdmin runs the platform: users, tenants, and any tenant's resources.
	RoleAdmin = "admin"
	// RoleUser owns exactly one tenant and may act only on that tenant.
	RoleUser = "user"
)

// IsAdmin reports whether a role name is the platform administrator role.
func IsAdmin(role string) bool {
	return role == RoleAdmin
}
