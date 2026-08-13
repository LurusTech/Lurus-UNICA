// Command setup_dify_workspaces is a ONE-OFF operations script, retired from
// any production path. Tenant provisioning is owned exclusively by the admin
// service (POST /api/v1/tenants), whose topology is one app + one dataset per
// tenant inside the default workspace, keyed on dify_agent_id. This script's
// workspace-per-line topology and its dify_workspace_id marker are the OTHER,
// abandoned convention: running it against a live environment produces a second,
// inconsistent binding for every tenant it touches. Its only remaining use is
// migration guidance for pre-refactor environments that were laid out per
// workspace. Do not run it against new environments.
//
// For each product line in the database, it creates a workspace, a Chat application,
// a knowledge base (dataset), configures the default system prompt, generates an
// API key, and updates the product_lines table with the resulting IDs and key.
//
// The script is idempotent: it checks config_json for an existing dify_workspace_id
// before creating resources, so re-running is safe.
//
// Environment variables:
//
//	POSTGRES_URL  — PostgreSQL connection string (required)
//	DIFY_ADMIN_URL — Dify Console API base URL, e.g. http://dify:5001/console/api (required)
//	DIFY_ADMIN_TOKEN — Bearer token for Dify Console API authentication (required)
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/lib/pq"

	"github.com/kefu/unica/router/internal/bridge"
)

// productLine represents a row from the product_lines table.
type productLine struct {
	ID         string
	Name       string
	AgentID    sql.NullString
	APIKey     sql.NullString
	BaseURL    sql.NullString
	ConfigJSON json.RawMessage
}

