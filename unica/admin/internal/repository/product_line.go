package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// ProductLineRepository handles product line database operations.
type ProductLineRepository struct {
	db *sql.DB
}

// NewProductLineRepository creates a new product line repository.
func NewProductLineRepository(db *sql.DB) *ProductLineRepository {
	return &ProductLineRepository{db: db}
}

// DB returns the underlying database connection for direct queries.
func (r *ProductLineRepository) DB() *sql.DB {
	return r.db
}

// GetConfigJSON returns the raw config_json blob for one product line, so
// callers that own a single key inside it can read their block without a
// model field per key.
func (r *ProductLineRepository) GetConfigJSON(ctx context.Context, id string) (json.RawMessage, error) {
	var raw []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(config_json, '{}'::jsonb) FROM product_lines WHERE id = $1`, id).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load config_json: %w", err)
	}
	return json.RawMessage(raw), nil
}

// GetDifyAppKey returns the product line's Dify app API key. It is not on the
// ProductLine model on purpose: the model is serialised into API responses and
// a credential must not travel with it.
func (r *ProductLineRepository) GetDifyAppKey(ctx context.Context, id string) (string, error) {
	var key string
	err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(dify_api_key, '') FROM product_lines WHERE id = $1`, id).Scan(&key)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("failed to load dify app key: %w", err)
	}
	return key, nil
}

// SetConfigKey replaces exactly one top-level key of config_json in a single
// SQL statement. The merge happens database-side (jsonb ||), so concurrent
// writers of different keys cannot clobber each other the way a Go-side
// read-modify-write would.
func (r *ProductLineRepository) SetConfigKey(ctx context.Context, id, key string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("failed to marshal config value: %w", err)
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE product_lines
		 SET config_json = COALESCE(config_json, '{}'::jsonb) || jsonb_build_object($2::text, $3::jsonb),
		     updated_at = NOW()
		 WHERE id = $1`,
		id, key, string(data))
	if err != nil {
		return fmt.Errorf("failed to update config_json: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to update config_json: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("product line not found: %s", id)
	}
	return nil
}

// Create inserts a new product line.
func (r *ProductLineRepository) Create(ctx context.Context, name, displayName string, chatwootAccountID *int) (*ProductLine, error) {
	pl := &ProductLine{}
	var cwID sql.NullInt64
	if chatwootAccountID != nil {
		cwID = sql.NullInt64{Int64: int64(*chatwootAccountID), Valid: true}
	}

	err := r.db.QueryRowContext(ctx,
		`INSERT INTO product_lines (name, display_name, chatwoot_account_id)
		 VALUES ($1, $2, $3)
		 RETURNING id, name, display_name, chatwoot_account_id, created_at, updated_at`,
		name, displayName, cwID,
	).Scan(&pl.ID, &pl.Name, &pl.DisplayName, &cwID, &pl.CreatedAt, &pl.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to create product line: %w", err)
	}
	if cwID.Valid {
		v := int(cwID.Int64)
		pl.ChatwootAccountID = &v
	}
	return pl, nil
}

// GetByID retrieves a product line by ID.
func (r *ProductLineRepository) GetByID(ctx context.Context, id string) (*ProductLine, error) {
	pl := &ProductLine{}
	var cwID sql.NullInt64
	var difyID sql.NullString
	var configJSON []byte

	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, COALESCE(display_name, name), chatwoot_account_id, dify_agent_id, created_at, COALESCE(updated_at, created_at), COALESCE(config_json, '{}')
		 FROM product_lines WHERE id = $1`, id,
	).Scan(&pl.ID, &pl.Name, &pl.DisplayName, &cwID, &difyID, &pl.CreatedAt, &pl.UpdatedAt, &configJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get product line: %w", err)
	}
	if cwID.Valid {
		v := int(cwID.Int64)
		pl.ChatwootAccountID = &v
	}
	applyDifyBindingFields(pl, difyID, configJSON)
	return pl, nil
}

