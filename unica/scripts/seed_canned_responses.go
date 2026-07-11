// Seed script for creating Chatwoot canned responses from a YAML configuration file.
//
// This script:
// 1. Reads canned response templates from a YAML config file
// 2. For each product line, connects to the corresponding Chatwoot account
// 3. Creates canned responses via the Chatwoot API (idempotent: skips existing short_codes)
//
// Usage:
//
//	go run scripts/seed_canned_responses.go \
//	  --config deploy/config/canned_responses.yaml \
//	  --chatwoot-url http://localhost:3000 \
//	  --api-token your-api-token
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// CannedResponseConfig represents the top-level YAML configuration.
type CannedResponseConfig struct {
	ProductLines []ProductLineConfig `yaml:"product_lines"`
}

// ProductLineConfig represents a single product line and its templates.
type ProductLineConfig struct {
	Name              string           `yaml:"name"`
	ChatwootAccountID int              `yaml:"chatwoot_account_id"`
	Templates         []TemplateConfig `yaml:"templates"`
}

// TemplateConfig represents a single canned response template.
type TemplateConfig struct {
	ShortCode string `yaml:"short_code"`
	Content   string `yaml:"content"`
}

// CannedResponse represents a Chatwoot canned response from the API.
type CannedResponse struct {
	ID        int    `json:"id"`
	ShortCode string `json:"short_code"`
	Content   string `json:"content"`
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	configPath := flag.String("config", "deploy/config/canned_responses.yaml", "Path to the canned responses YAML config file")
	chatwootURL := flag.String("chatwoot-url", "http://localhost:3000", "Chatwoot base URL")
	apiToken := flag.String("api-token", "", "Chatwoot API token (user_access_token)")
	dryRun := flag.Bool("dry-run", false, "Print actions without making API calls")
	flag.Parse()

	if *apiToken == "" && !*dryRun {
		log.Fatal("--api-token is required (use --dry-run to preview without API calls)")
	}

	// Read and parse YAML config
	cfg, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("Loaded %d product lines from %s", len(cfg.ProductLines), *configPath)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	ctx := context.Background()

	// Track summary
	var totalCreated, totalSkipped, totalFailed int

	for _, pl := range cfg.ProductLines {
		log.Printf("=== Processing product line: %s (account_id=%d) ===", pl.Name, pl.ChatwootAccountID)

		if *dryRun {
			for _, tmpl := range pl.Templates {
				log.Printf("  [DRY-RUN] Would create: %s -> %s", tmpl.ShortCode, truncate(tmpl.Content, 50))
			}
			totalCreated += len(pl.Templates)
			continue
		}

		// Fetch existing canned responses for this account
		existing, err := listCannedResponses(ctx, httpClient, *chatwootURL, *apiToken, pl.ChatwootAccountID)
		if err != nil {
			log.Printf("  ERROR: Failed to list existing canned responses: %v", err)
			totalFailed += len(pl.Templates)
			continue
		}

		// Build a set of existing short codes
		existingCodes := make(map[string]bool)
		for _, cr := range existing {
			existingCodes[cr.ShortCode] = true
		}

		for _, tmpl := range pl.Templates {
			if existingCodes[tmpl.ShortCode] {
				log.Printf("  SKIP: %s (already exists)", tmpl.ShortCode)
				totalSkipped++
				continue
			}

			if err := createCannedResponse(ctx, httpClient, *chatwootURL, *apiToken, pl.ChatwootAccountID, tmpl.ShortCode, tmpl.Content); err != nil {
				log.Printf("  ERROR: Failed to create %s: %v", tmpl.ShortCode, err)
				totalFailed++
				continue
			}

			log.Printf("  CREATED: %s", tmpl.ShortCode)
			totalCreated++
		}
	}

	// Print summary
	log.Println("=== Summary ===")
	log.Printf("  Created: %d", totalCreated)
	log.Printf("  Skipped: %d (already existed)", totalSkipped)
	log.Printf("  Failed:  %d", totalFailed)

	if totalFailed > 0 {
		os.Exit(1)
	}
}

// LoadConfig reads and parses the YAML configuration file.
func LoadConfig(path string) (*CannedResponseConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}

	var cfg CannedResponseConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse YAML config: %w", err)
	}

	// Validate
	for i, pl := range cfg.ProductLines {
		if pl.Name == "" {
			return nil, fmt.Errorf("product_lines[%d].name is required", i)
		}
		if pl.ChatwootAccountID <= 0 {
			return nil, fmt.Errorf("product_lines[%d].chatwoot_account_id must be > 0", i)
		}
		for j, tmpl := range pl.Templates {
			if tmpl.ShortCode == "" {
				return nil, fmt.Errorf("product_lines[%d].templates[%d].short_code is required", i, j)
			}
			if tmpl.Content == "" {
				return nil, fmt.Errorf("product_lines[%d].templates[%d].content is required", i, j)
			}
		}
	}

	return &cfg, nil
}

// listCannedResponses fetches all existing canned responses for an account.
func listCannedResponses(ctx context.Context, client *http.Client, baseURL, token string, accountID int) ([]CannedResponse, error) {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/canned_responses", baseURL, accountID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("api_access_token", token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET canned responses: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list canned responses returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result []CannedResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	return result, nil
}

// createCannedResponse creates a single canned response via the Chatwoot API.
func createCannedResponse(ctx context.Context, client *http.Client, baseURL, token string, accountID int, shortCode, content string) error {
	url := fmt.Sprintf("%s/api/v1/accounts/%d/canned_responses", baseURL, accountID)

	payload := map[string]string{
		"short_code": shortCode,
		"content":    content,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("api_access_token", token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("POST canned response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create canned response returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// truncate shortens a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
