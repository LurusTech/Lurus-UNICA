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

// ContextVariable is an app variable the router fills on every call.
type ContextVariable struct {
	Name  string
	Label string
}

// ContextVariables are the variables the router passes in the chat-messages
// `inputs` map. Dify silently drops an input that the app has not declared, so
// an app provisioned without these can never receive the ontology facts or the
// recalled knowledge, however well the prompt is written.
//
// Keep in step with the inputs map built in internal/routing/router.go.
var ContextVariables = []ContextVariable{
	{Name: "facts_context", Label: "确定性事实"},
	{Name: "experience_context", Label: "历史经验"},
	{Name: "knowledge_context", Label: "参考知识"},
	{Name: "customer_name", Label: "客户标识"},
	{Name: "channel", Label: "渠道"},
	{Name: "product_line", Label: "产品线"},
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
	cfg["user_input_form"] = withContextVariables(cfg["user_input_form"])

	if err := c.PutAppConfig(ctx, appID, cfg); err != nil {
		return fmt.Errorf("update app config %s: %w", appID, err)
	}
	log.Printf("[dify-admin] updated app config for app_id=%s (prompt_len=%d)", appID, len(systemPrompt))
	return nil
}

// withContextVariables returns the app's input form with any missing router
// context variable appended. Existing entries are preserved untouched: an
// operator may have added their own variables, and a form this client rewrote
// wholesale would drop them.
func withContextVariables(existing interface{}) []interface{} {
	form, _ := existing.([]interface{})

	declared := make(map[string]bool, len(form))
	for _, item := range form {
		entry, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// Each entry is keyed by its control type: paragraph, text-input, select.
		for _, spec := range entry {
			field, ok := spec.(map[string]interface{})
			if !ok {
				continue
			}
			if name, ok := field["variable"].(string); ok {
				declared[name] = true
			}
		}
	}

	for _, v := range ContextVariables {
		if declared[v.Name] {
			continue
		}
		form = append(form, map[string]interface{}{
			"paragraph": map[string]interface{}{
				"variable": v.Name,
				"label":    v.Label,
				"required": false,
				"default":  "",
			},
		})
	}
	return form
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
//
// It contains no policy numbers on purpose. Everything specific to the business
// arrives at call time through {{facts_context}}, which the router renders from
// that product line's ontology, so a policy change never means editing a prompt
// in Dify. The rules below are what a live model needed in order to actually use
// the injected facts rather than paraphrase them: rule 2 stops it from filling a
// declared gap with industry common sense, rule 3 stops it from collapsing a
// per-scope fact into a single number, and rule 5 is what produces the
// [FACT:...] claim tags the answer validator checks.
//
// The product line name is substituted here rather than passed as a Dify
// variable: one app serves one product line, so the name is a constant of the
// app, and the router's product_line input carries the ID rather than the name.
//
// Kept in step with the same template in unica/admin/internal/bridge/dify.go.
func DefaultSystemPrompt(productLineName string) string {
	const template = `你是{product_line_name}的在线客服。用简体中文、简洁专业地回答客户问题。

【本业务确定性事实】
{{facts_context}}

【历史经验】
{{experience_context}}

【参考知识】
{{knowledge_context}}

回答规则：
1. 上述"确定性事实"优先级最高，与其他信息冲突时以它为准，不得改写、换算或推测。
2. 事实中列为"不提供"的服务，客户问及时必须明确告知不支持，不要含糊带过，也不要用行业常识替客户补一个答案。
3. 若某项事实按情形不同（如按商品类别、服务阶段、客户类型分档），回答时必须说明适用情形，不得只给一个数字。
4. 确定性事实中没有的具体数值（价格、参数、库存、个人订单进度），不要编造，请客户提供具体信息或转人工。
5. 每引用一条确定性事实，在该句末尾附加标签 [FACT:属性名=取值]；若该事实分情形，写作 [FACT:情形.属性名=取值]。标签不会展示给客户。`
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
