// Setup script for configuring Chatwoot custom channel inboxes per product line.
//
// This script:
// 1. Reads product lines from the database
// 2. Creates/verifies API Channel inbox in Chatwoot for each product line
// 3. Configures the webhook URL in Chatwoot
// 4. Stores Chatwoot credentials in product_lines.config_json
//
// Usage:
//   go run scripts/setup_chatwoot_channels.go \
//     -postgres "postgres://postgres:password@localhost:5432/unica_core?sslmode=disable" \
//     -chatwoot-url "http://localhost:3000" \
//     -chatwoot-token "your-admin-api-token" \
//     -webhook-url "https://gateway.example.com/api/v1/webhook/chatwoot" \
//     -webhook-token "your-webhook-secret"
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	_ "github.com/lib/pq"
)

// ProductLine represents a row from the product_lines table.
type ProductLine struct {
	ID         string
	Name       string
	ConfigJSON sql.NullString
}

// ChatwootConfig is the chatwoot section stored in config_json.
type ChatwootConfig struct {
	AccountID    int    `json:"account_id"`
	InboxID      int    `json:"inbox_id"`
	APIToken     string `json:"api_token"`
	WebhookToken string `json:"webhook_token"`
	BaseURL      string `json:"base_url"`
}

// CWInbox represents a Chatwoot inbox from the API.
type CWInbox struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"channel_type"`
}

// CWCreateInboxRequest is the request body for creating an API Channel inbox.
type CWCreateInboxRequest struct {
	Name    string              `json:"name"`
	Channel CWCreateInboxChannel `json:"channel"`
}

// CWCreateInboxChannel holds channel-specific config for inbox creation.
type CWCreateInboxChannel struct {
	Type       string `json:"type"`
	WebhookURL string `json:"webhook_url"`
}

// CWWebhookUpdate represents the webhook configuration update request.
type CWWebhookUpdate struct {
	AccountID  int      `json:"account_id"`
	URL        string   `json:"url"`
	Subscriptions []string `json:"subscriptions"`
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	// Parse flags
	postgresURL := flag.String("postgres", "postgres://postgres:password@localhost:5432/unica_core?sslmode=disable", "PostgreSQL connection URL")
	chatwootURL := flag.String("chatwoot-url", "http://localhost:3000", "Chatwoot base URL")
	chatwootToken := flag.String("chatwoot-token", "", "Chatwoot admin API token (user_access_token)")
	chatwootAccountID := flag.Int("chatwoot-account-id", 1, "Chatwoot account ID")
	webhookURL := flag.String("webhook-url", "", "Webhook callback URL for Chatwoot (e.g., https://gateway.example.com/api/v1/webhook/chatwoot)")
	webhookToken := flag.String("webhook-token", "", "Secret token for webhook verification")
	flag.Parse()

	if *chatwootToken == "" {
		log.Fatal("--chatwoot-token is required")
	}
	if *webhookURL == "" {
		log.Fatal("--webhook-url is required")
	}
	if *webhookToken == "" {
		log.Fatal("--webhook-token is required")
	}

	// Connect to database
	db, err := sql.Open("postgres", *postgresURL)
	if err != nil {
		log.Fatalf("Failed to connect to PostgreSQL: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		log.Fatalf("PostgreSQL ping failed: %v", err)
	}
	log.Println("Connected to PostgreSQL")

	ctx := context.Background()
	httpClient := &http.Client{Timeout: 30 * time.Second}

	// Read all product lines
	productLines, err := loadProductLines(ctx, db)
	if err != nil {
		log.Fatalf("Failed to load product lines: %v", err)
	}
	log.Printf("Found %d product lines", len(productLines))

	// Process each product line
	for _, pl := range productLines {
		log.Printf("Processing product line: %s (%s)", pl.Name, pl.ID)

		// Check if Chatwoot config already exists
		if pl.ConfigJSON.Valid {
			var existing map[string]json.RawMessage
			if err := json.Unmarshal([]byte(pl.ConfigJSON.String), &existing); err == nil {
				if _, ok := existing["chatwoot"]; ok {
					log.Printf("  Product line %s already has chatwoot config, skipping", pl.ID)
					continue
				}
			}
		}

		// Create API Channel inbox in Chatwoot
		inboxName := fmt.Sprintf("UNICA - %s", pl.Name)
		inboxID, err := createAPIChannelInbox(httpClient, *chatwootURL, *chatwootToken, *chatwootAccountID, inboxName, *webhookURL)
		if err != nil {
			log.Printf("  ERROR: Failed to create inbox for %s: %v", pl.Name, err)
			continue
		}
		log.Printf("  Created inbox %d: %s", inboxID, inboxName)

		// Build Chatwoot config
		cwConfig := ChatwootConfig{
			AccountID:    *chatwootAccountID,
			InboxID:      inboxID,
			APIToken:     *chatwootToken,
			WebhookToken: *webhookToken,
			BaseURL:      *chatwootURL,
		}

		// Update product_lines.config_json
		if err := updateProductLineConfig(ctx, db, pl.ID, pl.ConfigJSON, cwConfig); err != nil {
			log.Printf("  ERROR: Failed to update config for %s: %v", pl.Name, err)
			continue
		}
		log.Printf("  Updated config_json for product line %s", pl.ID)
	}

	// Configure account-level webhook
	log.Println("Configuring account-level webhook...")
	if err := configureWebhook(httpClient, *chatwootURL, *chatwootToken, *chatwootAccountID, *webhookURL, *webhookToken); err != nil {
		log.Printf("WARNING: Failed to configure webhook: %v (you may need to configure it manually in Chatwoot)", err)
	} else {
		log.Println("Webhook configured successfully")
	}

	log.Println("Setup complete!")
}

// loadProductLines reads all product lines from the database.
func loadProductLines(ctx context.Context, db *sql.DB) ([]ProductLine, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, config_json FROM product_lines`)
	if err != nil {
		return nil, fmt.Errorf("query product_lines: %w", err)
	}
	defer rows.Close()

	var result []ProductLine
	for rows.Next() {
		var pl ProductLine
		if err := rows.Scan(&pl.ID, &pl.Name, &pl.ConfigJSON); err != nil {
			return nil, fmt.Errorf("scan product_line: %w", err)
		}
		result = append(result, pl)
	}
	return result, rows.Err()
}

