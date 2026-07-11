// Package bridge provides external service integration clients for the router.
// DifyAdminClient communicates with the Dify Console (Admin) API to manage
// workspaces, applications, datasets, and API keys programmatically.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// DifyAdminConfig holds the connection parameters for the Dify Console API.
type DifyAdminConfig struct {
	// BaseURL is the Dify console API root, e.g. "http://dify:5001/console/api".
	BaseURL string
	// AdminToken is the authentication token for the Dify console API.
	AdminToken string
}

// DifyAdminClient communicates with the Dify Console API for workspace management.
type DifyAdminClient struct {
	httpClient *http.Client
	config     DifyAdminConfig
}

// NewDifyAdminClient creates a new admin client for Dify Console API operations.
func NewDifyAdminClient(cfg DifyAdminConfig) *DifyAdminClient {
	return &DifyAdminClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		config: cfg,
	}
}

// --- Request / Response types ---

// WorkspaceCreateRequest is the payload for creating a new Dify workspace.
type WorkspaceCreateRequest struct {
	Name string `json:"name"`
}

// WorkspaceCreateResponse is the response after creating a workspace.
type WorkspaceCreateResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// AppCreateRequest is the payload for creating a new Dify application.
type AppCreateRequest struct {
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	Description string `json:"description,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// AppCreateResponse is the response after creating an application.
type AppCreateResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// AppConfigUpdateRequest is the payload for updating an app's configuration.
type AppConfigUpdateRequest struct {
	PrePrompt        string                 `json:"pre_prompt"`
	ModelConfig      map[string]interface{} `json:"model_config,omitempty"`
	OpeningStatement string                 `json:"opening_statement,omitempty"`
}

// DatasetCreateRequest is the payload for creating a new dataset (knowledge base).
type DatasetCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	WorkspaceID string `json:"workspace_id,omitempty"`
}

// DatasetCreateResponse is the response after creating a dataset.
type DatasetCreateResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// APIKeyCreateResponse is the response after generating an API key.
type APIKeyCreateResponse struct {
	ID        string `json:"id"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at,omitempty"`
}

// --- Admin API methods ---

// CreateWorkspace creates a new Dify workspace with the given name.
func (c *DifyAdminClient) CreateWorkspace(ctx context.Context, name string) (*WorkspaceCreateResponse, error) {
	reqBody := WorkspaceCreateRequest{Name: name}
	var resp WorkspaceCreateResponse
	if err := c.doJSON(ctx, http.MethodPost, "/workspaces", reqBody, &resp); err != nil {
		return nil, fmt.Errorf("create workspace %q: %w", name, err)
	}
	log.Printf("[dify-admin] created workspace %q (id=%s)", resp.Name, resp.ID)
	return &resp, nil
}

// CreateApp creates a new Chat application within a workspace.
func (c *DifyAdminClient) CreateApp(ctx context.Context, name string, workspaceID string) (*AppCreateResponse, error) {
	reqBody := AppCreateRequest{
		Name:        name,
		Mode:        "chat",
		Description: fmt.Sprintf("Customer service chat app for %s", name),
		WorkspaceID: workspaceID,
	}
	var resp AppCreateResponse
	if err := c.doJSON(ctx, http.MethodPost, "/apps", reqBody, &resp); err != nil {
		return nil, fmt.Errorf("create app %q in workspace %s: %w", name, workspaceID, err)
	}
	log.Printf("[dify-admin] created app %q (id=%s, mode=%s)", resp.Name, resp.ID, resp.Mode)
	return &resp, nil
}

// UpdateAppConfig updates the system prompt and model configuration of an app.
func (c *DifyAdminClient) UpdateAppConfig(ctx context.Context, appID string, systemPrompt string) error {
	reqBody := AppConfigUpdateRequest{
		PrePrompt: systemPrompt,
	}
	if err := c.doJSON(ctx, http.MethodPut, "/apps/"+appID, reqBody, nil); err != nil {
		return fmt.Errorf("update app config %s: %w", appID, err)
	}
	log.Printf("[dify-admin] updated app config for app_id=%s", appID)
	return nil
}

// CreateDataset creates a new dataset (knowledge base) within a workspace.
func (c *DifyAdminClient) CreateDataset(ctx context.Context, name string, workspaceID string) (*DatasetCreateResponse, error) {
	reqBody := DatasetCreateRequest{
		Name:        name,
		Description: fmt.Sprintf("Knowledge base for %s", name),
		WorkspaceID: workspaceID,
	}
	var resp DatasetCreateResponse
	if err := c.doJSON(ctx, http.MethodPost, "/datasets", reqBody, &resp); err != nil {
		return nil, fmt.Errorf("create dataset %q in workspace %s: %w", name, workspaceID, err)
	}
	log.Printf("[dify-admin] created dataset %q (id=%s)", resp.Name, resp.ID)
	return &resp, nil
}

// GenerateAPIKey creates a new API key for the specified application.
func (c *DifyAdminClient) GenerateAPIKey(ctx context.Context, appID string) (*APIKeyCreateResponse, error) {
	var resp APIKeyCreateResponse
	if err := c.doJSON(ctx, http.MethodPost, "/apps/"+appID+"/api-keys", nil, &resp); err != nil {
		return nil, fmt.Errorf("generate API key for app %s: %w", appID, err)
	}
	log.Printf("[dify-admin] generated API key for app_id=%s (key_id=%s)", appID, resp.ID)
	return &resp, nil
}

// DefaultSystemPrompt returns the default system prompt template with the product
// line name substituted in place of the {product_line_name} placeholder.
func DefaultSystemPrompt(productLineName string) string {
	const template = `You are a customer service AI assistant for {product_line_name}.
Your role is to help customers with product inquiries, troubleshooting, and general questions.
Always be polite, concise, and accurate.
If you are unsure about an answer, indicate your uncertainty clearly.
Never make up product specifications or pricing.`
	return strings.Replace(template, "{product_line_name}", productLineName, 1)
}

// --- Internal helpers ---

// doJSON performs an HTTP request with a JSON body and decodes the JSON response.
// If reqBody is nil, an empty JSON object is sent. If respOut is nil, the
// response body is read but not decoded.
func (c *DifyAdminClient) doJSON(ctx context.Context, method, path string, reqBody interface{}, respOut interface{}) error {
	if c.config.BaseURL == "" {
		return fmt.Errorf("dify admin base URL is empty")
	}
	if c.config.AdminToken == "" {
		return fmt.Errorf("dify admin token is empty")
	}

	var bodyReader io.Reader
	if reqBody != nil {
		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	} else {
		bodyReader = bytes.NewReader([]byte("{}"))
	}

	url := c.config.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.config.AdminToken)

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}

	log.Printf("[dify-admin] %s %s -> %d (%s) body_len=%d", method, path, resp.StatusCode, elapsed, len(respBody))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s %s: %s", resp.StatusCode, method, path, string(respBody))
	}

	if respOut != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, respOut); err != nil {
			return fmt.Errorf("unmarshal response: %w", err)
		}
	}

	return nil
}
