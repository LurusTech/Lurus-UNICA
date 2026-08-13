package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// userColumns is the projection every read here shares. Role and
// product_line_id ride along with the identity because they are the identity:
// nothing else decides what an account may touch.
const userColumns = `id, email, password_hash, display_name, role, product_line_id,
	chatwoot_user_id, is_active, created_at, updated_at`

// UserRepository handles user database operations.
type UserRepository struct {
	db *sql.DB
}

// NewUserRepository creates a new user repository.
func NewUserRepository(db *sql.DB) *UserRepository {
	return &UserRepository{db: db}
}

// scanUser reads one row of userColumns.
func scanUser(row interface{ Scan(...interface{}) error }) (*User, error) {
	user := &User{}
	var productLineID sql.NullString
	var chatwootUserID sql.NullInt64
	err := row.Scan(&user.ID, &user.Email, &user.PasswordHash, &user.DisplayName,
		&user.Role, &productLineID, &chatwootUserID,
		&user.IsActive, &user.CreatedAt, &user.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if productLineID.Valid {
		id := productLineID.String
		user.ProductLineID = &id
	}
	if chatwootUserID.Valid {
		id := int(chatwootUserID.Int64)
		user.ChatwootUserID = &id
	}
	return user, nil
}

// Create inserts a new user and returns the created record. productLineID must
// be nil for an admin and set for a user; the table's check constraint refuses
// any other combination.
func (r *UserRepository) Create(ctx context.Context, email, passwordHash, displayName, role string, productLineID *string) (*User, error) {
	var plID sql.NullString
	if productLineID != nil && *productLineID != "" {
		plID = sql.NullString{String: *productLineID, Valid: true}
	}
	user, err := scanUser(r.db.QueryRowContext(ctx,
		`INSERT INTO users (email, password_hash, display_name, role, product_line_id)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING `+userColumns,
		email, passwordHash, displayName, role, plID,
	))
	if err != nil {
		return nil, fmt.Errorf("failed to create user: %w", err)
	}
	return user, nil
}

// GetByID retrieves a user by ID.
func (r *UserRepository) GetByID(ctx context.Context, id string) (*User, error) {
	user, err := scanUser(r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = $1`, id))
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
	user, err := scanUser(r.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE email = $1`, email))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get user by email: %w", err)
	}
	return user, nil
}

// List returns all users, or only the ones belonging to tenantID when it is set.
func (r *UserRepository) List(ctx context.Context, tenantID string) ([]User, error) {
	var rows *sql.Rows
	var err error

	if tenantID == "" {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+userColumns+` FROM users ORDER BY created_at DESC`)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT `+userColumns+` FROM users WHERE product_line_id = $1 ORDER BY created_at DESC`,
			tenantID)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan user: %w", err)
		}
		users = append(users, *u)
	}
	return users, rows.Err()
}

// Update modifies user fields (display_name, is_active).
func (r *UserRepository) Update(ctx context.Context, id, displayName string, isActive bool) (*User, error) {
	user, err := scanUser(r.db.QueryRowContext(ctx,
		`UPDATE users SET display_name = $2, is_active = $3, updated_at = $4
		 WHERE id = $1
		 RETURNING `+userColumns,
		id, displayName, isActive, time.Now(),
	))
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

// SetChatwootUserID records the Chatwoot account this user signs in to, so the
// workbench can mint a login link for it instead of handing out a password.
func (r *UserRepository) SetChatwootUserID(ctx context.Context, id string, chatwootUserID int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE users SET chatwoot_user_id = $2, updated_at = $3 WHERE id = $1`,
		id, chatwootUserID, time.Now())
	return err
}