// createAPIChannelInbox creates an API Channel inbox in Chatwoot.
func createAPIChannelInbox(client *http.Client, baseURL, token string, accountID int, name, webhookURL string) (int, error) {
	reqBody := CWCreateInboxRequest{
		Name: name,
		Channel: CWCreateInboxChannel{
			Type:       "api",
			WebhookURL: webhookURL,
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return 0, fmt.Errorf("marshal inbox request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/accounts/%d/inboxes", baseURL, accountID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", token)

	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST inbox: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return 0, fmt.Errorf("create inbox returned %d: %s", resp.StatusCode, string(respBody))
	}

	var inbox CWInbox
	if err := json.Unmarshal(respBody, &inbox); err != nil {
		return 0, fmt.Errorf("unmarshal inbox response: %w", err)
	}

	return inbox.ID, nil
}

// updateProductLineConfig merges Chatwoot config into the product_lines.config_json field.
func updateProductLineConfig(ctx context.Context, db *sql.DB, plID string, existing sql.NullString, cwConfig ChatwootConfig) error {
	// Parse existing config or start fresh
	configMap := make(map[string]json.RawMessage)
	if existing.Valid && existing.String != "" {
		if err := json.Unmarshal([]byte(existing.String), &configMap); err != nil {
			// If parsing fails, start fresh
			configMap = make(map[string]json.RawMessage)
		}
	}

	// Add chatwoot section
	cwJSON, err := json.Marshal(cwConfig)
	if err != nil {
		return fmt.Errorf("marshal chatwoot config: %w", err)
	}
	configMap["chatwoot"] = cwJSON

	// Serialize full config
	fullConfig, err := json.Marshal(configMap)
	if err != nil {
		return fmt.Errorf("marshal full config: %w", err)
	}

	// Update database
	_, err = db.ExecContext(ctx,
		`UPDATE product_lines SET config_json = $1 WHERE id = $2`,
		string(fullConfig), plID,
	)
	if err != nil {
		return fmt.Errorf("update product_lines: %w", err)
	}

	return nil
}

// configureWebhook sets up the account-level webhook in Chatwoot.
func configureWebhook(client *http.Client, baseURL, token string, accountID int, webhookURL, webhookToken string) error {
	reqBody := map[string]interface{}{
		"url": webhookURL,
		"subscriptions": []string{
			"message_created",
			"conversation_status_changed",
		},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal webhook request: %w", err)
	}

	url := fmt.Sprintf("%s/api/v1/accounts/%d/webhooks", baseURL, accountID)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create webhook returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}
