package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// RoleRepository handles role database operations.
type RoleRepository struct {
	db *sql.DB
}

// NewRoleRepository creates a new role repository.
func NewRoleRepository(db *sql.DB) *RoleRepository {
	return &RoleRepository{db: db}
}

// GetByName retrieves a role by its name.
func (r *RoleRepository) GetByName(ctx context.Context, name string) (*RoleModel, error) {
	role := &RoleModel{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, description FROM roles WHERE name = $1`, name,
	).Scan(&role.ID, &role.Name, &role.Description)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get role: %w", err)
	}
	return role, nil
}

// GetByID retrieves a role by its ID.
func (r *RoleRepository) GetByID(ctx context.Context, id string) (*RoleModel, error) {
	role := &RoleModel{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, description FROM roles WHERE id = $1`, id,
	).Scan(&role.ID, &role.Name, &role.Description)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get role: %w", err)
	}
	return role, nil
}

// ListAll returns all defined roles.
func (r *RoleRepository) ListAll(ctx context.Context) ([]RoleModel, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, description FROM roles ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("failed to list roles: %w", err)
	}
	defer rows.Close()

	var roles []RoleModel
	for rows.Next() {
		var rm RoleModel
		if err := rows.Scan(&rm.ID, &rm.Name, &rm.Description); err != nil {
			return nil, fmt.Errorf("failed to scan role: %w", err)
		}
		roles = append(roles, rm)
	}
	return roles, rows.Err()
}

// AssignRole assigns a role to a user, optionally scoped to a product line.
func (r *RoleRepository) AssignRole(ctx context.Context, userID, roleID string, productLineID *string) (*UserRole, error) {
	ur := &UserRole{}
	var plID sql.NullString
	if productLineID != nil {
		plID = sql.NullString{String: *productLineID, Valid: true}
	}

	err := r.db.QueryRowContext(ctx,
		`INSERT INTO user_roles (user_id, role_id, product_line_id)
		 VALUES ($1, $2, $3)
		 RETURNING id, user_id, role_id, product_line_id, created_at`,
		userID, roleID, plID,
	).Scan(&ur.ID, &ur.UserID, &ur.RoleID, &plID, &ur.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to assign role: %w", err)
	}
	if plID.Valid {
		ur.ProductLineID = &plID.String
	}
	return ur, nil
}

// RemoveRole removes a role assignment from a user.
func (r *RoleRepository) RemoveRole(ctx context.Context, userID, roleID string) error {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM user_roles WHERE user_id = $1 AND role_id = $2`,
		userID, roleID)
	if err != nil {
		return fmt.Errorf("failed to remove role: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("role assignment not found")
	}
	return nil
}

// GetUserRoles returns all role assignments for a user.
func (r *RoleRepository) GetUserRoles(ctx context.Context, userID string) ([]UserRole, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT ur.id, ur.user_id, ur.role_id, r.name, ur.product_line_id, ur.created_at
		 FROM user_roles ur
		 INNER JOIN roles r ON ur.role_id = r.id
		 WHERE ur.user_id = $1
		 ORDER BY ur.created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user roles: %w", err)
	}
	defer rows.Close()

	var roles []UserRole
	for rows.Next() {
		var ur UserRole
		var plID sql.NullString
		if err := rows.Scan(&ur.ID, &ur.UserID, &ur.RoleID, &ur.RoleName, &plID, &ur.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan user role: %w", err)
		}
		if plID.Valid {
			ur.ProductLineID = &plID.String
		}
		roles = append(roles, ur)
	}
	return roles, rows.Err()
}

// GetUserProductLineIDs returns the unique product line IDs a user has access to.
func (r *RoleRepository) GetUserProductLineIDs(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT product_line_id FROM user_roles
		 WHERE user_id = $1 AND product_line_id IS NOT NULL`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get product line IDs: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("failed to scan product line ID: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetUserRoleNames returns the role names assigned to a user.
func (r *RoleRepository) GetUserRoleNames(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT r.name FROM user_roles ur
		 INNER JOIN roles r ON ur.role_id = r.id
		 WHERE ur.user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user role names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan role name: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
