package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewDifyClient(t *testing.T) {
	client := NewDifyClient()
	if client == nil {
		t.Fatal("expected non-nil DifyClient")
	}
	if client.httpClient == nil {
		t.Fatal("expected non-nil http client")
	}
}

func TestChat_EmptyAPIKey(t *testing.T) {
	client := NewDifyClient()
	cfg := DifyConfig{
		BaseURL: "http://localhost",
		APIKey:  "",
	}
	_, err := client.Chat(context.Background(), cfg, "hello", "user-1", "", nil)
	if err == nil {
		t.Fatal("expected error for empty API key")
	}
}

func TestChat_EmptyBaseURL(t *testing.T) {
	client := NewDifyClient()
	cfg := DifyConfig{
		BaseURL: "",
		APIKey:  "test-key",
	}
	_, err := client.Chat(context.Background(), cfg, "hello", "user-1", "", nil)
	if err == nil {
		t.Fatal("expected error for empty base URL")
	}
}

func TestChat_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat-messages" {
			t.Errorf("expected /chat-messages, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json, got %s", r.Header.Get("Content-Type"))
		}

		var reqBody difyRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		if reqBody.Query != "What is your return policy?" {
			t.Errorf("expected query 'What is your return policy?', got %q", reqBody.Query)
		}
		if reqBody.User != "user-123" {
			t.Errorf("expected user 'user-123', got %q", reqBody.User)
		}
		if reqBody.ResponseMode != "blocking" {
			t.Errorf("expected response_mode 'blocking', got %q", reqBody.ResponseMode)
		}

		resp := DifyResponse{
			Answer:         "Our return policy allows returns within 30 days.",
			ConversationID: "dify-conv-001",
		}
		resp.Metadata.Usage.TotalTokens = 42
		resp.Metadata.Usage.PromptTokens = 30
		resp.Metadata.Usage.CompletionTokens = 12
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDifyClient()
	cfg := DifyConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
	}

	resp, err := client.Chat(context.Background(), cfg, "What is your return policy?", "user-123", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Answer != "Our return policy allows returns within 30 days." {
		t.Errorf("unexpected answer: %s", resp.Answer)
	}
	if resp.ConversationID != "dify-conv-001" {
		t.Errorf("unexpected conversation ID: %s", resp.ConversationID)
	}
	if resp.Metadata.Usage.TotalTokens != 42 {
		t.Errorf("expected 42 tokens, got %d", resp.Metadata.Usage.TotalTokens)
	}
	if resp.Metadata.Usage.PromptTokens != 30 {
		t.Errorf("expected 30 prompt tokens, got %d", resp.Metadata.Usage.PromptTokens)
	}
	if resp.Metadata.Usage.CompletionTokens != 12 {
		t.Errorf("expected 12 completion tokens, got %d", resp.Metadata.Usage.CompletionTokens)
	}
}

func TestChat_WithInputs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody difyRequest
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Errorf("failed to decode request body: %v", err)
		}
		// Verify inputs are passed through
		if reqBody.Inputs["customer_name"] != "John" {
			t.Errorf("expected customer_name 'John', got %v", reqBody.Inputs["customer_name"])
		}
		if reqBody.Inputs["channel"] != "wechat" {
			t.Errorf("expected channel 'wechat', got %v", reqBody.Inputs["channel"])
		}

		resp := DifyResponse{Answer: "Hello John!"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDifyClient()
	cfg := DifyConfig{BaseURL: server.URL, APIKey: "test-key"}
	inputs := map[string]string{
		"customer_name": "John",
		"channel":       "wechat",
	}

	resp, err := client.Chat(context.Background(), cfg, "hi", "user-1", "", inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Answer != "Hello John!" {
		t.Errorf("unexpected answer: %s", resp.Answer)
	}
}

func TestChat_WithRetrieverResources(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respJSON := `{
			"answer": "Based on our docs...",
			"conversation_id": "conv-001",
			"retriever_resources": [
				{
					"dataset_id": "ds-1",
					"dataset_name": "FAQ",
					"document_id": "doc-1",
					"document_name": "Return Policy",
					"score": 0.92,
					"content": "Returns within 30 days..."
				}
			],
			"metadata": {
				"usage": {
					"total_tokens": 100,
					"prompt_tokens": 70,
					"completion_tokens": 30
				}
			}
		}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(respJSON))
	}))
	defer server.Close()

	client := NewDifyClient()
	cfg := DifyConfig{BaseURL: server.URL, APIKey: "test-key"}

	resp, err := client.Chat(context.Background(), cfg, "return policy?", "user-1", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.RetrieverResources) != 1 {
		t.Fatalf("expected 1 retriever resource, got %d", len(resp.RetrieverResources))
	}
	res := resp.RetrieverResources[0]
	if res.DatasetName != "FAQ" {
		t.Errorf("expected dataset_name 'FAQ', got %q", res.DatasetName)
	}
	if res.DocumentName != "Return Policy" {
		t.Errorf("expected document_name 'Return Policy', got %q", res.DocumentName)
	}
	if res.Score != 0.92 {
		t.Errorf("expected score 0.92, got %f", res.Score)
	}
}

func TestChat_RetrieverResourcesFromMetadata(t *testing.T) {
	// Some Dify versions return retriever_resources inside metadata only
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respJSON := `{
			"answer": "Answer from metadata RAG",
			"conversation_id": "conv-002",
			"metadata": {
				"usage": {"total_tokens": 50, "prompt_tokens": 30, "completion_tokens": 20},
				"retriever_resources": [
					{
						"dataset_id": "ds-2",
						"dataset_name": "Knowledge Base",
						"document_id": "doc-2",
						"document_name": "FAQ Doc",
						"score": 0.75,
						"content": "Some content"
					}
				]
			}
		}`
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(respJSON))
	}))
	defer server.Close()

	client := NewDifyClient()
	cfg := DifyConfig{BaseURL: server.URL, APIKey: "test-key"}

	resp, err := client.Chat(context.Background(), cfg, "test", "user-1", "", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should be merged from metadata
	if len(resp.RetrieverResources) != 1 {
		t.Fatalf("expected 1 retriever resource (merged from metadata), got %d", len(resp.RetrieverResources))
	}
	if resp.RetrieverResources[0].DatasetName != "Knowledge Base" {
		t.Errorf("expected 'Knowledge Base', got %q", resp.RetrieverResources[0].DatasetName)
	}
}

func TestChat_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal server error"}`))
	}))
	defer server.Close()

	// Override retry backoffs to make test fast
	origBackoffs := retryBackoffs
	retryBackoffs = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond}
	defer func() { retryBackoffs = origBackoffs }()

	client := NewDifyClient()
	cfg := DifyConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
	}

	_, err := client.Chat(context.Background(), cfg, "hello", "user-1", "", nil)
	if err == nil {
		t.Fatal("expected error for server error response")
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("expected retry exhaustion error, got: %v", err)
	}
}

