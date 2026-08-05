// Package bridge provides Dify API integration for the admin service.
// It proxies configuration changes (system prompt, knowledge listing, test messages)
// to the Dify platform on behalf of knowledge/product admins.
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

// DifyBridgeConfig holds connection parameters for the Dify APIs.
type DifyBridgeConfig struct {
	// AdminURL is the Dify console API root (e.g. "http://dify:5001/console/api").
	AdminURL string
	// AdminToken is the authentication token for the Dify console API.
	AdminToken string
	// APIBaseURL is the Dify service API root (e.g. "http://dify:5001/v1").
	APIBaseURL string
}

// DifyBridge communicates with the Dify platform for AI config management.
type DifyBridge struct {
	httpClient *http.Client
	config     DifyBridgeConfig
}

// NewDifyBridge creates a new Dify bridge client.
func NewDifyBridge(cfg DifyBridgeConfig) *DifyBridge {
	return &DifyBridge{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		config: cfg,
	}
}

// --- Response types ---

// AppInfo represents basic app information from Dify.
type AppInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Mode         string `json:"mode"`
	SystemPrompt string `json:"system_prompt,omitempty"`
}

// KnowledgeDocument represents a document in a Dify knowledge base.
type KnowledgeDocument struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	DataSourceType string `json:"data_source_type"`
	WordCount      int    `json:"word_count"`
	IndexingStatus string `json:"indexing_status"`
	Enabled        bool   `json:"enabled"`
	CreatedAt      int64  `json:"created_at"`
}

// KnowledgeListResponse represents the paginated response from the Dify datasets/documents API.
type KnowledgeListResponse struct {
	Data    []KnowledgeDocument `json:"data"`
	HasMore bool                `json:"has_more"`
	Total   int                 `json:"total"`
}

// TestMessageResponse represents the response from a Dify test chat message.
type TestMessageResponse struct {
	Answer         string  `json:"answer"`
	ConversationID string  `json:"conversation_id"`
	Confidence     float64 `json:"confidence,omitempty"`
	Metadata       struct {
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
		RetrieverResources []struct {
			Score float64 `json:"score"`
		} `json:"retriever_resources,omitempty"`
	} `json:"metadata"`
}

// DifyAppCreated holds the identifiers returned after provisioning a Dify chat app.
type DifyAppCreated struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mode string `json:"mode,omitempty"`
}

// DifyDatasetCreated holds the identifiers returned after provisioning a Dify dataset.
type DifyDatasetCreated struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// DifyAPIKeyCreated holds an API key generated for a Dify app.
type DifyAPIKeyCreated struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}

// difyLoginResponse mirrors the Dify console login API response envelope.
type difyLoginResponse struct {
	Result string `json:"result"`
	Data   struct {
		AccessToken string `json:"access_token"`
	} `json:"data"`
}

// --- API Methods ---

// APIBaseURL returns the configured Dify service API base URL, exposed so callers
// (e.g. handlers writing back a provisioning result) can persist it alongside app/dataset IDs.
func (b *DifyBridge) APIBaseURL() string {
	return b.config.APIBaseURL
}

// DefaultSystemPrompt returns the system prompt for a product line's chat app.
//
// It contains no policy numbers on purpose. Everything specific to the business
// arrives at call time through {{facts_context}}, which the router renders from
// that product line's ontology, so a policy change never means editing a prompt
// in Dify.
//
// Kept in step with the same template in unica/router/internal/bridge/dify_admin.go.
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

// Login authenticates against the Dify console API with an admin email/password and
// returns an access token that can be passed to the other provisioning methods.
func (b *DifyBridge) Login(ctx context.Context, email, password string) (string, error) {
	if b.config.AdminURL == "" {
		return "", fmt.Errorf("dify admin URL is empty")
	}
	if email == "" || password == "" {
		return "", fmt.Errorf("dify admin email/password is empty")
	}

	reqBody := map[string]interface{}{
		"email":       email,
		"password":    password,
		"remember_me": true,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal login request: %w", err)
	}

	url := b.config.AdminURL + "/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dify login: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read login response: %w", err)
	}

	log.Printf("[dify-bridge] POST /login -> %d (%s)", resp.StatusCode, elapsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d from POST /login: %s", resp.StatusCode, string(body))
	}

	var loginResp difyLoginResponse
	if err := json.Unmarshal(body, &loginResp); err != nil {
		return "", fmt.Errorf("unmarshal login response: %w", err)
	}
	if loginResp.Data.AccessToken == "" {
		return "", fmt.Errorf("dify login response missing access_token")
	}

	return loginResp.Data.AccessToken, nil
}