// GetByName retrieves a product line by its name. The column carries no unique
// constraint, so the oldest match wins: onboarding a customer twice must resume
// on the line it created the first time rather than pick an arbitrary row.
func (r *ProductLineRepository) GetByName(ctx context.Context, name string) (*ProductLine, error) {
	pl := &ProductLine{}
	var cwID sql.NullInt64
	var difyID sql.NullString
	var configJSON []byte

	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, COALESCE(display_name, name), chatwoot_account_id, dify_agent_id, created_at, COALESCE(updated_at, created_at), COALESCE(config_json, '{}')
		 FROM product_lines WHERE name = $1 ORDER BY created_at LIMIT 1`, name,
	).Scan(&pl.ID, &pl.Name, &pl.DisplayName, &cwID, &difyID, &pl.CreatedAt, &pl.UpdatedAt, &configJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get product line by name: %w", err)
	}
	if cwID.Valid {
		v := int(cwID.Int64)
		pl.ChatwootAccountID = &v
	}
	applyDifyBindingFields(pl, difyID, configJSON)
	return pl, nil
}

// SetChatwootAccountID writes only the chatwoot_account_id column. Update would
// also rewrite name and display_name, which would let a stale in-memory copy of
// the row overwrite a rename that happened in between.
func (r *ProductLineRepository) SetChatwootAccountID(ctx context.Context, id string, accountID int) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE product_lines SET chatwoot_account_id = $2, updated_at = NOW() WHERE id = $1`,
		id, accountID)
	if err != nil {
		return fmt.Errorf("failed to set chatwoot account id: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to set chatwoot account id: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("product line not found: %s", id)
	}
	return nil
}

