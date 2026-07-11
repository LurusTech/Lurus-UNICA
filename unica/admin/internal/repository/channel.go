package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// ChannelRepository handles channel_configs database operations.
type ChannelRepository struct {
	db *sql.DB
}

// NewChannelRepository creates a new channel repository.
func NewChannelRepository(db *sql.DB) *ChannelRepository {
	return &ChannelRepository{db: db}
}

// Create inserts a new channel configuration.
func (r *ChannelRepository) Create(ctx context.Context, cfg *ChannelConfig) (*ChannelConfig, error) {
	result := &ChannelConfig{}
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO channel_configs (product_line_id, platform, display_name, app_id, app_secret_encrypted, extra_config_encrypted, webhook_token)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING id, product_line_id, platform, display_name, app_id, app_secret_encrypted, extra_config_encrypted,
		           webhook_token, is_enabled, is_verified, last_test_at, last_test_result, created_at, updated_at`,
		cfg.ProductLineID, cfg.Platform, cfg.DisplayName, cfg.AppID,
		cfg.AppSecretEncrypted, cfg.ExtraConfigEncrypted, cfg.WebhookToken,
	).Scan(&result.ID, &result.ProductLineID, &result.Platform, &result.DisplayName,
		&result.AppID, &result.AppSecretEncrypted, &result.ExtraConfigEncrypted,
		&result.WebhookToken, &result.IsEnabled, &result.IsVerified,
		&result.LastTestAt, &result.LastTestResult, &result.CreatedAt, &result.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create channel config: %w", err)
	}
	return result, nil
}

// GetByID retrieves a channel configuration by ID.
func (r *ChannelRepository) GetByID(ctx context.Context, id string) (*ChannelConfig, error) {
	cfg := &ChannelConfig{}
	err := r.db.QueryRowContext(ctx,
		`SELECT id, product_line_id, platform, display_name, app_id, app_secret_encrypted, extra_config_encrypted,
		        webhook_token, is_enabled, is_verified, last_test_at, last_test_result, created_at, updated_at
		 FROM channel_configs WHERE id = $1`, id,
	).Scan(&cfg.ID, &cfg.ProductLineID, &cfg.Platform, &cfg.DisplayName,
		&cfg.AppID, &cfg.AppSecretEncrypted, &cfg.ExtraConfigEncrypted,
		&cfg.WebhookToken, &cfg.IsEnabled, &cfg.IsVerified,
		&cfg.LastTestAt, &cfg.LastTestResult, &cfg.CreatedAt, &cfg.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get channel config: %w", err)
	}
	return cfg, nil
}

// List returns channel configs filtered by product line IDs.
// If plIDs is empty, returns all channels (for SuperAdmin).
func (r *ChannelRepository) List(ctx context.Context, plIDs []string) ([]ChannelConfig, error) {
	var rows *sql.Rows
	var err error

	if len(plIDs) == 0 {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id, product_line_id, platform, display_name, app_id, app_secret_encrypted, extra_config_encrypted,
			        webhook_token, is_enabled, is_verified, last_test_at, last_test_result, created_at, updated_at
			 FROM channel_configs ORDER BY created_at DESC`)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id, product_line_id, platform, display_name, app_id, app_secret_encrypted, extra_config_encrypted,
			        webhook_token, is_enabled, is_verified, last_test_at, last_test_result, created_at, updated_at
			 FROM channel_configs
			 WHERE product_line_id = ANY($1)
			 ORDER BY created_at DESC`, pqStringArray(plIDs))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list channel configs: %w", err)
	}
	defer rows.Close()

	var configs []ChannelConfig
	for rows.Next() {
		var c ChannelConfig
		if err := rows.Scan(&c.ID, &c.ProductLineID, &c.Platform, &c.DisplayName,
			&c.AppID, &c.AppSecretEncrypted, &c.ExtraConfigEncrypted,
			&c.WebhookToken, &c.IsEnabled, &c.IsVerified,
			&c.LastTestAt, &c.LastTestResult, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan channel config: %w", err)
		}
		configs = append(configs, c)
	}
	return configs, rows.Err()
}

// Update modifies a channel configuration.
func (r *ChannelRepository) Update(ctx context.Context, cfg *ChannelConfig) (*ChannelConfig, error) {
	result := &ChannelConfig{}
	err := r.db.QueryRowContext(ctx,
		`UPDATE channel_configs
		 SET display_name = $2, app_id = $3, app_secret_encrypted = $4,
		     extra_config_encrypted = $5, webhook_token = $6, updated_at = $7
		 WHERE id = $1
		 RETURNING id, product_line_id, platform, display_name, app_id, app_secret_encrypted, extra_config_encrypted,
		           webhook_token, is_enabled, is_verified, last_test_at, last_test_result, created_at, updated_at`,
		cfg.ID, cfg.DisplayName, cfg.AppID, cfg.AppSecretEncrypted,
		cfg.ExtraConfigEncrypted, cfg.WebhookToken, time.Now(),
	).Scan(&result.ID, &result.ProductLineID, &result.Platform, &result.DisplayName,
		&result.AppID, &result.AppSecretEncrypted, &result.ExtraConfigEncrypted,
		&result.WebhookToken, &result.IsEnabled, &result.IsVerified,
		&result.LastTestAt, &result.LastTestResult, &result.CreatedAt, &result.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update channel config: %w", err)
	}
	return result, nil
}

// Delete removes a channel configuration by ID.
func (r *ChannelRepository) Delete(ctx context.Context, id string) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM channel_configs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete channel config: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("channel config not found")
	}
	return nil
}

// SetEnabled toggles the is_enabled flag.
func (r *ChannelRepository) SetEnabled(ctx context.Context, id string, enabled bool) (*ChannelConfig, error) {
	result := &ChannelConfig{}
	err := r.db.QueryRowContext(ctx,
		`UPDATE channel_configs SET is_enabled = $2, updated_at = $3 WHERE id = $1
		 RETURNING id, product_line_id, platform, display_name, app_id, app_secret_encrypted, extra_config_encrypted,
		           webhook_token, is_enabled, is_verified, last_test_at, last_test_result, created_at, updated_at`,
		id, enabled, time.Now(),
	).Scan(&result.ID, &result.ProductLineID, &result.Platform, &result.DisplayName,
		&result.AppID, &result.AppSecretEncrypted, &result.ExtraConfigEncrypted,
		&result.WebhookToken, &result.IsEnabled, &result.IsVerified,
		&result.LastTestAt, &result.LastTestResult, &result.CreatedAt, &result.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to toggle channel: %w", err)
	}
	return result, nil
}

// UpdateTestResult records the result of a connection test.
func (r *ChannelRepository) UpdateTestResult(ctx context.Context, id string, verified bool, resultMsg string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE channel_configs SET is_verified = $2, last_test_at = $3, last_test_result = $4, updated_at = $3
		 WHERE id = $1`,
		id, verified, time.Now(), resultMsg)
	if err != nil {
		return fmt.Errorf("failed to update test result: %w", err)
	}
	return nil
}