// CreateChatApp provisions a new Dify chat app in the default workspace and applies the
// default system prompt to it, mirroring UpdateSystemPrompt semantics.
func (b *DifyBridge) CreateChatApp(ctx context.Context, token, name string) (*DifyAppCreated, error) {
	reqBody := map[string]interface{}{
		"name":        name,
		"mode":        "chat",
		"description": fmt.Sprintf("Customer service chat app for %s", name),
	}
	body, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/apps", reqBody, token)
	if err != nil {
		return nil, fmt.Errorf("create chat app %q: %w", name, err)
	}

	var resp DifyAppCreated
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal create app response: %w", err)
	}
	log.Printf("[dify-bridge] created chat app %q (id=%s)", resp.Name, resp.ID)

	// Setting the system prompt requires a configured model provider, which usually
	// does not exist yet at provisioning time. Treat failure as non-fatal: the prompt
	// can be configured later in the Dify console or via the AI-config API.
	prompt := DefaultSystemPrompt(name)
	if err := b.updateSystemPromptWithToken(ctx, resp.ID, prompt, token); err != nil {
		log.Printf("[dify-bridge] WARN: default system prompt not applied for app_id=%s (configure it in Dify after adding a model provider): %v", resp.ID, err)
	} else {
		log.Printf("[dify-bridge] set default system prompt for app_id=%s (len=%d)", resp.ID, len(prompt))
	}

	return &resp, nil
}

// CreateDataset provisions a new Dify dataset (knowledge base) in the default workspace.
func (b *DifyBridge) CreateDataset(ctx context.Context, token, name string) (*DifyDatasetCreated, error) {
	reqBody := map[string]interface{}{
		"name":        name,
		"description": fmt.Sprintf("Knowledge base for %s", name),
	}
	body, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/datasets", reqBody, token)
	if err != nil {
		return nil, fmt.Errorf("create dataset %q: %w", name, err)
	}

	var resp DifyDatasetCreated
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal create dataset response: %w", err)
	}
	log.Printf("[dify-bridge] created dataset %q (id=%s)", resp.Name, resp.ID)
	return &resp, nil
}

// CreateAppAPIKey generates a new API key for the specified Dify app.
func (b *DifyBridge) CreateAppAPIKey(ctx context.Context, token, appID string) (*DifyAPIKeyCreated, error) {
	body, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/apps/"+appID+"/api-keys", map[string]interface{}{}, token)
	if err != nil {
		return nil, fmt.Errorf("create API key for app %s: %w", appID, err)
	}

	var resp DifyAPIKeyCreated
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal create API key response: %w", err)
	}
	log.Printf("[dify-bridge] created API key for app_id=%s (key_id=%s)", appID, resp.ID)
	return &resp, nil
}

// GetAppConfig retrieves the current configuration of a Dify app (including system prompt).
func (b *DifyBridge) GetAppConfig(ctx context.Context, appID string) (*AppInfo, error) {
	if appID == "" {
		return nil, fmt.Errorf("app ID is empty")
	}

	body, err := b.doAdminRequest(ctx, http.MethodGet, "/apps/"+appID, nil)
	if err != nil {
		return nil, fmt.Errorf("get app config: %w", err)
	}

	// Parse the full app response to extract system prompt
	var raw map[string]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unmarshal app config: %w", err)
	}

	info := &AppInfo{
		ID: appID,
	}
	if name, ok := raw["name"].(string); ok {
		info.Name = name
	}
	if mode, ok := raw["mode"].(string); ok {
		info.Mode = mode
	}
	// System prompt may be in model_config.pre_prompt or pre_prompt
	if prePrompt, ok := raw["pre_prompt"].(string); ok {
		info.SystemPrompt = prePrompt
	} else if mc, ok := raw["model_config"].(map[string]interface{}); ok {
		if pp, ok := mc["pre_prompt"].(string); ok {
			info.SystemPrompt = pp
		}
	}

	return info, nil
}

// UpdateSystemPrompt updates the system prompt of a Dify app.
func (b *DifyBridge) UpdateSystemPrompt(ctx context.Context, appID string, prompt string) error {
	return b.updateSystemPromptWithToken(ctx, appID, prompt, "")
}

