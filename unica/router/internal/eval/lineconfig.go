package eval

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// LineRecord is what an offline replay needs to route one product line the
// way the router does: its id, its Dify app, and its config_json.
type LineRecord struct {
	ID         string
	BaseURL    string
	APIKey     string
	ConfigJSON json.RawMessage
}

// LookupLine reads a product line by name. Shared by cmd/evalset and
// cmd/evolve, so both replay against the same credentials the same way.
func LookupLine(ctx context.Context, db *sql.DB, name string) (LineRecord, error) {
	var rec LineRecord
	var baseURL, apiKey sql.NullString
	var configJSON []byte

	err := db.QueryRowContext(ctx,
		`SELECT id, dify_base_url, dify_api_key, config_json FROM product_lines WHERE name = $1`,
		name).Scan(&rec.ID, &baseURL, &apiKey, &configJSON)
	if err == sql.ErrNoRows {
		return rec, fmt.Errorf("product line %q not found in product_lines; "+
			"the name must match the database exactly", name)
	}
	if err != nil {
		return rec, fmt.Errorf("look up product line %q: %w", name, err)
	}
	if !apiKey.Valid || apiKey.String == "" {
		return rec, fmt.Errorf("product line %q has no dify_api_key; provision it first", name)
	}
	rec.BaseURL, rec.APIKey, rec.ConfigJSON = baseURL.String, apiKey.String, configJSON
	return rec, nil
}