func TestChat_RetrySuccess(t *testing.T) {
	// First 2 calls fail, third succeeds
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error": "temporary failure"}`))
			return
		}
		resp := DifyResponse{Answer: "recovered answer", ConversationID: "conv-retry"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Override retry backoffs to make test fast
	origBackoffs := retryBackoffs
	retryBackoffs = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond}
	defer func() { retryBackoffs = origBackoffs }()

	client := NewDifyClient()
	cfg := DifyConfig{BaseURL: server.URL, APIKey: "test-key"}

	resp, err := client.Chat(context.Background(), cfg, "hello", "user-1", "", nil)
	if err != nil {
		t.Fatalf("expected success after retry, got error: %v", err)
	}
	if resp.Answer != "recovered answer" {
		t.Errorf("unexpected answer: %s", resp.Answer)
	}
	if atomic.LoadInt32(&callCount) != 3 {
		t.Errorf("expected 3 total attempts, got %d", atomic.LoadInt32(&callCount))
	}
}

func TestChat_RetryExhausted(t *testing.T) {
	var callCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(`bad gateway`))
	}))
	defer server.Close()

	origBackoffs := retryBackoffs
	retryBackoffs = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond}
	defer func() { retryBackoffs = origBackoffs }()

	client := NewDifyClient()
	cfg := DifyConfig{BaseURL: server.URL, APIKey: "test-key"}

	_, err := client.Chat(context.Background(), cfg, "hello", "user-1", "", nil)
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if atomic.LoadInt32(&callCount) != 3 {
		t.Errorf("expected 3 attempts (1 + 2 retries), got %d", atomic.LoadInt32(&callCount))
	}
}

func TestChat_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	origBackoffs := retryBackoffs
	retryBackoffs = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond}
	defer func() { retryBackoffs = origBackoffs }()

	client := NewDifyClient()
	cfg := DifyConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
	}

	_, err := client.Chat(context.Background(), cfg, "hello", "user-1", "", nil)
	if err == nil {
		t.Fatal("expected error for invalid JSON response")
	}
}

func TestChat_WithConversationID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody difyRequest
		json.NewDecoder(r.Body).Decode(&reqBody)
		if reqBody.ConversationID != "existing-conv" {
			t.Errorf("expected conversation_id 'existing-conv', got %q", reqBody.ConversationID)
		}
		resp := DifyResponse{Answer: "follow-up response"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := NewDifyClient()
	cfg := DifyConfig{
		BaseURL: server.URL,
		APIKey:  "test-key",
	}

	resp, err := client.Chat(context.Background(), cfg, "follow up question", "user-1", "existing-conv", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Answer != "follow-up response" {
		t.Errorf("unexpected answer: %s", resp.Answer)
	}
}

func TestChat_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`error`))
	}))
	defer server.Close()

	origBackoffs := retryBackoffs
	retryBackoffs = []time.Duration{5 * time.Second, 5 * time.Second}
	defer func() { retryBackoffs = origBackoffs }()

	client := NewDifyClient()
	cfg := DifyConfig{BaseURL: server.URL, APIKey: "test-key"}

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately after first failure triggers retry wait
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := client.Chat(ctx, cfg, "hello", "user-1", "", nil)
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
}

func TestDifyConfig_Fields(t *testing.T) {
	cfg := DifyConfig{
		BaseURL: "http://dify:5001/v1",
		APIKey:  "app-key-123",
	}
	if cfg.BaseURL != "http://dify:5001/v1" {
		t.Error("base URL mismatch")
	}
	if cfg.APIKey != "app-key-123" {
		t.Error("API key mismatch")
	}
}

func TestRetrieverResource_Fields(t *testing.T) {
	res := RetrieverResource{
		DatasetID:    "ds-1",
		DatasetName:  "Product FAQ",
		DocumentID:   "doc-1",
		DocumentName: "Returns",
		Score:        0.95,
		Content:      "Return policy content",
	}
	if res.DatasetID != "ds-1" {
		t.Error("dataset_id mismatch")
	}
	if res.Score != 0.95 {
		t.Error("score mismatch")
	}
}