// configData represents the JSONB config_json column contents.
type configData struct {
	DifyWorkspaceID string `json:"dify_workspace_id,omitempty"`
	DifyDatasetID   string `json:"dify_dataset_id,omitempty"`
	DifyAppID       string `json:"dify_app_id,omitempty"`
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)
	log.Println("[setup] starting Dify multi-workspace setup...")

	postgresURL := os.Getenv("POSTGRES_URL")
	if postgresURL == "" {
		log.Fatal("[setup] POSTGRES_URL environment variable is required")
	}
	difyAdminURL := os.Getenv("DIFY_ADMIN_URL")
	if difyAdminURL == "" {
		log.Fatal("[setup] DIFY_ADMIN_URL environment variable is required")
	}
	difyAdminToken := os.Getenv("DIFY_ADMIN_TOKEN")
	if difyAdminToken == "" {
		log.Fatal("[setup] DIFY_ADMIN_TOKEN environment variable is required")
	}

	// Connect to PostgreSQL
	db, err := sql.Open("postgres", postgresURL)
	if err != nil {
		log.Fatalf("[setup] failed to open database: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("[setup] failed to connect to database: %v", err)
	}
	log.Println("[setup] connected to PostgreSQL")

	// Create Dify admin client
	adminClient := bridge.NewDifyAdminClient(bridge.DifyAdminConfig{
		BaseURL:    difyAdminURL,
		AdminToken: difyAdminToken,
	})

	// Load all product lines from the database
	productLines, err := loadProductLines(db)
	if err != nil {
		log.Fatalf("[setup] failed to load product lines: %v", err)
	}
	log.Printf("[setup] found %d product lines to process", len(productLines))

	if len(productLines) == 0 {
		log.Println("[setup] no product lines found. Nothing to do.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	var successCount, skipCount, errorCount int

	for _, pl := range productLines {
		// Parse existing config to check for idempotency
		var cfg configData
		if len(pl.ConfigJSON) > 0 && string(pl.ConfigJSON) != "{}" {
			if err := json.Unmarshal(pl.ConfigJSON, &cfg); err != nil {
				log.Printf("[setup] WARNING: failed to parse config_json for %q (id=%s): %v", pl.Name, pl.ID, err)
			}
		}

		// Skip if workspace already provisioned (idempotent check)
		if cfg.DifyWorkspaceID != "" {
			log.Printf("[setup] SKIP %q — already provisioned (workspace_id=%s)", pl.Name, cfg.DifyWorkspaceID)
			skipCount++
			continue
		}

		log.Printf("[setup] provisioning workspace for %q (id=%s)...", pl.Name, pl.ID)

		if err := provisionProductLine(ctx, db, adminClient, pl); err != nil {
			log.Printf("[setup] ERROR provisioning %q: %v", pl.Name, err)
			errorCount++
			continue
		}

		successCount++
		log.Printf("[setup] SUCCESS provisioned %q", pl.Name)
	}

	log.Printf("[setup] complete: %d provisioned, %d skipped, %d errors (total=%d)",
		successCount, skipCount, errorCount, len(productLines))

	if errorCount > 0 {
		os.Exit(1)
	}
}

// loadProductLines reads all product lines from the database.
func loadProductLines(db *sql.DB) ([]productLine, error) {
	rows, err := db.Query(`
		SELECT id, name, dify_agent_id, dify_api_key, dify_base_url, config_json
		FROM product_lines
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("query product_lines: %w", err)
	}
	defer rows.Close()

	var result []productLine
	for rows.Next() {
		var pl productLine
		if err := rows.Scan(&pl.ID, &pl.Name, &pl.AgentID, &pl.APIKey, &pl.BaseURL, &pl.ConfigJSON); err != nil {
			return nil, fmt.Errorf("scan product_line row: %w", err)
		}
		result = append(result, pl)
	}
	return result, rows.Err()
}

// provisionProductLine creates all Dify resources for a single product line
// and updates the database with the resulting IDs and API key.
func provisionProductLine(ctx context.Context, db *sql.DB, client *bridge.DifyAdminClient, pl productLine) error {
	// Step 1: Create workspace
	ws, err := client.CreateWorkspace(ctx, pl.Name)
	if err != nil {
		return fmt.Errorf("create workspace: %w", err)
	}

	// Step 2: Create Chat application
	appName := pl.Name + " Chat"
	app, err := client.CreateApp(ctx, appName, ws.ID)
	if err != nil {
		return fmt.Errorf("create app: %w", err)
	}

	// Step 3: Configure system prompt
	systemPrompt := bridge.DefaultSystemPrompt(pl.Name)
	if err := client.UpdateAppConfig(ctx, app.ID, systemPrompt); err != nil {
		return fmt.Errorf("update app config: %w", err)
	}

	// Step 4: Create dataset (knowledge base)
	datasetName := pl.Name + " Knowledge Base"
	dataset, err := client.CreateDataset(ctx, datasetName, ws.ID)
	if err != nil {
		return fmt.Errorf("create dataset: %w", err)
	}

	// Step 5: Generate API key
	apiKey, err := client.GenerateAPIKey(ctx, app.ID)
	if err != nil {
		return fmt.Errorf("generate API key: %w", err)
	}

	// Step 6: Update product_lines table
	cfgJSON := configData{
		DifyWorkspaceID: ws.ID,
		DifyDatasetID:   dataset.ID,
		DifyAppID:       app.ID,
	}
	cfgBytes, err := json.Marshal(cfgJSON)
	if err != nil {
		return fmt.Errorf("marshal config_json: %w", err)
	}

	_, err = db.ExecContext(ctx, `
		UPDATE product_lines
		SET dify_agent_id = $1,
		    dify_api_key = $2,
		    config_json = $3
		WHERE id = $4
	`, app.ID, apiKey.Token, cfgBytes, pl.ID)
	if err != nil {
		return fmt.Errorf("update product_lines: %w", err)
	}

	log.Printf("[setup] %q: workspace=%s app=%s dataset=%s key=%s...",
		pl.Name, ws.ID, app.ID, dataset.ID, maskKey(apiKey.Token))

	return nil
}

// maskKey returns a masked version of an API key for safe logging.
func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:4] + "****" + key[len(key)-4:]
}