// List returns all product lines, optionally filtered by IDs.
func (r *ProductLineRepository) List(ctx context.Context, ids []string) ([]ProductLine, error) {
	var rows *sql.Rows
	var err error

	if len(ids) == 0 {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id, name, COALESCE(display_name, name), chatwoot_account_id, dify_agent_id, created_at, COALESCE(updated_at, created_at), COALESCE(config_json, '{}')
			 FROM product_lines ORDER BY name`)
	} else {
		rows, err = r.db.QueryContext(ctx,
			`SELECT id, name, COALESCE(display_name, name), chatwoot_account_id, dify_agent_id, created_at, COALESCE(updated_at, created_at), COALESCE(config_json, '{}')
			 FROM product_lines WHERE id = ANY($1) ORDER BY name`,
			pqStringArray(ids))
	}

	if err != nil {
		return nil, fmt.Errorf("failed to list product lines: %w", err)
	}
	defer rows.Close()

	var pls []ProductLine
	for rows.Next() {
		var pl ProductLine
		var cwID sql.NullInt64
		var difyID sql.NullString
		var configJSON []byte
		if err := rows.Scan(&pl.ID, &pl.Name, &pl.DisplayName, &cwID, &difyID, &pl.CreatedAt, &pl.UpdatedAt, &configJSON); err != nil {
			return nil, fmt.Errorf("failed to scan product line: %w", err)
		}
		if cwID.Valid {
			v := int(cwID.Int64)
			pl.ChatwootAccountID = &v
		}
		applyDifyBindingFields(&pl, difyID, configJSON)
		pls = append(pls, pl)
	}
	return pls, rows.Err()
}

// Update modifies a product line's name and display name.
func (r *ProductLineRepository) Update(ctx context.Context, id, name, displayName string, chatwootAccountID *int) (*ProductLine, error) {
	pl := &ProductLine{}
	var cwID sql.NullInt64
	if chatwootAccountID != nil {
		cwID = sql.NullInt64{Int64: int64(*chatwootAccountID), Valid: true}
	}

	err := r.db.QueryRowContext(ctx,
		`UPDATE product_lines SET name = $2, display_name = $3, chatwoot_account_id = $4, updated_at = $5
		 WHERE id = $1
		 RETURNING id, name, display_name, chatwoot_account_id, created_at, updated_at`,
		id, name, displayName, cwID, time.Now(),
	).Scan(&pl.ID, &pl.Name, &pl.DisplayName, &cwID, &pl.CreatedAt, &pl.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update product line: %w", err)
	}
	if cwID.Valid {
		v := int(cwID.Int64)
		pl.ChatwootAccountID = &v
	}
	return pl, nil
}

// Delete removes a product line. Returns (deleted, blocked, error): blocked is true
// when channel_configs rows still reference the product line, in which case nothing
// is deleted and the caller should reject the request.
func (r *ProductLineRepository) Delete(ctx context.Context, id string) (bool, bool, error) {
	var channelCount int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM channel_configs WHERE product_line_id = $1`, id,
	).Scan(&channelCount)
	if err != nil {
		return false, false, fmt.Errorf("failed to count channels for product line: %w", err)
	}
	if channelCount > 0 {
		return false, true, nil
	}

	res, err := r.db.ExecContext(ctx, `DELETE FROM product_lines WHERE id = $1`, id)
	if err != nil {
		return false, false, fmt.Errorf("failed to delete product line: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, false, fmt.Errorf("failed to read delete result: %w", err)
	}
	return n > 0, false, nil
}

// UpdateDifyBinding stores the Dify app/dataset binding for a product line. extraConfig
// entries (e.g. "dify_dataset_id") are merged into the existing config_json, matching the
// write-back format used by the Dify workspace provisioning script.
func (r *ProductLineRepository) UpdateDifyBinding(ctx context.Context, id, agentID, apiKey, baseURL string, extraConfig map[string]string) (*ProductLine, error) {
	var existingRaw []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(config_json, '{}') FROM product_lines WHERE id = $1`, id,
	).Scan(&existingRaw)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to load product line config: %w", err)
	}

	existingConfig := make(map[string]interface{})
	if len(existingRaw) > 0 {
		json.Unmarshal(existingRaw, &existingConfig)
	}
	for k, v := range extraConfig {
		existingConfig[k] = v
	}
	configBytes, err := json.Marshal(existingConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal product line config: %w", err)
	}

	pl := &ProductLine{}
	var cwID sql.NullInt64
	var difyID sql.NullString
	var configJSON []byte

	err = r.db.QueryRowContext(ctx,
		`UPDATE product_lines SET dify_agent_id = $2, dify_api_key = $3, dify_base_url = $4, config_json = $5, updated_at = $6
		 WHERE id = $1
		 RETURNING id, name, COALESCE(display_name, name), chatwoot_account_id, dify_agent_id, created_at, COALESCE(updated_at, created_at), COALESCE(config_json, '{}')`,
		id, agentID, apiKey, baseURL, configBytes, time.Now(),
	).Scan(&pl.ID, &pl.Name, &pl.DisplayName, &cwID, &difyID, &pl.CreatedAt, &pl.UpdatedAt, &configJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to update dify binding: %w", err)
	}
	if cwID.Valid {
		v := int(cwID.Int64)
		pl.ChatwootAccountID = &v
	}
	applyDifyBindingFields(pl, difyID, configJSON)
	return pl, nil
}

// applyDifyBindingFields populates the read-only Dify binding fields (dify_agent_id,
// has_dify_binding, dify_dataset_id) on a product line from scanned column values.
func applyDifyBindingFields(pl *ProductLine, difyID sql.NullString, configJSON []byte) {
	if difyID.Valid && difyID.String != "" {
		v := difyID.String
		pl.DifyAgentID = &v
		pl.HasDifyBinding = true
	}

	if len(configJSON) == 0 {
		return
	}
	var cfg map[string]interface{}
	if err := json.Unmarshal(configJSON, &cfg); err != nil {
		return
	}
	if v, ok := cfg["dify_dataset_id"].(string); ok && v != "" {
		pl.DifyDatasetID = &v
	}
}
