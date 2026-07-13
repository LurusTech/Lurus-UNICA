// This file implements the client for the acest kb-server, which exposes two
// knowledge systems over one HTTP endpoint: the experience playbook (hybrid
// semantic+keyword search over distilled experience "bullets") and the
// external knowledge base (document sources). It also accepts raw experience
// submissions that the server distills through its evaluation/dedup pipeline.
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
)

// AcestConfig holds connection parameters for the acest kb-server.
// The server is deployment-global (one instance per environment, possibly on
// the same host as UNICA), so this is loaded once from env, not per product line.
type AcestConfig struct {
	BaseURL string // e.g. http://127.0.0.1:7423
	Token   string // Bearer token (ACEST_KB_API_TOKEN on the server side)
}

// AcestClient calls the acest kb-server HTTP API.
type AcestClient struct {
	httpClient *http.Client
}

// NewAcestClient creates an AcestClient with a default timeout. Individual
// recall calls should additionally bound latency via context deadlines so a
// slow knowledge server never blocks message processing.
func NewAcestClient() *AcestClient {
	return &AcestClient{
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// NewAcestClientWithHTTP creates an AcestClient with a custom http.Client (for testing).
func NewAcestClientWithHTTP(hc *http.Client) *AcestClient {
	return &AcestClient{httpClient: hc}
}

// PlaybookSearchResult is one hit from the experience playbook.
type PlaybookSearchResult struct {
	ID        string   `json:"id"`
	Section   string   `json:"section"`
	Content   string   `json:"content"`
	Score     float64  `json:"score"`
	Tags      []string `json:"tags"`
	UpdatedAt string   `json:"updated_at"`
}

type playbookSearchRequest struct {
	Query          string   `json:"query"`
	Limit          int      `json:"limit,omitempty"`
	SemanticWeight *float32 `json:"semantic_weight,omitempty"`
}

type playbookSearchResponse struct {
	Query      string                 `json:"query"`
	Results    []PlaybookSearchResult `json:"results"`
	TotalFound int                    `json:"total_found"`
	SearchType string                 `json:"search_type"`
}

// PlaybookSearch runs a hybrid search over the experience playbook.
func (c *AcestClient) PlaybookSearch(ctx context.Context, cfg AcestConfig, query string, limit int) ([]PlaybookSearchResult, error) {
	if limit <= 0 {
		limit = 3
	}
	var resp playbookSearchResponse
	if err := c.postJSON(ctx, cfg, "/api/v1/search", playbookSearchRequest{Query: query, Limit: limit}, &resp); err != nil {
		return nil, fmt.Errorf("playbook search: %w", err)
	}
	return resp.Results, nil
}

// KBSearchResult is one hit from the external knowledge base.
type KBSearchResult struct {
	SourceID string  `json:"source_id"`
	DocID    string  `json:"doc_id"`
	ChunkID  string  `json:"chunk_id,omitempty"`
	Title    string  `json:"title,omitempty"`
	Text     string  `json:"text"`
	Score    float64 `json:"score"`
	URI      string  `json:"uri,omitempty"`
}

type kbSearchRequest struct {
	Query    string   `json:"query"`
	Sources  []string `json:"sources,omitempty"`
	TopK     int      `json:"top_k,omitempty"`
	MinScore *float32 `json:"min_score,omitempty"`
}

type kbSearchResponse struct {
	Query      string           `json:"query"`
	TotalFound int              `json:"total_found"`
	Results    []KBSearchResult `json:"results"`
}

// KBSearch queries the external knowledge base across the given sources
// (all enabled sources when sources is empty).
func (c *AcestClient) KBSearch(ctx context.Context, cfg AcestConfig, query string, sources []string, topK int) ([]KBSearchResult, error) {
	if topK <= 0 {
		topK = 3
	}
	var resp kbSearchResponse
	if err := c.postJSON(ctx, cfg, "/api/v1/kb/search", kbSearchRequest{Query: query, Sources: sources, TopK: topK}, &resp); err != nil {
		return nil, fmt.Errorf("kb search: %w", err)
	}
	return resp.Results, nil
}

// Experience is a raw interaction submitted for distillation. The kb-server
// runs it through its evaluator/curator pipeline (scoring, dedup, merge)
// before anything is stored — this is intentionally the only way to add
// experience content.
type Experience struct {
	UserQuery         string   `json:"user_query"`
	AssistantResponse string   `json:"assistant_response"`
	Success           bool     `json:"success"`
	Error             string   `json:"error,omitempty"`
	ToolsUsed         []string `json:"tools_used,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
}

type experienceResponse struct {
	TaskID string `json:"task_id"`
}

// SubmitExperience submits a raw experience for asynchronous distillation and
// returns the server-side task ID.
func (c *AcestClient) SubmitExperience(ctx context.Context, cfg AcestConfig, exp Experience) (string, error) {
	var resp experienceResponse
	if err := c.postJSON(ctx, cfg, "/api/v2/experiences", exp, &resp); err != nil {
		return "", fmt.Errorf("submit experience: %w", err)
	}
	return resp.TaskID, nil
}

// Health checks kb-server liveness (unauthenticated endpoint).
func (c *AcestClient) Health(ctx context.Context, cfg AcestConfig) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.BaseURL+"/api/v1/health", nil)
	if err != nil {
		return fmt.Errorf("create health request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("health request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kb-server unhealthy: status %d", resp.StatusCode)
	}
	return nil
}

// acestError is the kb-server error envelope: {"error":{"code","message"}}.
type acestError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// postJSON sends an authenticated JSON POST and decodes the response into out.
func (c *AcestClient) postJSON(ctx context.Context, cfg AcestConfig, path string, body, out interface{}) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BaseURL+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read response %s: %w", path, err)
	}

	log.Printf("[acest] POST %s status=%d took=%s", path, resp.StatusCode, time.Since(start))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr acestError
		if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error.Code != "" {
			return fmt.Errorf("%s returned %d: %s (%s)", path, resp.StatusCode, apiErr.Error.Message, apiErr.Error.Code)
		}
		return fmt.Errorf("%s returned status %d", path, resp.StatusCode)
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("unmarshal response %s: %w", path, err)
		}
	}
	return nil
}
