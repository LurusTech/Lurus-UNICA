// setup_dify_workspaces creates Dify workspaces, Chat Apps, datasets, and API keys
// for each product line in the database. This script is idempotent — it skips
// product lines that already have a dify_workspace_id in their config_json.
//
// Usage:
//   POSTGRES_URL=... DIFY_ADMIN_URL=... DIFY_ADMIN_TOKEN=... go run scripts/setup_dify_workspaces.go
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

type productLine struct {
	ID        string
	Name      string
	ConfigJSON json.RawMessage
}

type configJSON struct {
	DifyWorkspaceID string `json:"dify_workspace_id,omitempty"`
	DifyDatasetID   string `json:"dify_dataset_id,omitempty"`
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)
	log.Println("[setup-dify] starting Dify workspace setup...")

	postgresURL := envOrDefault("POSTGRES_URL", "postgres://postgres:password@localhost:5432/unica_core?sslmode=disable")
	difyAdminURL := envOrDefault("DIFY_ADMIN_URL", "http://localhost:5001/console/api")
	difyAdminToken := envOrDefault("DIFY_ADMIN_TOKEN", "")
	difyBaseURL := envOrDefault("DIFY_BASE_URL", "http://dify:5001/v1")

	if difyAdminToken == "" {
		log.Fatal("[setup-dify] DIFY_ADMIN_TOKEN is required")
	}

	// Connect to PostgreSQL
	db, err := sql.Open("postgres", postgresURL)
	if err != nil {
		log.Fatalf("[setup-dify] failed to open PostgreSQL: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("[setup-dify] cannot connect to PostgreSQL: %v", err)
	}
	log.Println("[setup-dify] connected to PostgreSQL")

	// Create Dify admin client
	adminClient := bridge.NewDifyAdminClient(bridge.DifyAdminConfig{
		BaseURL:    difyAdminURL,
		AdminToken: difyAdminToken,
	})

	// Load product lines
	productLines, err := loadProductLines(ctx, db)
	if err != nil {
		log.Fatalf("[setup-dify] failed to load product lines: %v", err)
	}
	log.Printf("[setup-dify] found %d product lines", len(productLines))

	if len(productLines) == 0 {
		log.Println("[setup-dify] no product lines found. Nothing to do.")
		return
	}

	// Process each product line
	var created, skipped int
	for _, pl := range productLines {
		// Check if already configured (idempotent)
		var cfg configJSON
		if len(pl.ConfigJSON) > 0 {
			json.Unmarshal(pl.ConfigJSON, &cfg)
		}
		if cfg.DifyWorkspaceID != "" {
			log.Printf("[setup-dify] SKIP product line %q (id=%s): already has workspace %s", pl.Name, pl.ID, cfg.DifyWorkspaceID)
			skipped++
			continue
		}

		log.Printf("[setup-dify] setting up product line %q (id=%s)...", pl.Name, pl.ID)

		// 1. Create workspace
		workspace, err := adminClient.CreateWorkspace(ctx, pl.Name)
		if err != nil {
			log.Printf("[setup-dify] ERROR creating workspace for %q: %v", pl.Name, err)
			continue
		}

		// 2. Create Chat App
		appName := fmt.Sprintf("%s Customer Service", pl.Name)
		app, err := adminClient.CreateApp(ctx, appName, workspace.ID)
		if err != nil {
			log.Printf("[setup-dify] ERROR creating app for %q: %v", pl.Name, err)
			continue
		}

		// 3. Configure system prompt
		systemPrompt := bridge.DefaultSystemPrompt(pl.Name)
		if err := adminClient.UpdateAppConfig(ctx, app.ID, systemPrompt); err != nil {
			log.Printf("[setup-dify] ERROR updating app config for %q: %v", pl.Name, err)
			continue
		}

		// 4. Create dataset (knowledge base)
		datasetName := fmt.Sprintf("%s Knowledge Base", pl.Name)
		dataset, err := adminClient.CreateDataset(ctx, datasetName, workspace.ID)
		if err != nil {
			log.Printf("[setup-dify] ERROR creating dataset for %q: %v", pl.Name, err)
			continue
		}

		// 5. Generate API key
		apiKey, err := adminClient.GenerateAPIKey(ctx, app.ID)
		if err != nil {
			log.Printf("[setup-dify] ERROR generating API key for %q: %v", pl.Name, err)
			continue
		}

		// 6. Update product_lines table
		newConfig := map[string]interface{}{
			"dify_workspace_id": workspace.ID,
			"dify_dataset_id":   dataset.ID,
		}
		// Merge with existing config
		var existingConfig map[string]interface{}
		if len(pl.ConfigJSON) > 0 {
			json.Unmarshal(pl.ConfigJSON, &existingConfig)
		}
		if existingConfig == nil {
			existingConfig = make(map[string]interface{})
		}
		for k, v := range newConfig {
			existingConfig[k] = v
		}
		configBytes, _ := json.Marshal(existingConfig)

		_, err = db.ExecContext(ctx,
			`UPDATE product_lines SET
				dify_agent_id = $1,
				dify_api_key = $2,
				dify_base_url = $3,
				config_json = $4
			WHERE id = $5`,
			app.ID, apiKey.Token, difyBaseURL, configBytes, pl.ID,
		)
		if err != nil {
			log.Printf("[setup-dify] ERROR updating product_lines for %q: %v", pl.Name, err)
			continue
		}

		log.Printf("[setup-dify] OK product line %q: workspace=%s, app=%s, dataset=%s", pl.Name, workspace.ID, app.ID, dataset.ID)
		created++
	}

	log.Printf("[setup-dify] complete: %d created, %d skipped, %d total", created, skipped, len(productLines))
}

func loadProductLines(ctx context.Context, db *sql.DB) ([]productLine, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, COALESCE(config_json, '{}') FROM product_lines ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query product_lines: %w", err)
	}
	defer rows.Close()

	var result []productLine
	for rows.Next() {
		var pl productLine
		if err := rows.Scan(&pl.ID, &pl.Name, &pl.ConfigJSON); err != nil {
			return nil, fmt.Errorf("scan product_line: %w", err)
		}
		result = append(result, pl)
	}
	return result, rows.Err()
}

func envOrDefault(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