// updateSystemPromptWithToken sets an app's system prompt and declares the
// variables the router injects, leaving the rest of the configuration alone.
//
// Dify has no partial update for this. Sending {"pre_prompt": ...} to
// PUT /apps/{id} — what this did before — addresses the app *rename* endpoint,
// which a real Dify 0.15.3 answers with 400 "Missing required parameter in the
// JSON body: name" and which would not have touched the prompt even with a name
// supplied. The prompt lives on POST /apps/{id}/model-config, which replaces the
// whole configuration object, so the current one is read first and written back
// with only the prompt and the input form changed.
func (b *DifyBridge) updateSystemPromptWithToken(ctx context.Context, appID, prompt, token string) error {
	if appID == "" {
		return fmt.Errorf("app ID is empty")
	}

	body, err := b.doAdminRequestWithToken(ctx, http.MethodGet, "/apps/"+appID, nil, token)
	if err != nil {
		return fmt.Errorf("update system prompt: read current config: %w", err)
	}
	var envelope struct {
		ModelConfig map[string]interface{} `json:"model_config"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("update system prompt: unmarshal current config: %w", err)
	}
	cfg := envelope.ModelConfig
	if cfg == nil {
		cfg = map[string]interface{}{}
	}

	// In advanced prompt mode pre_prompt is ignored, so writing it would report
	// success and change nothing.
	if mode, _ := cfg["prompt_type"].(string); mode == "advanced" {
		return fmt.Errorf("update system prompt: app %s is in advanced prompt mode, where pre_prompt is ignored; edit it in the Dify console", appID)
	}

	cfg["pre_prompt"] = prompt
	cfg["prompt_type"] = "simple"
	cfg["user_input_form"] = withContextVariables(cfg["user_input_form"])

	if _, err := b.doAdminRequestWithToken(ctx, http.MethodPost, "/apps/"+appID+"/model-config", cfg, token); err != nil {
		return fmt.Errorf("update system prompt: %w", err)
	}
	log.Printf("[dify-bridge] updated system prompt for app_id=%s (len=%d)", appID, len(prompt))
	return nil
}

// contextVariables are the app variables the router passes in the chat-messages
// `inputs` map. Dify silently drops an input the app has not declared, so an app
// provisioned without these can never receive the ontology facts or the recalled
// knowledge, however well the prompt is written.
//
// Kept in step with unica/router/internal/bridge/dify_admin.go.
var contextVariables = []struct{ Name, Label string }{
	{"facts_context", "确定性事实"},
	{"experience_context", "历史经验"},
	{"knowledge_context", "参考知识"},
	{"customer_name", "客户标识"},
	{"channel", "渠道"},
	{"product_line", "产品线"},
}

// withContextVariables returns the app's input form with any missing router
// context variable appended. Existing entries are preserved: an operator may
// have added their own, and a form rewritten wholesale would drop them.
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

	for _, v := range contextVariables {
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

// ListKnowledgeDocuments lists documents in a Dify dataset (knowledge base).
func (b *DifyBridge) ListKnowledgeDocuments(ctx context.Context, datasetID string, apiKey string) (*KnowledgeListResponse, error) {
	if datasetID == "" {
		return nil, fmt.Errorf("dataset ID is empty")
	}

	url := fmt.Sprintf("%s/datasets/%s/documents?page=1&limit=100", b.config.APIBaseURL, datasetID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list knowledge docs: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d listing documents: %s", resp.StatusCode, string(body))
	}

	var result KnowledgeListResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal knowledge list: %w", err)
	}

	return &result, nil
}

// SendTestMessage sends a test chat message to a Dify app and returns the response.
func (b *DifyBridge) SendTestMessage(ctx context.Context, apiKey string, message string, userID string) (*TestMessageResponse, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key is empty")
	}

	reqBody := map[string]interface{}{
		"inputs":        map[string]interface{}{},
		"query":         message,
		"user":          userID,
		"response_mode": "blocking",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := b.config.APIBaseURL + "/chat-messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send test message: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	log.Printf("[dify-bridge] test message -> %d (%s)", resp.StatusCode, elapsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from test message: %s", resp.StatusCode, string(body))
	}

	var result TestMessageResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal test response: %w", err)
	}

	// Derive confidence from retriever resources if available
	if len(result.Metadata.RetrieverResources) > 0 {
		maxScore := 0.0
		for _, rr := range result.Metadata.RetrieverResources {
			if rr.Score > maxScore {
				maxScore = rr.Score
			}
		}
		result.Confidence = maxScore
	}

	return &result, nil
}

// --- Internal helpers ---

// doAdminRequest performs an HTTP request to the Dify Console (admin) API using the
// bridge's static AdminToken.
func (b *DifyBridge) doAdminRequest(ctx context.Context, method, path string, reqBody interface{}) ([]byte, error) {
	return b.doAdminRequestWithToken(ctx, method, path, reqBody, "")
}

// doAdminRequestWithToken performs an HTTP request to the Dify Console (admin) API.
// When token is non-empty it is used for the Authorization header instead of the
// bridge's static AdminToken (used for per-call tokens obtained via Login).
func (b *DifyBridge) doAdminRequestWithToken(ctx context.Context, method, path string, reqBody interface{}, token string) ([]byte, error) {
	if b.config.AdminURL == "" {
		return nil, fmt.Errorf("dify admin URL is empty")
	}
	authToken := token
	if authToken == "" {
		authToken = b.config.AdminToken
	}
	if authToken == "" {
		return nil, fmt.Errorf("dify admin token is empty")
	}

	var bodyReader io.Reader
	if reqBody != nil {
		bodyBytes, err := json.Marshal(reqBody)
		if err != nil {
			return nil, fmt.Errorf("marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	url := b.config.AdminURL + path
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	start := time.Now()
	resp, err := b.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	log.Printf("[dify-bridge] %s %s -> %d (%s)", method, path, resp.StatusCode, elapsed)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s %s: %s", resp.StatusCode, method, path, string(body))
	}

	return body, nil
}
