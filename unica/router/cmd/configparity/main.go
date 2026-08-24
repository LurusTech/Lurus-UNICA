// Command configparity checks that the guardrail settings the admin console
// shows for a tenant are the settings the router would actually apply to that
// tenant's next message.
//
// The two sides of the comparison are the real ones, not reconstructions: the
// router side calls pkg/guardrail.Load on the tenant's stored config_json —
// the same call the message pipeline makes — and the console side is the
// answer of GET /api/v1/tenants/{id}/ai-settings. Nothing here re-implements
// the back-fill rules, deliberately: a third copy of them is exactly the defect
// this check exists to catch.
//
// A divergence here is not cosmetic. The console writes back what it displays,
// so a value shown wrongly is a value persisted wrongly on the next save.
//
//	POSTGRES_URL=... ADMIN_URL=http://127.0.0.1:8081 \
//	ADMIN_EMAIL=... ADMIN_PASSWORD=... go run ./cmd/configparity
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"

	"github.com/kefu/unica/pkg/guardrail"
)

type tenant struct {
	ID         string
	Name       string
	ConfigJSON sql.NullString
}

// consoleSettings is the subset of GET .../ai-settings this check compares. The
// guardrail fields are inlined in that response, so they are read from the top
// level here too.
type consoleSettings struct {
	ConfidenceThreshold float64  `json:"confidence_threshold"`
	HandoffKeywords     []string `json:"handoff_keywords"`
	BlockedTopics       []string `json:"blocked_topics"`
	HoldingMessage      string   `json:"holding_message"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func run() error {
	dsn := os.Getenv("POSTGRES_URL")
	if dsn == "" {
		return fmt.Errorf("POSTGRES_URL is required")
	}
	adminURL := strings.TrimRight(env("ADMIN_URL", "http://127.0.0.1:8081"), "/")

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	tenants, err := loadTenants(db)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	token, err := login(client, adminURL,
		env("ADMIN_EMAIL", "rehearsal@unica.local"),
		env("ADMIN_PASSWORD", "Rehearsal-2026!"))
	if err != nil {
		return fmt.Errorf("admin login: %w", err)
	}

	var mismatched, unreachable int
	for _, t := range tenants {
		// The router's own reading of what this tenant is configured with.
		runtime := guardrail.Load(json.RawMessage(t.ConfigJSON.String))

		console, err := fetchSettings(client, adminURL, token, t.ID)
		if err != nil {
			fmt.Printf("%-12s UNREACHABLE  %v\n", t.Name, err)
			unreachable++
			continue
		}

		diffs := compare(runtime, console)
		if len(diffs) == 0 {
			fmt.Printf("%-12s OK\n", t.Name)
			continue
		}
		mismatched++
		fmt.Printf("%-12s MISMATCH\n", t.Name)
		for _, d := range diffs {
			fmt.Printf("               %s\n", d)
		}
	}

	fmt.Printf("\n%d tenant(s): %d in agreement, %d mismatched, %d unreachable\n",
		len(tenants), len(tenants)-mismatched-unreachable, mismatched, unreachable)
	if mismatched > 0 || unreachable > 0 {
		os.Exit(1)
	}
	return nil
}

func loadTenants(db *sql.DB) ([]tenant, error) {
	rows, err := db.Query(
		`SELECT id, COALESCE(display_name, name), config_json::text
		   FROM product_lines ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("query product lines: %w", err)
	}
	defer rows.Close()

	var out []tenant
	for rows.Next() {
		var t tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.ConfigJSON); err != nil {
			return nil, fmt.Errorf("scan product line: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func login(client *http.Client, adminURL, email, password string) (string, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := client.Post(adminURL+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("login returned no access token")
	}
	return out.AccessToken, nil
}

func fetchSettings(client *http.Client, adminURL, token, tenantID string) (*consoleSettings, error) {
	req, err := http.NewRequest(http.MethodGet,
		adminURL+"/api/v1/tenants/"+tenantID+"/ai-settings", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out consoleSettings
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// compare reports every field on which the two sides disagree. Keyword and
// topic lists are compared as sets: their order carries no meaning to the
// evaluator, and reporting a reordering as a mismatch would train the reader to
// ignore this check.
func compare(runtime *guardrail.Config, console *consoleSettings) []string {
	var diffs []string
	if runtime.ConfidenceThreshold != console.ConfidenceThreshold {
		diffs = append(diffs, fmt.Sprintf("confidence_threshold: router=%v console=%v",
			runtime.ConfidenceThreshold, console.ConfidenceThreshold))
	}
	if !sameSet(runtime.HandoffKeywords, console.HandoffKeywords) {
		diffs = append(diffs, fmt.Sprintf("handoff_keywords: router=%v console=%v",
			runtime.HandoffKeywords, console.HandoffKeywords))
	}
	if !sameSet(runtime.BlockedTopics, console.BlockedTopics) {
		diffs = append(diffs, fmt.Sprintf("blocked_topics: router=%v console=%v",
			runtime.BlockedTopics, console.BlockedTopics))
	}
	if runtime.HoldingMessage != console.HoldingMessage {
		diffs = append(diffs, fmt.Sprintf("holding_message: router=%q console=%q",
			runtime.HoldingMessage, console.HoldingMessage))
	}
	return diffs
}

func sameSet(a, b []string) bool {
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	if len(x) == 0 && len(y) == 0 {
		return true
	}
	return reflect.DeepEqual(x, y)
}
