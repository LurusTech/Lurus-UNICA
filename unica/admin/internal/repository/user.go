package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// UserRepository handles user database operations.
type UserRepository struct {
	db *sql.DB
}

// NewUserRepository creates a new user repository.
func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

// Create inserts a new user and returns the created record.
func (r *UserRepository) Create(ctx context.Context, email, passwordHash, displayName string) (*User, error) {
	user := &User{}
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash, display_name)
		 VALUES ($1, $2, $3)
		 RETURNING id, email, password_hash, display_name, is_active, created_at, updated_at`,
		email, passwordHash, displayName,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.IsActive, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	return user, nil
}

// GetByID retrieves a user by ID.
func (r *UserRepository) GetByID(ctx context.Context, id string) (*User, error) {
	user := &User{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, display_name, is_active, created_at, updated_at
		 FROM users WHERE id = $1`, id,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.IsActive, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	return user, nil
}

// GetByEmail retrieves a user by email address.
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*User, error) {
	user := &User{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, email, password_hash, display_name, is_active, created_at, updated_at
		 FROM users WHERE email = $1`, email,
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.IsActive, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}
	return user, nil
}

// List returns all users, optionally filtered by product line IDs.
func (r *UserRepository) List(ctx context.Context, productLineIDs []string) ([]User, error) {
	var rows *sql.Rows
	var err error

	if len(productLineIDs) == 0 {
		rows, err = r.db.QueryContext(ctx,
			`SELECT DISTINCT u.id, u.email, u.password_hash, u.display_name, u.is_active, u.created_at, u.updated_at
			 FROM users u ORDER BY u.created_at DESC`)
	} else {
		// Build parameterized IN clause
		query := `SELECT DISTINCT u.id, u.email, u.password_hash, u.display_name, u.is_active, u.created_at, u.updated_at
			FROM users u
			INNER JOIN user_roles ur ON u.id = ur.user_id
			WHERE ur.product_line_id = ANY($1)
			ORDER BY u.created_at DESC`
		rows, err = r.db.QueryContext(ctx, query, pqStringArray(productLineIDs))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.DisplayName,
			&u.IsActive, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// Update modifies user fields (display_name, is_active).
func (r *UserRepository) Update(ctx context.Context, id, displayName string, isActive bool) (*User, error) {
	user := &User{}
	err := r.db.QueryRowContext(ctx,
		`UPDATE users SET display_name = $2, is_active = $3, updated_at = $4
		 WHERE id = $1
		 RETURNING id, email, password_hash, display_name, is_active, created_at, updated_at`,
		id, displayName, isActive, time.Now(),
	).Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.IsActive, &user.CreatedAt, &user.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update user: %w", err)
	}
	return user, nil
}

// Deactivate sets a user's is_active flag to false.
func (r *UserRepository) Deactivate(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE users SET is_active = false, updated_at = $2 WHERE id = $1`,
		id, time.Now())
	if err != nil {
		return fmt.Errorf("failed to deactivate user: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user not found")
	}
	return nil
}

// UpdatePassword updates a user's password hash.
func (r *UserRepository) UpdatePassword(ctx context.Context, id, passwordHash string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE users SET password_hash = $2, updated_at = $3 WHERE id = $1`,
		id, passwordHash, time.Now())
	return err
}

// pqStringArray converts a string slice into a PostgreSQL-compatible array format.
func pqStringArray(arr []string) interface{} {
	// Use a text representation for ANY($1) with a string array
	if len(arr) == 0 {
		return "{}"
	}
	result := "{"
	for i, s := range arr {
		if i > 0 {
			result += ","
		}
		result += s
	}
	result += "}"
	return result
}
