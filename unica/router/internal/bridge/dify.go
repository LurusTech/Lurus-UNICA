// Package bridge provides external service integration clients for the router.
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

// DifyConfig holds the Dify API connection parameters for a single request.
type DifyConfig struct {
	BaseURL string
	APIKey  string
}

// RetrieverResource represents a single RAG retrieval source from Dify.
type RetrieverResource struct {
	DatasetID    string  `json:"dataset_id"`
	DatasetName  string  `json:"dataset_name"`
	DocumentID   string  `json:"document_id"`
	DocumentName string  `json:"document_name"`
	Score        float64 `json:"score"`
	Content      string  `json:"content"`
}

// DifyResponse represents the response from the Dify chat-messages API.
type DifyResponse struct {
	Answer             string              `json:"answer"`
	ConversationID     string              `json:"conversation_id"`
	RetrieverResources []RetrieverResource `json:"retriever_resources,omitempty"`
	Metadata           struct {
		Usage struct {
			TotalTokens      int `json:"total_tokens"`
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		RetrieverResources []RetrieverResource `json:"retriever_resources,omitempty"`
	} `json:"metadata"`
}

// difyRequest represents the request body for the Dify chat-messages API.
type difyRequest struct {
	Inputs         map[string]interface{} `json:"inputs"`
	Query          string                 `json:"query"`
	User           string                 `json:"user"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	ResponseMode   string                 `json:"response_mode"`
}

// DifyClient communicates with the Dify AI platform API.
type DifyClient struct {
	httpClient *http.Client
}

// NewDifyClient creates a new DifyClient with sensible defaults.
func NewDifyClient() *DifyClient {
	return &DifyClient{
		httpClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// MaxRetries is the number of retry attempts for Dify API calls.
const MaxRetries = 2

// retryBackoffs defines the backoff durations for each retry attempt.
var retryBackoffs = []time.Duration{1 * time.Second, 2 * time.Second}

// Chat sends a chat message to the Dify API and returns the AI response.
// The inputs parameter allows passing contextual key-value pairs (e.g. customer_name, channel).
// Retry logic: up to 2 retries with 1s/2s exponential backoff on transient failures.
func (d *DifyClient) Chat(ctx context.Context, cfg DifyConfig, query string, userID string, convID string, inputs map[string]string) (*DifyResponse, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("dify API key is empty")
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("dify base URL is empty")
	}

	// Convert inputs to map[string]interface{} for the request body
	inputsMap := make(map[string]interface{}, len(inputs))
	for k, v := range inputs {
		inputsMap[k] = v
	}

	reqBody := difyRequest{
		Inputs:         inputsMap,
		Query:          query,
		User:           userID,
		ConversationID: convID,
		ResponseMode:   "blocking",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal dify request: %w", err)
	}

	url := cfg.BaseURL + "/chat-messages"

	var lastErr error
	for attempt := 0; attempt <= MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := retryBackoffs[attempt-1]
			log.Printf("[dify] retry attempt %d/%d after %s backoff", attempt, MaxRetries, backoff)
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
			case <-time.After(backoff):
			}
		}

		resp, err := d.doRequest(ctx, url, cfg.APIKey, bodyBytes)
		if err != nil {
			lastErr = err
			log.Printf("[dify] attempt %d/%d failed: %v", attempt+1, MaxRetries+1, err)
			continue
		}
		// Merge retriever resources from metadata if top-level is empty
		if len(resp.RetrieverResources) == 0 && len(resp.Metadata.RetrieverResources) > 0 {
			resp.RetrieverResources = resp.Metadata.RetrieverResources
		}
		return resp, nil
	}

	return nil, fmt.Errorf("dify API call failed after %d attempts: %w", MaxRetries+1, lastErr)
}

// doRequest performs a single HTTP request to the Dify API.
func (d *DifyClient) doRequest(ctx context.Context, url, apiKey string, bodyBytes []byte) (*DifyResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create dify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	start := time.Now()
	resp, err := d.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dify API call failed: %w", err)
	}
	defer resp.Body.Close()

	elapsed := time.Since(start)
	log.Printf("[dify] API call completed in %s (status=%d, url=%s)", elapsed, resp.StatusCode, url)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read dify response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dify API returned status %d: %s", resp.StatusCode, string(body))
	}

	var difyResp DifyResponse
	if err := json.Unmarshal(body, &difyResp); err != nil {
		return nil, fmt.Errorf("unmarshal dify response: %w", err)
	}

	return &difyResp, nil
}
