// Package bridge provides external service integration clients for the router.
// DifyAdminClient communicates with the Dify Console (Admin) API to manage
// workspaces, applications, datasets, and API keys programmatically.
//
// This client backs cmd/setup_dify_workspaces only, which is a retired one-off
// script — see that command's doc comment. The production provisioning
// implementation is the admin service's bridge; this one is not an alternative
// onboarding path and must not gain new callers.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/kefu/unica/pkg/difyapp"
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

// AppModelConfig is a Dify app's whole configuration object.
//
// Deliberately a map rather than a struct. The object carries a large and
// version-dependent set of fields — agent mode, file upload, dataset retrieval,
// annotation reply, speech, sensitive-word avoidance — and the update endpoint
// replaces all of it. Modelling only the fields this client cares about would
// reset everything else to its zero value on every write, quietly undoing
// whatever an operator configured in the console.
type AppModelConfig map[string]interface{}

// appConfigEnvelope is the shape of GET /apps/{id} that carries the config.
type appConfigEnvelope struct {
	ModelConfig AppModelConfig `json:"model_config"`
}

// ContextVariables are the variables the router passes in the chat-messages
// `inputs` map. The list itself lives in difyapp because the admin service
// provisions the same apps and must declare the same set.
//
// Keep in step with the inputs map built in internal/routing/router.go.
var ContextVariables = difyapp.ContextVariables

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

// GetAppConfig fetches an app's current configuration object.
func (c *DifyAdminClient) GetAppConfig(ctx context.Context, appID string) (AppModelConfig, error) {
	var envelope appConfigEnvelope
	if err := c.doJSON(ctx, http.MethodGet, "/apps/"+appID, nil, &envelope); err != nil {
		return nil, fmt.Errorf("get app config %s: %w", appID, err)
	}
	if envelope.ModelConfig == nil {
		return AppModelConfig{}, nil
	}
	return envelope.ModelConfig, nil
}

// PutAppConfig writes a complete configuration object back to an app.
func (c *DifyAdminClient) PutAppConfig(ctx context.Context, appID string, cfg AppModelConfig) error {
	if err := c.doJSON(ctx, http.MethodPost, "/apps/"+appID+"/model-config", cfg, nil); err != nil {
		return fmt.Errorf("put app config %s: %w", appID, err)
	}
	return nil
}

// UpdateAppConfig sets an app's system prompt and declares the variables the
// router injects, leaving the rest of the configuration as it was.
//
// This is a read-modify-write because Dify has no partial update for it. The
// previous implementation sent {"pre_prompt": ...} to PUT /apps/{id}, which is
// the app *rename* endpoint: a real Dify 0.15.3 answers 400 "Missing required
// parameter in the JSON body: name", and would not have touched the prompt even
// with a name supplied. The prompt lives on POST /apps/{id}/model-config, which
// takes the entire configuration object.
func (c *DifyAdminClient) UpdateAppConfig(ctx context.Context, appID string, systemPrompt string) error {
	cfg, err := c.GetAppConfig(ctx, appID)
	if err != nil {
		return fmt.Errorf("update app config %s: %w", appID, err)
	}

	// In advanced prompt mode the app is driven by chat_prompt_config and
	// pre_prompt is ignored. Writing it anyway would report success and change
	// nothing, which is the failure mode this whole function is fixing.
	if mode, _ := cfg["prompt_type"].(string); mode == "advanced" {
		return fmt.Errorf("update app config %s: app is in advanced prompt mode, where pre_prompt is ignored; edit it in the Dify console", appID)
	}

	cfg["pre_prompt"] = systemPrompt
	cfg["prompt_type"] = "simple"
	cfg["user_input_form"] = difyapp.WithContextVariables(cfg["user_input_form"])

	if err := c.PutAppConfig(ctx, appID, cfg); err != nil {
		return fmt.Errorf("update app config %s: %w", appID, err)
	}
	log.Printf("[dify-admin] updated app config for app_id=%s (prompt_len=%d)", appID, len(systemPrompt))
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

// DefaultSystemPrompt returns the system prompt for a product line's chat app.
// The template itself lives in difyapp, shared with the admin service, which
// provisions the same apps.
var DefaultSystemPrompt = difyapp.DefaultSystemPrompt

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
	switch {
	case reqBody != nil:
		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	case method == http.MethodGet:
		// No body at all: the empty object below exists for the POST endpoints
		// that take no arguments, and a GET carrying a body is a needless way to
		// meet a proxy that rejects one.
	default:
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
